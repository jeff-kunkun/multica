package issuewatchdog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// endRun starts a run of agentID on issueID ten minutes ago that reports
// progress (so the ordinary idle wake stays quiet), lets during act as the
// run (status changes, comments), then ends it with status the way the
// platform does, which writes the run.* office event. With w set, the task
// event is delivered to the watchdog too.
func (b *reviewBench) endRun(w *Watchdog, issueID, agentID, status, failureReason string, during func()) string {
	b.t.Helper()
	taskID := b.fx.Task(b.t, agentID, testutil.Cols{
		"issue_id": issueID, "runtime_id": b.runtimeID, "status": "running",
		"created_at": b.now.Add(-10 * time.Minute), "dispatched_at": b.now.Add(-10 * time.Minute),
		"started_at": b.now.Add(-10 * time.Minute),
	})
	b.comment(issueID, agentID, "进展：在做", 6*time.Minute)
	if during != nil {
		during()
	}
	if _, err := b.pool.Exec(b.ctx, `
UPDATE agent_task_queue SET status = $2, completed_at = clock_timestamp(), failure_reason = NULLIF($3, '')
WHERE id = $1::uuid`, taskID, status, failureReason); err != nil {
		b.t.Fatal(err)
	}
	if w != nil {
		b.taskEvent(w, issueID, taskID, agentID, status)
	}
	return taskID
}

func (b *reviewBench) taskEvent(w *Watchdog, issueID, taskID, agentID, status string) {
	eventType := map[string]string{
		"completed": protocol.EventTaskCompleted, "failed": protocol.EventTaskFailed, "cancelled": protocol.EventTaskCancelled,
	}[status]
	w.HandleEvent(events.Event{
		Type: eventType, WorkspaceID: b.workspaceID, ActorType: "system",
		Payload: map[string]any{"task_id": taskID, "agent_id": agentID, "issue_id": issueID, "status": status},
	})
}

// setStatus changes an issue's status, and merges meta into its metadata, as
// a status write does (the office event trigger fires).
func (b *reviewBench) setStatus(issueID, status string, meta map[string]string) {
	b.t.Helper()
	raw, _ := json.Marshal(meta)
	if meta == nil {
		raw = []byte("{}")
	}
	if _, err := b.pool.Exec(b.ctx, `
UPDATE issue SET status = $2, metadata = metadata || $3::jsonb WHERE id = $1::uuid`,
		issueID, status, string(raw)); err != nil {
		b.t.Fatal(err)
	}
}

// runRoute is the route recorded for an ended run, or "" when none was.
func (b *reviewBench) runRoute(issueID, taskID string) string {
	b.t.Helper()
	var route string
	_ = b.pool.QueryRow(b.ctx, `
SELECT target FROM office_issue_watchdog_attempt
WHERE issue_id = $1::uuid AND event_key = 'run-end:' || $2`, issueID, taskID).Scan(&route)
	return route
}

func (b *reviewBench) identifier(issueID string) string {
	b.t.Helper()
	var ident string
	if err := b.pool.QueryRow(b.ctx, `
SELECT w.issue_prefix || '-' || i.number::text FROM issue i JOIN workspace w ON w.id = i.workspace_id
WHERE i.id = $1::uuid`, issueID).Scan(&ident); err != nil {
		b.t.Fatal(err)
	}
	return ident
}

