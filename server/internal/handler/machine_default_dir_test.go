package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/coderesolve"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestMachineDefaultDirPatchReportAndClaim(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	rt, owner, member := runtimeVisibilityFixture(t)
	key, err := testHandler.Queries.GetRuntimeMachineKey(ctx, parseUUID(rt))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, "DELETE FROM runtime_machine_default_dir WHERE machine_key=$1", key) })

	for _, bad := range []string{"relative/work", strings.Repeat("/x", maxDefaultDirLen)} {
		w := patchRuntimeCustomName(owner, rt, map[string]any{"default_work_dir": bad})
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"invalid_default_work_dir"`) {
			t.Fatalf("%q: %d %s", bad[:min(len(bad), 20)], w.Code, w.Body.String())
		}
	}
	// A workspace admin may rename the runtime but not point someone else's
	// machine at a folder.
	for _, actor := range []string{member, testUserID} {
		if w := patchRuntimeCustomName(actor, rt, map[string]any{"default_work_dir": "/Users/owner/work"}); w.Code != http.StatusForbidden {
			t.Fatalf("non-owner: %d %s", w.Code, w.Body.String())
		}
	}

	w := patchRuntimeCustomName(owner, rt, map[string]any{"default_work_dir": "  /Users/owner/work  "})
	if w.Code != http.StatusOK {
		t.Fatalf("owner PATCH: %d %s", w.Code, w.Body.String())
	}
	var resp AgentRuntimeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.DefaultWorkDir == nil || resp.DefaultWorkDir.Path != "/Users/owner/work" || resp.DefaultWorkDir.Exists != nil {
		t.Fatalf("default_work_dir = %+v, want the trimmed path, not yet looked at", resp.DefaultWorkDir)
	}

	// The machine reports. A report about another path changes nothing.
	report := func(path string, exists bool) string {
		req := newRequestAs(owner, http.MethodPost, "/api/daemon/runtimes/"+rt+"/default-dir", map[string]any{"path": path, "exists": exists})
		req = withURLParam(req, "runtimeId", rt)
		out := testutil.Call(t, testHandler.ReportRuntimeDefaultDir, req).Want(http.StatusOK)
		var body struct {
			Path string `json:"path"`
		}
		out.JSON(&body)
		return body.Path
	}
	if got := report("/Users/owner/old", true); got != "/Users/owner/work" {
		t.Fatalf("reply path = %q", got)
	}
	if dir, _ := testHandler.Queries.GetRuntimeMachineDefaultDir(ctx, parseUUID(rt)); dir.PathExists.Valid {
		t.Fatalf("a stale path's report marked the current one: %+v", dir)
	}
	report("/Users/owner/work", false)
	rows := []AgentRuntimeResponse{runtimeToResponse(mustRuntime(t, rt))}
	testHandler.hydrateRuntimeDefaultDirs(ctx, owner, rows)
	if rows[0].DefaultWorkDir == nil || rows[0].DefaultWorkDir.Exists == nil || *rows[0].DefaultWorkDir.Exists {
		t.Fatalf("after report: %+v, want exists=false", rows[0].DefaultWorkDir)
	}
	// Other members never see the owner's path.
	rows = []AgentRuntimeResponse{runtimeToResponse(mustRuntime(t, rt))}
	testHandler.hydrateRuntimeDefaultDirs(ctx, member, rows)
	if rows[0].DefaultWorkDir != nil {
		t.Fatalf("member saw the owner's default dir: %+v", rows[0].DefaultWorkDir)
	}
	// Re-saving the same path keeps the report; a new path resets it.
	patchRuntimeCustomName(owner, rt, map[string]any{"default_work_dir": "/Users/owner/work"})
	if dir, _ := testHandler.Queries.GetRuntimeMachineDefaultDir(ctx, parseUUID(rt)); !dir.PathExists.Valid {
		t.Fatal("re-saving the same path forgot the machine's report")
	}

	// The claim carries it only for a run with nothing to work in.
	scratch := coderesolve.NewSharedScratch(coderesolve.ScratchTarget{SessionID: "s"})
	remote := coderesolve.NewRemoteCache(coderesolve.RemoteTarget{})
	if got := testHandler.defaultWorkDirForClaim(ctx, AgentTaskResponse{CodeDecision: &scratch}, parseUUID(rt)); got != "/Users/owner/work" {
		t.Fatalf("scratch claim default dir = %q", got)
	}
	if got := testHandler.defaultWorkDirForClaim(ctx, AgentTaskResponse{CodeDecision: &remote}, parseUUID(rt)); got != "" {
		t.Fatalf("remote-cache claim got default dir %q", got)
	}
	if got := testHandler.defaultWorkDirForClaim(ctx, AgentTaskResponse{CodeDecision: &scratch, IsLeaderTask: true}, parseUUID(rt)); got != "" {
		t.Fatalf("leader claim got default dir %q", got)
	}

	// An unrelated PATCH keeps it; "" clears it.
	patchRuntimeCustomName(owner, rt, map[string]any{"custom_name": "Default dir machine"})
	if dir, err := testHandler.Queries.GetRuntimeMachineDefaultDir(ctx, parseUUID(rt)); err != nil || dir.Path != "/Users/owner/work" {
		t.Fatalf("unrelated PATCH: %+v %v", dir, err)
	}
	w = patchRuntimeCustomName(owner, rt, map[string]any{"default_work_dir": ""})
	var cleared AgentRuntimeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &cleared); err != nil || w.Code != http.StatusOK || cleared.DefaultWorkDir != nil {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
	if got := testHandler.defaultWorkDirForClaim(ctx, AgentTaskResponse{CodeDecision: &scratch}, parseUUID(rt)); got != "" {
		t.Fatalf("cleared default dir still claimed: %q", got)
	}
}

func mustRuntime(t *testing.T, id string) db.AgentRuntime {
	t.Helper()
	rt, err := testHandler.Queries.GetAgentRuntime(context.Background(), parseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	return rt
}
