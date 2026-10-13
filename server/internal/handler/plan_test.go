package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

type planTestSetup struct {
	issueID string
	agentID string
}

func newPlanTestSetup(t *testing.T) planTestSetup {
	t.Helper()
	var parent IssueResponse
	testutil.Call(t, testHandler.CreateIssue, newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": "plan parent " + t.Name(),
	})).Want(http.StatusCreated).JSON(&parent)
	issueID := parent.ID
	dbfx.Cleanup(t, `DELETE FROM comment WHERE issue_id = $1`, issueID)
	dbfx.Cleanup(t, `DELETE FROM issue WHERE id = $1`, issueID)
	agentID := createHandlerTestAgent(t, "Plan Agent "+t.Name(), nil)
	// Step issues the handler creates hang off the parent; remove them and
	// their queued tasks before the parent's own cleanup runs.
	dbfx.Cleanup(t, `DELETE FROM issue WHERE parent_issue_id = $1`, issueID)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id IN (SELECT id FROM issue WHERE parent_issue_id = $1)`, issueID)
	dbfx.Cleanup(t, `DELETE FROM plan WHERE issue_id = $1`, issueID)
	return planTestSetup{issueID: issueID, agentID: agentID}
}

func (s planTestSetup) proposeBody() map[string]any {
	return map[string]any{
		"goal":         "Ship the plan card",
		"out_of_scope": "No UI in this round",
		"done_when":    "The CLI walks propose to approve",
		"steps": []map[string]any{
			{"title": "Write the API", "assignee_type": "agent", "assignee_id": s.agentID},
			{"title": "Read the result", "assignee_type": "member", "assignee_id": testUserID},
		},
	}
}

func proposePlan(t *testing.T, issueID string, body map[string]any, want int) PlanResponse {
	t.Helper()
	req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/plans", body), "id", issueID)
	var out PlanResponse
	resp := testutil.Call(t, testHandler.ProposePlan, req).Want(want)
	if want == http.StatusCreated {
		resp.JSON(&out)
	}
	return out
}

func approveRequest(issueID string, version int) *http.Request {
	return testutil.WithURLParams(newRequest(http.MethodPost, fmt.Sprintf("/api/issues/%s/plans/%d/approve", issueID, version), nil),
		"id", issueID, "version", strconv.Itoa(version))
}

func childIssueCount(t *testing.T, parentID string) int {
	return dbfx.Count(t, `SELECT count(*) FROM issue WHERE parent_issue_id = $1`, parentID)
}

func childTaskCount(t *testing.T, parentID string) int {
	return dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id IN (SELECT id FROM issue WHERE parent_issue_id = $1)`, parentID)
}

// A second workspace member with the plain "member" role.
func planTestMember(t *testing.T) string {
	t.Helper()
	suffix := time.Now().UnixNano()
	userID := dbfx.User(t, "Plan Member", fmt.Sprintf("plan-member-%d@multica.ai", suffix))
	dbfx.Member(t, testWorkspaceID, userID, "member")
	dbfx.Cleanup(t, `DELETE FROM plan_approver WHERE user_id = $1`, userID)
	return userID
}

func TestProposePlanStartsNothing(t *testing.T) {
	s := newPlanTestSetup(t)
	plan := proposePlan(t, s.issueID, s.proposeBody(), http.StatusCreated)

	if plan.Version != 1 || plan.Status != "proposed" || len(plan.Steps) != 2 {
		t.Fatalf("plan = v%d %s with %d steps, want v1 proposed with 2", plan.Version, plan.Status, len(plan.Steps))
	}
	if plan.CommentID == nil {
		t.Fatalf("plan has no card comment")
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE id = $1 AND issue_id = $2`, *plan.CommentID, s.issueID); n != 1 {
		t.Fatalf("plan card comment rows = %d, want 1", n)
	}
	if n := childIssueCount(t, s.issueID); n != 0 {
		t.Fatalf("proposing created %d issues, want 0", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id = $1`, s.agentID); n != 0 {
		t.Fatalf("proposing queued %d tasks for the step agent, want 0", n)
	}
}