// The routes a run ending takes on its own event (C1, C2, C3, C5), each once:
// the same event again and the catch-up scan add nothing.
func TestRunEndRoutesByWhereTheRunLeftTheIssue(t *testing.T) {
	b := newReviewBench(t)
	const key = "run-end-key"
	receiver, received, _ := newFakeReceiver(t, key, http.StatusAccepted)
	defer receiver.Close()
	labelID := b.label("等 Kun 拍板")
	cfg := Config{StopRouting: true, KunLabel: "等 Kun 拍板", KunGroup: "Multica 办公室", WebhookURL: receiver.URL, WebhookKey: key}

	off := b.watchdog(Config{KunLabel: cfg.KunLabel, WebhookURL: receiver.URL, WebhookKey: key})
	offID := b.issue("switch off", "in_progress", b.dev, nil)
	b.endRun(off, offID, b.dev, "failed", "agent_error", nil)
	b.run(off, b.now.Add(time.Minute))
	if got := received(); len(got) != 0 || b.allWakes(offID) != 0 {
		t.Fatalf("with StopRouting off, an ended run produced %+v and %d comments", got, b.allWakes(offID))
	}
	t.Log("switch off: an ended run is left alone")

	w := b.watchdog(cfg)

	// C1: delivered with a reviewer on the issue.
	deliveredID := b.issue("delivered to review", "in_progress", b.dev,
		testutil.Cols{"reviewer_type": "agent", "reviewer_id": b.reviewer})
	deliveredTask := b.endRun(w, deliveredID, b.dev, "completed", "", func() { b.setStatus(deliveredID, "in_review", nil) })
	b.taskEvent(w, deliveredID, deliveredTask, b.dev, "completed")
	if n := b.wakes(deliveredID, b.reviewer); n != 1 || b.allWakes(deliveredID) != 1 || b.queuedFor(deliveredID, b.reviewer) == 0 {
		t.Fatalf("delivered with a reviewer: reviewer wakes = %d of %d, want one that queues a run", n, b.allWakes(deliveredID))
	}
	t.Log("C1 run moved the issue to in_review: the reviewer was woken once, a review run queued")

	// C1: delivered with no reviewer; the in_review alert asks for acceptance.
	acceptID := b.issue("delivered, no reviewer", "in_progress", b.dev, nil)
	acceptTask := b.endRun(w, acceptID, b.dev, "completed", "", func() { b.setStatus(acceptID, "in_review", nil) })
	w.HandleEvent(events.Event{
		Type: protocol.EventIssueUpdated, WorkspaceID: b.workspaceID,
		Payload: map[string]any{"status_changed": true, "issue": map[string]any{"id": acceptID}},
	})
	alerts := eventsOf(received(), acceptID, "status_in_review")
	if b.runRoute(acceptID, acceptTask) != routeReview || b.allWakes(acceptID) != 0 ||
		len(alerts) != 1 || !strings.Contains(alerts[0].Reason, "请负责经理验收") {
		t.Fatalf("delivered with no reviewer: route=%q comments=%d alerts=%+v", b.runRoute(acceptID, acceptTask),
			b.allWakes(acceptID), alerts)
	}
	t.Log("C1 no reviewer: nobody woken; the single in_review alert asks the manager to accept")

	// C2: blocked on a decision.
	decisionID := b.issue("blocked on a decision", "in_progress", b.dev, nil)
	b.endRun(w, decisionID, b.dev, "completed", "", func() {
		b.setStatus(decisionID, "blocked", map[string]string{"close.block_kind": "decision", "close.block_action": "Kun 选方案 A 或 B"})
	})
	var labelled int
	if err := b.pool.QueryRow(b.ctx, `SELECT count(*) FROM issue_to_label WHERE issue_id = $1::uuid AND label_id = $2::uuid`,
		decisionID, labelID).Scan(&labelled); err != nil {
		t.Fatal(err)
	}
	pushes := eventsOf(received(), decisionID, KunDecisionEventType)
	if labelled != 1 || len(pushes) != 1 || pushes[0].Group != "Multica 办公室" {
		t.Fatalf("blocked on a decision: label=%d pushes=%+v, want the label and one group push", labelled, pushes)
	}
	t.Log("C2 blocked·decision: 「等 Kun 拍板」 attached and pushed to the Multica 办公室 group once")

	// C3: a failure with no retry left, and one with a retry queued.
	failedID := b.issue("run failed for good", "in_progress", b.dev, nil)
	failedTask := b.endRun(w, failedID, b.dev, "failed", "agent_error", nil)
	b.taskEvent(w, failedID, failedTask, b.dev, "failed")
	failures := eventsOf(received(), failedID, "run_failed")
	if len(failures) != 1 || failures[0].WakeTarget != "manager" || !strings.Contains(failures[0].Reason, "agent_error") {
		t.Fatalf("failed run: manager events = %+v, want one naming the reason", failures)
	}
	retryID := b.issue("run failed, retry queued", "in_progress", b.dev, nil)
	retryTask := b.endRun(nil, retryID, b.dev, "failed", "timeout", nil)
	b.fx.Task(t, b.dev, testutil.Cols{"issue_id": retryID, "runtime_id": b.runtimeID, "status": "queued", "parent_task_id": retryTask})
	b.taskEvent(w, retryID, retryTask, b.dev, "failed")
	if got := eventsOf(received(), retryID, "run_failed"); len(got) != 0 || b.runRoute(retryID, retryTask) != routeBusy {
		t.Fatalf("failed run with a retry queued: events=%+v route=%q", got, b.runRoute(retryID, retryTask))
	}
	t.Log("C3 failure with no retry: one manager alert naming the reason; with a retry queued: nothing")

	// C5: put back in todo for want of instructions.
	instructionID := b.issue("missing instructions", "in_progress", b.dev, nil)
	b.endRun(w, instructionID, b.dev, "completed", "", func() {
		b.setStatus(instructionID, "todo", map[string]string{"backlog.waiting_for": "Kun 给 API 额度"})
	})
	var notes int
	if err := b.pool.QueryRow(b.ctx, `
SELECT count(*) FROM comment WHERE issue_id = $1::uuid AND author_type = 'system'
  AND content LIKE '%缺什么：Kun 给 API 额度%' AND content NOT LIKE '%mention://%'`, instructionID).Scan(&notes); err != nil {
		t.Fatal(err)
	}
	asks := eventsOf(received(), instructionID, "needs_instruction")
	if notes != 1 || len(asks) != 1 || !strings.Contains(asks[0].Reason, "Kun 给 API 额度") {
		t.Fatalf("missing instructions: notes=%d manager=%+v", notes, asks)
	}
	t.Log("C5 back in todo: a note says what is missing, the manager is asked, status left alone")

	// Someone else's run (the reviewer's) is not routed.
	reviewedID := b.issue("reviewer's run ends", "in_review", b.dev,
		testutil.Cols{"reviewer_type": "agent", "reviewer_id": b.reviewer})
	reviewedTask := b.endRun(w, reviewedID, b.reviewer, "completed", "", nil)
	if b.runRoute(reviewedID, reviewedTask) != routeNotAssignee || b.allWakes(reviewedID) != 0 {
		t.Fatalf("reviewer's run: route=%q comments=%d", b.runRoute(reviewedID, reviewedTask), b.allWakes(reviewedID))
	}

	// D2: a run whose task event never arrived (a restart) is routed by the scan.
	missedID := b.issue("event missed", "in_progress", b.dev, nil)
	b.endRun(nil, missedID, b.dev, "failed", "agent_error", nil)

	before := len(received())
	b.run(w, b.now.Add(time.Minute))
	b.run(w, b.now.Add(2*time.Minute))
	if got := eventsOf(received(), missedID, "run_failed"); len(got) != 1 {
		t.Fatalf("missed task event: catch-up alerts = %+v, want one", got)
	}
	// Turning the switch on catches up the runs of the last hour, once.
	if got := eventsOf(received(), offID, "run_failed"); len(got) != 1 {
		t.Fatalf("run ended while the switch was off: catch-up alerts = %+v, want one", got)
	}
	if got := received(); len(got) != before+2 || b.wakes(deliveredID, b.reviewer) != 1 {
		t.Fatalf("the catch-up scan repeated routed runs: %+v", got[before:])
	}
	t.Log("D2 missed task events (and the hour before the switch went on) are routed by the scan; nothing is sent twice")
}

