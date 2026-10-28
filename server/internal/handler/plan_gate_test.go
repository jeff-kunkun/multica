package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/featureflags"
)

// Plan first (DENE-2670): an issue created with plan_first refuses its agent's
// review, done and PR-link moves until a person approves a plan; the answer is
// a reply under the plan card that wakes the agent on that comment, which is
// the path that resumes the issue session.
func TestPlanFirstGate(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.OfficePlanFirst, true)
	agentID := handlerTestAgentID(t)

	w := createIssueRaw(t, map[string]any{
		"title": "plan first", "status": "todo", "plan_first": true,
		"assignee_type": "agent", "assignee_id": agentID,
	}, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("create plan-first issue: %d %s", w.Code, w.Body.String())
	}
	var issue IssueResponse
	if err := json.Unmarshal(w.Body.Bytes(), &issue); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM plan WHERE issue_id = $1`, issue.ID)
		testPool.Exec(ctx, `DELETE FROM issue_plan_gate WHERE issue_id = $1`, issue.ID)
	})
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_plan_gate WHERE issue_id = $1`, issue.ID); n != 1 {
		t.Fatalf("gate rows = %d, want 1", n)
	}

	call := func(method, path string, body any, agent bool, params map[string]string, fn func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
		t.Helper()
		req := newRequest(method, path, body)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", issue.ID)
		for k, v := range params {
			rctx.URLParams.Add(k, v)
		}
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		if agent {
			req = asTaskAgent(req, agentID)
		}
		rec := httptest.NewRecorder()
		fn(rec, req)
		return rec
	}
	setStatus := func(status string, agent bool) *httptest.ResponseRecorder {
		return call("PUT", "/api/issues/"+issue.ID, map[string]any{"status": status}, agent, nil, testHandler.UpdateIssue)
	}
	wantPlanNotApproved := func(what string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), planNotApprovedCode) {
			t.Fatalf("%s: %d %s, want 409 %s", what, rec.Code, rec.Body.String(), planNotApprovedCode)
		}
	}

	// Before any approval the agent cannot hand in or finish.
	wantPlanNotApproved("agent -> in_review", setStatus("in_review", true))
	wantPlanNotApproved("agent -> done", setStatus("done", true))
	wantPlanNotApproved("agent links a PR", call("POST", "/api/issues/"+issue.ID+"/pull-requests",
		map[string]any{"url": "https://github.com/acme/repo/pull/1"}, true, nil, testHandler.LinkIssuePullRequest))
	batch := newRequest("POST", "/api/issues/batch-update", map[string]any{"issue_ids": []string{issue.ID}, "updates": map[string]any{"status": "in_review"}})
	batchRec := httptest.NewRecorder()
	testHandler.BatchUpdateIssues(batchRec, asTaskAgent(batch, agentID))
	var batchOut struct {
		Code     string                      `json:"code"`
		Updated  int                         `json:"updated"`
		Rejected []BatchUpdateIssueRejection `json:"rejected"`
	}
	if err := json.Unmarshal(batchRec.Body.Bytes(), &batchOut); err != nil {
		t.Fatalf("decode batch: %v", err)
	}
	if batchRec.Code != http.StatusConflict || batchOut.Code != planNotApprovedCode || batchOut.Updated != 0 ||
		len(batchOut.Rejected) != 1 || batchOut.Rejected[0].Code != planNotApprovedCode {
		t.Fatalf("batch agent -> in_review: %d %s, want 409 %s", batchRec.Code, batchRec.Body.String(), planNotApprovedCode)
	}
	if rec := setStatus("in_progress", true); rec.Code != http.StatusOK {
		t.Fatalf("agent -> in_progress: %d %s", rec.Code, rec.Body.String())
	}

	plans := func() IssuePlansResponse {
		t.Helper()
		rec := call("GET", "/api/issues/"+issue.ID+"/plans", nil, false, nil, testHandler.ListIssuePlans)
		if rec.Code != http.StatusOK {
			t.Fatalf("list plans: %d %s", rec.Code, rec.Body.String())
		}
		var out IssuePlansResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode plans: %v", err)
		}
		return out
	}
	if got := plans(); !got.Gated || got.Approved || !got.CanApprove || len(got.Plans) != 0 {
		t.Fatalf("initial plan state = %+v", got)
	}

	propose := func(goal string) PlanResponse {
		t.Helper()
		rec := call("POST", "/api/issues/"+issue.ID+"/plans", map[string]any{
			"goal": goal, "out_of_scope": "no mobile", "done_when": "tests pass",
			"steps": []map[string]string{{"title": "server"}, {"title": "ui"}},
		}, true, nil, testHandler.ProposeIssuePlan)
		if rec.Code != http.StatusCreated {
			t.Fatalf("propose %q: %d %s", goal, rec.Code, rec.Body.String())
		}
		var p PlanResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatalf("decode plan: %v", err)
		}
		return p
	}
	v1 := propose("first try")
	if v1.Version != 1 || v1.Status != "proposed" || v1.CommentID == nil || v1.AuthorType != "agent" {
		t.Fatalf("v1 = %+v", v1)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE id = $1 AND author_type = 'agent' AND content LIKE '%计划 v1%'`, *v1.CommentID); n != 1 {
		t.Fatalf("plan card comment missing")
	}

	answer := func(version, action string, body any, agent bool) *httptest.ResponseRecorder {
		fn := testHandler.ApproveIssuePlan
		if action == "request-changes" {
			fn = testHandler.RequestIssuePlanChanges
		}
		return call("POST", "/api/issues/"+issue.ID+"/plans/"+version+"/"+action, body, agent, map[string]string{"version": version}, fn)
	}
	if rec := answer("1", "approve", nil, true); rec.Code != http.StatusForbidden {
		t.Fatalf("agent approves: %d %s, want 403", rec.Code, rec.Body.String())
	}
	if rec := answer("1", "request-changes", map[string]any{"feedback": " "}, false); rec.Code != http.StatusBadRequest {
		t.Fatalf("request changes without feedback: %d %s, want 400", rec.Code, rec.Body.String())
	}

	// A reply under the agent's card is a comment-triggered run: the resume path.
	wokenBy := func(what, cardID string) {
		t.Helper()
		var replyID string
		dbfx.QueryRow(t, `SELECT id FROM comment WHERE parent_id = $1 AND author_type = 'member' ORDER BY created_at DESC LIMIT 1`, cardID).Scan(&replyID)
		if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND trigger_comment_id = $3`, issue.ID, agentID, replyID); n != 1 {
			t.Fatalf("%s: runs triggered by the reply = %d, want 1", what, n)
		}
	}
	if rec := answer("1", "request-changes", map[string]any{"feedback": "drop the ui step"}, false); rec.Code != http.StatusOK {
		t.Fatalf("request changes: %d %s", rec.Code, rec.Body.String())
	}
	wokenBy("request changes", *v1.CommentID)
	if got := plans(); got.Approved || got.Plans[0].ChangesRequestedAt == nil {
		t.Fatalf("after request changes = %+v", got)
	}
	wantPlanNotApproved("agent -> in_review after changes requested", setStatus("in_review", true))

	v2 := propose("second try")
	if v2.Version != 2 {
		t.Fatalf("v2 version = %d", v2.Version)
	}
	if rec := answer("1", "approve", nil, false); rec.Code != http.StatusConflict {
		t.Fatalf("approve superseded v1: %d %s, want 409", rec.Code, rec.Body.String())
	}
	approved := answer("2", "approve", nil, false)
	if approved.Code != http.StatusOK {
		t.Fatalf("approve v2: %d %s", approved.Code, approved.Body.String())
	}
	var approvedOut PlanAnswerResponse
	if err := json.Unmarshal(approved.Body.Bytes(), &approvedOut); err != nil {
		t.Fatalf("decode approve: %v", err)
	}
	if approvedOut.Wake == nil || len(approvedOut.Wake.Woken) == 0 || approvedOut.Wake.NoneWoken {
		t.Fatalf("approve v2 wake = %+v, want the agent woken", approvedOut.Wake)
	}
	wokenBy("approve", *v2.CommentID)
	if got := plans(); !got.Approved || got.Plans[0].Status != "approved" || got.Plans[1].Status != "superseded" {
		t.Fatalf("after approve = %+v", got)
	}
	// Approving again changes nothing and wakes nobody a second time.
	if rec := answer("2", "approve", nil, false); rec.Code != http.StatusOK {
		t.Fatalf("re-approve v2: %d %s", rec.Code, rec.Body.String())
	}
	wokenBy("re-approve", *v2.CommentID)
	// A wake that never produced a run (here: the runs are gone) is retried by
	// approving again, on the same approval reply.
	testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issue.ID)
	if rec := answer("2", "approve", nil, false); rec.Code != http.StatusOK {
		t.Fatalf("re-approve after a lost wake: %d %s", rec.Code, rec.Body.String())
	}
	wokenBy("re-approve after a lost wake", *v2.CommentID)
	if rec := call("POST", "/api/issues/"+issue.ID+"/plans", map[string]any{
		"goal": "late", "out_of_scope": "x", "done_when": "y", "steps": []map[string]string{{"title": "z"}},
	}, true, nil, testHandler.ProposeIssuePlan); rec.Code != http.StatusConflict {
		t.Fatalf("propose after approval: %d %s, want 409", rec.Code, rec.Body.String())
	}

	if rec := setStatus("in_review", true); rec.Code == http.StatusConflict && strings.Contains(rec.Body.String(), planNotApprovedCode) {
		t.Fatalf("agent -> in_review after approval still refused: %s", rec.Body.String())
	}
}