func TestProposePlanValidation(t *testing.T) {
	s := newPlanTestSetup(t)
	for name, mutate := range map[string]func(map[string]any){
		"missing out_of_scope": func(b map[string]any) { b["out_of_scope"] = "  " },
		"missing done_when":    func(b map[string]any) { delete(b, "done_when") },
		"no steps":             func(b map[string]any) { b["steps"] = []map[string]any{} },
		"six steps": func(b map[string]any) {
			steps := []map[string]any{}
			for i := range 6 {
				steps = append(steps, map[string]any{"title": fmt.Sprintf("s%d", i), "assignee_type": "member", "assignee_id": testUserID})
			}
			b["steps"] = steps
		},
		"step without owner": func(b map[string]any) {
			b["steps"] = []map[string]any{{"title": "x", "assignee_type": "", "assignee_id": ""}}
		},
		"duplicate step titles": func(b map[string]any) {
			b["steps"] = []map[string]any{
				{"title": "same", "assignee_type": "member", "assignee_id": testUserID},
				{"title": "same", "assignee_type": "agent", "assignee_id": s.agentID},
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			body := s.proposeBody()
			mutate(body)
			proposePlan(t, s.issueID, body, http.StatusBadRequest)
		})
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM plan WHERE issue_id = $1`, s.issueID); n != 0 {
		t.Fatalf("invalid proposals wrote %d plans", n)
	}
}

func TestApprovePlanRejectsUnauthorizedCallers(t *testing.T) {
	s := newPlanTestSetup(t)
	proposePlan(t, s.issueID, s.proposeBody(), http.StatusCreated)

	t.Run("plain member", func(t *testing.T) {
		req := approveRequest(s.issueID, 1)
		req.Header.Set("X-User-ID", planTestMember(t))
		testutil.Call(t, testHandler.ApprovePlan, req).Want(http.StatusForbidden)
	})
	t.Run("agent task token", func(t *testing.T) {
		req := approveRequest(s.issueID, 1)
		req.Header.Set("X-Actor-Source", "task_token")
		req.Header.Set("X-Agent-ID", s.agentID)
		testutil.Call(t, testHandler.ApprovePlan, req).Want(http.StatusForbidden)
	})
	t.Run("agent identity headers", func(t *testing.T) {
		req := approveRequest(s.issueID, 1)
		req.Header.Set("X-Agent-ID", s.agentID)
		req.Header.Set("X-Task-ID", createHandlerTestTaskForAgent(t, s.agentID))
		testutil.Call(t, testHandler.ApprovePlan, req).Want(http.StatusForbidden)
	})
	t.Run("cloud pat", func(t *testing.T) {
		req := approveRequest(s.issueID, 1)
		req.Header.Set("X-Actor-Source", "cloud_pat")
		testutil.Call(t, testHandler.ApprovePlan, req).Want(http.StatusForbidden)
	})

	if n := childIssueCount(t, s.issueID); n != 0 {
		t.Fatalf("rejected approvals created %d issues", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM plan WHERE issue_id = $1 AND status = 'proposed'`, s.issueID); n != 1 {
		t.Fatalf("plan left proposed = %d, want 1", n)
	}
}

