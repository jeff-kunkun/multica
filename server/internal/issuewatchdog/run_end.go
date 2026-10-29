package issuewatchdog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/service"
)

// Stop routing (DENE-2740): when the assignee's run on an issue ends, where
// the run left the issue says who moves it next. Each ended run is routed
// once: the run-end:<task> claim marks it routed, so the live task event and
// the catch-up scan never both act, and every action keeps its own key.
const (
	// runEndSettle is how long a run that left its issue in_progress waits
	// before it counts as having delivered nothing: completing a run can queue
	// a follow-up run for comments that arrived during it, just after the
	// task event.
	runEndSettle = 30 * time.Second
	// runEndLookback is how far back the catch-up scan reads ended runs.
	runEndLookback = time.Hour
	runEndBatch    = 200
)

// Route names, recorded as the run-end claim's target.
const (
	routeClosed           = "closed"
	routeNotAssignee      = "not_assignee"
	routeBusy             = "busy"
	routeKun              = "kun"
	routeFailed           = "failed"
	routeReview           = "review"
	routeKunDecision      = "kun_decision"
	routeDependency       = "dependency"
	routeBlocked          = "blocked"
	routeNeedsInstruction = "needs_instruction"
	routeParentWaiting    = "parent_waiting"
	routeNoDelivery       = "no_delivery"
	routeIdle             = "idle"
)

// LabelFunc attaches the workspace's issue label with this name to an issue.
type LabelFunc func(ctx context.Context, issueID, label string) error

// SetLabel sets the label path the stop routing uses for the Kun label.
func (w *Watchdog) SetLabel(label LabelFunc) { w.label = label }

type runEnd struct {
	TaskID        string
	AgentID       string
	Status        string
	FailureReason string
	MaxAttempts   int
	AutoRetry     bool
	StartedAt     time.Time
	EndedAt       time.Time
	// Superseded: another run on the issue is live, or was queued at or
	// after this one ended (a retry is queued in the same transaction).
	Superseded bool
	// StatusChanged: the issue changed status while the run was going.
	StatusChanged bool
	BlockKind     string
	WaitingFor    string
	// LastComment is the assignee's last substantive comment in the run.
	LastComment string
}

func (w *Watchdog) loadRunEnd(ctx context.Context, issue Issue, taskID string) (runEnd, error) {
	var (
		run      runEnd
		maxTries int32
	)
	err := w.pool.QueryRow(ctx, `
WITH t AS (
    SELECT t.id, t.issue_id, t.agent_id, t.status, COALESCE(t.failure_reason, '') AS failure_reason,
           t.max_attempts, COALESCE(a.auto_retry_enabled, true) AS auto_retry,
           COALESCE(t.started_at, t.dispatched_at, t.created_at) AS started_at,
           COALESCE(t.completed_at, now()) AS ended_at
    FROM agent_task_queue t
    LEFT JOIN agent a ON a.id = t.agent_id
    WHERE t.id = $2::uuid AND t.issue_id = $3::uuid
)
SELECT t.agent_id::text, t.status, t.failure_reason, t.max_attempts, t.auto_retry, t.started_at, t.ended_at,
       EXISTS (SELECT 1 FROM agent_task_queue o
               WHERE o.issue_id = t.issue_id AND o.id <> t.id
                 AND (o.status = ANY($4) OR o.created_at >= t.ended_at)),
       EXISTS (SELECT 1 FROM office_event e
               WHERE e.workspace_id = $1::uuid AND e.issue_id = t.issue_id
                 AND e.event_type = 'issue.status_changed'
                 AND e.occurred_at >= t.started_at AND e.occurred_at <= t.ended_at),
       COALESCE(i.metadata->>$5, ''), COALESCE(i.metadata->>$6, ''),
       COALESCE((SELECT c.content FROM comment c
                 WHERE c.workspace_id = i.workspace_id AND c.issue_id = i.id
                   AND c.author_type = 'agent' AND c.author_id = t.agent_id AND c.type = 'comment'
                   AND c.created_at >= t.started_at AND c.created_at <= t.ended_at
                 ORDER BY c.created_at DESC, c.id DESC LIMIT 1), '')
FROM t JOIN issue i ON i.id = t.issue_id`,
		issue.WorkspaceID, taskID, issue.ID, activeTaskStatuses,
		closeprotocol.KeyBlockKind, service.MetaKeyBacklogWaitingFor,
	).Scan(&run.AgentID, &run.Status, &run.FailureReason, &maxTries, &run.AutoRetry, &run.StartedAt,
		&run.EndedAt, &run.Superseded, &run.StatusChanged, &run.BlockKind, &run.WaitingFor, &run.LastComment)
	if err != nil {
		return runEnd{}, err
	}
	run.TaskID = taskID
	run.MaxAttempts = int(maxTries)
	return run, nil
}

