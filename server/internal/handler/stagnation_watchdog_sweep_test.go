package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func watchdogWorkspaceID() pgtype.UUID {
	return parseUUID(testWorkspaceID)
}

func attachWatchdogChild(t *testing.T, childID, parentID string, stage int32) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(),
		`UPDATE issue SET parent_issue_id = $2, stage = $3 WHERE id = $1`,
		childID, parentID, stage); err != nil {
		t.Fatalf("attach child: %v", err)
	}
}

func setIssueStatusDirect(t *testing.T, issueID, status string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(),
		`UPDATE issue SET status = $2 WHERE id = $1`, issueID, status); err != nil {
		t.Fatalf("set status: %v", err)
	}
}

func setIssueActivityAt(t *testing.T, issueID string, at time.Time) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(),
		`UPDATE issue SET last_activity_at = $2, updated_at = $2 WHERE id = $1`,
		issueID, at); err != nil {
		t.Fatalf("set last_activity_at: %v", err)
	}
}

func setCloseMeta(t *testing.T, issueID, key, value string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"value": value})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issueID+"/metadata/"+key, json.RawMessage(body))
	req = withURLParams(req, "id", issueID, "key", key)
	testHandler.SetIssueMetadataKey(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("set %s: expected 200, got %d: %s", key, w.Code, w.Body.String())
	}
}

func issueStatusOf(t *testing.T, issueID string) string {
	t.Helper()
	var status string
	if err := testPool.QueryRow(context.Background(),
		`SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	return status
}

func watchdogCommentContains(t *testing.T, issueID, needle string) bool {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM comment WHERE issue_id = $1 AND author_type = 'system' AND content LIKE $2`,
		issueID, "%"+needle+"%").Scan(&n); err != nil {
		t.Fatalf("count watchdog comments: %v", err)
	}
	return n > 0
}

func runWatchdog(t *testing.T, now time.Time) {
	t.Helper()
	testHandler.SweepStagnationWatchdog(context.Background(), now, watchdogWorkspaceID())
}

func TestSweepScanA_HitMentionsParentAndDoesNotDone(t *testing.T) {
	parent := createIssueHTTP(t, "watchdog-a-parent", "in_progress")
	child1 := createIssueHTTP(t, "watchdog-a-child-1", "todo")
	child2 := createIssueHTTP(t, "watchdog-a-child-2", "backlog")
	agentID := handlerTestAgentID(t)
	setIssueAssigneeDirect(t, parent.ID, "agent", agentID)
	attachWatchdogChild(t, child1.ID, parent.ID, 1)
	attachWatchdogChild(t, child2.ID, parent.ID, 2)
	setIssueStatusDirect(t, child1.ID, "done")
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, parent.ID)
		testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, parent.ID)
	})

	runWatchdog(t, time.Now().UTC())

	if !watchdogCommentContains(t, parent.ID, watchdogMarkerABarrier) {
		t.Fatal("scan A hit must post a watchdog comment on the parent")
	}
	content, _, _, _ := systemCommentOn(t, parent.ID)
	if !strings.Contains(content, "mention://agent/"+agentID) {
		t.Errorf("scan A hit must mention the parent assignee, got: %s", content)
	}
	if got := countPendingTasksForAgent(t, parent.ID, agentID); got != 1 {
		t.Fatalf("scan A hit must enqueue once, got %d", got)
	}
	if issueStatusOf(t, parent.ID) != "in_progress" {
		t.Fatalf("scan must not write parent status, got %s", issueStatusOf(t, parent.ID))
	}
	if issueStatusOf(t, child2.ID) != "backlog" {
		t.Fatal("scan must not promote backlog children")
	}
}

func TestSweepScanA_MissWhenStageStillOpen(t *testing.T) {
	parent := createIssueHTTP(t, "watchdog-a-miss-parent", "in_progress")
	child := createIssueHTTP(t, "watchdog-a-miss-child", "in_progress")
	agentID := handlerTestAgentID(t)
	setIssueAssigneeDirect(t, parent.ID, "agent", agentID)
	if _, err := testPool.Exec(context.Background(),
		`UPDATE issue SET parent_issue_id = $2, stage = 1 WHERE id = $1`,
		child.ID, parent.ID); err != nil {
		t.Fatalf("attach child: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, parent.ID)
		testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, parent.ID)
	})

	runWatchdog(t, time.Now().UTC())

	if watchdogCommentContains(t, parent.ID, "watchdog:scan-a") {
		t.Fatal("open stage must not hit scan A")
	}
	if got := countPendingTasksForAgent(t, parent.ID, agentID); got != 0 {
		t.Fatalf("open stage must not enqueue, got %d", got)
	}
}

