package workspacelink

import (
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestBriefHints(t *testing.T) {
	ws := util.MustParseUUID("00000000-0000-0000-0000-000000000001")
	viewer := util.MustParseUUID("00000000-0000-0000-0000-000000000003")
	peer := util.MustParseUUID("00000000-0000-0000-0000-000000000002")
	rows := []db.ListWorkspaceLinksForWorkspaceRow{
		{SourceWorkspaceID: ws, TargetWorkspaceID: viewer, TargetName: "Viewer", Status: "active"},
		{SourceWorkspaceID: peer, TargetWorkspaceID: ws, SourceName: "Source", Status: "active"},
		{SourceWorkspaceID: peer, TargetWorkspaceID: ws, SourceName: "Pending", Status: "pending"},
		{SourceWorkspaceID: peer, TargetWorkspaceID: ws, SourceName: "Revoked", Status: "revoked"},
		{SourceWorkspaceID: peer, TargetWorkspaceID: peer, SourceName: "Unrelated", Status: "active"},
	}
	got, err := json.Marshal(briefHints(rows, ws))
	if err != nil {
		t.Fatal(err)
	}
	// Claim discovery must contain only workspace names and local directions.
	want := `[{"name":"Source","side":"viewer"},{"name":"Viewer","side":"source"}]`
	if string(got) != want {
		t.Fatalf("got %s want %s", got, want)
	}
	rows = append(rows, db.ListWorkspaceLinksForWorkspaceRow{SourceWorkspaceID: ws, TargetWorkspaceID: peer, TargetName: "Source", Status: "active"})
	hints := briefHints(rows, ws)
	if len(hints) != 2 || hints[0].Side != "both" {
		t.Fatalf("bidirectional peer counted twice: %+v", hints)
	}
	if len(briefHints(nil, ws)) != 0 {
		t.Fatal("empty workspace has hints")
	}
}
