package main

import (
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"testing"
)

func TestBotActivityProvenance(t *testing.T) {
	q := db.New(testPool)
	bus := events.New()
	registerActivityListeners(bus, q)
	issueID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() { cleanupActivities(t, issueID); cleanupTestIssue(t, issueID) })
	botID := "10000000-0000-4000-8000-000000000094"
	var frames []events.Event
	bus.Subscribe(protocol.EventActivityCreated, func(e events.Event) { frames = append(frames, e) })
	bus.Publish(events.Event{Type: protocol.EventIssueUpdated, WorkspaceID: testWorkspaceID, ActorType: "member", ActorID: testUserID, ViaBotID: botID, ViaBotName: "Grok", Payload: map[string]any{
		"issue":          handler.IssueResponse{ID: issueID, WorkspaceID: testWorkspaceID, Title: "Bot title", Status: "in_progress", AssigneeType: botSourceString("member"), AssigneeID: botSourceString(testUserID)},
		"status_changed": true, "prev_status": "todo", "title_changed": true, "prev_title": "old", "assignee_changed": true,
	}})
	rows := listActivitiesForIssue(t, q, issueID)
	if len(rows) != 3 {
		t.Fatalf("want title/status/assignment activities, got %d", len(rows))
	}
	for _, r := range rows {
		if util.UUIDToString(r.ViaBotID) != botID || r.ViaBotName.String != "Grok" || util.UUIDToString(r.ActorID) != testUserID {
			t.Fatalf("lost actor/source: %+v", r)
		}
	}
	if len(frames) != 3 {
		t.Fatalf("want 3 realtime frames, got %d", len(frames))
	}
	for _, f := range frames {
		if f.ViaBotID != botID {
			t.Fatal("lost event source")
		}
		entry := f.Payload.(map[string]any)["entry"].(map[string]any)
		if entry["via_bot_name"] != "Grok" {
			t.Fatal("lost realtime source")
		}
	}
}

func botSourceString(v string) *string { return &v }
