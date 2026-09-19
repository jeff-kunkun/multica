package main

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// TestRoutingAssignmentLandsInTheTimeline drives the payload routing actually
// publishes through the activity listener that actually runs.
//
// The point of crossing that boundary: routing's own package tests all passed
// while its writes produced no timeline row at all, because the event it
// published carried neither `assignee_changed` nor the previous pair (DENE-633
// review, F3). The spec names `multica issue timeline` as the way to verify
// that routing neither loops nor double-assigns, so an owner the system
// changed has to leave a record — and only a test that builds the payload with
// the production function can prove it does.
func TestRoutingAssignmentLandsInTheTimeline(t *testing.T) {
	queries := db.New(testPool)
	bus := events.New()
	registerActivityListeners(bus, queries)

	issueID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() {
		cleanupActivities(t, issueID)
		cleanupTestIssue(t, issueID)
	})

	base := db.Issue{
		ID:          util.MustParseUUID(issueID),
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
		Title:       "routing timeline test",
		Status:      "todo",
		Priority:    "medium",
		CreatorType: "member",
		CreatorID:   util.MustParseUUID(testUserID),
	}
	seatID := util.MustParseUUID(testUserID) // any valid uuid; the row only records it

	t.Run("filling an empty executor slot", func(t *testing.T) {
		t.Cleanup(func() { cleanupActivities(t, issueID) })
		next := base
		next.AssigneeType = pgtype.Text{String: "agent", Valid: true}
		next.AssigneeID = seatID

		bus.Publish(events.Event{
			Type:        protocol.EventIssueUpdated,
			WorkspaceID: testWorkspaceID,
			ActorType:   "system",
			Payload:     handler.RoutingIssueUpdatedPayload(base, next),
		})

		activities := listActivitiesForIssue(t, queries, issueID)
		if len(activities) != 1 || activities[0].Action != "assignee_changed" {
			t.Fatalf("routing's assignment left %d activity rows (%v); the timeline shows no owner change",
				len(activities), actions(activities))
		}
		var details map[string]string
		if err := json.Unmarshal(activities[0].Details, &details); err != nil {
			t.Fatalf("details: %v", err)
		}
		if details["to_type"] != "agent" {
			t.Fatalf("to_type = %q, want agent", details["to_type"])
		}
	})

	t.Run("handing off to a reviewer records where it came from", func(t *testing.T) {
		t.Cleanup(func() { cleanupActivities(t, issueID) })
		prev := base
		prev.AssigneeType = pgtype.Text{String: "agent", Valid: true}
		prev.AssigneeID = seatID
		next := prev
		next.AssigneeType = pgtype.Text{String: "member", Valid: true}

		bus.Publish(events.Event{
			Type:        protocol.EventIssueUpdated,
			WorkspaceID: testWorkspaceID,
			ActorType:   "system",
			Payload:     handler.RoutingIssueUpdatedPayload(prev, next),
		})

		activities := listActivitiesForIssue(t, queries, issueID)
		if len(activities) != 1 {
			t.Fatalf("expected 1 activity, got %d (%v)", len(activities), actions(activities))
		}
		var details map[string]string
		if err := json.Unmarshal(activities[0].Details, &details); err != nil {
			t.Fatalf("details: %v", err)
		}
		// The "from" half is what proves there was no loop: without it the
		// timeline cannot tell a handoff from a first assignment.
		if details["from_type"] != "agent" {
			t.Fatalf("from_type = %q, want agent — the previous owner was dropped from the record",
				details["from_type"])
		}
		if details["to_type"] != "member" {
			t.Fatalf("to_type = %q, want member", details["to_type"])
		}
	})

	t.Run("a reviewer-property write is not an owner change", func(t *testing.T) {
		t.Cleanup(func() { cleanupActivities(t, issueID) })
		bus.Publish(events.Event{
			Type:        protocol.EventIssueUpdated,
			WorkspaceID: testWorkspaceID,
			ActorType:   "system",
			Payload:     handler.RoutingIssueUpdatedPayload(base, base),
		})
		if activities := listActivitiesForIssue(t, queries, issueID); len(activities) != 0 {
			t.Fatalf("a write that changed no assignee produced %d activity rows (%v)",
				len(activities), actions(activities))
		}
	})
}

func actions(rows []db.ActivityLog) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Action)
	}
	return out
}