func TestApprovePlanCreatesStepIssuesAndStartsThem(t *testing.T) {
	s := newPlanTestSetup(t)
	proposePlan(t, s.issueID, s.proposeBody(), http.StatusCreated)
	v2 := proposePlan(t, s.issueID, s.proposeBody(), http.StatusCreated)
	if v2.Version != 2 {
		t.Fatalf("second proposal version = %d, want 2", v2.Version)
	}

	// The replaced version can no longer be approved.
	testutil.Call(t, testHandler.ApprovePlan, approveRequest(s.issueID, 1)).Want(http.StatusConflict)
	if n := childIssueCount(t, s.issueID); n != 0 {
		t.Fatalf("stale approval created %d issues", n)
	}

	var out struct {
		Plan   PlanResponse    `json:"plan"`
		Issues []IssueResponse `json:"issues"`
	}
	testutil.Call(t, testHandler.ApprovePlan, approveRequest(s.issueID, 2)).Want(http.StatusOK).JSON(&out)
	if out.Plan.Status != "approved" || out.Plan.ApprovedBy == nil || *out.Plan.ApprovedBy != testUserID {
		t.Fatalf("plan after approval = %s by %v", out.Plan.Status, out.Plan.ApprovedBy)
	}
	if len(out.Issues) != 2 {
		t.Fatalf("approval returned %d issues, want 2", len(out.Issues))
	}
	for i, step := range out.Plan.Steps {
		if step.IssueID == nil || *step.IssueID != out.Issues[i].ID {
			t.Fatalf("step %d issue_id = %v, want %s", i+1, step.IssueID, out.Issues[i].ID)
		}
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue WHERE parent_issue_id = $1 AND assignee_type = 'agent' AND assignee_id = $2 AND status = 'todo'`, s.issueID, s.agentID); n != 1 {
		t.Fatalf("agent step issues = %d, want 1", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id = $1 AND issue_id IN (SELECT id FROM issue WHERE parent_issue_id = $2)`, s.agentID, s.issueID); n != 1 {
		t.Fatalf("tasks queued for the agent step = %d, want 1", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1 AND parent_id = $2`, s.issueID, *v2.CommentID); n != 1 {
		t.Fatalf("approval replies under the plan card = %d, want 1", n)
	}

	// Approving again is a no-op once every step has its issue.
	var again struct {
		Issues []IssueResponse `json:"issues"`
	}
	testutil.Call(t, testHandler.ApprovePlan, approveRequest(s.issueID, 2)).Want(http.StatusOK).JSON(&again)
	if len(again.Issues) != 0 || childIssueCount(t, s.issueID) != 2 || childTaskCount(t, s.issueID) != 1 {
		t.Fatalf("re-approval created more work: issues=%d children=%d tasks=%d", len(again.Issues), childIssueCount(t, s.issueID), childTaskCount(t, s.issueID))
	}

	// A new proposal after approval is a fresh version that needs its own approval.
	v3 := proposePlan(t, s.issueID, s.proposeBody(), http.StatusCreated)
	if v3.Version != 3 || v3.Status != "proposed" {
		t.Fatalf("post-approval proposal = v%d %s", v3.Version, v3.Status)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM plan WHERE issue_id = $1 AND version = 2 AND status = 'approved'`, s.issueID); n != 1 {
		t.Fatalf("approved v2 changed by a new proposal")
	}
}

func TestPlanApproverGrant(t *testing.T) {
	s := newPlanTestSetup(t)
	proposePlan(t, s.issueID, s.proposeBody(), http.StatusCreated)
	memberID := planTestMember(t)

	// A plain member cannot grant themselves approval.
	selfGrant := newRequest(http.MethodPost, "/api/plan-approvers", map[string]any{"user_id": memberID})
	selfGrant.Header.Set("X-User-ID", memberID)
	testutil.Call(t, testHandler.AddPlanApprover, selfGrant).Want(http.StatusForbidden)

	// The owner grants it; the member can now approve.
	testutil.Call(t, testHandler.AddPlanApprover, newRequest(http.MethodPost, "/api/plan-approvers", map[string]any{"user_id": memberID})).Want(http.StatusOK)
	var list struct {
		Approvers []PlanApproverResponse `json:"approvers"`
	}
	testutil.Call(t, testHandler.ListPlanApprovers, newRequest(http.MethodGet, "/api/plan-approvers", nil)).Want(http.StatusOK).JSON(&list)
	found := false
	for _, a := range list.Approvers {
		found = found || a.UserID == memberID
	}
	if !found {
		t.Fatalf("granted member missing from approver list")
	}

	req := approveRequest(s.issueID, 1)
	req.Header.Set("X-User-ID", memberID)
	testutil.Call(t, testHandler.ApprovePlan, req).Want(http.StatusOK)
	if n := childIssueCount(t, s.issueID); n != 2 {
		t.Fatalf("granted approver created %d issues, want 2", n)
	}

	// Revoked approvers lose the right on the next plan.
	revoke := testutil.WithURLParams(newRequest(http.MethodDelete, "/api/plan-approvers/"+memberID, nil), "userId", memberID)
	testutil.Call(t, testHandler.RemovePlanApprover, revoke).Want(http.StatusNoContent)
	proposePlan(t, s.issueID, s.proposeBody(), http.StatusCreated)
	req = approveRequest(s.issueID, 2)
	req.Header.Set("X-User-ID", memberID)
	testutil.Call(t, testHandler.ApprovePlan, req).Want(http.StatusForbidden)
}
