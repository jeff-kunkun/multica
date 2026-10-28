package issuewatchdog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// WakeFunc leaves an issue-local watchdog comment that @mentions target and,
// for an agent, starts or joins its run on that issue. role labels the
// mention ("开发员工", "审查员", "父单负责人").
type WakeFunc func(ctx context.Context, issueID, targetType, targetID, role, reason string) error

// SetWake installs the wake path for the review rules. Without it they record
// a failed attempt instead of waking anyone.
func (w *Watchdog) SetWake(wake WakeFunc) { w.wake = wake }

// NoteFunc leaves an issue-local watchdog comment that @mentions nobody.
type NoteFunc func(ctx context.Context, issueID, content string) error

// SetNote installs the comment path for the review-round stop.
func (w *Watchdog) SetNote(note NoteFunc) { w.note = note }

type reviewStage int

const (
	stageWorking reviewStage = iota // the developer has the ball
	stageHold
	stageAwaitingReview
	stageAwaitingSignoff
)

type timelineComment struct {
	ID        string
	Author    actor
	Content   string
	CreatedAt time.Time
}

// reviewState is where the review loop stands, read from the comments: the
// hold, review request, or pass that set the stage, and who the reviewer is.
type reviewState struct {
	Stage    reviewStage
	At       timelineComment
	Reviewer actor
	// LastIsStage: no substantive comment came after the one that set the stage.
	LastIsStage bool
	// FailedRounds holds the first hold of each review round since the last
	// pass. A hold opens a new round when it is the first one or the developer
	// has commented since the previous round's hold.
	FailedRounds []timelineComment
	// AskedSince: the developer asked for another review after the latest
	// failed round.
	AskedSince bool
}

// roundLimitHold is the hold that took the issue to limit failed rounds in a
// row, while the developer has not asked for another review since.
func (st reviewState) roundLimitHold(limit int) (timelineComment, bool) {
	if limit <= 0 || len(st.FailedRounds) < limit || st.AskedSince {
		return timelineComment{}, false
	}
	return st.FailedRounds[len(st.FailedRounds)-1], true
}

type issueRuns struct {
	Active       int
	ActiveAgents map[string]bool
	// LastCreatedAt and LastActivityAt are zero when the issue never had a run.
	LastCreatedAt  time.Time
	LastActivityAt time.Time
}

var activeTaskStatuses = []string{"queued", "dispatched", "running", "deferred", "waiting_local_directory"}

func assigneeActor(issue Issue) actor {
	if issue.AssigneeID == "" || (issue.AssigneeType != "agent" && issue.AssigneeType != "member") {
		return actor{}
	}
	return actor{Type: issue.AssigneeType, ID: issue.AssigneeID}
}

func reviewWakeStatus(status string) bool {
	switch status {
	case "todo", "in_progress", "in_review", "blocked":
		return true
	}
	return false
}