// runEndRoute picks where an ended run goes. It returns "" when the run left
// its issue in_progress and has not settled yet.
func runEndRoute(issue Issue, run runEnd, settled bool) string {
	switch {
	case issue.Status == "done" || issue.Status == "cancelled":
		return routeClosed
	case issue.AssigneeType != "agent" || issue.AssigneeID != run.AgentID:
		return routeNotAssignee
	case run.Superseded:
		return routeBusy
	case issue.KunLabel:
		return routeKun
	}
	if run.Status != "completed" {
		if issue.Status != "in_review" {
			return routeFailed
		}
		if run.StatusChanged {
			return routeReview
		}
		return routeIdle
	}
	switch issue.Status {
	case "in_review":
		if run.StatusChanged {
			return routeReview
		}
	case "blocked":
		if !run.StatusChanged {
			return routeBlocked
		}
		switch run.BlockKind {
		case closeprotocol.BlockDecision, closeprotocol.BlockPermission:
			return routeKunDecision
		case closeprotocol.BlockDependency:
			return routeDependency
		}
		return routeBlocked
	case "todo", "backlog":
		if run.StatusChanged {
			return routeNeedsInstruction
		}
	case "in_progress":
		if !settled {
			return ""
		}
		return routeNoDelivery
	}
	return routeIdle
}

