package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// The autonomous events (a PR mirror changing, a run ending, an autopilot run
// ending) reach a Bot that asked for the whole workspace, and only that Bot.
func TestWorkspaceActivityListenersFeedTheWholeWorkspaceScope(t *testing.T) {
	ctx := context.Background()
	q := db.New(testPool)
	bot, err := q.CreateBotAccessToken(ctx, db.CreateBotAccessTokenParams{
		UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID),
		Name: "listener fixture", TokenHash: uuid.NewString(), TokenPrefix: "mul_lstn",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(ctx, `DELETE FROM personal_access_token WHERE id=$1`, bot.ID) })
	var binding string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO member_bot_binding (workspace_id, user_id, bot_id, verified_at, activity_scope) VALUES ($1,$2,$3,now(),'{}') RETURNING id`,
		testWorkspaceID, testUserID, bot.ID).Scan(&binding); err != nil {
		t.Fatal(err)
	}

	// A Bot only gets pushes once it has a webhook configured.
	if _, err := testPool.Exec(ctx, `UPDATE bot_wake SET webhook_secret=$2 WHERE bot_id=$1`, bot.ID, []byte("fixture-secret")); err != nil {
		t.Fatal(err)
	}

	bus := events.New()
	registerWorkspaceActivityListeners(bus, q)
	count := func() int {
		var n int
		_ = testPool.QueryRow(ctx, `SELECT count(*) FROM human_needed_event WHERE bot_id=$1 AND event_type='activity'`, bot.ID).Scan(&n)
		return n
	}
	suffix := fmt.Sprint(time.Now().UnixNano())
	publish := func() {
		bus.Publish(events.Event{Type: protocol.EventPullRequestUpdated, WorkspaceID: testWorkspaceID, ActorType: "system",
			Payload: map[string]any{"pull_request": map[string]any{"id": "pr-" + suffix, "number": 7, "state": "open", "title": "Add thing"}}})
		bus.Publish(events.Event{Type: protocol.EventTaskFailed, WorkspaceID: testWorkspaceID, ActorType: "system",
			Payload: map[string]any{"task_id": "t-" + suffix, "agent_id": "", "issue_id": "", "status": "failed", "failure_reason": "agent_error", "retry_pending": false}})
		bus.Publish(events.Event{Type: protocol.EventTaskFailed, WorkspaceID: testWorkspaceID, ActorType: "system",
			Payload: map[string]any{"task_id": "retry-" + suffix, "retry_pending": true}})
		bus.Publish(events.Event{Type: protocol.EventTaskCompleted, WorkspaceID: testWorkspaceID, ActorType: "system",
			Payload: map[string]any{"task_id": "chat-" + suffix, "chat_session_id": uuid.NewString()}})
		bus.Publish(events.Event{Type: protocol.EventAutopilotRunDone, WorkspaceID: testWorkspaceID, ActorType: "system",
			Payload: map[string]any{"run_id": "run-" + suffix, "autopilot_id": "", "status": "failed"}})
	}

	publish()
	if n := count(); n != 0 {
		t.Fatalf("a Bot that did not ask for the whole workspace got %d events", n)
	}
	if _, err := testPool.Exec(ctx, `UPDATE member_bot_binding SET activity_scope='{owner_visible_all}' WHERE id=$1`, binding); err != nil {
		t.Fatal(err)
	}
	publish()
	// PR updated + run failed + autopilot run done; the retry attempt and the
	// chat's own run are not the workspace's business.
	if n := count(); n != 3 {
		t.Fatalf("whole-workspace Bot got %d events, want 3", n)
	}
	publish() // the same things again: deduplicated, not repeated
	if n := count(); n != 3 {
		t.Fatalf("repeated events were queued again: %d", n)
	}
}

func TestWorkspaceActivityListenersAreWired(t *testing.T) {
	bus := events.New()
	registerWorkspaceActivityListeners(bus, db.New(testPool))
	for _, ev := range []string{
		protocol.EventPullRequestUpdated, protocol.EventPullRequestLinked, protocol.EventPullRequestUnlinked,
		protocol.EventTaskCompleted, protocol.EventTaskFailed, protocol.EventTaskCancelled, protocol.EventAutopilotRunDone,
	} {
		if bus.SubscriberCount(ev) == 0 {
			t.Errorf("nothing listens to %s", ev)
		}
	}
}

// The registration function is only worth anything if the server calls it.
func TestMainRegistersTheWorkspaceActivityListeners(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "registerWorkspaceActivityListeners(") {
		t.Fatal("main.go does not register the workspace activity listeners")
	}
}
