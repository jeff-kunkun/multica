package handler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func issueAlerts(t *testing.T, issueID, kind string) int {
	t.Helper()
	var n int
	dbfx.QueryRow(t, `SELECT count(*) FROM office_event WHERE issue_id=$1 AND event_type='issue.alert'
		AND changes @> jsonb_build_array(jsonb_build_object('field','alert','new',$2::text))`, issueID, kind).Scan(&n)
	return n
}

func TestIssueRelayHandoffAndManagerAlerts(t *testing.T) {
	f := newOfficeFixture(t)
	ctx := context.Background()
	agentA := createHandlerTestAgent(t, "RelayA", []byte("[]"))
	agentB := createHandlerTestAgent(t, "RelayB", []byte("[]"))
	issueID := createCommentTriggerPreviewIssue(t, "relay issue", "agent", agentA)
	dbfx.Exec(t, `UPDATE issue SET description=$2 WHERE id=$1`, issueID,
		"做完后交棒\n完成后 [@RelayB](mention://agent/"+agentB+")")
	agentType := "agent"
	resp := func(status string) IssueResponse {
		return IssueResponse{ID: issueID, Identifier: "T-1", Title: "relay issue", Status: status, AssigneeType: &agentType, AssigneeID: &agentA}
	}
	countHandoffs := func() (comments, tasks int) {
		dbfx.QueryRow(t, `SELECT count(*) FROM comment WHERE issue_id=$1 AND content LIKE '%mention://agent/'||$2||'%'`, issueID, agentB).Scan(&comments)
		dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id=$1 AND agent_id=$2`, issueID, agentB).Scan(&tasks)
		return
	}

	// A workflow step already moved the issue on: the description line stays quiet.
	testHandler.relayOnIssueUpdated(events.Event{
		Type: protocol.EventIssueUpdated, WorkspaceID: testWorkspaceID, ActorType: "agent", ActorID: agentA,
		Payload: map[string]any{"status_changed": true, "issue": resp("in_review"), "workflow_handoff": map[string]any{}},
	})
	// A person's own move is not a hand-off either.
	testHandler.relayOnIssueUpdated(events.Event{
		Type: protocol.EventIssueUpdated, WorkspaceID: testWorkspaceID, ActorType: "member", ActorID: testUserID,
		Payload: map[string]any{"status_changed": true, "issue": resp("in_review")},
	})
	if c, tk := countHandoffs(); c != 0 || tk != 0 {
		t.Fatalf("skipped handoff posted: comments=%d tasks=%d", c, tk)
	}
	// The agent finishing posts the @ and queues the next agent.
	testHandler.relayOnIssueUpdated(events.Event{
		Type: protocol.EventIssueUpdated, WorkspaceID: testWorkspaceID, ActorType: "agent", ActorID: agentA,
		Payload: map[string]any{"status_changed": true, "issue": resp("in_review")},
	})
	if c, tk := countHandoffs(); c != 1 || tk != 1 {
		t.Fatalf("handoff: comments=%d tasks=%d, want 1/1", c, tk)
	}
	// A blocked status is left to issue.status_changed; the relay adds nothing.
	testHandler.relayOnIssueUpdated(events.Event{
		Type: protocol.EventIssueUpdated, WorkspaceID: testWorkspaceID, ActorType: "agent", ActorID: agentA,
		Payload: map[string]any{"status_changed": true, "issue": resp("blocked")},
	})
	if n := issueAlerts(t, issueID, "stuck") + issueAlerts(t, issueID, "blocked"); n != 0 {
		t.Fatalf("blocked status alerted %d times", n)
	}

	// An agent's "stuck" comment alerts; a plain one, or a person's, does not.
	for _, c := range []struct{ author, authorID, content string }{
		{"agent", agentA, "all fine"},
		{"member", testUserID, "我卡住了"},
		{"agent", agentA, "我卡住了：缺授权"},
	} {
		id := dbfx.Insert(t, "comment", testutil.Cols{"issue_id": issueID, "workspace_id": testWorkspaceID,
			"author_type": c.author, "author_id": c.authorID, "content": c.content, "type": "comment"})
		testHandler.relayOnCommentCreated(events.Event{
			Type: protocol.EventCommentCreated, WorkspaceID: testWorkspaceID, ActorType: c.author, ActorID: c.authorID,
			Payload: map[string]any{"comment": map[string]any{"id": id}},
		})
	}
	if n := issueAlerts(t, issueID, "stuck"); n != 1 {
		t.Fatalf("stuck alerts = %d, want 1", n)
	}

	// Stall: 20 minutes of silence alerts once; fresh activity re-arms.
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='completed' WHERE issue_id=$1`, issueID)
	old := time.Now().Add(-20 * time.Minute)
	dbfx.Exec(t, `UPDATE issue SET status='in_progress', updated_at=$2, last_activity_at=$2 WHERE id=$1`, issueID, old)
	testHandler.sweepStalled(ctx)
	testHandler.sweepStalled(ctx)
	if n := issueAlerts(t, issueID, "stalled"); n != 1 {
		t.Fatalf("stalled alerts = %d, want exactly 1", n)
	}
	ev := latestOfficeEvent(t, `issue_id=$1 AND event_type='issue.alert'`, issueID)
	f.seal(t)
	ev = latestOfficeEvent(t, `event_id=$1`, ev.eventID)
	if ev.meaning != "alert" || !strings.Contains(ev.summary, "停摆") || !strings.Contains(ev.summary, "RelayA") || ev.actorType != "system" {
		t.Fatalf("sealed alert: %+v", ev)
	}
	if status, _, _, _ := deliveryStatus(t, ev.eventID, uuidToString(f.bot.ID)); status == "" {
		t.Fatal("the issue owner's Bot got no delivery for the alert")
	}
	dbfx.Exec(t, `UPDATE issue SET updated_at=$2, last_activity_at=$2 WHERE id=$1`, issueID, time.Now().Add(-16*time.Minute))
	testHandler.sweepStalled(ctx)
	if n := issueAlerts(t, issueID, "stalled"); n != 1 {
		t.Fatalf("alerted again inside the same silence: %d", n)
	}
	dbfx.Exec(t, `UPDATE office_event SET occurred_at=now()-interval '30 minutes' WHERE issue_id=$1 AND event_type='issue.alert'`, issueID)
	testHandler.sweepStalled(ctx)
	if n := issueAlerts(t, issueID, "stalled"); n != 2 {
		t.Fatalf("new silence after activity: %d alerts, want 2", n)
	}
}