func TestSweepScanA_SkipsEnqueueWhenAssigneeAlreadyRunning(t *testing.T) {
	parent := createIssueHTTP(t, "watchdog-a-active-parent", "in_progress")
	child1 := createIssueHTTP(t, "watchdog-a-active-child-1", "todo")
	child2 := createIssueHTTP(t, "watchdog-a-active-child-2", "backlog")
	agentID := handlerTestAgentID(t)
	setIssueAssigneeDirect(t, parent.ID, "agent", agentID)
	attachWatchdogChild(t, child1.ID, parent.ID, 1)
	attachWatchdogChild(t, child2.ID, parent.ID, 2)
	setIssueStatusDirect(t, child1.ID, "done")
	insertIssueTaskWithStatus(t, agentID, parent.ID, "running")
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, parent.ID)
	})

	runWatchdog(t, time.Now().UTC())

	if watchdogCommentContains(t, parent.ID, watchdogMarkerABarrier) {
		t.Fatal("active (issue, agent) must skip scan A barrier entirely")
	}
	if got := countPendingTasksForAgent(t, parent.ID, agentID); got != 1 {
		t.Fatalf("running pair must not get a second enqueue, got %d", got)
	}
}

func TestSweepScanA_StalledReviewMentionsParentWithoutDone(t *testing.T) {
	parent := createIssueHTTP(t, "watchdog-a2-parent", "in_progress")
	child := createIssueHTTP(t, "watchdog-a2-child", "in_review")
	agentID := handlerTestAgentID(t)
	setIssueAssigneeDirect(t, parent.ID, "agent", agentID)
	if _, err := testPool.Exec(context.Background(),
		`UPDATE issue SET parent_issue_id = $2, stage = 1 WHERE id = $1`,
		child.ID, parent.ID); err != nil {
		t.Fatalf("attach child: %v", err)
	}
	stale := time.Now().UTC().Add(-31 * time.Minute)
	setIssueActivityAt(t, child.ID, stale)
	setCloseMeta(t, child.ID, "close.at", stale.Format(time.RFC3339))
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, parent.ID)
		testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, parent.ID)
	})

	runWatchdog(t, time.Now().UTC())

	if !watchdogCommentContains(t, parent.ID, watchdogMarkerAReview) {
		t.Fatal("stale in_review stage must hit scan A review")
	}
	if issueStatusOf(t, child.ID) != "in_review" {
		t.Fatal("scan A review must not auto-done the child")
	}
	if issueStatusOf(t, parent.ID) != "in_progress" {
		t.Fatal("scan A review must not auto-done the parent")
	}
}

func TestSweepScanB_HitAndMiss(t *testing.T) {
	agentID := handlerTestAgentID(t)
	hit := createIssueHTTP(t, "watchdog-b-hit", "in_progress")
	missFresh := createIssueHTTP(t, "watchdog-b-fresh", "in_progress")
	missReview := createIssueHTTP(t, "watchdog-b-review", "in_review")
	setIssueAssigneeDirect(t, hit.ID, "agent", agentID)
	setIssueAssigneeDirect(t, missFresh.ID, "agent", agentID)
	setIssueAssigneeDirect(t, missReview.ID, "agent", agentID)
	stale := time.Now().UTC().Add(-31 * time.Minute)
	setIssueActivityAt(t, hit.ID, stale)
	setIssueActivityAt(t, missReview.ID, stale)
	t.Cleanup(func() {
		for _, id := range []string{hit.ID, missFresh.ID, missReview.ID} {
			testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, id)
			testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, id)
		}
	})

	runWatchdog(t, time.Now().UTC())

	if !watchdogCommentContains(t, hit.ID, watchdogMarkerB) {
		t.Fatal("stale in_progress with no active task must hit scan B")
	}
	if watchdogCommentContains(t, missFresh.ID, watchdogMarkerB) {
		t.Fatal("fresh in_progress must miss scan B")
	}
	if watchdogCommentContains(t, missReview.ID, watchdogMarkerB) {
		t.Fatal("in_review must miss scan B")
	}
}

