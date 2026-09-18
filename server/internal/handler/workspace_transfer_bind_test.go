package handler

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// DENE-364: the transfer report used to *describe* the runtimes an imported
// agent could use and stop there, so a migrated agent arrived with no runtime
// and every button on it was dead. These tests pin the executable half: the
// one-candidate auto-bind, the two cases the rule must refuse to guess, and the
// explicit endpoint the Desktop card uses for the agents it left alone.

const xferBindProvider = "xfer-bind-provider"

func xferBindRuntime(t *testing.T, dst, name string, over testutil.Cols) string {
	t.Helper()
	cols := testutil.Cols{
		"workspace_id": dst,
		"provider":     xferBindProvider,
		"runtime_mode": "local",
		"visibility":   "public",
		"owner_id":     testUserID,
	}
	for k, v := range over {
		cols[k] = v
	}
	return dbfx.Runtime(t, name, cols)
}

// xferBindBody builds a one-agent transfer whose source runtime is stated as
// provider + runtime mode, which is what the target matches on.
func xferBindBody(agentSourceID, agentName string, options map[string]any, extraHints ...map[string]any) map[string]any {
	hints := append([]map[string]any{{
		"source_agent_id":   agentSourceID,
		"source_runtime_id": uuid.NewString(),
		"provider":          xferBindProvider,
		"runtime_mode":      "local",
	}}, extraHints...)
	body := map[string]any{
		"dry_run":     false,
		"on_conflict": "skip",
		"runtime_profiles": map[string]any{
			"profiles":      []any{},
			"runtimes_hint": []any{},
			"agent_hints":   hints,
		},
		"config": map[string]any{
			"format":         service.ConfigBundleFormat,
			"schema_version": 1,
			"bundle_id":      uuid.NewString(),
			"exported_at":    "2026-09-16T00:00:00Z",
			"source":         map[string]any{"workspace_id": uuid.NewString(), "slug": "x", "name": "x", "exported_by": testUserID},
			"entities": map[string]any{
				"agents": []map[string]any{{
					"source_id":            agentSourceID,
					"name":                 agentName,
					"runtime_mode":         "local",
					"visibility":           "workspace",
					"permission_mode":      "private",
					"max_concurrent_tasks": 1,
				}},
			},
		},
	}
	if options != nil {
		body["options"] = options
	}
	return body
}

func xferRunConfig(t *testing.T, dst string, body map[string]any) service.TransferConfigReport {
	t.Helper()
	resp := testutil.Call(t, testHandler.ImportWorkspaceTransferConfig,
		transferReq("POST", "/api/workspaces/"+dst+"/transfer/config", dst, body)).Want(http.StatusOK)
	var report service.TransferConfigReport
	resp.JSON(&report)
	if len(report.RuntimesToBind) != 1 {
		t.Fatalf("want 1 binding row, got %d (%+v)", len(report.RuntimesToBind), report.RuntimesToBind)
	}
	return report
}

func xferAgentRuntimeID(t *testing.T, dst, agentName string) string {
	t.Helper()
	var runtimeID *string
	dbfx.QueryRow(t, `SELECT runtime_id::text FROM agent WHERE workspace_id = $1 AND name = $2`, dst, agentName).Scan(&runtimeID)
	if runtimeID == nil {
		return ""
	}
	return *runtimeID
}

func xferCleanup(t *testing.T, dst string) {
	t.Helper()
	dbfx.Cleanup(t, `DELETE FROM agent WHERE workspace_id = $1`, dst)
	dbfx.Cleanup(t, `DELETE FROM agent_runtime WHERE workspace_id = $1`, dst)
}

// The core of the feature, and the negative control for this PR: with exactly
// one candidate the import must have written agent.runtime_id by the time it
// answers. Turning the unique-candidate rule back into a report-only row makes
// this test fail on the runtime_id assertion.
func TestTransferConfig_AutoBindsUniqueRuntimeCandidate(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	xferCleanup(t, dst)
	agentName := "XferBind-" + uuid.NewString()[:6]
	runtimeID := xferBindRuntime(t, dst, "BindTarget-"+uuid.NewString()[:6], nil)

	report := xferRunConfig(t, dst, xferBindBody(uuid.NewString(), agentName, nil))
	bind := report.RuntimesToBind[0]

	if bind.Status != service.RuntimeBindBound || bind.BoundRuntimeID != runtimeID {
		t.Fatalf("status/bound = %s/%s, want %s/%s", bind.Status, bind.BoundRuntimeID, service.RuntimeBindBound, runtimeID)
	}
	if got := xferAgentRuntimeID(t, dst, agentName); got != runtimeID {
		t.Fatalf("agent.runtime_id = %q, want %q — the import reported a binding it never wrote", got, runtimeID)
	}
}

// The switch is what makes the auto-bind a default rather than a policy: with
// it off the agent stays unbound and the card still gets the candidate to pick.
func TestTransferConfig_AutoBindSwitchOffLeavesAgentUnbound(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	xferCleanup(t, dst)
	agentName := "XferBindOff-" + uuid.NewString()[:6]
	runtimeID := xferBindRuntime(t, dst, "BindTargetOff-"+uuid.NewString()[:6], nil)

	report := xferRunConfig(t, dst, xferBindBody(uuid.NewString(), agentName, map[string]any{"auto_bind_runtimes": false}))
	bind := report.RuntimesToBind[0]

	if bind.Status != service.RuntimeBindPending {
		t.Fatalf("status = %s, want %s", bind.Status, service.RuntimeBindPending)
	}
	if len(bind.CandidateIDs) != 1 || bind.CandidateIDs[0] != runtimeID {
		t.Fatalf("candidate_ids = %v, want [%s]", bind.CandidateIDs, runtimeID)
	}
	if got := xferAgentRuntimeID(t, dst, agentName); got != "" {
		t.Fatalf("agent.runtime_id = %q, want empty with auto_bind_runtimes=false", got)
	}
}

