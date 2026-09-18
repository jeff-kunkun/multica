package service

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
)

// Identity mapping between a transfer bundle and the target workspace is pure:
// it reads the ref index and the resolved maps and writes nothing. These cases
// are the canonical matrix for that mapping; the DB-backed transfer tests cover
// the write path that consumes it.

func testImporterID(t *testing.T) pgtype.UUID {
	t.Helper()
	id, err := util.ParseUUID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("parse importer uuid: %v", err)
	}
	return id
}

func newIdentityState(t *testing.T, report *TransferIssuesReport) *transferIssueState {
	t.Helper()
	return &transferIssueState{
		env:      TransferImportEnv{ImporterID: testImporterID(t)},
		wsID:     "ws-src",
		report:   report,
		people:   map[string]pgtype.UUID{},
		agents:   map[string]pgtype.UUID{},
		squads:   map[string]pgtype.UUID{},
		projects: map[string]pgtype.UUID{},
	}
}

func strPtr(s string) *string { return &s }

func TestResolveAssignee_UnmappedFallsBackToImporter(t *testing.T) {
	report := &TransferIssuesReport{}
	s := newIdentityState(t, report)

	gotType, gotID := s.resolveAssignee(context.Background(), TransferIssueRow{
		SourceID:     "issue-1",
		AssigneeType: strPtr("agent"),
		AssigneeID:   strPtr("agent-gone"),
	})

	if gotType != "member" || gotID != s.env.ImporterID {
		t.Fatalf("assignee = (%q, %v), want the importer as a member", gotType, gotID)
	}
	if len(report.AssigneeUnmapped) != 1 {
		t.Fatalf("AssigneeUnmapped = %v, want exactly one reported degradation", report.AssigneeUnmapped)
	}
	ref := report.AssigneeUnmapped[0]
	// The degradation is still reported — only its resolution changed, so a
	// migration report keeps listing every row the owner may want to re-point.
	if ref.Resolution != "importer" {
		t.Fatalf("Resolution = %q, want %q", ref.Resolution, "importer")
	}
	if ref.Reason != "assignee_unmapped" || ref.RefType != "agent" || ref.RefID != "agent-gone" {
		t.Fatalf("degradation ref = %+v, want it to name the unmapped source agent", ref)
	}
}

func TestResolveAssignee_NoAssigneeStaysEmpty(t *testing.T) {
	report := &TransferIssuesReport{}
	s := newIdentityState(t, report)

	gotType, gotID := s.resolveAssignee(context.Background(), TransferIssueRow{SourceID: "issue-1"})

	// A source issue nobody owned must not gain an owner on import; the
	// importer fallback applies only to a reference that failed to map.
	if gotType != "" || gotID.Valid {
		t.Fatalf("assignee = (%q, %v), want it left unassigned", gotType, gotID)
	}
	if len(report.AssigneeUnmapped) != 0 {
		t.Fatalf("AssigneeUnmapped = %v, want nothing reported", report.AssigneeUnmapped)
	}
}

func TestResolveAssignee_MappedAgentKeepsIdentity(t *testing.T) {
	report := &TransferIssuesReport{}
	s := newIdentityState(t, report)
	target, err := util.ParseUUID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("parse target uuid: %v", err)
	}
	s.agents["agent-src"] = target

	gotType, gotID := s.resolveAssignee(context.Background(), TransferIssueRow{
		SourceID:     "issue-1",
		AssigneeType: strPtr("agent"),
		AssigneeID:   strPtr("agent-src"),
	})

	if gotType != "agent" || gotID != target {
		t.Fatalf("assignee = (%q, %v), want the mapped target agent", gotType, gotID)
	}
	if len(report.AssigneeUnmapped) != 0 {
		t.Fatalf("AssigneeUnmapped = %v, want nothing reported", report.AssigneeUnmapped)
	}
}

// A system agent is referenced by issue rows through its source uuid, so the
// ref index must be keyed that way — keying it by system_key made every
// assignee, creator, comment author and mention pointing at Mika degrade.
func TestSystemAgentRefKey(t *testing.T) {
	if got := systemAgentRefKey(ConfigSystemAgent{SourceID: "sys-uuid", SystemKey: "mika"}); got != "sys-uuid" {
		t.Fatalf("key = %q, want the source agent uuid", got)
	}
	// Bundles exported before SourceID existed keep today's behaviour rather
	// than losing the entry.
	if got := systemAgentRefKey(ConfigSystemAgent{SystemKey: "mika"}); got != "mika" {
		t.Fatalf("legacy key = %q, want the system_key fallback", got)
	}
}

func TestExportAgentsGroup_SystemAgentCarriesSourceID(t *testing.T) {
	src := &fakeTransferSource{payloads: map[string]any{
		"/api/agents": []map[string]any{
			{"id": "sys-1", "system_key": "mika", "instructions": "hi", "name": "Mika"},
		},
	}}
	bundle := &ConfigBundle{Entities: ConfigEntities{}, Stats: map[string]int{}}
	var gaps []TransferExportGap
	exportAgentsGroup(context.Background(), src, bundle, &gaps, func(string, error) {})

	if len(bundle.Entities.SystemAgents) != 1 {
		t.Fatalf("system_agents=%v", bundle.Entities.SystemAgents)
	}
	sa := bundle.Entities.SystemAgents[0]
	if sa.SourceID != "sys-1" {
		t.Fatalf("SourceID = %q, want the agent uuid an issue row would reference", sa.SourceID)
	}

	// The end of the contract that broke: the key the export writes must be the
	// key the import looks up, which is whatever an issue row carries.
	refs := map[string]TransferAgentRef{systemAgentRefKey(sa): {SystemKey: sa.SystemKey}}
	if ref, ok := refs["sys-1"]; !ok || ref.SystemKey != "mika" {
		t.Fatalf("refs = %v, want the source agent uuid to resolve to system_key mika", refs)
	}
}