func (w *Watchdog) loadReviewState(ctx context.Context, issue Issue) (reviewState, error) {
	rows, err := w.pool.Query(ctx, `
SELECT id, author_type, author_id, content, created_at FROM (
    SELECT c.id::text AS id, c.author_type, COALESCE(c.author_id::text, '') AS author_id,
           c.content, c.created_at
    FROM comment c
    WHERE c.workspace_id = $1::uuid AND c.issue_id = $2::uuid
      AND c.author_type IN ('member', 'agent') AND c.type = 'comment'
      AND c.content NOT LIKE '%' || $3 || '%'
    ORDER BY c.created_at DESC, c.id DESC
    LIMIT 200
) recent
ORDER BY created_at ASC, id ASC`, issue.WorkspaceID, issue.ID, watchdogCommentMarker)
	if err != nil {
		return reviewState{}, err
	}
	var comments []timelineComment
	for rows.Next() {
		var c timelineComment
		if err := rows.Scan(&c.ID, &c.Author.Type, &c.Author.ID, &c.Content, &c.CreatedAt); err != nil {
			rows.Close()
			return reviewState{}, err
		}
		comments = append(comments, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return reviewState{}, err
	}
	var statusChangedAt pgtype.Timestamptz
	if err := w.pool.QueryRow(ctx, `
SELECT max(occurred_at) FROM office_event
WHERE workspace_id = $1::uuid AND issue_id = $2::uuid AND event_type = 'issue.status_changed'`,
		issue.WorkspaceID, issue.ID).Scan(&statusChangedAt); err != nil {
		return reviewState{}, err
	}
	st := reviewTimeline(issue, comments)
	if st.Stage == stageAwaitingSignoff && statusChangedAt.Valid && statusChangedAt.Time.After(st.At.CreatedAt) {
		st.Stage = stageWorking
	}
	return st, nil
}

// reviewTimeline walks the comments oldest first. A verdict from anyone but
// the developer sets hold or awaiting-signoff; the developer's review request
// sets awaiting-review; the developer's other comments end a hold; a signed
// manager reply ends awaiting-signoff.
func reviewTimeline(issue Issue, comments []timelineComment) reviewState {
	developer := assigneeActor(issue)
	var (
		st           reviewState
		last         timelineComment
		failed       []timelineComment
		developerRan bool // the developer commented since the latest failed round
		asked        bool
	)
	for _, c := range comments {
		if isPureAcknowledgment(c.Content) {
			continue
		}
		last = c
		if !developer.empty() && c.Author == developer {
			developerRan = true
			if isReviewRequest(c.Content) {
				if reviewer := requestedReviewer(issue, developer, c.Content); !reviewer.empty() {
					st = reviewState{Stage: stageAwaitingReview, At: c, Reviewer: reviewer}
					asked = true
					continue
				}
			}
			if st.Stage == stageHold {
				st.Stage = stageWorking
			}
			continue
		}
		switch reviewVerdict(c.Content) {
		case "hold":
			st = reviewState{Stage: stageHold, At: c, Reviewer: c.Author}
			if len(failed) == 0 || developerRan {
				failed = append(failed, c)
				developerRan, asked = false, false
			}
		case "pass":
			st = reviewState{Stage: stageAwaitingSignoff, At: c, Reviewer: c.Author}
			failed, developerRan, asked = nil, false, false
		default:
			if st.Stage == stageAwaitingSignoff && c.Author.Type == "member" && isManagerReply(c.Content, issue.ManagerName) {
				st.Stage = stageWorking
			}
		}
	}
	st.LastIsStage = st.Stage != stageWorking && last.ID == st.At.ID
	st.FailedRounds, st.AskedSince = failed, asked
	return st
}

// requestedReviewer is the issue's reviewer field when set, else the first
// person the request @mentions other than the developer. A request that
// mentions nobody else is not a review request.
func requestedReviewer(issue Issue, developer actor, content string) actor {
	var first actor
	for _, a := range mentionedActors(content) {
		if a != developer {
			first = a
			break
		}
	}
	if first.empty() {
		return actor{}
	}
	if (issue.Reviewer.Type == "agent" || issue.Reviewer.Type == "member") && issue.Reviewer.ID != "" &&
		issue.Reviewer != developer {
		return issue.Reviewer
	}
	return first
}

// loadRuns summarizes agent tasks on the given issues.
func (w *Watchdog) loadRuns(ctx context.Context, issueIDs ...string) (issueRuns, error) {
	var (
		out             issueRuns
		agents          []string
		created, active pgtype.Timestamptz
	)
	err := w.pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE status = ANY($2)),
       COALESCE(array_agg(DISTINCT agent_id::text) FILTER (WHERE status = ANY($2)), '{}'),
       max(created_at),
       max(GREATEST(created_at, COALESCE(dispatched_at, created_at),
                    COALESCE(started_at, created_at), COALESCE(completed_at, created_at)))
FROM agent_task_queue
WHERE issue_id = ANY($1::uuid[])`, issueIDs, activeTaskStatuses).Scan(&out.Active, &agents, &created, &active)
	if err != nil {
		return issueRuns{}, err
	}
	out.ActiveAgents = make(map[string]bool, len(agents))
	for _, id := range agents {
		out.ActiveAgents[id] = true
	}
	if created.Valid {
		out.LastCreatedAt = created.Time
	}
	if active.Valid {
		out.LastActivityAt = active.Time
	}
	return out, nil
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// developerOrParentOwner is who fixes a hold: the assignee, unless there is
// none or the assignee is the reviewer, then the parent issue's assignee.
func (w *Watchdog) developerOrParentOwner(ctx context.Context, issue Issue, reviewer actor) (actor, string) {
	if developer := assigneeActor(issue); !developer.empty() && developer != reviewer {
		return developer, "开发员工"
	}
	if issue.ParentID == "" {
		return actor{}, ""
	}
	parent, err := w.loadIssue(ctx, issue.WorkspaceID, issue.ParentID)
	if err != nil {
		return actor{}, ""
	}
	return assigneeActor(parent), "父单负责人"
}

// holdTarget is the attempt log's name for who a hold wake went to.
func holdTarget(role string) string {
	if role == "父单负责人" {
		return "parent_owner"
	}
	return "developer"
}

func holdReason(role string, prefix string) string {
	if role == "父单负责人" {
		return "这张单没有开发员工可叫。" + prefix + "请指派开发员工或自己接手，按审查意见修改，改完后点名审查员复审。"
	}
	return prefix + "请按审查意见修改，改完后点名审查员复审。"
}

// wakeOnce claims key, then wakes target through the wake path. It reports
// whether this call claimed the key; a key already claimed is a no-op.
func (w *Watchdog) wakeOnce(ctx context.Context, issue Issue, key, kind, targetLabel string, target actor, role, reason string, now time.Time) (bool, error) {
	id, claimed, err := w.claim(ctx, issue, key, kind, targetLabel, issue.ManagerName, nil)
	if err != nil || !claimed {
		return false, err
	}
	wakeErr := errors.New("watchdog wake path is unavailable")
	if w.wake != nil {
		wakeErr = w.wake(ctx, issue.ID, target.Type, target.ID, role, reason)
	}
	w.finishAttempt(ctx, id, wakeErr == nil)
	if wakeErr != nil {
		return true, errors.New("issue watchdog review wake failed")
	}
	w.deferIdleWake(ctx, issue.ID, now)
	return true, nil
}

// deferIdleWake restarts the idle-wake clock so a review wake and an idle
// wake never land on the same issue together.
func (w *Watchdog) deferIdleWake(ctx context.Context, issueID string, now time.Time) {
	_, _ = w.pool.Exec(ctx, `
UPDATE office_issue_watchdog_state
SET last_progress_at = GREATEST(last_progress_at, $2),
    last_nudge_at = CASE WHEN last_nudge_at IS NULL THEN NULL ELSE GREATEST(last_nudge_at, $2) END,
    updated_at = now()
WHERE issue_id = $1::uuid`, issueID, now)
}

// onHold handles a hold as soon as it is posted. With RoundStop, a hold that
// makes RoundLimit failed rounds in a row stops the loop for Kun. Otherwise,
// with HoldWake, a hold that does not @mention whoever has to fix it wakes
// them.
func (w *Watchdog) onHold(ctx context.Context, issue Issue, commentID string, author actor, content string) {
	if commentID == "" || issue.KunLabel || !reviewWakeStatus(issue.Status) || reviewVerdict(content) != "hold" {
		return
	}
	if developer := assigneeActor(issue); developer == author {
		return
	}
	if w.cfg.RoundStop {
		st, err := w.loadReviewState(ctx, issue)
		if err != nil {
			return
		}
		if hold, stop := st.roundLimitHold(w.cfg.RoundLimit); stop {
			_ = w.stopForKun(ctx, issue, st, hold, w.now())
			return
		}
	}
	if !w.cfg.HoldWake {
		return
	}
	target, role := w.developerOrParentOwner(ctx, issue, author)
	if target.empty() || target == author || mentions(content, target) {
		return
	}
	if target.Type == "agent" {
		runs, err := w.loadRuns(ctx, issue.ID)
		if err != nil || runs.ActiveAgents[target.ID] {
			return
		}
	}
	_, _ = w.wakeOnce(ctx, issue, "hold-wake:"+commentID, "hold_wake", holdTarget(role), target, role,
		holdReason(role, "审查员给了 hold，但没有点名你。"), w.now())
}

// stopForKun lays a review loop that hit RoundLimit out as options for Kun: a
// comment on the issue that @mentions nobody, so no run starts, and a manager
// event so the responsible manager takes it to Kun. One hold, one stop.
func (w *Watchdog) stopForKun(ctx context.Context, issue Issue, st reviewState, hold timelineComment, now time.Time) error {
	id, claimed, err := w.claim(ctx, issue, "round-limit:"+hold.ID, "review_round_limit", "kun", issue.ManagerName, nil)
	if err != nil || !claimed {
		return err
	}
	noteErr := errors.New("watchdog note path is unavailable")
	if w.note != nil {
		noteErr = w.note(ctx, issue.ID, roundLimitNote(issue, st.FailedRounds))
	}
	w.finishAttempt(ctx, id, noteErr == nil)
	managerErr := w.notifyManager(ctx, issue, "review_round_limit", "round-limit-manager:"+hold.ID,
		fmt.Sprintf("审查连续 %d 轮不通过，看门狗已停止提醒并在单子里列出选项，请交 Kun 拍板", len(st.FailedRounds)),
		"manager", now)
	if noteErr != nil {
		return errors.New("issue watchdog round-limit note failed")
	}
	return managerErr
}

func roundLimitNote(issue Issue, rounds []timelineComment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "审查已连续 %d 轮不通过，按规矩停止来回修改，请 Kun 拍板。Kun 决定前，看门狗不再叫醒这张单上的任何人。\n\n各轮审查结论：\n", len(rounds))
	for i, r := range rounds {
		fmt.Fprintf(&b, "%d. %s\n", i+1, verdictSummary(r.Content))
	}
	manager := issue.ManagerName
	if manager == "" {
		manager = "负责经理"
	}
	b.WriteString("\n可选：\n")
	b.WriteString("A. 再改一轮：开发员工按最后一轮意见修改后点名复审；再不通过，看门狗会再停下来找 Kun。\n")
	b.WriteString("B. 先合并：接受当前提交，剩下的意见另开小单跟进。\n")
	fmt.Fprintf(&b, "C. 请 %s 裁决：对照分歧点定下怎么改，或换一位审查员。\n", manager)
	b.WriteString("D. 放弃这个改动：取消这张单，或换方案重新开单。\n\n")
	b.WriteString("选 A 时，开发员工改完点名复审，看门狗即恢复提醒；B、C、D 由负责经理按决定处理。")
	return b.String()
}

// verdictSummary is a hold's first line, without its signature, cut to 120
// characters.
func verdictSummary(content string) string {
	text := stripSignature(strings.TrimSpace(strings.ReplaceAll(content, "**", "")))
	line, _, _ := strings.Cut(text, "\n")
	line = strings.TrimSpace(line)
	if r := []rune(line); len(r) > 120 {
		line = string(r[:120]) + "…"
	}
	return line
}

// checkReviewWakes runs the scan half of the review rules. handled means a
// rule owns this issue for this scan and the idle wake must stay quiet;
// stuckDue, when set, moves the developer's first idle wake earlier.
func (w *Watchdog) checkReviewWakes(ctx context.Context, issue Issue, now time.Time) (bool, *time.Time, error) {
	if !(w.cfg.HoldWake || w.cfg.ReviewWake || w.cfg.StuckWake || w.cfg.RoundStop) || !reviewWakeStatus(issue.Status) {
		return false, nil, nil
	}
	st, err := w.loadReviewState(ctx, issue)
	if err != nil {
		return false, nil, err
	}
	if w.cfg.RoundStop {
		if hold, stop := st.roundLimitHold(w.cfg.RoundLimit); stop {
			return true, nil, w.stopForKun(ctx, issue, st, hold, now)
		}
	}
	runs, err := w.loadRuns(ctx, issue.ID)
	if err != nil {
		return false, nil, err
	}

	if w.cfg.HoldWake && st.Stage == stageHold && st.LastIsStage && runs.Active == 0 &&
		!runs.LastCreatedAt.After(st.At.CreatedAt) && now.Sub(st.At.CreatedAt) >= w.cfg.HoldWait {
		if target, role := w.developerOrParentOwner(ctx, issue, st.Reviewer); !target.empty() {
			prefix := fmt.Sprintf("审查结论是 hold，已过 %d 分钟没有任何运行。", int(now.Sub(st.At.CreatedAt).Minutes()))
			sent, err := w.wakeOnce(ctx, issue, "hold-unanswered:"+st.At.ID, "hold_unanswered", holdTarget(role),
				target, role, holdReason(role, prefix), now)
			if sent || err != nil {
				return true, nil, err
			}
		}
	}

	if w.cfg.ReviewWake && st.Stage == stageAwaitingReview && now.Sub(st.At.CreatedAt) >= w.cfg.ReviewWait &&
		!(st.Reviewer.Type == "agent" && runs.ActiveAgents[st.Reviewer.ID]) {
		reason := fmt.Sprintf("开发员工 %d 分钟前请你复审，还没有结论。请给出结论（verdict: pass 或 hold）。",
			int(now.Sub(st.At.CreatedAt).Minutes()))
		sent, err := w.wakeOnce(ctx, issue, "review-overdue:"+st.At.ID, "review_overdue", "reviewer",
			st.Reviewer, "审查员", reason, now)
		if sent || err != nil {
			return true, nil, err
		}
	}

	if !w.cfg.StuckWake || issue.Status != "in_progress" || runs.Active > 0 {
		return false, nil, nil
	}
	anchor := later(runs.LastActivityAt, issue.ProgressAt)
	idle := now.Sub(anchor)
	switch st.Stage {
	case stageAwaitingReview:
		if idle >= w.cfg.NoRunWait {
			reason := fmt.Sprintf("这张单在等你复审，已 %d 分钟没有任何运行。请给出结论（verdict: pass 或 hold）。", int(idle.Minutes()))
			_, err := w.wakeOnce(ctx, issue, fmt.Sprintf("stuck-reviewer:%s:%d", st.At.ID, anchor.UnixNano()),
				"stuck_reviewer", "reviewer", st.Reviewer, "审查员", reason, now)
			return true, nil, err
		}
		return true, nil, nil
	case stageAwaitingSignoff:
		if idle >= w.cfg.NoRunWait {
			err := w.notifyManager(ctx, issue, "awaiting_signoff", "awaiting-signoff:"+st.At.ID,
				fmt.Sprintf("审查已通过，单子在等签字，已 %d 分钟没有任何运行", int(idle.Minutes())), "manager", now)
			return true, nil, err
		}
		return true, nil, nil
	}
	due := anchor.Add(w.cfg.NoRunWait)
	return false, &due, nil
}

// checkChild wakes the parent's owner, on the parent, when a sub-issue has
// had no progress and no run for ChildWait and nobody is looking after it:
// the sub-issue has no assignee, or the parent's owner has not moved on
// either issue for ChildWait. A sub-issue owned by the parent's owner is left
// to the ordinary idle wake.
func (w *Watchdog) checkChild(ctx context.Context, child Issue, now time.Time) error {
	switch child.Status {
	case "todo", "in_progress", "blocked":
	default:
		return nil
	}
	runs, err := w.loadRuns(ctx, child.ID)
	if err != nil || runs.Active > 0 {
		return err
	}
	anchor := later(runs.LastActivityAt, child.ProgressAt)
	stalled := now.Sub(anchor)
	if stalled < w.cfg.ChildWait {
		return nil
	}
	if parked, err := w.parkedForKun(ctx, child); err != nil || parked {
		return err // waits on Kun, not stalled
	}
	parent, err := w.loadIssue(ctx, child.WorkspaceID, child.ParentID)
	if err != nil {
		return err
	}
	if parent.Status == "done" || parent.Status == "cancelled" {
		return nil
	}
	owner := assigneeActor(parent)
	childOwner := assigneeActor(child)
	if !childOwner.empty() && childOwner == owner {
		return nil
	}
	parentIdle := false
	if !owner.empty() {
		busy, lastMoved, err := w.ownerActivity(ctx, owner, parent.ID, child.ID)
		if err != nil || busy {
			return err
		}
		parentIdle = now.Sub(lastMoved) >= w.cfg.ChildWait
	}
	if !childOwner.empty() && !parentIdle {
		return nil
	}
	minutes := int(stalled.Minutes())
	key := fmt.Sprintf("child-stalled:%s:%d", child.ID, anchor.UnixNano())
	if owner.empty() {
		return w.notifyManager(ctx, child, "child_unattended", key,
			fmt.Sprintf("子单已 %d 分钟没有进展也没人在跑，父单也没有负责人", minutes), "manager", now)
	}
	// A parent waiting on Kun wakes nobody, so the manager hears about the
	// child instead.
	if parked, err := w.parkedForKun(ctx, parent); err != nil {
		return err
	} else if parked {
		why := "父单审查连续不通过、正等 Kun 拍板"
		if parent.KunLabel {
			why = "父单正等 Kun 拍板"
		}
		return w.notifyManager(ctx, child, "child_unattended", key,
			fmt.Sprintf("子单已 %d 分钟没有进展也没人在跑，%s，看门狗不叫醒父单上的人", minutes, why),
			"manager", now)
	}
	cause := "子单没有负责人"
	if !childOwner.empty() {
		cause = "子单负责人没在动，你这边也没跟进"
	}
	reason := fmt.Sprintf("子单 %s「%s」已 %d 分钟没有进展，也没人在跑（%s）。请指派负责人或亲自推进。",
		child.Identifier, child.Title, minutes, cause)
	_, err = w.wakeOnce(ctx, parent, key, "child_unattended", "parent_owner", owner, "父单负责人", reason, now)
	return err
}

// parkedForKun reports whether issue waits on Kun: it carries the
// Kun-decision label, or RoundStop has stopped its review loop.
func (w *Watchdog) parkedForKun(ctx context.Context, issue Issue) (bool, error) {
	if issue.KunLabel {
		return true, nil
	}
	if !w.cfg.RoundStop {
		return false, nil
	}
	st, err := w.loadReviewState(ctx, issue)
	if err != nil {
		return false, err
	}
	_, stop := st.roundLimitHold(w.cfg.RoundLimit)
	return stop, nil
}

// ownerActivity reports whether owner has a live task on either issue, and
// the last time owner commented on or ran either issue.
func (w *Watchdog) ownerActivity(ctx context.Context, owner actor, issueIDs ...string) (bool, time.Time, error) {
	var (
		busy      bool
		lastMoved pgtype.Timestamptz
	)
	err := w.pool.QueryRow(ctx, `
SELECT EXISTS (
         SELECT 1 FROM agent_task_queue t
         WHERE $1 = 'agent' AND t.agent_id = $2::uuid AND t.issue_id = ANY($3::uuid[])
           AND t.status = ANY($4)),
       GREATEST(
         (SELECT max(c.created_at) FROM comment c
          WHERE c.issue_id = ANY($3::uuid[]) AND c.author_type = $1 AND c.author_id = $2::uuid
            AND c.type = 'comment'
            AND regexp_replace(lower(trim(c.content)),
                '[[:space:][:punct:]，。！？、；：…]+', '', 'g') <> '收到'),
         (SELECT max(GREATEST(t.created_at, COALESCE(t.started_at, t.created_at), COALESCE(t.completed_at, t.created_at)))
          FROM agent_task_queue t
          WHERE $1 = 'agent' AND t.agent_id = $2::uuid AND t.issue_id = ANY($3::uuid[])))`,
		owner.Type, owner.ID, issueIDs, activeTaskStatuses).Scan(&busy, &lastMoved)
	if err != nil {
		return false, time.Time{}, err
	}
	if !lastMoved.Valid {
		return busy, time.Time{}, nil
	}
	return busy, lastMoved.Time, nil
}
