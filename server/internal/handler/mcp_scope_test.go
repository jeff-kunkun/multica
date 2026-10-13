package handler

import (
	"errors"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestMCPFastScopesCannotWidenGrant(t *testing.T) {
	if testPool == nil {
		t.Skip("test database not available")
	}
	other := dbfx.Workspace(t, "Other MCP workspace", "mcp-other-"+randomID(), testutil.Cols{"issue_prefix": "MCT"})
	dbfx.Member(t, other, testUserID, "owner")
	req := withChatTestWorkspaceCtx(t, newRequest("GET", "/", nil))
	req.Header.Set("X-Actor-Source", "mcp_connection")
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	scopes, err := testHandler.fastScopes(req, testUserID, fastRequest{Scope: &fastScope{Mode: "all_visible"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 1 || scopes[0].ID != testWorkspaceID {
		t.Fatalf("grant widened: %+v", scopes)
	}
	_, err = testHandler.fastScopes(req, testUserID, fastRequest{Scope: &fastScope{Mode: "workspace_ids", WorkspaceIDs: []string{other}}})
	if !errors.Is(err, errFastScope) {
		t.Fatalf("foreign workspace accepted: %v", err)
	}
}
