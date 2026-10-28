package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/service"
)

// createAfterIssue posts a sub-issue through CreateIssue and returns the
// recorder; a 201 is decoded and cleaned up.
func createAfterIssue(t *testing.T, body map[string]any) (*httptest.ResponseRecorder, IssueResponse) {
	t.Helper()
	if _, ok := body["title"]; !ok {
		body["title"] = "after " + time.Now().Format(time.RFC3339Nano)
	}
	w := httptest.NewRecorder()
	testHandler.CreateIssue(w, newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, body))
	var issue IssueResponse
	if w.Code == http.StatusCreated {
		if err := json.Unmarshal(w.Body.Bytes(), &issue); err != nil {
			t.Fatalf("decode issue: %v", err)
		}
		t.Cleanup(func() { cleanupChildDoneIssue(issue.ID) })
	}
	return w, issue
}

func mustCreateAfterIssue(t *testing.T, body map[string]any) IssueResponse {
	t.Helper()
	w, issue := createAfterIssue(t, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create issue: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	return issue
}

func issueStatusAndWaiting(t *testing.T, issueID string) (status, waitingFor string) {
	t.Helper()
	if err := testPool.QueryRow(context.Background(),
		`SELECT status, coalesce(metadata->>'backlog.waiting_for', '') FROM issue WHERE id = $1`, issueID,
	).Scan(&status, &waitingFor); err != nil {
		t.Fatalf("read issue: %v", err)
	}
	return status, waitingFor
}

// issueAfterKey is the issue's backlog.after value, "" when absent.
func issueAfterKey(t *testing.T, issueID string) string {
	t.Helper()
	var after string
	if err := testPool.QueryRow(context.Background(),
		`SELECT coalesce(metadata->>'backlog.after', '') FROM issue WHERE id = $1`, issueID,
	).Scan(&after); err != nil {
		t.Fatalf("read issue: %v", err)
	}
	return after
}

func countAfterStarts(t *testing.T, issueID string) int {
	t.Helper()
	return dbfx.Count(t, `SELECT count(*) FROM activity_log WHERE issue_id = $1 AND action = 'status_changed'
		AND details->>'source' = 'after' AND details->>'to' = 'todo'`, issueID)
}

// A sub-issue ordered after a sibling waits in backlog, naming the sibling;
// when the sibling is done it moves to todo and its agent starts.
func TestAfterSiblingDoneStartsDependent(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID := handlerTestAgentID(t)
	parent := mustCreateAfterIssue(t, map[string]any{"status": "in_progress"})
	first := mustCreateAfterIssue(t, map[string]any{"status": "in_progress", "parent_issue_id": parent.ID})
	second := mustCreateAfterIssue(t, map[string]any{
		"parent_issue_id": parent.ID, "after_issue_ids": []string{first.ID},
		"assignee_type": "agent", "assignee_id": agentID,
	})

	status, waiting := issueStatusAndWaiting(t, second.ID)
	if status != "backlog" || !strings.Contains(waiting, first.Identifier) {
		t.Fatalf("second = %s / %q, want backlog waiting for %s", status, waiting, first.Identifier)
	}
	if line, _ := second.Metadata["backlog.waiting_for"].(string); second.Status != "backlog" || line != waiting {
		t.Fatalf("create response = %s / %q, want backlog / %q as stored", second.Status, line, waiting)
	}
	if n := countPendingTasksForAgent(t, second.ID, agentID); n != 0 {
		t.Fatalf("parked sub-issue started %d run(s)", n)
	}
	if after := issueAfterKey(t, second.ID); after != first.ID {
		t.Fatalf("backlog.after = %q, want %s", after, first.ID)
	}

	updateChildStatus(t, first.ID, "done")

	status, waiting = issueStatusAndWaiting(t, second.ID)
	if status != "todo" || waiting != "" {
		t.Fatalf("second after first done = %s / %q, want todo with the waiting line dropped", status, waiting)
	}
	if n := countPendingTasksForAgent(t, second.ID, agentID); n != 1 {
		t.Fatalf("started sub-issue has %d pending run(s), want 1", n)
	}
	if n := countAfterStarts(t, second.ID); n != 1 {
		t.Fatalf("timeline has %d start entries, want 1", n)
	}
	if after := issueAfterKey(t, second.ID); after != "" {
		t.Fatalf("backlog.after = %q after the run was queued, want it dropped", after)
	}

	// The fact is handled once: a later reopen and done of the predecessor
	// leaves the started sibling alone.
	updateChildStatus(t, second.ID, "in_review")
	updateChildStatus(t, first.ID, "todo")
	updateChildStatus(t, first.ID, "done")
	if status, _ := issueStatusAndWaiting(t, second.ID); status != "in_review" {
		t.Fatalf("second = %s after the predecessor closed again, want in_review", status)
	}
}

// With two predecessors, the last one to finish starts the dependent.
func TestAfterWaitsForEveryPredecessor(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	parent := mustCreateAfterIssue(t, map[string]any{"status": "in_progress"})
	a := mustCreateAfterIssue(t, map[string]any{"status": "in_progress", "parent_issue_id": parent.ID})
	b := mustCreateAfterIssue(t, map[string]any{"status": "todo", "parent_issue_id": parent.ID})
	c := mustCreateAfterIssue(t, map[string]any{"parent_issue_id": parent.ID, "after_issue_ids": []string{a.ID, b.ID, a.ID}})

	updateChildStatus(t, a.ID, "done")
	if status, waiting := issueStatusAndWaiting(t, c.ID); status != "backlog" || waiting == "" {
		t.Fatalf("c after one of two = %s / %q, want still parked", status, waiting)
	}
	updateChildStatus(t, b.ID, "done")
	if status, _ := issueStatusAndWaiting(t, c.ID); status != "todo" {
		t.Fatalf("c after both = %s, want todo", status)
	}
}

// A predecessor that is already done does not park the new sub-issue, and an
// explicit waiting line is kept.
func TestAfterFinishedPredecessorDoesNotPark(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	parent := mustCreateAfterIssue(t, map[string]any{"status": "in_progress"})
	done := mustCreateAfterIssue(t, map[string]any{"status": "done", "parent_issue_id": parent.ID})
	next := mustCreateAfterIssue(t, map[string]any{"parent_issue_id": parent.ID, "after_issue_ids": []string{done.ID}})
	if status, _ := issueStatusAndWaiting(t, next.ID); status != "todo" {
		t.Fatalf("next = %s, want todo", status)
	}
	if after := issueAfterKey(t, next.ID); after != "" {
		t.Fatalf("next keeps backlog.after %q with nothing to wait for", after)
	}

	open := mustCreateAfterIssue(t, map[string]any{"status": "todo", "parent_issue_id": parent.ID})
	held := mustCreateAfterIssue(t, map[string]any{
		"parent_issue_id": parent.ID, "after_issue_ids": []string{open.ID}, "waiting_for": "等设计稿",
	})
	if status, waiting := issueStatusAndWaiting(t, held.ID); status != "backlog" || waiting != "等设计稿" {
		t.Fatalf("held = %s / %q, want backlog with the caller's line", status, waiting)
	}
}

// A run that cannot be queued after the dependent moved to todo is not lost:
// the order stays on the issue, the sub-issue event stays unprocessed, and
// the next processing (the sweep's) queues the run without moving the status
// again.
func TestAfterRetriesRunThatFailedToQueue(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := handlerTestAgentID(t)
	parent := mustCreateAfterIssue(t, map[string]any{"status": "in_progress"})
	first := mustCreateAfterIssue(t, map[string]any{"status": "in_progress", "parent_issue_id": parent.ID})
	second := mustCreateAfterIssue(t, map[string]any{
		"parent_issue_id": parent.ID, "after_issue_ids": []string{first.ID},
		"assignee_type": "agent", "assignee_id": agentID,
	})

	// The queue refuses this one issue's run, as a database outage would.
	// second.ID is a server-generated UUID.
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE OR REPLACE FUNCTION dene2671_refuse_run() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.issue_id = '%s'::uuid THEN RAISE EXCEPTION 'queue unavailable'; END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER dene2671_refuse_run BEFORE INSERT ON agent_task_queue
			FOR EACH ROW EXECUTE FUNCTION dene2671_refuse_run();
	`, second.ID)); err != nil {
		t.Fatalf("install refusal: %v", err)
	}
	dropRefusal := func() {
		_, _ = testPool.Exec(context.Background(), `DROP TRIGGER IF EXISTS dene2671_refuse_run ON agent_task_queue; DROP FUNCTION IF EXISTS dene2671_refuse_run()`)
	}
	t.Cleanup(dropRefusal)

	updateChildStatus(t, first.ID, "done")

	status, waiting := issueStatusAndWaiting(t, second.ID)
	if status != "todo" || waiting != "" {
		t.Fatalf("second = %s / %q, want todo even though its run failed", status, waiting)
	}
	if n := countPendingTasksForAgent(t, second.ID, agentID); n != 0 {
		t.Fatalf("refused run left %d pending task(s)", n)
	}
	if after := issueAfterKey(t, second.ID); after != first.ID {
		t.Fatalf("backlog.after = %q after a failed queue, want it kept for the retry", after)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_child_event WHERE child_id = $1 AND processed_at IS NULL`, first.ID); n == 0 {
		t.Fatal("the close was marked processed although the run was not queued; the sweep would never retry")
	}

	dropRefusal()
	if err := (&service.IssueWakeupService{Tasks: testHandler.TaskService}).ProcessChildEvents(ctx, parseUUID(parent.ID)); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if n := countPendingTasksForAgent(t, second.ID, agentID); n != 1 {
		t.Fatalf("after the retry second has %d pending run(s), want 1", n)
	}
	if after := issueAfterKey(t, second.ID); after != "" {
		t.Fatalf("backlog.after = %q after the retry queued the run, want it dropped", after)
	}
	if n := countAfterStarts(t, second.ID); n != 1 {
		t.Fatalf("timeline has %d start entries, want the status moved once", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_child_event WHERE child_id = $1 AND processed_at IS NULL`, first.ID); n != 0 {
		t.Fatalf("%d sub-issue event(s) still unprocessed after the retry", n)
	}
}

// The run is queued but dropping the order then fails, so the close stays
// unprocessed. That run finishes before the retry, so the queue's pending
// dedup no longer sees it. The retry finds the run queued since the start and
// only drops the order: one run in all.
func TestAfterRetryAfterOrderDropFailedQueuesOnce(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := handlerTestAgentID(t)
	parent := mustCreateAfterIssue(t, map[string]any{"status": "in_progress"})
	first := mustCreateAfterIssue(t, map[string]any{"status": "in_progress", "parent_issue_id": parent.ID})
	second := mustCreateAfterIssue(t, map[string]any{
		"parent_issue_id": parent.ID, "after_issue_ids": []string{first.ID},
		"assignee_type": "agent", "assignee_id": agentID,
	})

	// Dropping this one issue's order fails, as a database outage would.
	// second.ID is a server-generated UUID.
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE OR REPLACE FUNCTION dene2671_keep_order() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.id = '%s'::uuid AND OLD.metadata ? 'backlog.after' AND NOT NEW.metadata ? 'backlog.after' THEN
				RAISE EXCEPTION 'metadata write unavailable';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER dene2671_keep_order BEFORE UPDATE ON issue
			FOR EACH ROW EXECUTE FUNCTION dene2671_keep_order();
	`, second.ID)); err != nil {
		t.Fatalf("install failure: %v", err)
	}
	dropFailure := func() {
		_, _ = testPool.Exec(context.Background(), `DROP TRIGGER IF EXISTS dene2671_keep_order ON issue; DROP FUNCTION IF EXISTS dene2671_keep_order()`)
	}
	t.Cleanup(dropFailure)

	updateChildStatus(t, first.ID, "done")

	if status, _ := issueStatusAndWaiting(t, second.ID); status != "todo" {
		t.Fatalf("second = %s, want todo", status)
	}
	if n := countPendingTasksForAgent(t, second.ID, agentID); n != 1 {
		t.Fatalf("second has %d pending run(s) after the start, want 1", n)
	}
	if after := issueAfterKey(t, second.ID); after != first.ID {
		t.Fatalf("backlog.after = %q, want it kept by the failed drop", after)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_child_event WHERE child_id = $1 AND processed_at IS NULL`, first.ID); n == 0 {
		t.Fatal("the close was marked processed although the order was not dropped")
	}

	// The run finishes before the retry.
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE issue_id = $1`, second.ID); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	dropFailure()
	if err := (&service.IssueWakeupService{Tasks: testHandler.TaskService}).ProcessChildEvents(ctx, parseUUID(parent.ID)); err != nil {
		t.Fatalf("retry: %v", err)
	}

	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, second.ID); n != 1 {
		t.Fatalf("second has %d task(s) in all after the retry, want the one run", n)
	}
	if after := issueAfterKey(t, second.ID); after != "" {
		t.Fatalf("backlog.after = %q after the retry, want it dropped", after)
	}
	if n := countAfterStarts(t, second.ID); n != 1 {
		t.Fatalf("timeline has %d start entries, want the status moved once", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_child_event WHERE child_id = $1 AND processed_at IS NULL`, first.ID); n != 0 {
		t.Fatalf("%d sub-issue event(s) still unprocessed after the retry", n)
	}
}

// A sibling that finishes while the waiting sub-issue is being created, with
// its close already processed, leaves only the new issue's attached event to
// find it. When the check right after create cannot queue the run, the sweep
// processing that event queues it, once.
func TestAfterSweepStartsWhenSiblingClosedDuringCreate(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := handlerTestAgentID(t)
	parent := mustCreateAfterIssue(t, map[string]any{"status": "in_progress"})
	first := mustCreateAfterIssue(t, map[string]any{"status": "in_progress", "parent_issue_id": parent.ID})
	title := "after race " + time.Now().Format(time.RFC3339Nano)

	// Inside the create transaction, after the order was validated against an
	// open sibling: the sibling closes and its close event is processed, as a
	// concurrent close that ran before the new row was visible would leave it.
	// The queue refuses the new issue's run. Both ids come from the server and
	// the title from this test.
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE OR REPLACE FUNCTION dene2671_close_sibling() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.title = '%[1]s' THEN
				UPDATE issue SET status = 'done' WHERE id = '%[2]s'::uuid;
				UPDATE issue_child_event SET processed_at = now() WHERE child_id = '%[2]s'::uuid AND processed_at IS NULL;
			END IF;
			RETURN NULL;
		END $$;
		CREATE TRIGGER dene2671_close_sibling AFTER INSERT ON issue
			FOR EACH ROW EXECUTE FUNCTION dene2671_close_sibling();
		CREATE OR REPLACE FUNCTION dene2671_refuse_run() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF EXISTS (SELECT 1 FROM issue WHERE id = NEW.issue_id AND title = '%[1]s') THEN RAISE EXCEPTION 'queue unavailable'; END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER dene2671_refuse_run BEFORE INSERT ON agent_task_queue
			FOR EACH ROW EXECUTE FUNCTION dene2671_refuse_run();
	`, title, first.ID)); err != nil {
		t.Fatalf("install race: %v", err)
	}
	dropTriggers := func() {
		_, _ = testPool.Exec(context.Background(), `
			DROP TRIGGER IF EXISTS dene2671_close_sibling ON issue; DROP FUNCTION IF EXISTS dene2671_close_sibling();
			DROP TRIGGER IF EXISTS dene2671_refuse_run ON agent_task_queue; DROP FUNCTION IF EXISTS dene2671_refuse_run()`)
	}
	t.Cleanup(dropTriggers)

	second := mustCreateAfterIssue(t, map[string]any{
		"title": title, "parent_issue_id": parent.ID, "after_issue_ids": []string{first.ID},
		"assignee_type": "agent", "assignee_id": agentID,
	})
	dropTriggers()

	if status, _ := issueStatusAndWaiting(t, first.ID); status != "done" {
		t.Fatalf("first = %s, want done inside the create", status)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_child_event WHERE child_id = $1 AND processed_at IS NULL`, first.ID); n != 0 {
		t.Fatalf("first has %d unprocessed event(s), want its close already handled", n)
	}
	if status, _ := issueStatusAndWaiting(t, second.ID); status != "todo" {
		t.Fatalf("second = %s, want todo from the check after create", status)
	}
	// The create answers with the issue as stored after that check, not the
	// parked row it inserted.
	if _, waiting := second.Metadata["backlog.waiting_for"]; second.Status != "todo" || waiting {
		t.Fatalf("create response = %s with waiting line %v, want todo without one", second.Status, second.Metadata["backlog.waiting_for"])
	}
	if after, _ := second.Metadata["backlog.after"].(string); after != first.ID {
		t.Fatalf("create response backlog.after = %q, want it kept like the stored row", after)
	}
	if n := countPendingTasksForAgent(t, second.ID, agentID); n != 0 {
		t.Fatalf("refused run left %d pending task(s)", n)
	}
	if after := issueAfterKey(t, second.ID); after != first.ID {
		t.Fatalf("backlog.after = %q after the failed queue, want it kept", after)
	}

	// The sweep picks the parent up once its events are 30 seconds old.
	if _, err := testPool.Exec(ctx, `UPDATE issue_child_event SET created_at = created_at - interval '1 minute' WHERE parent_id = $1 AND processed_at IS NULL`, parent.ID); err != nil {
		t.Fatalf("age events: %v", err)
	}
	sweep := &service.IssueWakeupService{Tasks: testHandler.TaskService}
	for i := 0; i < 2; i++ {
		if err := sweep.SweepChildEvents(ctx); err != nil {
			t.Logf("sweep %d: %v", i+1, err)
		}
	}
	if n := countPendingTasksForAgent(t, second.ID, agentID); n != 1 {
		t.Fatalf("after the sweep second has %d pending run(s), want 1", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, second.ID); n != 1 {
		t.Fatalf("second has %d task(s) in all, want exactly 1", n)
	}
	if after := issueAfterKey(t, second.ID); after != "" {
		t.Fatalf("backlog.after = %q after the sweep queued the run, want it dropped", after)
	}
	if n := countAfterStarts(t, second.ID); n != 1 {
		t.Fatalf("timeline has %d start entries, want the status moved once", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_child_event WHERE child_id = $1 AND processed_at IS NULL`, second.ID); n != 0 {
		t.Fatalf("%d event(s) of second still unprocessed after the sweep", n)
	}
}