func TestTransferConfig_LeavesAmbiguousAgentForManualPick(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	xferCleanup(t, dst)
	agentName := "XferAmbiguous-" + uuid.NewString()[:6]
	first := xferBindRuntime(t, dst, "BindA-"+uuid.NewString()[:6], nil)
	second := xferBindRuntime(t, dst, "BindB-"+uuid.NewString()[:6], nil)

	report := xferRunConfig(t, dst, xferBindBody(uuid.NewString(), agentName, nil))
	bind := report.RuntimesToBind[0]

	if bind.Status != service.RuntimeBindPending || bind.BoundRuntimeID != "" {
		t.Fatalf("status/bound = %s/%s, want %s with nothing bound", bind.Status, bind.BoundRuntimeID, service.RuntimeBindPending)
	}
	if len(bind.CandidateIDs) != 2 {
		t.Fatalf("candidate_ids = %v, want both runtimes", bind.CandidateIDs)
	}
	seen := map[string]bool{bind.CandidateIDs[0]: true, bind.CandidateIDs[1]: true}
	if !seen[first] || !seen[second] {
		t.Fatalf("candidate_ids = %v, want %s and %s", bind.CandidateIDs, first, second)
	}
	if got := xferAgentRuntimeID(t, dst, agentName); got != "" {
		t.Fatalf("agent.runtime_id = %q, want empty: the rule must not guess between candidates", got)
	}
}

func TestTransferConfig_RecordsReasonWhenNoRuntimeMatches(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	xferCleanup(t, dst)
	agentName := "XferNoRuntime-" + uuid.NewString()[:6]
	xferBindRuntime(t, dst, "BindOtherProvider-"+uuid.NewString()[:6], testutil.Cols{"provider": "some-other-provider"})

	report := xferRunConfig(t, dst, xferBindBody(uuid.NewString(), agentName, nil))
	bind := report.RuntimesToBind[0]

	if bind.Status != service.RuntimeBindNoCandidate || bind.ReasonCode != service.RuntimeBindReasonNoRuntime {
		t.Fatalf("status/reason = %s/%s, want %s/%s", bind.Status, bind.ReasonCode, service.RuntimeBindNoCandidate, service.RuntimeBindReasonNoRuntime)
	}
	if bind.Reason == "" {
		t.Fatal("a zero-candidate row must carry a readable reason")
	}
	if got := xferAgentRuntimeID(t, dst, agentName); got != "" {
		t.Fatalf("agent.runtime_id = %q, want empty", got)
	}
}

func TestBindWorkspaceTransferRuntimes_AppliesPick(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	xferCleanup(t, dst)
	agentName := "XferPick-" + uuid.NewString()[:6]
	agentID := dbfx.Agent(t, agentName, "", testutil.Cols{"workspace_id": dst, "owner_id": testUserID})
	runtimeID := xferBindRuntime(t, dst, "PickTarget-"+uuid.NewString()[:6], nil)

	resp := testutil.Call(t, testHandler.BindWorkspaceTransferRuntimes, transferReq(
		"POST", "/api/workspaces/"+dst+"/transfer/bind-runtimes", dst,
		map[string]any{"bindings": []map[string]any{{"agent_id": agentID, "runtime_id": runtimeID}}},
	)).Want(http.StatusOK)
	var report service.TransferBindRuntimesReport
	resp.JSON(&report)
	if report.Bound != 1 || report.Failed != 0 || len(report.Bindings) != 1 || !report.Bindings[0].Bound {
		t.Fatalf("report = %+v", report)
	}
	if got := xferAgentRuntimeID(t, dst, agentName); got != runtimeID {
		t.Fatalf("agent.runtime_id = %q, want %q", got, runtimeID)
	}
}

// A transfer must not be able to bind what the agent editor would refuse: a
// private runtime belongs to its owner's machine and account.
func TestBindWorkspaceTransferRuntimes_RejectsForeignPrivateRuntime(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	xferCleanup(t, dst)
	agentName := "XferPrivate-" + uuid.NewString()[:6]
	agentID := dbfx.Agent(t, agentName, "", testutil.Cols{"workspace_id": dst, "owner_id": testUserID})
	otherUser := dbfx.User(t, "XferOwner "+uuid.NewString()[:6], "xfer-owner-"+uuid.NewString()[:6]+"@example.com")
	dbfx.Member(t, dst, otherUser, "member")
	runtimeID := xferBindRuntime(t, dst, "ForeignPrivate-"+uuid.NewString()[:6], testutil.Cols{
		"visibility": "private", "owner_id": otherUser,
	})

	resp := testutil.Call(t, testHandler.BindWorkspaceTransferRuntimes, transferReq(
		"POST", "/api/workspaces/"+dst+"/transfer/bind-runtimes", dst,
		map[string]any{"bindings": []map[string]any{{"agent_id": agentID, "runtime_id": runtimeID}}},
	)).Want(http.StatusOK)
	var report service.TransferBindRuntimesReport
	resp.JSON(&report)
	if report.Failed != 1 || report.Bound != 0 {
		t.Fatalf("report = %+v", report)
	}
	if code := report.Bindings[0].ErrorCode; code != "runtime_private" {
		t.Fatalf("error_code = %q, want runtime_private", code)
	}
	if got := xferAgentRuntimeID(t, dst, agentName); got != "" {
		t.Fatalf("agent.runtime_id = %q, want empty", got)
	}
}