func TestSweepScanB_MissWhenIssueHasActiveTask(t *testing.T) {
	agentID := handlerTestAgentID(t)
	issue := createIssueHTTP(t, "watchdog-b-active", "in_progress")
	setIssueAssigneeDirect(t, issue.ID, "agent", agentID)
	setIssueActivityAt(t, issue.ID, time.Now().UTC().Add(-31*time.Minute))
	insertIssueTaskWithStatus(t, agentID, issue.ID, "queued")
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, issue.ID)
	})

	runWatchdog(t, time.Now().UTC())

	if watchdogCommentContains(t, issue.ID, watchdogMarkerB) {
		t.Fatal("active task must exclude scan B")
	}
}

func TestSweepScanC_HitWhenFailureRecordAndBarrierStillClosed(t *testing.T) {
	parent := createIssueHTTP(t, "watchdog-c-parent", "in_progress")
	child1 := createIssueHTTP(t, "watchdog-c-child-1", "todo")
	child2 := createIssueHTTP(t, "watchdog-c-child-2", "backlog")
	agentID := handlerTestAgentID(t)
	setIssueAssigneeDirect(t, parent.ID, "agent", agentID)
	attachWatchdogChild(t, child1.ID, parent.ID, 1)
	attachWatchdogChild(t, child2.ID, parent.ID, 2)
	setIssueStatusDirect(t, child1.ID, "done")
	if _, err := testPool.Exec(context.Background(),
		`INSERT INTO stage_wakeup_failure (workspace_id, parent_issue_id, child_issue_id, kind, error)
		 VALUES ($1, $2, $3, $4, $5)`,
		testWorkspaceID, parent.ID, child1.ID, wakeFailEnqueueAgent, "boom"); err != nil {
		t.Fatalf("insert wakeup failure: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM stage_wakeup_failure WHERE parent_issue_id = $1`, parent.ID)
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, parent.ID)
		testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, parent.ID)
	})

	runWatchdog(t, time.Now().UTC())

	if !watchdogCommentContains(t, parent.ID, watchdogMarkerC) {
		t.Fatal("scan C must mention the parent when a wakeup failure still matches scan A")
	}
	var swept bool
	if err := testPool.QueryRow(context.Background(),
		`SELECT swept_at IS NOT NULL FROM stage_wakeup_failure WHERE parent_issue_id = $1`,
		parent.ID).Scan(&swept); err != nil {
		t.Fatalf("read swept_at: %v", err)
	}
	if !swept {
		t.Fatal("scan C must mark the failure row swept")
	}
}

func TestSweepScanC_MissWhenParentNoLongerMatchesScanA(t *testing.T) {
	parent := createIssueHTTP(t, "watchdog-c-miss-parent", "in_progress")
	child := createIssueHTTP(t, "watchdog-c-miss-child", "in_progress")
	if _, err := testPool.Exec(context.Background(),
		`UPDATE issue SET parent_issue_id = $2, stage = 1 WHERE id = $1`,
		child.ID, parent.ID); err != nil {
		t.Fatalf("attach child: %v", err)
	}
	if _, err := testPool.Exec(context.Background(),
		`INSERT INTO stage_wakeup_failure (workspace_id, parent_issue_id, child_issue_id, kind)
		 VALUES ($1, $2, $3, $4)`,
		testWorkspaceID, parent.ID, child.ID, wakeFailListSiblings); err != nil {
		t.Fatalf("insert wakeup failure: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM stage_wakeup_failure WHERE parent_issue_id = $1`, parent.ID)
		testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, parent.ID)
	})

	runWatchdog(t, time.Now().UTC())

	if watchdogCommentContains(t, parent.ID, watchdogMarkerC) {
		t.Fatal("scan C must not fire when the parent no longer matches scan A")
	}
}