// routeRunEnd routes one ended run. settled is false on the live task event,
// which leaves a run that ended in_progress to the catch-up scan.
func (w *Watchdog) routeRunEnd(ctx context.Context, issueID, taskID string, settled bool, now time.Time) error {
	issue, err := w.loadIssue(ctx, w.cfg.WorkspaceID, issueID)
	if err != nil {
		return err
	}
	run, err := w.loadRunEnd(ctx, issue, taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	route := runEndRoute(issue, run, settled)
	if route == routeNoDelivery {
		open, err := w.openChildren(ctx, issue)
		if err != nil {
			return err
		}
		if open > 0 {
			route = routeParentWaiting
		}
	}
	if route == "" {
		return nil
	}
	id, claimed, err := w.claim(ctx, issue, "run-end:"+taskID, "run_end", route, issue.ManagerName, nil)
	if err != nil || !claimed {
		return err
	}
	err = w.actOnRunEnd(ctx, issue, run, route, now)
	w.finishAttempt(ctx, id, err == nil)
	return err
}

func (w *Watchdog) actOnRunEnd(ctx context.Context, issue Issue, run runEnd, route string, now time.Time) error {
	switch route {
	case routeReview:
		// With no reviewer, the in_review status alert already asks the
		// responsible manager to accept the issue.
		reviewer := issue.Reviewer
		if (reviewer.Type != "agent" && reviewer.Type != "member") || reviewer.ID == "" || reviewer == assigneeActor(issue) {
			return nil
		}
		_, err := w.wakeOnce(ctx, issue, "run-review:"+run.TaskID, "delivered_review", "reviewer", reviewer, "审查员",
			"负责员工已交付并转为审核中，请审查并给出结论（verdict: pass 或 hold）。", now)
		return err
	case routeKunDecision:
		return w.labelForKun(ctx, issue, run, now)
	case routeFailed:
		reason := "负责员工的运行中断，自动重跑已用完或这类中断不重跑，现在没人在跑。请决定重派、补指示还是交 Kun"
		if run.FailureReason != "" {
			reason = fmt.Sprintf("负责员工的运行中断（%s），自动重跑已用完或这类中断不重跑，现在没人在跑。请决定重派、补指示还是交 Kun", run.FailureReason)
		}
		if run.Status == "cancelled" {
			reason = "负责员工的运行被取消，现在没人在跑。请决定重派还是暂停"
		}
		return w.notifyManager(ctx, issue, "run_failed", "run-failed:"+run.TaskID, reason, "manager", now)
	case routeNeedsInstruction:
		return w.askForInstruction(ctx, issue, run, now)
	case routeParentWaiting:
		return w.notifyManager(ctx, issue, "parent_waiting", "parent-waiting:"+run.TaskID,
			"负责员工停下时父单还在进行中，手上只剩没做完的子单。请把父单改回待办，等子单做完再接着做", "manager", now)
	case routeNoDelivery:
		return w.rerunOrStop(ctx, issue, run, now)
	}
	return nil
}

// labelForKun puts the Kun label on an issue its assignee blocked on a
// decision or a permission, then pushes it to the Kun group.
func (w *Watchdog) labelForKun(ctx context.Context, issue Issue, run runEnd, now time.Time) error {
	if w.cfg.KunLabel == "" {
		return nil
	}
	id, claimed, err := w.claim(ctx, issue, "run-kun:"+run.TaskID, "kun_auto_label", "kun_label", issue.ManagerName, nil)
	if err != nil || !claimed {
		return err
	}
	labelErr := errors.New("watchdog label path is unavailable")
	if w.label != nil {
		labelErr = w.label(ctx, issue.ID, w.cfg.KunLabel)
	}
	w.finishAttempt(ctx, id, labelErr == nil)
	if labelErr != nil {
		return errors.New("issue watchdog Kun label failed")
	}
	issue, err = w.loadIssue(ctx, issue.WorkspaceID, issue.ID)
	if err != nil {
		return err
	}
	return w.checkKunDecision(ctx, issue, now)
}

// askForInstruction leaves the issue where its assignee put it, writes on it
// what is missing, and calls the responsible manager.
func (w *Watchdog) askForInstruction(ctx context.Context, issue Issue, run runEnd, now time.Time) error {
	missing := strings.TrimSpace(run.WaitingFor)
	if missing == "" && run.LastComment != "" {
		missing = "没单独写，负责员工最后说：" + verdictSummary(run.LastComment)
	}
	if missing == "" {
		missing = "负责员工没写"
	}
	where := "待办"
	if issue.Status == "backlog" {
		where = "暂缓"
	}
	id, claimed, err := w.claim(ctx, issue, "run-instruction-note:"+run.TaskID, "needs_instruction_note", "issue", issue.ManagerName, nil)
	if err != nil {
		return err
	}
	var noteErr error
	if claimed {
		noteErr = errors.New("watchdog note path is unavailable")
		if w.note != nil {
			noteErr = w.note(ctx, issue.ID, fmt.Sprintf(
				"负责员工停下时把单子放回了%s，缺指示才能继续。\n\n缺什么：%s\n\n请负责经理补指示或改派；补完把单子改回待办并点名负责员工。", where, missing))
		}
		w.finishAttempt(ctx, id, noteErr == nil)
	}
	managerErr := w.notifyManager(ctx, issue, "needs_instruction", "run-instruction:"+run.TaskID,
		fmt.Sprintf("负责员工把单子放回了%s，缺指示：%s。请补指示或改派", where, clipRunes(missing, reasonCommandMaxRunes)), "manager", now)
	if noteErr != nil {
		return errors.New("issue watchdog instruction note failed")
	}
	return managerErr
}

// rerunOrStop handles a run that ended with its issue still in_progress and
// nobody running: whoever the review stage waits on is woken, else the
// assignee is rerun up to its run's max_attempts-1 times per status, then the
// responsible manager is called. The watchdog never changes the status.
func (w *Watchdog) rerunOrStop(ctx context.Context, issue Issue, run runEnd, now time.Time) error {
	st, err := w.loadReviewState(ctx, issue)
	if err != nil {
		return err
	}
	switch st.Stage {
	case stageAwaitingReview:
		_, err := w.wakeOnce(ctx, issue, "run-end-reviewer:"+st.At.ID, "stuck_reviewer", "reviewer", st.Reviewer, "审查员",
			"开发员工请你审查后停下了，单子在等你。请给出结论（verdict: pass 或 hold）。", now)
		return err
	case stageAwaitingSignoff:
		return w.notifyManager(ctx, issue, "awaiting_signoff", "awaiting-signoff:"+st.At.ID,
			"审查已通过，负责员工停下了，单子在等签字", "manager", now)
	}
	anchor, err := w.statusChangedAt(ctx, issue)
	if err != nil {
		return err
	}
	prefix := fmt.Sprintf("no-delivery-rerun:%s:%d:", issue.ID, anchor.UnixNano())
	var reruns int
	if err := w.pool.QueryRow(ctx, `
SELECT count(*) FROM office_issue_watchdog_attempt
WHERE workspace_id = $1::uuid AND issue_id = $2::uuid AND event_key LIKE $3 || '%'`,
		issue.WorkspaceID, issue.ID, prefix).Scan(&reruns); err != nil {
		return err
	}
	budget := run.MaxAttempts - 1
	if !run.AutoRetry {
		budget = 0
	}
	if reruns < budget {
		_, err := w.wakeOnce(ctx, issue, prefix+strconv.Itoa(reruns+1), "no_delivery_rerun", "developer",
			assigneeActor(issue), "负责员工",
			"你上一轮停下时单子还在进行中，没交东西也没改状态。请接着做完：做完就转审核中并点名审查员；卡住就改成阻塞，写清卡在谁、要谁做什么。", now)
		return err
	}
	return w.notifyManager(ctx, issue, "no_delivery", fmt.Sprintf("no-delivery:%s:%d", issue.ID, anchor.UnixNano()),
		fmt.Sprintf("负责员工停下时单子还在进行中，没交东西也没改状态，自动重跑 %d 次后又停了。请决定改指示、换人还是改成阻塞", reruns),
		"manager", now)
}

// statusChangedAt is when the issue last changed status, or its creation.
func (w *Watchdog) statusChangedAt(ctx context.Context, issue Issue) (time.Time, error) {
	var at pgtype.Timestamptz
	if err := w.pool.QueryRow(ctx, `
SELECT max(occurred_at) FROM office_event
WHERE workspace_id = $1::uuid AND issue_id = $2::uuid AND event_type = 'issue.status_changed'`,
		issue.WorkspaceID, issue.ID).Scan(&at); err != nil {
		return time.Time{}, err
	}
	if at.Valid {
		return at.Time, nil
	}
	return issue.CreatedAt, nil
}

func (w *Watchdog) openChildren(ctx context.Context, issue Issue) (int, error) {
	var n int
	err := w.pool.QueryRow(ctx, `
SELECT count(*) FROM issue
WHERE workspace_id = $1::uuid AND parent_issue_id = $2::uuid AND status NOT IN ('done', 'cancelled')`,
		issue.WorkspaceID, issue.ID).Scan(&n)
	return n, err
}

// catchUpRunEnds routes ended runs the live event missed (a restart, a
// failed load) and every run that left its issue in_progress, once settled.
func (w *Watchdog) catchUpRunEnds(ctx context.Context, now time.Time) error {
	rows, err := w.pool.Query(ctx, `
SELECT e.issue_id::text, e.task_id::text
FROM issue i
JOIN office_event e ON e.issue_id = i.id AND e.workspace_id = i.workspace_id
WHERE i.workspace_id = $1::uuid AND i.status NOT IN ('done', 'cancelled')
  AND e.event_type IN ('run.completed', 'run.failed', 'run.cancelled')
  AND e.task_id IS NOT NULL
  AND e.occurred_at >= $2 AND e.occurred_at < $3
  AND NOT EXISTS (SELECT 1 FROM office_issue_watchdog_attempt a
                  WHERE a.workspace_id = i.workspace_id AND a.issue_id = i.id
                    AND a.event_key = 'run-end:' || e.task_id::text)
ORDER BY e.occurred_at ASC
LIMIT $4`, w.cfg.WorkspaceID, now.Add(-runEndLookback), now.Add(-runEndSettle), runEndBatch)
	if err != nil {
		return err
	}
	type pending struct{ issueID, taskID string }
	var runs []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.issueID, &p.taskID); err != nil {
			rows.Close()
			return err
		}
		runs = append(runs, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range runs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.routeRunEnd(ctx, p.issueID, p.taskID, true, now); err != nil {
			slog.WarnContext(ctx, "issue watchdog run-end routing failed", "issue_id", p.issueID, "task_id", p.taskID)
		}
	}
	return nil
}

