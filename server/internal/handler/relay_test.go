package handler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func relayAlerts(t *testing.T, issueID, kind string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM bot_event
		WHERE workspace_id = $1 AND type = 'issue:alert' AND payload->>'issue_id' = $2 AND payload->>'kind' = $3`,
		testWorkspaceID, issueID, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRelay(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `
		INSERT INTO workspace_bot (workspace_id, user_id, name, created_by) VALUES ($1, $2, 'relay-test', $2)`,
		testWorkspaceID, testUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM workspace_bot WHERE workspace_id = $1 AND name = 'relay-test'`, testWorkspaceID)
	})
	agentA := createHandlerTestAgent(t, "RelayA")
	agentB := createHandlerTestAgent(t, "RelayB")
	issueID := createCommentTriggerPreviewIssue(t, "relay issue", "agent", agentA)
	if _, err := testPool.Exec(ctx, `UPDATE issue SET description = $2 WHERE id = $1`, issueID,
		"做完后交棒\n完成后 [@RelayB](mention://agent/"+agentB+")"); err != nil {
		t.Fatal(err)
	}
	agentType := "agent"
	resp := func(status string) IssueResponse {
		return IssueResponse{ID: issueID, Identifier: "T-1", Title: "relay issue", Status: status,
			AssigneeType: &agentType, AssigneeID: &agentA}
	}

	// Hand-off: in_review posts the @ and queues the next agent.
	testHandler.relayOnIssueUpdated(events.Event{
		Type: protocol.EventIssueUpdated, WorkspaceID: testWorkspaceID, ActorType: "agent", ActorID: agentA,
		Payload: map[string]any{"status_changed": true, "issue": resp("in_review")},
	})
	var comments, tasks int
	testPool.QueryRow(ctx, `SELECT count(*) FROM comment WHERE issue_id = $1 AND content LIKE '%mention://agent/'||$2||'%'`, issueID, agentB).Scan(&comments)
	testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, issueID, agentB).Scan(&tasks)
	if comments != 1 || tasks != 1 {
		t.Fatalf("handoff: comments=%d tasks=%d, want 1/1", comments, tasks)
	}

	// Blocked status alerts the manager channel.
	testHandler.relayOnIssueUpdated(events.Event{
		Type: protocol.EventIssueUpdated, WorkspaceID: testWorkspaceID, ActorType: "agent", ActorID: agentA,
		Payload: map[string]any{"status_changed": true, "issue": resp("blocked")},
	})
	if n := relayAlerts(t, issueID, "blocked"); n != 1 {
		t.Fatalf("blocked alerts = %d, want 1", n)
	}

	// A "stuck" comment by an agent alerts too; a plain one does not.
	for _, c := range []string{"all fine", "我卡住了：缺授权"} {
		testHandler.relayOnCommentCreated(events.Event{
			Type: protocol.EventCommentCreated, WorkspaceID: testWorkspaceID, ActorType: "agent", ActorID: agentA,
			Payload: map[string]any{"comment": CommentResponse{IssueID: issueID, Type: "comment", Content: c}},
		})
	}
	if n := relayAlerts(t, issueID, "blocked"); n != 2 {
		t.Fatalf("after stuck comment alerts = %d, want 2", n)
	}

	// Stall: 20 minutes of silence alerts once; fresh activity re-arms.
	testPool.Exec(ctx, `UPDATE agent_task_queue SET status = 'completed' WHERE issue_id = $1`, issueID)
	old := time.Now().Add(-20 * time.Minute)
	testPool.Exec(ctx, `UPDATE issue SET status = 'in_progress', updated_at = $2, last_activity_at = $2 WHERE id = $1`, issueID, old)
	testHandler.sweepStalled(ctx)
	testHandler.sweepStalled(ctx)
	if n := relayAlerts(t, issueID, "stalled"); n != 1 {
		t.Fatalf("stalled alerts = %d, want exactly 1", n)
	}
	var text string
	testPool.QueryRow(ctx, `SELECT payload->>'text' FROM bot_event WHERE payload->>'issue_id' = $1 AND payload->>'kind' = 'stalled'`, issueID).Scan(&text)
	if !strings.Contains(text, "停摆") || !strings.Contains(text, "RelayA") {
		t.Fatalf("unexpected stalled text: %q", text)
	}
}
