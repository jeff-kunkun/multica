package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// activityBinding gives the test user a verified Bot channel with the stream
// on. The webhook secret is a placeholder: these tests stop at the outbox row.
func activityBinding(t *testing.T, scope ...string) (fx *testutil.Fixture, botID string) {
	t.Helper()
	fx = workspaceFixture(t)
	if scope == nil {
		scope = []string{}
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	botID = fx.Insert(t, "personal_access_token", testutil.Cols{
		"user_id": testUserID, "workspace_id": testWorkspaceID, "name": "stream listener bot",
		"token_hash": "hash-" + suffix, "token_prefix": "mbt_" + suffix[:6], "kind": "bot", "bot_type": "personal",
	})
	fx.Exec(t, `UPDATE bot_wake SET webhook_secret = '\x01' WHERE bot_id = $1`, botID)
	binding := fx.Insert(t, "member_bot_binding", testutil.Cols{"workspace_id": testWorkspaceID, "user_id": testUserID, "bot_id": botID})
	fx.Exec(t, `UPDATE member_bot_binding SET verified_at = now(), activity_scope = $2 WHERE id = $1`, binding, scope)
	return fx, botID
}

func activityEvents(t *testing.T, fx *testutil.Fixture, botID string) int {
	t.Helper()
	return fx.Count(t, `SELECT count(*) FROM human_needed_event WHERE bot_id = $1 AND event_type = 'activity'`, botID)
}

func TestActivityStreamListenersFeedComments_Status_AndChats(t *testing.T) {
	queries := db.New(testPool)
	bus := events.New()
	registerActivityStreamListeners(bus, queries)
	fx, botID := activityBinding(t, "owned")
	agentID, _ := firstFixtureAgent(t)
	issueID := fx.Issue(t, "stream listener", testutil.Cols{"visibility": "workspace"})

	// An agent's comment on a ticket the person created.
	commentID := fx.Comment(t, issueID, "完成了第一步", testutil.Cols{"author_type": "agent", "author_id": agentID})
	bus.Publish(events.Event{
		Type: protocol.EventCommentCreated, WorkspaceID: testWorkspaceID, ActorType: "agent", ActorID: agentID,
		Payload: map[string]any{"comment": handler.CommentResponse{ID: commentID, IssueID: issueID, AuthorType: "agent", AuthorID: agentID, Content: "完成了第一步"}},
	})
	if got := activityEvents(t, fx, botID); got != 1 {
		t.Fatalf("comment: %d events", got)
	}
	// The same comment published twice is one event.
	bus.Publish(events.Event{
		Type: protocol.EventCommentCreated, WorkspaceID: testWorkspaceID, ActorType: "agent", ActorID: agentID,
		Payload: map[string]any{"comment": handler.CommentResponse{ID: commentID, IssueID: issueID, AuthorType: "agent", AuthorID: agentID, Content: "完成了第一步"}},
	})
	if got := activityEvents(t, fx, botID); got != 1 {
		t.Fatalf("duplicate publish wrote %d events", got)
	}

	// The person's own comment is skipped.
	own := fx.Comment(t, issueID, "我自己说的")
	bus.Publish(events.Event{
		Type: protocol.EventCommentCreated, WorkspaceID: testWorkspaceID, ActorType: "member", ActorID: testUserID,
		Payload: map[string]any{"comment": handler.CommentResponse{ID: own, IssueID: issueID, AuthorType: "member", AuthorID: testUserID, Content: "我自己说的"}},
	})
	if got := activityEvents(t, fx, botID); got != 1 {
		t.Fatalf("own comment was streamed: %d", got)
	}

	// A status change.
	bus.Publish(events.Event{
		Type: protocol.EventIssueUpdated, WorkspaceID: testWorkspaceID, ActorType: "agent", ActorID: agentID,
		Payload: map[string]any{
			"issue":          handler.IssueResponse{ID: issueID, WorkspaceID: testWorkspaceID, Status: "blocked", UpdatedAt: "2026-10-12T00:00:00Z"},
			"status_changed": true, "prev_status": "in_progress",
		},
	})
	if got := activityEvents(t, fx, botID); got != 2 {
		t.Fatalf("status change: %d events", got)
	}
	var importance string
	fx.QueryRow(t, `SELECT activity->>'importance' FROM human_needed_event WHERE bot_id=$1 AND kind='status.changed'`, botID).Scan(&importance)
	if importance != "high" {
		t.Fatalf("blocked should be high, got %q", importance)
	}

	// A chat the person opened: the agent's reply reaches them, their own message does not.
	chatID := fx.ChatSession(t, agentID)
	replyID := fx.Insert(t, "chat_message", testutil.Cols{"chat_session_id": chatID, "role": "assistant", "content": "查完了"})
	bus.Publish(events.Event{
		Type: protocol.EventChatMessage, WorkspaceID: testWorkspaceID, ActorType: "agent", ActorID: agentID, ChatSessionID: chatID,
		Payload: protocol.ChatMessagePayload{ChatSessionID: chatID, MessageID: replyID, Role: "assistant", Content: "查完了"},
	})
	bus.Publish(events.Event{
		Type: protocol.EventChatMessage, WorkspaceID: testWorkspaceID, ActorType: "member", ActorID: testUserID, ChatSessionID: chatID,
		Payload: protocol.ChatMessagePayload{ChatSessionID: chatID, MessageID: "11111111-1111-4111-8111-111111111111", Role: "user", Content: "帮我查", SenderUserID: testUserID},
	})
	if got := activityEvents(t, fx, botID); got != 3 {
		t.Fatalf("chat: %d events (the agent's reply only)", got)
	}
}

func TestActivityStreamListenersLeaveUnboundPeopleAlone(t *testing.T) {
	queries := db.New(testPool)
	bus := events.New()
	registerActivityStreamListeners(bus, queries)
	fx, botID := activityBinding(t) // bound and verified, stream off
	agentID, _ := firstFixtureAgent(t)
	issueID := fx.Issue(t, "stream off", testutil.Cols{"visibility": "workspace"})
	commentID := fx.Comment(t, issueID, "hello", testutil.Cols{"author_type": "agent", "author_id": agentID})
	bus.Publish(events.Event{
		Type: protocol.EventCommentCreated, WorkspaceID: testWorkspaceID, ActorType: "agent", ActorID: agentID,
		Payload: map[string]any{"comment": handler.CommentResponse{ID: commentID, IssueID: issueID, AuthorType: "agent", AuthorID: agentID, Content: "hello"}},
	})
	if got := activityEvents(t, fx, botID); got != 0 {
		t.Fatalf("stream off still wrote %d events", got)
	}
}
