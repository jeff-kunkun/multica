package issuewatchdog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Progress reminders (DENE-2981) wake the owner while an issue is in progress,
// so the owner can say one line in the project group without being asked: when
// an employee comments on the issue, and every progressWakeInterval a run goes
// on. One issue wakes its owner at most once per interval, counting every owner
// reminder it had, so a chatty run cannot flood the group.
const (
	ownerProgressEvent   = "owner_progress"
	reminderKindProgress = "progress"

	progressCauseComment = "comment"
	progressCauseRunning = "running"

	progressWakeInterval = 15 * time.Minute
	// progressWakeSettle lets a delivery that follows its comment within
	// seconds turn the reminder into the status one instead of both.
	progressWakeSettle = 30 * time.Second

	maxRunSteps     = 3
	runStepMaxRunes = 160
)

// lastOwnerWake is when the owner was last woken, or may have been, about the
// issue. Rows that provably never reached the owner do not count.
func (w *Watchdog) lastOwnerWake(ctx context.Context, issue Issue) (time.Time, bool, error) {
	var at pgtype.Timestamptz
	err := w.pool.QueryRow(ctx, `
SELECT max(GREATEST(attempted_at, available_at)) FROM office_issue_watchdog_attempt
WHERE workspace_id = $1::uuid AND issue_id = $2::uuid AND event_type = ANY($3)
  AND NOT (outcome = 'failed' AND delivery_result IN ('not_sent', 'resolved_before_delivery', 'stale'))`,
		issue.WorkspaceID, issue.ID, ownerReminderEvents).Scan(&at)
	return at.Time, at.Valid, err
}

// latestEmployeeComment is the newest substantive comment an agent wrote.
func (w *Watchdog) latestEmployeeComment(ctx context.Context, issue Issue) (time.Time, bool, error) {
	var at pgtype.Timestamptz
	err := w.pool.QueryRow(ctx, `
SELECT max(c.created_at) FROM comment c
WHERE c.workspace_id = $1::uuid AND c.issue_id = $2::uuid
  AND c.author_type = 'agent' AND c.type = 'comment'
  AND c.content NOT LIKE '%' || $3 || '%'
  AND regexp_replace(lower(trim(c.content)), '[[:space:][:punct:]，。！？、；：…]+', '', 'g') <> '收到'`,
		issue.WorkspaceID, issue.ID, watchdogCommentMarker).Scan(&at)
	return at.Time, at.Valid, err
}

// runningSince is when the issue's current live run started. A run whose
// runtime went offline is not progress; the stall reminder covers it.
func (w *Watchdog) runningSince(ctx context.Context, issue Issue) (time.Time, bool, error) {
	var at pgtype.Timestamptz
	err := w.pool.QueryRow(ctx, `
SELECT min(COALESCE(t.started_at, t.dispatched_at, t.created_at))
FROM agent_task_queue t LEFT JOIN agent_runtime rt ON rt.id = t.runtime_id
WHERE t.issue_id = $1::uuid AND t.status = 'running' AND COALESCE(rt.status, 'online') <> 'offline'`, issue.ID).Scan(&at)
	return at.Time, at.Valid, err
}

// checkProgress queues one progress reminder for an in-progress issue when an
// employee commented since the owner was last woken, or a run has gone on for
// an interval since then. The key is the interval bucket, so two scanners
// racing on the same issue land on the same row.
func (w *Watchdog) checkProgress(ctx context.Context, issue Issue, now time.Time) error {
	if !progressStillCurrent(issue) {
		return nil
	}
	lastWake, woken, err := w.lastOwnerWake(ctx, issue)
	if err != nil {
		return err
	}
	if woken && now.Sub(lastWake) < progressWakeInterval {
		return nil
	}
	// Without an earlier wake only the last interval counts, so turning the
	// feature on does not report old comments.
	since := now.Add(-progressWakeInterval)
	if woken && lastWake.After(since) {
		since = lastWake
	}
	cause := ""
	commentAt, commented, err := w.latestEmployeeComment(ctx, issue)
	if err != nil {
		return err
	}
	if commented && commentAt.After(since) {
		cause = progressCauseComment
	} else {
		started, running, err := w.runningSince(ctx, issue)
		if err != nil {
			return err
		}
		anchor := started
		if woken && lastWake.After(anchor) {
			anchor = lastWake
		}
		if running && now.Sub(anchor) >= progressWakeInterval {
			cause = progressCauseRunning
		}
	}
	if cause == "" {
		return nil
	}
	manager, registration := w.cfg.Owners.Resolve(issue.ManagerName, issue.ProjectID)
	key := fmt.Sprintf("owner-progress:%d", now.Unix()/int64(progressWakeInterval/time.Second))
	event := Reminder{
		Kind: reminderKindProgress, EventID: issue.ID + ":" + key, IssueID: issue.ID,
		IssueNumber: issue.Identifier, Title: issue.Title, Status: issue.Status, Manager: manager,
		ProjectName: issue.ProjectName, Cause: cause, OccurredAt: now, RegistrationNeeded: registration,
		Reason: progressWakeReason(registration),
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = w.pool.Exec(ctx, `
INSERT INTO office_issue_watchdog_attempt
(workspace_id, issue_id, event_key, event_type, target, manager_name, payload, attempted_at, available_at)
VALUES ($1::uuid,$2::uuid,$3,$4,'manager',$5,$6::jsonb,$7,$8)
ON CONFLICT (workspace_id,issue_id,event_key) DO NOTHING`,
		issue.WorkspaceID, issue.ID, key, ownerProgressEvent, manager, payload, now, now.Add(progressWakeSettle))
	return err
}

func progressWakeReason(registration bool) string {
	reason := "单子在跑，员工有新进展。请负责人照事件里的最新回报和运行步骤，在项目群用一句大白话报进度；不用回单，也不用再去查。"
	if registration {
		reason += " 项目归属缺失，请扫帚补登记。"
	}
	return reason
}

// progressStillCurrent: the issue is still in progress and not waiting on
// Kun. A delivery, block or close since then has its own status reminder.
func progressStillCurrent(issue Issue) bool {
	return issue.Status == "in_progress" && !waitsOnKun(issue)
}

// refreshRunSteps adds the latest narrated steps of the issue's newest run,
// masked and clipped like the comment summary, and how long it has run.
func refreshRunSteps(ctx context.Context, store QueryStore, event *Reminder, issue Issue, now time.Time) error {
	event.Steps, event.RunMinutes = nil, 0
	var (
		taskID  string
		started pgtype.Timestamptz
		running bool
	)
	err := store.QueryRow(ctx, `
SELECT id::text, COALESCE(started_at, dispatched_at, created_at), status = 'running'
FROM agent_task_queue WHERE issue_id = $1::uuid
ORDER BY (status = 'running') DESC, created_at DESC, id DESC LIMIT 1`, issue.ID).Scan(&taskID, &started, &running)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if running && started.Valid {
		event.RunMinutes = int(now.Sub(started.Time).Minutes())
	}
	rows, err := store.Query(ctx, `
SELECT content FROM task_message
WHERE task_id = $1::uuid AND type = 'text' AND btrim(COALESCE(content, '')) <> ''
ORDER BY seq DESC LIMIT $2`, taskID, maxRunSteps)
	if err != nil {
		return err
	}
	defer rows.Close()
	var steps []string
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			return err
		}
		text := strings.TrimSpace(whitespaceRun.ReplaceAllString(content, " "))
		steps = append([]string{clipRunes(maskSecrets(text), runStepMaxRunes)}, steps...)
	}
	event.Steps = steps
	return rows.Err()
}
