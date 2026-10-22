package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// asAgent stamps the request with the identity pair resolveActor trusts. The
// running task is what makes the agent's claim credible.
func asAgent(t *testing.T, req *http.Request, issueID string) string {
	t.Helper()
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issueID, "running")
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	return agentID
}

func readIssueStatus(t *testing.T, issueID string) string {
	t.Helper()
	var status string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	return status
}

// TestListIssueParkingRecords is the stage-2 read (DENE-881): the latest
// record per issue, flat, with the parent id to nest by.
func TestListIssueParkingRecords(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	parent := createIssueHTTP(t, "parking parent", "in_progress")
	child := createIssueHTTP(t, "parking child", "in_progress")
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE issue SET parent_issue_id = $1 WHERE id = $2`, parent.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO issue_parking_record
		(issue_id, workspace_id, state, category, stuck_kind, unexplained, summary, summary_source, next_owner_type, next_owner_id, issue_status, timeline)
		SELECT id, workspace_id, 'parked', 'stalled_unclosed', 'no_close', true, '运行已结束：票没有收口', 'template', 'agent', 'a1', status, '[{"kind":"run_started"}]'
		FROM issue WHERE id = $1`, child.ID); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	testHandler.ListIssueParkingRecords(w, newRequest("GET", "/api/issues/parking?unexplained_only=true", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Records []ParkingRecordResponse `json:"records"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	var got *ParkingRecordResponse
	for i := range resp.Records {
		if resp.Records[i].IssueID == child.ID {
			got = &resp.Records[i]
		}
	}
	if got == nil {
		t.Fatalf("child record missing: %s", w.Body.String())
	}
	if got.ParentIssueID == nil || *got.ParentIssueID != parent.ID || got.Identifier != child.Identifier {
		t.Errorf("parent/identifier = %v/%s", got.ParentIssueID, got.Identifier)
	}
	if !got.Unexplained || got.NextOwner.Type != "agent" || string(got.Timeline) != `[{"kind": "run_started"}]` && string(got.Timeline) != `[{"kind":"run_started"}]` {
		t.Errorf("record = %+v timeline=%s", got, got.Timeline)
	}
}