// A sub-issue moved under another parent names no sibling there any more, so
// its own attached event leaves it parked for a person.
func TestAfterMovedIssueStaysParked(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	parent := mustCreateAfterIssue(t, map[string]any{"status": "in_progress"})
	otherParent := mustCreateAfterIssue(t, map[string]any{"status": "in_progress"})
	first := mustCreateAfterIssue(t, map[string]any{"status": "in_progress", "parent_issue_id": parent.ID})
	second := mustCreateAfterIssue(t, map[string]any{"parent_issue_id": parent.ID, "after_issue_ids": []string{first.ID}})

	w := httptest.NewRecorder()
	testHandler.UpdateIssue(w, withURLParam(newRequest("PUT", "/api/issues/"+second.ID, map[string]any{"parent_issue_id": otherParent.ID}), "id", second.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("move: %d %s", w.Code, w.Body.String())
	}
	if status, _ := issueStatusAndWaiting(t, second.ID); status != "backlog" {
		t.Fatalf("moved second = %s, want it left in backlog", status)
	}
}

// A person who moves a parked sub-issue on has started it; the order goes
// with the waiting line, so its sibling finishing later starts nothing.
func TestAfterPersonStartingItDropsOrder(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	parent := mustCreateAfterIssue(t, map[string]any{"status": "in_progress"})
	first := mustCreateAfterIssue(t, map[string]any{"status": "in_progress", "parent_issue_id": parent.ID})
	second := mustCreateAfterIssue(t, map[string]any{"parent_issue_id": parent.ID, "after_issue_ids": []string{first.ID}})

	updateChildStatus(t, second.ID, "in_progress")
	if after := issueAfterKey(t, second.ID); after != "" {
		t.Fatalf("backlog.after = %q after a person started it, want it dropped", after)
	}
	updateChildStatus(t, first.ID, "done")
	if status, _ := issueStatusAndWaiting(t, second.ID); status != "in_progress" {
		t.Fatalf("second = %s, want the person's in_progress left alone", status)
	}
	if n := countAfterStarts(t, second.ID); n != 0 {
		t.Fatalf("timeline has %d automatic start(s) for an issue a person started", n)
	}
}

func TestAfterRejectsWhatCannotFinish(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	parent := mustCreateAfterIssue(t, map[string]any{"status": "in_progress"})
	otherParent := mustCreateAfterIssue(t, map[string]any{"status": "in_progress"})
	stranger := mustCreateAfterIssue(t, map[string]any{"status": "todo", "parent_issue_id": otherParent.ID})
	cancelled := mustCreateAfterIssue(t, map[string]any{"status": "cancelled", "parent_issue_id": parent.ID})
	open := mustCreateAfterIssue(t, map[string]any{"status": "todo", "parent_issue_id": parent.ID})

	for name, test := range map[string]struct {
		body map[string]any
		want string
	}{
		"no parent":   {map[string]any{"after_issue_ids": []string{open.ID}}, "needs parent_issue_id"},
		"not sibling": {map[string]any{"parent_issue_id": parent.ID, "after_issue_ids": []string{stranger.ID}}, "not a sub-issue of the same parent"},
		"cancelled":   {map[string]any{"parent_issue_id": parent.ID, "after_issue_ids": []string{cancelled.ID}}, "is cancelled"},
		"in progress": {map[string]any{"parent_issue_id": parent.ID, "status": "in_progress", "after_issue_ids": []string{open.ID}}, "starts in backlog"},
		"bad id":      {map[string]any{"parent_issue_id": parent.ID, "after_issue_ids": []string{"nope"}}, "after_issue_ids"},
	} {
		t.Run(name, func(t *testing.T) {
			w, _ := createAfterIssue(t, test.body)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), test.want) {
				t.Fatalf("got %d %s, want 400 mentioning %q", w.Code, w.Body.String(), test.want)
			}
		})
	}
}

