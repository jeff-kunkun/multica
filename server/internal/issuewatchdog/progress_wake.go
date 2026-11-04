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
// an employee comments on the issue, and when the issue has gone
// progressWakeInterval without any activity (no comment, no run step, no run
// start or end, no status move). Activity restarts that clock, and one quiet
// stretch wakes the owner once. One issue wakes its owner at most once per
// interval, counting every owner reminder it had, so a chatty run cannot flood
// the group.
const (
	ownerProgressEvent   = "owner_progress"
	reminderKindProgress = "progress"

	progressCauseComment = "comment"
	progressCauseQuiet   = "quiet"

	progressWakeInterval = 10 * time.Minute
	// progressWakeSettle lets a delivery that follows its comment within
	// seconds turn the reminder into the status one instead of both.
	progressWakeSettle = 30 * time.Second

	// progressCommentLookback caps how far back an unreported comment counts
	// after an earlier wake; before any wake only the last interval counts.
	progressCommentLookback = time.Hour

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

// lastActivity is the issue's newest sign of life: a comment from anyone
// (not the watchdog's own, not a bare 收到), a status move, a run being queued,
// started or finished, or a step a live run wrote.
func lastActivity(ctx context.Context, store QueryStore, issue Issue) (time.Time, error) {
	var at time.Time
	err := store.QueryRow(ctx, `
SELECT GREATEST(i.created_at,
  COALESCE((SELECT max(c.created_at) FROM comment c
            WHERE c.workspace_id = i.workspace_id AND c.issue_id = i.id
              AND c.author_type IN ('member', 'agent') AND c.type = 'comment'
              AND c.content NOT LIKE '%' || $2 || '%'
              AND regexp_replace(lower(trim(c.content)), '[[:space:][:punct:]，。！？、；：…]+', '', 'g') <> '收到'), i.created_at),
  COALESCE((SELECT max(e.occurred_at) FROM office_event e
            WHERE e.workspace_id = i.workspace_id AND e.issue_id = i.id
              AND e.event_type = 'issue.status_changed'), i.created_at),
  COALESCE((SELECT max(COALESCE(t.completed_at, t.started_at, t.dispatched_at, t.created_at))
            FROM agent_task_queue t WHERE t.issue_id = i.id), i.created_at),
  COALESCE((SELECT max(m.created_at) FROM agent_task_queue t
            CROSS JOIN LATERAL (SELECT created_at FROM task_message
                                WHERE task_id = t.id ORDER BY seq DESC LIMIT 1) m
            WHERE t.issue_id = i.id AND t.status = ANY($3)), i.created_at))
FROM issue i WHERE i.workspace_id = $4::uuid AND i.id = $1::uuid`,
		issue.ID, watchdogCommentMarker, activeTaskStatuses, issue.WorkspaceID).Scan(&at)
	return at, err
}

// checkProgress queues one progress reminder for an in-progress issue when an
// employee commented since the owner was last woken, or the issue has been
// quiet for an interval. A comment's key is the interval bucket and a quiet
// stretch's key is when it began, so two scanners racing on the same issue
// land on the same row and one quiet stretch never rings twice.
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
	// One progress reminder per issue waits at a time: while the relay is down
	// the next interval does not stack a second one behind it.
	var waiting bool
	if err := w.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM office_issue_watchdog_attempt
WHERE workspace_id = $1::uuid AND issue_id = $2::uuid AND event_type = $3 AND outcome = 'claimed')`,
		issue.WorkspaceID, issue.ID, ownerProgressEvent).Scan(&waiting); err != nil || waiting {
		return err
	}
	// A comment counts from the last wake, so one written during the quiet
	// interval is reported once it ends. Without an earlier wake only the last
	// interval counts, so turning the feature on does not report old comments.
	since := now.Add(-progressWakeInterval)
	if woken {
		since = lastWake
		if floor := now.Add(-progressCommentLookback); since.Before(floor) {
			since = floor
		}
	}
	cause, key, settle := "", "", progressWakeSettle
	var quietSince time.Time
	commentAt, commented, err := w.latestEmployeeComment(ctx, issue)
	if err != nil {
		return err
	}
	if commented && commentAt.After(since) {
		cause = progressCauseComment
		key = fmt.Sprintf("owner-progress:%d", now.Unix()/int64(progressWakeInterval/time.Second))
	} else {
		if quietSince, err = lastActivity(ctx, w.pool, issue); err != nil {
			return err
		}
		if now.Sub(quietSince) >= progressWakeInterval {
			// Found already overdue (first scan, scanner outage): the stall
			// reminder this scan queues is the one bell, not both together.
			if stall, err := w.stallDue(ctx, issue, now); err != nil || stall {
				return err
			}
			cause, settle = progressCauseQuiet, 0
			key = fmt.Sprintf("owner-quiet:%d", quietSince.UnixMicro())
		}
	}
	if cause == "" {
		return nil
	}
	manager, registration := w.cfg.Owners.Resolve(issue.ManagerName, issue.ProjectID)
	event := Reminder{
		Kind: reminderKindProgress, EventID: issue.ID + ":" + key, IssueID: issue.ID,
		IssueNumber: issue.Identifier, Title: issue.Title, Status: issue.Status, Manager: manager,
		ProjectName: issue.ProjectName, Cause: cause, OccurredAt: now, RegistrationNeeded: registration,
		Reason: progressWakeReason(cause, registration), QuietSince: quietSince,
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
		issue.WorkspaceID, issue.ID, key, ownerProgressEvent, manager, payload, now, now.Add(settle))
	return err
}

func progressWakeReason(cause string, registration bool) string {
	reason := "单子在跑，员工有新进展。请负责人照事件里的最新回报和运行步骤，在项目群用一句大白话报进度；不用回单，也不用再去查。"
	if cause == progressCauseQuiet {
		reason = "单子进行中，已 10 分钟没有新评论、没有新运行步骤。请负责人照事件里的实时运行记录，在项目群用一句大白话报现在在做什么或卡在哪；不用回单。"
	}
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

// quietStillCurrent: a quiet reminder still describes the issue, so nothing
// happened on it since the quiet stretch began. Activity in between restarts
// the clock instead of ringing late.
func quietStillCurrent(ctx context.Context, store QueryStore, issue Issue, event Reminder) (bool, error) {
	if !progressStillCurrent(issue) {
		return false, nil
	}
	if event.Cause != progressCauseQuiet {
		return true, nil
	}
	at, err := lastActivity(ctx, store, issue)
	return err == nil && at.Equal(event.QuietSince), err
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