// C4 and the parent case: a run that ends with its issue still in_progress
// waits for the scan, reruns the developer once (max_attempts 2), then goes
// to the manager; a parent with open sub-issues goes to the manager at once.
func TestRunThatDeliversNothingIsRerunOnceThenGoesToTheManager(t *testing.T) {
	b := newReviewBench(t)
	const key = "no-delivery-key"
	receiver, received, _ := newFakeReceiver(t, key, http.StatusAccepted)
	defer receiver.Close()
	w := b.watchdog(Config{StopRouting: true, WebhookURL: receiver.URL, WebhookKey: key})

	stuckID := b.issue("stopped without delivering", "in_progress", b.dev, nil)
	first := b.endRun(w, stuckID, b.dev, "completed", "", func() { b.comment(stuckID, b.dev, "先做到这里", 5*time.Minute) })
	if route := b.runRoute(stuckID, first); route != "" || b.allWakes(stuckID) != 0 {
		t.Fatalf("the live event acted before the run settled: route=%q", route)
	}
	parentID := b.issue("parent with an open sub-issue", "in_progress", b.dev, nil)
	b.issue("open sub-issue", "todo", b.reviewer, testutil.Cols{"parent_issue_id": parentID})
	parentTask := b.endRun(w, parentID, b.dev, "completed", "", nil)
	followedID := b.issue("follow-up run queued after the end", "in_progress", b.dev, nil)
	followedTask := b.endRun(w, followedID, b.dev, "completed", "", nil)
	b.fx.Task(t, b.dev, testutil.Cols{"issue_id": followedID, "runtime_id": b.runtimeID, "status": "queued"})

	b.run(w, b.now.Add(time.Minute))
	if n := b.wakes(stuckID, b.dev); n != 1 || b.queuedFor(stuckID, b.dev) == 0 {
		t.Fatalf("first empty stop: developer reruns = %d, want 1 that queues a run", n)
	}
	t.Log("C4 first empty stop: the developer is rerun once after the run settles")

	var rerun string
	if err := b.pool.QueryRow(b.ctx, `
UPDATE agent_task_queue SET status = 'running', started_at = clock_timestamp(), dispatched_at = clock_timestamp()
WHERE issue_id = $1::uuid AND status = 'queued' RETURNING id::text`, stuckID).Scan(&rerun); err != nil {
		t.Fatal(err)
	}
	if _, err := b.pool.Exec(b.ctx, `UPDATE agent_task_queue SET status = 'completed', completed_at = clock_timestamp() WHERE id = $1::uuid`, rerun); err != nil {
		t.Fatal(err)
	}
	b.taskEvent(w, stuckID, rerun, b.dev, "completed")
	b.run(w, b.now.Add(2*time.Minute))
	b.run(w, b.now.Add(3*time.Minute))
	stops := eventsOf(received(), stuckID, "no_delivery")
	if n := b.wakes(stuckID, b.dev); n != 1 || len(stops) != 1 || stops[0].WakeTarget != "manager" {
		t.Fatalf("second empty stop: reruns = %d, manager = %+v; want no second rerun and one manager alert", n, stops)
	}
	var status string
	if err := b.pool.QueryRow(b.ctx, `SELECT status FROM issue WHERE id = $1::uuid`, stuckID).Scan(&status); err != nil || status != "in_progress" {
		t.Fatalf("the watchdog changed the status to %q", status)
	}
	t.Log("C4 second empty stop: no more reruns, one manager alert, status untouched")

	if got := eventsOf(received(), parentID, "parent_waiting"); len(got) != 1 || b.allWakes(parentID) != 0 ||
		b.runRoute(parentID, parentTask) != routeParentWaiting {
		t.Fatalf("parent with open sub-issues: manager = %+v, comments = %d", got, b.allWakes(parentID))
	}
	t.Log("C6 parent: the manager is asked to move it back to todo; nobody rerun")

	if b.runRoute(followedID, followedTask) != routeBusy || b.allWakes(followedID) != 0 {
		t.Fatalf("a follow-up run was queued, yet the ended run was routed %q", b.runRoute(followedID, followedTask))
	}
	t.Log("a follow-up run queued after the end: left alone")
}