func TestSweepScanD_WaitingOnHitAndMiss(t *testing.T) {
	agentID := handlerTestAgentID(t)
	waited := createIssueHTTP(t, "watchdog-d1-waited", "in_progress")
	waiter := createIssueHTTP(t, "watchdog-d1-waiter", "in_review")
	openWaited := createIssueHTTP(t, "watchdog-d1-open-waited", "in_progress")
	openWaiter := createIssueHTTP(t, "watchdog-d1-open-waiter", "in_review")
	setIssueAssigneeDirect(t, waiter.ID, "agent", agentID)
	setIssueAssigneeDirect(t, openWaiter.ID, "agent", agentID)
	setCloseMeta(t, waiter.ID, "close.waiting_on", waited.Identifier)
	setCloseMeta(t, openWaiter.ID, "close.waiting_on", openWaited.Identifier)
	setIssueStatusDirect(t, waited.ID, "done")
	t.Cleanup(func() {
		for _, id := range []string{waiter.ID, openWaiter.ID} {
			testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, id)
			testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, id)
		}
	})

	runWatchdog(t, time.Now().UTC())

	if !watchdogCommentContains(t, waiter.ID, watchdogMarkerDWait) {
		t.Fatal("resolved waiting_on must hit scan D")
	}
	if watchdogCommentContains(t, openWaiter.ID, watchdogMarkerDWait) {
		t.Fatal("unresolved waiting_on must miss scan D")
	}
}

func TestSweepScanD_MentionHitAndMiss(t *testing.T) {
	agentID := handlerTestAgentID(t)
	hit := createIssueHTTP(t, "watchdog-d2-hit", "in_review")
	miss := createIssueHTTP(t, "watchdog-d2-fresh", "in_review")
	setIssueAssigneeDirect(t, hit.ID, "agent", agentID)
	setIssueAssigneeDirect(t, miss.ID, "agent", agentID)
	stale := time.Now().UTC().Add(-31 * time.Minute).Format(time.RFC3339)
	fresh := time.Now().UTC().Add(-5 * time.Minute).Format(time.RFC3339)
	setCloseMeta(t, hit.ID, "close.wake_action", "mention")
	setCloseMeta(t, hit.ID, "close.next_owner_id", agentID)
	setCloseMeta(t, hit.ID, "close.next_owner_type", "agent")
	setCloseMeta(t, hit.ID, "close.at", stale)
	setCloseMeta(t, miss.ID, "close.wake_action", "mention")
	setCloseMeta(t, miss.ID, "close.next_owner_id", agentID)
	setCloseMeta(t, miss.ID, "close.next_owner_type", "agent")
	setCloseMeta(t, miss.ID, "close.at", fresh)
	t.Cleanup(func() {
		for _, id := range []string{hit.ID, miss.ID} {
			testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, id)
			testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, id)
		}
	})

	runWatchdog(t, time.Now().UTC())

	if !watchdogCommentContains(t, hit.ID, watchdogMarkerDMention) {
		t.Fatal("stale mention wake must hit scan D2")
	}
	if watchdogCommentContains(t, miss.ID, watchdogMarkerDMention) {
		t.Fatal("fresh close.at must miss scan D2")
	}
}

func TestRecordStageWakeupFailure_IsQueryable(t *testing.T) {
	parent := createIssueHTTP(t, "watchdog-record-parent", "in_progress")
	child := createIssueHTTP(t, "watchdog-record-child", "in_progress")
	testHandler.recordStageWakeupFailure(context.Background(),
		watchdogWorkspaceID(), parseUUID(parent.ID), parseUUID(child.ID),
		wakeFailCreateComment, context.DeadlineExceeded)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM stage_wakeup_failure WHERE parent_issue_id = $1`, parent.ID)
	})
	var kind, errText string
	if err := testPool.QueryRow(context.Background(),
		`SELECT kind, error FROM stage_wakeup_failure WHERE parent_issue_id = $1`,
		parent.ID).Scan(&kind, &errText); err != nil {
		t.Fatalf("read failure record: %v", err)
	}
	if kind != wakeFailCreateComment {
		t.Fatalf("kind = %q, want %s", kind, wakeFailCreateComment)
	}
	if errText == "" {
		t.Fatal("failure record must keep the error text")
	}
}
