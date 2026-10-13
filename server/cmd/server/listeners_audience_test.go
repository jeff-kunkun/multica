package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/realtime"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// fakeAudience answers audience lookups from fixed maps keyed by id string.
type fakeAudience struct {
	issues   map[string][]pgtype.UUID
	projects map[string][]pgtype.UUID
}

func (f fakeAudience) ListIssueAudience(_ context.Context, id pgtype.UUID) ([]pgtype.UUID, error) {
	return f.issues[uuid.UUID(id.Bytes).String()], nil
}

func (f fakeAudience) ListProjectAudience(_ context.Context, arg db.ListProjectAudienceParams) ([]pgtype.UUID, error) {
	return f.projects[uuid.UUID(arg.ProjectID.Bytes).String()], nil
}

func audienceUUID(s string) pgtype.UUID {
	return pgtype.UUID{Bytes: uuid.MustParse(s), Valid: true}
}

func TestRegisterListeners_IssueEventsReachOnlyTheIssueAudience(t *testing.T) {
	const (
		ws      = "00000000-0000-0000-0000-0000000000aa"
		issueID = "00000000-0000-0000-0000-000000000001"
		owner   = "00000000-0000-0000-0000-0000000000f1"
		member  = "00000000-0000-0000-0000-0000000000f2"
	)
	bus := events.New()
	fb := &fakeBroadcaster{}
	registerListeners(bus, fb, fakeAudience{issues: map[string][]pgtype.UUID{
		issueID: {audienceUUID(owner), audienceUUID(member)},
	}})

	for _, payload := range []map[string]any{
		{"issue": map[string]any{"id": issueID, "title": "secret"}},
		{"comment": map[string]any{"issue_id": issueID}},
		{"issue_id": issueID},
	} {
		fb.scopeCalls, fb.workspaceCalls = nil, nil
		bus.Publish(events.Event{Type: protocol.EventIssueUpdated, WorkspaceID: ws, Payload: payload})
		if len(fb.workspaceCalls) != 0 {
			t.Fatalf("issue event leaked to workspace fanout: %+v", payload)
		}
		got := map[string]bool{}
		for _, c := range fb.scopeCalls {
			if c.scopeType != realtime.ScopeMember {
				t.Fatalf("scope = %q, want member", c.scopeType)
			}
			got[c.scopeID] = true
		}
		if len(got) != 2 || !got[realtime.MemberScopeID(ws, owner)] || !got[realtime.MemberScopeID(ws, member)] {
			t.Fatalf("member scopes = %v for payload %+v", got, payload)
		}
	}
}

func TestRegisterListeners_AudienceFallbacks(t *testing.T) {
	const (
		ws        = "00000000-0000-0000-0000-0000000000aa"
		goneIssue = "00000000-0000-0000-0000-000000000009"
		projectID = "00000000-0000-0000-0000-000000000002"
	)
	bus := events.New()
	fb := &fakeBroadcaster{}
	registerListeners(bus, fb, fakeAudience{projects: map[string][]pgtype.UUID{
		projectID: {audienceUUID("00000000-0000-0000-0000-0000000000f1")},
	}})

	cases := []struct {
		name      string
		event     events.Event
		workspace bool
	}{
		{"deleted issue has nothing left to hide", events.Event{Type: protocol.EventIssueDeleted, WorkspaceID: ws, Payload: map[string]any{"issue_id": goneIssue}}, true},
		{"unrelated event", events.Event{Type: protocol.EventLabelCreated, WorkspaceID: ws, Payload: map[string]any{"label": map[string]any{"id": "x"}}}, true},
		{"project delete reaches members who lost it", events.Event{Type: protocol.EventProjectDeleted, WorkspaceID: ws, Payload: map[string]any{"project_id": projectID}}, true},
		{"sharing change reaches everyone", events.Event{Type: protocol.EventProjectMembersChanged, WorkspaceID: ws, Payload: map[string]any{"project_id": projectID}}, true},
		{"project update goes to its audience", events.Event{Type: protocol.EventProjectUpdated, WorkspaceID: ws, Payload: map[string]any{"project": map[string]any{"id": projectID}}}, false},
	}
	for _, tc := range cases {
		fb.scopeCalls, fb.workspaceCalls = nil, nil
		bus.Publish(tc.event)
		if gotWorkspace := len(fb.workspaceCalls) == 1; gotWorkspace != tc.workspace {
			t.Fatalf("%s: workspace fanout = %v, want %v (scope calls %d)", tc.name, gotWorkspace, tc.workspace, len(fb.scopeCalls))
		}
		if !tc.workspace && len(fb.scopeCalls) != 1 {
			t.Fatalf("%s: scope calls = %d, want 1", tc.name, len(fb.scopeCalls))
		}
	}
}