// C6: an issue blocked on a dependency is woken once its named predecessor is
// done, on the status event, and the scan does not wake it again.
func TestDependencyDoneWakesTheBlockedIssue(t *testing.T) {
	b := newReviewBench(t)
	w := b.watchdog(Config{StopRouting: true})
	firstID := b.issue("predecessor one", "in_progress", b.reviewer, nil)
	secondID := b.issue("predecessor two", "in_review", b.reviewer, nil)
	first, second := b.identifier(firstID), b.identifier(secondID)
	blockedID := b.issue("waits on both", "blocked", b.dev, testutil.Cols{"metadata": json.RawMessage(fmt.Sprintf(
		`{"close.block_kind": "dependency", "close.block_action": "等 %s 和 [%s](mention://issue/%s) 合并"}`,
		first, second, secondID))})
	vagueID := b.issue("names no issue", "blocked", b.dev, testutil.Cols{"metadata": json.RawMessage(
		`{"close.block_kind": "dependency", "close.block_action": "等上一张单"}`)})
	twiceID := b.issue("names one issue twice", "blocked", b.dev, testutil.Cols{"metadata": json.RawMessage(fmt.Sprintf(
		`{"close.block_kind": "dependency", "close.block_action": "等 %s（[%s](mention://issue/%s)）合并"}`,
		second, second, secondID))})

	b.setStatus(firstID, "done", nil)
	w.HandleEvent(events.Event{Type: protocol.EventIssueUpdated, WorkspaceID: b.workspaceID,
		Payload: map[string]any{"status_changed": true, "issue": map[string]any{"id": firstID}}})
	if n := b.allWakes(blockedID); n != 0 {
		t.Fatalf("one of two predecessors done, yet the blocked issue got %d wakes", n)
	}
	b.setStatus(secondID, "done", nil)
	w.HandleEvent(events.Event{Type: protocol.EventIssueUpdated, WorkspaceID: b.workspaceID,
		Payload: map[string]any{"status_changed": true, "issue": map[string]any{"id": secondID}}})
	b.run(w, b.now.Add(time.Minute))
	if n := b.wakes(blockedID, b.dev); n != 1 || b.allWakes(blockedID) != 1 {
		t.Fatalf("both predecessors done: assignee wakes = %d of %d, want 1", n, b.allWakes(blockedID))
	}
	if n := b.wakes(twiceID, b.dev); n != 1 || b.allWakes(twiceID) != 1 {
		t.Fatalf("one predecessor named by number and by mention: assignee wakes = %d of %d, want 1", n, b.allWakes(twiceID))
	}
	if n := b.allWakes(vagueID); n != 0 {
		t.Fatalf("a block action naming no issue got %d wakes", n)
	}
	t.Log("blocked·dependency: woken once when every named predecessor is done, even one named twice; a vague wait is left alone")
}

