package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func issueForGuardTest(t *testing.T, issueID string) db.Issue {
	t.Helper()
	issue, err := testHandler.Queries.GetIssue(context.Background(), util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue %s: %v", issueID, err)
	}
	return issue
}

// TestHaltIssueCancelsActiveRunsAndPreservesHumanTriggers verifies that the
// issue-level halt cancels every active queue state, blocks agent-originated
// work, and still permits a member's explicit trigger.
func TestHaltIssueCancelsActiveRunsAndPreservesHumanTriggers(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "Guard Halt Agent", []byte("[]"))
	issueID := dbfx.Issue(t, "halt issue", testutil.Cols{
		"creator_type":  "agent",
		"creator_id":    agentID,
		"assignee_type": "agent",
		"assignee_id":   agentID,
		"visibility":    "workspace",
	})
	active := []string{"queued", "dispatched", "running", "waiting_local_directory", "deferred"}
	taskIDs := make(map[string]string, len(active))
	// One agent per status: idx_one_pending_task_per_issue_agent_thread is
	// unique over (issue, agent, thread) for queued/dispatched, so seeding all
	// five states onto a single agent collides before the halt ever runs.
	for _, status := range active {
		statusAgent := createHandlerTestAgent(t, "Guard Halt Agent "+status, []byte("[]"))
		taskIDs[status] = insertIssueTaskWithStatus(t, statusAgent, issueID, status)
	}

	req := newRequest(http.MethodPost, "/api/issues/"+issueID+"/halt", nil)
	req = withURLParam(req, "id", issueID)
	resp := httptest.NewRecorder()
	testHandler.HaltIssue(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("halt issue: status = %d, body = %s", resp.Code, resp.Body.String())
	}
	for status, taskID := range taskIDs {
		if got := taskStatus(t, taskID); got != "cancelled" {
			t.Errorf("%s task status = %q, want cancelled", status, got)
		}
	}

	issue := issueForGuardTest(t, issueID)
	if _, err := testHandler.TaskService.EnqueueTaskForIssue(context.Background(), issue); !errors.Is(err, service.ErrAgentChainBudgetExceeded) {
		t.Fatalf("agent trigger during halt error = %v, want ErrAgentChainBudgetExceeded", err)
	}
	if _, err := testHandler.TaskService.EnqueueTaskForIssueByActor(context.Background(), issue, util.MustParseUUID(testUserID)); err != nil {
		t.Fatalf("human trigger during halt: %v", err)
	}
}