func TestPlanFirstUngatedIssueUnchanged(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.OfficePlanFirst, true)
	agentID := handlerTestAgentID(t)
	issue := createIssueHTTP(t, "no plan first", "in_progress")

	req := withURLParam(newRequest("POST", "/api/issues/"+issue.ID+"/plans", map[string]any{
		"goal": "g", "out_of_scope": "o", "done_when": "d", "steps": []map[string]string{{"title": "s"}},
	}), "id", issue.ID)
	rec := httptest.NewRecorder()
	testHandler.ProposeIssuePlan(rec, asTaskAgent(req, agentID))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "plan_not_requested") {
		t.Fatalf("propose on ungated issue: %d %s", rec.Code, rec.Body.String())
	}

	req = withURLParam(newRequest("PUT", "/api/issues/"+issue.ID, map[string]any{"status": "in_review"}), "id", issue.ID)
	rec = httptest.NewRecorder()
	testHandler.UpdateIssue(rec, asTaskAgent(req, agentID))
	if strings.Contains(rec.Body.String(), planNotApprovedCode) {
		t.Fatalf("ungated issue refused for a plan: %s", rec.Body.String())
	}
}

// With the office_plan_first server flag off the server behaves as before:
// plan_first is ignored, an existing gate holds nothing back, the claim has
// no plan_gate, and the plan endpoints are refused.
func TestPlanFirstServerFlagOff(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.OfficePlanFirst, false)
	agentID := handlerTestAgentID(t)

	w := createIssueRaw(t, map[string]any{
		"title": "plan first flag off", "status": "in_progress", "plan_first": true,
		"assignee_type": "agent", "assignee_id": agentID,
	}, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var issue IssueResponse
	if err := json.Unmarshal(w.Body.Bytes(), &issue); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue_plan_gate WHERE issue_id = $1`, issue.ID)
	})
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_plan_gate WHERE issue_id = $1`, issue.ID); n != 0 {
		t.Fatalf("gate rows with the flag off = %d, want 0", n)
	}

	// A gate written while the flag was on holds nothing back once it is off.
	testPool.Exec(context.Background(), `INSERT INTO issue_plan_gate (issue_id, workspace_id, requested_by_type, requested_by_id) SELECT id, workspace_id, 'member', creator_id FROM issue WHERE id = $1`, issue.ID)
	gated, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(issue.ID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	if got := testHandler.claimPlanGate(context.Background(), gated); got != "" {
		t.Fatalf("claim plan_gate with the flag off = %q, want none", got)
	}
	req := withURLParam(newRequest("PUT", "/api/issues/"+issue.ID, map[string]any{"status": "in_review"}), "id", issue.ID)
	rec := httptest.NewRecorder()
	testHandler.UpdateIssue(rec, asTaskAgent(req, agentID))
	if strings.Contains(rec.Body.String(), planNotApprovedCode) {
		t.Fatalf("flag off still refused for a plan: %s", rec.Body.String())
	}

	for _, ep := range []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
	}{
		{"list", testHandler.ListIssuePlans},
		{"propose", testHandler.ProposeIssuePlan},
		{"approve", testHandler.ApproveIssuePlan},
	} {
		req := withURLParam(newRequest("POST", "/api/issues/"+issue.ID+"/plans", map[string]any{}), "id", issue.ID)
		rec := httptest.NewRecorder()
		ep.fn(rec, req)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "plan_first_disabled") {
			t.Fatalf("%s with the flag off: %d %s, want 403 plan_first_disabled", ep.name, rec.Code, rec.Body.String())
		}
	}
}