func TestDependencyRefs(t *testing.T) {
	id := "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
	numbers, ids := dependencyRefs("等 dene-12、DENE-12、DENE-2740 和 [DENE-7](mention://issue/"+id+")，不管 PR-3 和 DENE-27a", "DENE")
	if fmt.Sprint(numbers) != "[12 2740]" || len(ids) != 1 || ids[0] != id {
		t.Fatalf("refs = %v %v", numbers, ids)
	}
}

// D1: a run whose runtime has been offline past OfflineWait alerts the
// responsible manager once; the run itself is left to the reconnect grace.
func TestOfflineRuntimeRunAlertsManagerOnce(t *testing.T) {
	b := newReviewBench(t)
	const key = "offline-key"
	receiver, received, _ := newFakeReceiver(t, key, http.StatusAccepted)
	defer receiver.Close()
	w := b.watchdog(Config{StopRouting: true, OfflineWait: 10 * time.Minute, WebhookURL: receiver.URL, WebhookKey: key})
	issueID := b.issue("run on a machine that dropped", "in_progress", b.dev, nil)
	taskID := b.fx.Task(t, b.dev, testutil.Cols{
		"issue_id": issueID, "runtime_id": b.runtimeID, "status": "running",
		"dispatched_at": b.now.Add(-30 * time.Minute), "started_at": b.now.Add(-30 * time.Minute),
	})
	if _, err := b.pool.Exec(b.ctx, `
UPDATE agent_runtime SET status = 'offline', last_seen_at = $2, updated_at = $2 WHERE id = $1::uuid`,
		b.runtimeID, b.now.Add(-5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	b.run(w, b.now)
	if got := eventsOf(received(), issueID, "runtime_offline"); len(got) != 0 {
		t.Fatalf("offline 5 minutes: alerts = %+v, want none yet", got)
	}
	b.run(w, b.now.Add(6*time.Minute))
	b.run(w, b.now.Add(7*time.Minute))
	got := eventsOf(received(), issueID, "runtime_offline")
	if len(got) != 1 || got[0].WakeTarget != "manager" || !strings.Contains(got[0].Reason, "已掉线 11 分钟") {
		t.Fatalf("offline 11 minutes: alerts = %+v, want one", got)
	}
	var status string
	if err := b.pool.QueryRow(b.ctx, `SELECT status FROM agent_task_queue WHERE id = $1::uuid`, taskID).Scan(&status); err != nil || status != "running" {
		t.Fatalf("the run was touched: %q", status)
	}
	t.Log("D1 runtime offline 11m with a run: one manager alert, the run left running")
}
