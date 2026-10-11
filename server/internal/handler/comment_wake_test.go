package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// postCommentWakeAs posts content and returns the decoded wake summary. A
// non-empty agentID/taskID authors it as that agent.
func postCommentWakeAs(t *testing.T, issueID, content, agentID, taskID string, parentID ...string) *CommentWake {
	t.Helper()
	body := map[string]any{"content": content}
	if len(parentID) > 0 {
		body["parent_id"] = parentID[0]
	}
	w := httptest.NewRecorder()
	r := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", body), "id", issueID)
	if agentID != "" {
		r.Header.Set("X-Agent-ID", agentID)
		r.Header.Set("X-Task-ID", taskID)
	}
	testHandler.CreateComment(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp CommentResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode comment: %v", err)
	}
	if resp.Wake == nil {
		t.Fatal("create response carries no wake summary")
	}
	return resp.Wake
}

// assignedAgentWithTask makes an issue assigned to a fresh agent whose one run
// on it sits in taskStatus, and returns the agent, that run, and the comment
// that triggered it (a comment-triggered run must reply under it).
func assignedAgentWithTask(t *testing.T, title, taskStatus string) (issueID, agentID, taskID, triggerID string) {
	t.Helper()
	agentID = createHandlerTestAgent(t, title+" Seat", nil)
	issueID = createCommentTriggerPreviewIssue(t, title, "agent", agentID)
	triggerID = postCommentForTriggerPreviewTest(t, issueID, map[string]any{"content": "start"})
	if err := testPool.QueryRow(context.Background(), `
		UPDATE agent_task_queue SET status = $3,
			completed_at = CASE WHEN $3 = 'completed' THEN now() END,
			started_at = CASE WHEN $3 = 'running' THEN now() END
		WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'
		RETURNING id
	`, issueID, agentID, taskStatus).Scan(&taskID); err != nil {
		t.Fatalf("set assignee task %s: %v", taskStatus, err)
	}
	return issueID, agentID, taskID, triggerID
}

// DENE-1757: the assignee posts "please continue" with no @ and no handoff.
// It cannot wake itself, nothing is running, the issue is open — the receipt
// must say no one was woken.
func TestCreateComment_WakeAssigneeSelfCommentWakesNoOne(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	issueID, agentID, taskID, triggerID := assignedAgentWithTask(t, "wake self comment", "completed")
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET status = 'in_review' WHERE id = $1`, issueID); err != nil {
		t.Fatal(err)
	}

	wake := postCommentWakeAs(t, issueID, "请执行席继续原票返工", agentID, taskID, triggerID)
	if len(wake.Woken) != 0 || !wake.NoneWoken {
		t.Fatalf("wake = %+v, want none woken", wake)
	}
}

func TestCreateComment_WakeListsAssigneeAndMentionedAgents(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	assigneeID := createHandlerTestAgent(t, "Wake Assignee", nil)
	mentionedID := createHandlerTestAgent(t, "Wake Mentioned", nil)
	issueID := createCommentTriggerPreviewIssue(t, "wake lists woken agents", "agent", assigneeID)

	wake := postCommentWakeAs(t, issueID, "plain note to the assignee", "", "")
	if wake.NoneWoken || len(wake.Woken) != 1 || wake.Woken[0].AgentID != assigneeID ||
		wake.Woken[0].Source != string(commentTriggerSourceIssueAssignee) || wake.Woken[0].Status != DispatchQueued ||
		wake.Woken[0].AgentName != "Wake Assignee" {
		t.Fatalf("assignee wake = %+v", wake)
	}

	other := createCommentTriggerPreviewIssue(t, "wake lists mentioned agent", "", "")
	wake = postCommentWakeAs(t, other, fmt.Sprintf("[@M](mention://agent/%s) take this", mentionedID), "", "")
	if wake.NoneWoken || len(wake.Woken) != 1 || wake.Woken[0].AgentID != mentionedID ||
		wake.Woken[0].Source != string(commentTriggerSourceMentionAgent) {
		t.Fatalf("mention wake = %+v", wake)
	}
}

// A closed issue, a run already in flight, and a /note are not unattended.
func TestCreateComment_WakeNoneWokenNotClaimedWhenAttendedOrClosed(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	for _, status := range []string{"done", "cancelled"} {
		issueID, agentID, taskID, triggerID := assignedAgentWithTask(t, "wake closed "+status, "completed")
		if _, err := testPool.Exec(context.Background(), `UPDATE issue SET status = $2 WHERE id = $1`, issueID, status); err != nil {
			t.Fatal(err)
		}
		if wake := postCommentWakeAs(t, issueID, "wrap-up note", agentID, taskID, triggerID); wake.NoneWoken {
			t.Fatalf("%s issue: wake = %+v, want none_woken false", status, wake)
		}
	}

	issueID, agentID, taskID, triggerID := assignedAgentWithTask(t, "wake running run", "running")
	if wake := postCommentWakeAs(t, issueID, "progress from inside the run", agentID, taskID, triggerID); wake.NoneWoken {
		t.Fatalf("running run: wake = %+v, want none_woken false", wake)
	}

	unassigned := createCommentTriggerPreviewIssue(t, "wake note", "", "")
	if wake := postCommentWakeAs(t, unassigned, "/note just for me", "", ""); wake.NoneWoken {
		t.Fatalf("/note: wake = %+v, want none_woken false", wake)
	}
	if wake := postCommentWakeAs(t, unassigned, "anyone?", "", ""); !wake.NoneWoken {
		t.Fatalf("unassigned open issue: wake = %+v, want none_woken", wake)
	}
}