// checkOfflineRuns calls the responsible manager once per run whose runtime
// has been offline for OfflineWait. The runtime keeps its reconnect grace;
// this only tells someone the run is stuck in the meantime.
func (w *Watchdog) checkOfflineRuns(ctx context.Context, now time.Time) error {
	rows, err := w.pool.Query(ctx, `
SELECT t.id::text, t.issue_id::text, COALESCE(NULLIF(rt.custom_name, ''), rt.name),
       GREATEST(COALESCE(rt.last_seen_at, rt.updated_at), rt.updated_at)
FROM agent_task_queue t
JOIN agent_runtime rt ON rt.id = t.runtime_id
JOIN issue i ON i.id = t.issue_id
WHERE i.workspace_id = $1::uuid AND i.status NOT IN ('done', 'cancelled')
  AND t.status IN ('dispatched', 'running', 'waiting_local_directory')
  AND rt.status = 'offline'
  AND GREATEST(COALESCE(rt.last_seen_at, rt.updated_at), rt.updated_at) < $2
ORDER BY t.id
LIMIT $3`, w.cfg.WorkspaceID, now.Add(-w.cfg.OfflineWait), runEndBatch)
	if err != nil {
		return err
	}
	type stranded struct {
		taskID, issueID, runtime string
		seen                     time.Time
	}
	var runs []stranded
	for rows.Next() {
		var s stranded
		if err := rows.Scan(&s.taskID, &s.issueID, &s.runtime, &s.seen); err != nil {
			rows.Close()
			return err
		}
		runs = append(runs, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, s := range runs {
		issue, err := w.loadIssue(ctx, w.cfg.WorkspaceID, s.issueID)
		if err != nil {
			continue
		}
		reason := fmt.Sprintf("负责员工所在的本机「%s」已掉线 %d 分钟，手上的运行停在半路。本机回来会接着跑；等不了就请改派，或叫 Kun 看本机",
			s.runtime, int(now.Sub(s.seen).Minutes()))
		if err := w.notifyManager(ctx, issue, "runtime_offline", "runtime-offline:"+s.taskID, reason, "manager", now); err != nil {
			slog.WarnContext(ctx, "issue watchdog offline-run alert failed", "issue_id", issue.ID, "task_id", s.taskID)
		}
	}
	return nil
}

var (
	issueRefPattern = regexp.MustCompile(`(?i)\b([a-z][a-z0-9]*)-(\d+)\b`)
	// A mention link's text repeats the number; the whole link is one ref.
	issueMentionPattern = regexp.MustCompile(`(?:\[[^\]]*\]\()?mention://issue/([0-9a-fA-F-]{36})\)?`)
)

// checkDependencyWaits wakes the assignee of each issue blocked on a
// dependency once every issue its block action names is done or cancelled.
// The action must name them by number (DENE-12) or issue mention; one that
// names none is left alone.
func (w *Watchdog) checkDependencyWaits(ctx context.Context, now time.Time) error {
	var prefix string
	if err := w.pool.QueryRow(ctx, `SELECT issue_prefix FROM workspace WHERE id = $1::uuid`,
		w.cfg.WorkspaceID).Scan(&prefix); err != nil {
		return err
	}
	rows, err := w.pool.Query(ctx, `
SELECT id::text, COALESCE(metadata->>$2, '')
FROM issue
WHERE workspace_id = $1::uuid AND status = 'blocked' AND metadata->>$3 = $4
ORDER BY updated_at ASC
LIMIT $5`, w.cfg.WorkspaceID, closeprotocol.KeyBlockAction, closeprotocol.KeyBlockKind,
		closeprotocol.BlockDependency, runEndBatch)
	if err != nil {
		return err
	}
	type waiting struct{ id, action string }
	var blocked []waiting
	for rows.Next() {
		var b waiting
		if err := rows.Scan(&b.id, &b.action); err != nil {
			rows.Close()
			return err
		}
		blocked = append(blocked, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, b := range blocked {
		if err := w.wakeIfDependenciesDone(ctx, b.id, b.action, prefix, now); err != nil {
			slog.WarnContext(ctx, "issue watchdog dependency wake failed", "issue_id", b.id)
		}
	}
	return nil
}

func (w *Watchdog) wakeIfDependenciesDone(ctx context.Context, issueID, action, prefix string, now time.Time) error {
	numbers, ids := dependencyRefs(action, prefix)
	if len(numbers) == 0 && len(ids) == 0 {
		return nil
	}
	rows, err := w.pool.Query(ctx, `
SELECT i.id::text, i.number, ws.issue_prefix || '-' || i.number::text, i.status
FROM issue i JOIN workspace ws ON ws.id = i.workspace_id
WHERE i.workspace_id = $1::uuid AND i.id <> $2::uuid
  AND (i.number = ANY($3::int[]) OR i.id = ANY($4::uuid[]))`,
		w.cfg.WorkspaceID, issueID, numbers, ids)
	if err != nil {
		return err
	}
	// One issue may be named both by number and by mention, so a name counts
	// as found when any returned row carries it, not by counting rows.
	var finished []string
	gotNumbers, gotIDs := map[int32]bool{}, map[string]bool{}
	open := false
	for rows.Next() {
		var id, ident, status string
		var number int32
		if err := rows.Scan(&id, &number, &ident, &status); err != nil {
			rows.Close()
			return err
		}
		gotNumbers[number], gotIDs[id] = true, true
		if status == "done" || status == "cancelled" {
			finished = append(finished, ident)
		} else {
			open = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if open {
		return nil
	}
	for _, n := range numbers {
		if !gotNumbers[n] {
			return nil
		}
	}
	for _, id := range ids {
		if !gotIDs[id] {
			return nil
		}
	}
	issue, err := w.loadIssue(ctx, w.cfg.WorkspaceID, issueID)
	if err != nil || issue.Status != "blocked" || issue.KunLabel {
		return err
	}
	anchor, err := w.statusChangedAt(ctx, issue)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("dependency-done:%s:%d", issue.ID, anchor.UnixNano())
	reason := fmt.Sprintf("这张单在等的 %s 已经做完了。请核实后接着做，把状态改回进行中；还不能继续，就写清还在等什么。",
		strings.Join(finished, "、"))
	assignee := assigneeActor(issue)
	if assignee.empty() {
		return w.notifyManager(ctx, issue, "dependency_done", key, "单子等的 "+strings.Join(finished, "、")+" 已经做完，但没有负责人可叫，请安排人接着做", "manager", now)
	}
	if assignee.Type == "agent" {
		runs, err := w.loadRuns(ctx, issue.ID)
		if err != nil || runs.ActiveAgents[assignee.ID] {
			return err
		}
	}
	_, err = w.wakeOnce(ctx, issue, key, "dependency_done", "assignee", assignee, "负责员工", reason, now)
	return err
}

// dependencyRefs reads the issues a block action names: numbers carrying the
// workspace's prefix, and issue mention ids.
func dependencyRefs(action, prefix string) ([]int32, []string) {
	var (
		numbers []int32
		ids     []string
		seen    = map[string]bool{}
	)
	for _, m := range issueMentionPattern.FindAllStringSubmatch(action, -1) {
		id := strings.ToLower(m[1])
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	text := issueMentionPattern.ReplaceAllString(action, "")
	for _, m := range issueRefPattern.FindAllStringSubmatch(text, -1) {
		if !strings.EqualFold(m[1], prefix) || seen[m[2]] {
			continue
		}
		n, err := strconv.ParseInt(m[2], 10, 32)
		if err != nil {
			continue
		}
		seen[m[2]] = true
		numbers = append(numbers, int32(n))
	}
	return numbers, ids
}