// The comment menu's sub-issue takes the same order: it sits under the
// comment's issue and waits for the sibling named in after_issue_ids.
func TestAfterFromComment(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	parent := mustCreateAfterIssue(t, map[string]any{"status": "in_progress"})
	first := mustCreateAfterIssue(t, map[string]any{"status": "in_progress", "parent_issue_id": parent.ID})
	var commentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content)
		VALUES ($1, $2, 'member', $3, 'also add a CSV export') RETURNING id
	`, parent.ID, testWorkspaceID, testUserID).Scan(&commentID); err != nil {
		t.Fatalf("insert comment: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM comment WHERE id = $1`, commentID) })

	w := httptest.NewRecorder()
	testHandler.PreviewCommentSubIssue(w, withURLParam(newRequest(http.MethodGet, "/api/comments/"+commentID+"/sub-issue-preview", nil), "commentId", commentID))
	if w.Code != http.StatusOK {
		t.Fatalf("preview = %d: %s", w.Code, w.Body.String())
	}
	var preview sourceContextPreviewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	w = httptest.NewRecorder()
	testHandler.CreateCommentSubIssue(w, withURLParam(newRequest(http.MethodPost, "/api/comments/"+commentID+"/sub-issues", map[string]any{
		"mode": "manual", "capture_token": preview.CaptureToken,
		"issue": map[string]any{"title": "CSV export " + time.Now().Format(time.RFC3339Nano), "after_issue_ids": []string{first.ID}},
	}), "commentId", commentID))
	if w.Code != http.StatusCreated {
		t.Fatalf("create from comment = %d: %s", w.Code, w.Body.String())
	}
	var created IssueResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue_source_context WHERE issue_id = $1`, created.ID)
		cleanupChildDoneIssue(created.ID)
	})
	if created.ParentIssueID == nil || *created.ParentIssueID != parent.ID || created.Status != "backlog" {
		t.Fatalf("created = parent %v status %s, want backlog under %s", created.ParentIssueID, created.Status, parent.ID)
	}

	updateChildStatus(t, first.ID, "done")
	if status, waiting := issueStatusAndWaiting(t, created.ID); status != "todo" || waiting != "" {
		t.Fatalf("comment sub-issue after first done = %s / %q, want todo", status, waiting)
	}
}

// TestAfterFromCommentSiblingClosedDuringCreate: the comment path answers with
// the issue as stored after its check, too. The sibling closes inside the
// create transaction, so the check after commit starts the new sub-issue and
// the response says todo without a waiting line.
func TestAfterFromCommentSiblingClosedDuringCreate(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	parent := mustCreateAfterIssue(t, map[string]any{"status": "in_progress"})
	first := mustCreateAfterIssue(t, map[string]any{"status": "in_progress", "parent_issue_id": parent.ID})
	title := "after comment race " + time.Now().Format(time.RFC3339Nano)
	var commentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content)
		VALUES ($1, $2, 'member', $3, 'also add a PDF export') RETURNING id
	`, parent.ID, testWorkspaceID, testUserID).Scan(&commentID); err != nil {
		t.Fatalf("insert comment: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM comment WHERE id = $1`, commentID) })

	// Both ids come from the server and the title from this test.
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE OR REPLACE FUNCTION dene2671_close_sibling() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.title = '%[1]s' THEN
				UPDATE issue SET status = 'done' WHERE id = '%[2]s'::uuid;
				UPDATE issue_child_event SET processed_at = now() WHERE child_id = '%[2]s'::uuid AND processed_at IS NULL;
			END IF;
			RETURN NULL;
		END $$;
		CREATE TRIGGER dene2671_close_sibling AFTER INSERT ON issue
			FOR EACH ROW EXECUTE FUNCTION dene2671_close_sibling();
	`, title, first.ID)); err != nil {
		t.Fatalf("install race: %v", err)
	}
	dropTrigger := func() {
		_, _ = testPool.Exec(context.Background(), `
			DROP TRIGGER IF EXISTS dene2671_close_sibling ON issue; DROP FUNCTION IF EXISTS dene2671_close_sibling()`)
	}
	t.Cleanup(dropTrigger)

	w := httptest.NewRecorder()
	testHandler.PreviewCommentSubIssue(w, withURLParam(newRequest(http.MethodGet, "/api/comments/"+commentID+"/sub-issue-preview", nil), "commentId", commentID))
	if w.Code != http.StatusOK {
		t.Fatalf("preview = %d: %s", w.Code, w.Body.String())
	}
	var preview sourceContextPreviewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	w = httptest.NewRecorder()
	testHandler.CreateCommentSubIssue(w, withURLParam(newRequest(http.MethodPost, "/api/comments/"+commentID+"/sub-issues", map[string]any{
		"mode": "manual", "capture_token": preview.CaptureToken,
		"issue": map[string]any{"title": title, "after_issue_ids": []string{first.ID}},
	}), "commentId", commentID))
	dropTrigger()
	if w.Code != http.StatusCreated {
		t.Fatalf("create from comment = %d: %s", w.Code, w.Body.String())
	}
	var created IssueResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue_source_context WHERE issue_id = $1`, created.ID)
		cleanupChildDoneIssue(created.ID)
	})

	if status, waiting := issueStatusAndWaiting(t, created.ID); status != "todo" || waiting != "" {
		t.Fatalf("stored = %s / %q, want todo from the check after create", status, waiting)
	}
	if _, waiting := created.Metadata["backlog.waiting_for"]; created.Status != "todo" || waiting {
		t.Fatalf("create response = %s with waiting line %v, want todo without one", created.Status, created.Metadata["backlog.waiting_for"])
	}
}
