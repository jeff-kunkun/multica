package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestAddSquadMemberPreviewKeepsFullRoster(t *testing.T) {
	ids := []pgtype.UUID{
		parseUUID("00000000-0000-0000-0000-000000000001"),
		parseUUID("00000000-0000-0000-0000-000000000002"),
		parseUUID("00000000-0000-0000-0000-000000000003"),
		parseUUID("00000000-0000-0000-0000-000000000004"),
	}
	summary := &squadMemberSummary{}
	for i, id := range ids {
		role := "member"
		if i == 0 {
			role = "leader"
		}
		addSquadMemberPreview(summary, "agent", id, role)
	}
	if summary.count != 4 {
		t.Fatalf("count = %d, want 4", summary.count)
	}
	if len(summary.preview) != 3 {
		t.Fatalf("preview len = %d, want 3 (hover cap)", len(summary.preview))
	}
	if len(summary.members) != 4 {
		t.Fatalf("members len = %d, want 4 (full roster)", len(summary.members))
	}

	resp := SquadResponse{
		MemberPreview: []SquadMemberPreviewResponse{},
		Members:       []SquadMemberPreviewResponse{},
	}
	applySquadMemberSummary(&resp, summary)
	if resp.MemberCount != 4 {
		t.Fatalf("MemberCount = %d, want 4", resp.MemberCount)
	}
	if len(resp.MemberPreview) != 3 {
		t.Fatalf("MemberPreview len = %d, want 3", len(resp.MemberPreview))
	}
	if len(resp.Members) != 4 {
		t.Fatalf("Members len = %d, want 4", len(resp.Members))
	}
	if resp.Members[0].Role != "leader" || resp.Members[3].MemberID != uuidToString(ids[3]) {
		t.Fatalf("Members = %+v, want leader first and the 4th agent retained", resp.Members)
	}
}

func TestApplySquadMemberSummaryEmptyMembersJSONIsArray(t *testing.T) {
	resp := SquadResponse{
		MemberPreview: []SquadMemberPreviewResponse{},
		Members:       []SquadMemberPreviewResponse{},
	}
	applySquadMemberSummary(&resp, &squadMemberSummary{})
	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	members, ok := raw["members"].([]any)
	if !ok {
		t.Fatalf("members = %#v (%T), want []", raw["members"], raw["members"])
	}
	if len(members) != 0 {
		t.Fatalf("members = %#v, want empty array", members)
	}
}

func TestListSquadsReturnsFullMembers(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	leaderID := createHandlerTestAgent(t, "list-squads-full-leader", nil)
	w1 := createHandlerTestAgent(t, "list-squads-full-w1", nil)
	w2 := createHandlerTestAgent(t, "list-squads-full-w2", nil)
	w3 := createHandlerTestAgent(t, "list-squads-full-w3", nil)

	fullID := dbfx.Squad(t, "List Full Members Squad", leaderID)
	dbfx.SquadMember(t, fullID, "agent", leaderID, testutil.Cols{"role": "leader"})
	dbfx.SquadMember(t, fullID, "agent", w1, testutil.Cols{"role": "member"})
	dbfx.SquadMember(t, fullID, "agent", w2, testutil.Cols{"role": "member"})
	dbfx.SquadMember(t, fullID, "agent", w3, testutil.Cols{"role": "member"})

	emptyID := dbfx.Squad(t, "List Empty Members Squad", leaderID)

	var raw []map[string]any
	testutil.Call(t, testHandler.ListSquads, squadScopeReq("", http.MethodGet, "/api/squads", nil, nil)).
		Want(http.StatusOK).JSON(&raw)

	byID := make(map[string]map[string]any, len(raw))
	for _, row := range raw {
		id, _ := row["id"].(string)
		byID[id] = row
	}

	full, ok := byID[fullID]
	if !ok {
		t.Fatalf("ListSquads missing squad %s", fullID)
	}
	members, ok := full["members"].([]any)
	if !ok {
		t.Fatalf("full squad members = %#v (%T), want array", full["members"], full["members"])
	}
	if len(members) != 4 {
		t.Fatalf("full squad members len = %d, want 4: %#v", len(members), members)
	}
	preview, ok := full["member_preview"].([]any)
	if !ok {
		t.Fatalf("full squad member_preview = %#v (%T), want array", full["member_preview"], full["member_preview"])
	}
	if len(preview) != 3 {
		t.Fatalf("full squad member_preview len = %d, want 3", len(preview))
	}
	gotIDs := make(map[string]bool, 4)
	for _, item := range members {
		member, _ := item.(map[string]any)
		gotIDs[member["member_id"].(string)] = true
	}
	for _, want := range []string{leaderID, w1, w2, w3} {
		if !gotIDs[want] {
			t.Fatalf("full squad members missing %s: %#v", want, members)
		}
	}

	empty, ok := byID[emptyID]
	if !ok {
		t.Fatalf("ListSquads missing squad %s", emptyID)
	}
	emptyMembers, ok := empty["members"].([]any)
	if !ok {
		t.Fatalf("empty squad members = %#v (%T), want [] not null", empty["members"], empty["members"])
	}
	if len(emptyMembers) != 0 {
		t.Fatalf("empty squad members = %#v, want empty array", emptyMembers)
	}
}
