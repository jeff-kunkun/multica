package handler

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// The three tiers of the transfer bind rule (DENE-364): a unique candidate
// binds without a human pick, several wait for the Desktop card, none is
// reported with a readable reason instead of being dropped in silence.

func transferRuntimeHint(provider, mode string) map[string]any {
	return map[string]any{
		"source_runtime_id": uuid.NewString(),
		"provider":          provider,
		"runtime_mode":      mode,
		"display_name":      provider + " (mac)",
	}
}

// linkedTransferBody joins the agent to its hint the way a current CLI does.
func linkedTransferBody(agentName string, hint map[string]any) map[string]any {
	return transferAgentConfigBody(agentName, hint["source_runtime_id"].(string), hint, nil)
}

// transferAgentConfigBody is a one-agent V2 import. runtimeSourceID is the
// agent's join to hint — empty models a bundle exported before that field
// existed. profiles is the runtime_profiles section, empty for built-in
// runtimes.
func transferAgentConfigBody(agentName, runtimeSourceID string, hint map[string]any, profiles []any) map[string]any {
	agent := map[string]any{
		"source_id":       uuid.NewString(),
		"name":            agentName,
		"runtime_mode":    "local",
		"visibility":      "workspace",
		"permission_mode": "private",
	}
	if runtimeSourceID != "" {
		agent["runtime_source_id"] = runtimeSourceID
	}
	if profiles == nil {
		profiles = []any{}
	}
	return map[string]any{
		"dry_run":     false,
		"on_conflict": "skip",
		"manifest": map[string]any{
			"format": service.TransferBundleFormat, "schema_version": 1,
			"source": map[string]any{"exported_by": testUserID},
		},
		"runtime_profiles": map[string]any{
			"profiles":      profiles,
			"runtimes_hint": []any{hint},
		},
		"config": map[string]any{
			"format": service.ConfigBundleFormat, "schema_version": 1,
			"bundle_id": uuid.NewString(), "exported_at": "2026-09-16T00:00:00Z",
			"source": map[string]any{
				"workspace_id": uuid.NewString(), "slug": "src", "name": "src",
				"exported_by": testUserID,
			},
			"entities": map[string]any{"agents": []any{agent}},
		},
	}
}

func importTransferConfig(t *testing.T, dst string, body map[string]any) service.TransferConfigReport {
	t.Helper()
	resp := testutil.Call(t, testHandler.ImportWorkspaceTransferConfig,
		transferReq("POST", "/api/workspaces/"+dst+"/transfer/config", dst, body)).Want(http.StatusOK)
	var report service.TransferConfigReport
	resp.JSON(&report)
	return report
}

// boundRuntimeOf reads the agent's runtime as the empty string when unbound.
func boundRuntimeOf(t *testing.T, dst, agentName string) string {
	t.Helper()
	var got string
	dbfx.QueryRow(t,
		`SELECT COALESCE(runtime_id::text, '') FROM agent WHERE workspace_id = $1 AND name = $2`,
		dst, agentName,
	).Scan(&got)
	return got
}

func singleBind(t *testing.T, report service.TransferConfigReport) service.TransferRuntimeBind {
	t.Helper()
	if len(report.RuntimesToBind) != 1 {
		t.Fatalf("runtimes_to_bind=%d want 1 (%+v)", len(report.RuntimesToBind), report.RuntimesToBind)
	}
	return report.RuntimesToBind[0]
}

func TestTransferRuntimeBind_UniqueCandidateAutoBinds(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	name := "XferAuto-" + uuid.NewString()[:6]
	runtimeID := dbfx.Runtime(t, "Claude (mac)", testutil.Cols{
		"workspace_id": dst, "provider": "claude", "runtime_mode": "local",
	})
	report := importTransferConfig(t, dst, linkedTransferBody(name, transferRuntimeHint("claude", "local")))

	bind := singleBind(t, report)
	if bind.Action != service.TransferBindBound {
		t.Fatalf("action=%q reason=%q want %q", bind.Action, bind.Reason, service.TransferBindBound)
	}
	if !bind.AutoBind || bind.BoundRuntimeID != runtimeID {
		t.Fatalf("auto_bind=%v bound_runtime_id=%q want %q", bind.AutoBind, bind.BoundRuntimeID, runtimeID)
	}
	if got := boundRuntimeOf(t, dst, name); got != runtimeID {
		t.Fatalf("agent.runtime_id=%q want %q", got, runtimeID)
	}
}

func TestTransferRuntimeBind_MultipleCandidatesWaitForTheCard(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	name := "XferMulti-" + uuid.NewString()[:6]
	first := dbfx.Runtime(t, "Claude (mac)", testutil.Cols{
		"workspace_id": dst, "provider": "claude", "runtime_mode": "local",
	})
	second := dbfx.Runtime(t, "Claude (air)", testutil.Cols{
		"workspace_id": dst, "provider": "claude", "runtime_mode": "local",
	})
	report := importTransferConfig(t, dst, linkedTransferBody(name, transferRuntimeHint("claude", "local")))

	bind := singleBind(t, report)
	if bind.Action != service.TransferBindCandidates {
		t.Fatalf("action=%q want %q", bind.Action, service.TransferBindCandidates)
	}
	if bind.AutoBind || bind.BoundRuntimeID != "" {
		t.Fatalf("multi-candidate bind guessed: %+v", bind)
	}
	if len(bind.Candidates) != 2 || bind.Candidates[0].RuntimeID != first || bind.Candidates[1].RuntimeID != second {
		t.Fatalf("candidates=%+v want [%s %s]", bind.Candidates, first, second)
	}
	if len(bind.CandidateIDs) != 2 || bind.CandidateIDs[0] != first {
		t.Fatalf("candidate_ids=%v", bind.CandidateIDs)
	}
	if got := boundRuntimeOf(t, dst, name); got != "" {
		t.Fatalf("agent.runtime_id=%q, multi-candidate import must not write", got)
	}
}

func TestTransferRuntimeBind_NoCandidateReportsWhy(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	name := "XferNone-" + uuid.NewString()[:6]
	// A codex runtime exists, but the source agent ran claude: the provider is
	// what decides, so this must not become a candidate.
	dbfx.Runtime(t, "Codex (mac)", testutil.Cols{
		"workspace_id": dst, "provider": "codex", "runtime_mode": "local",
	})
	report := importTransferConfig(t, dst, linkedTransferBody(name, transferRuntimeHint("claude", "local")))

	bind := singleBind(t, report)
	if bind.Action != service.TransferBindNoCandidate {
		t.Fatalf("action=%q want %q", bind.Action, service.TransferBindNoCandidate)
	}
	if bind.ReasonCode != service.TransferBindReasonNoRuntime {
		t.Fatalf("reason_code=%q want %q", bind.ReasonCode, service.TransferBindReasonNoRuntime)
	}
	if bind.Reason == "" || bind.Provider != "claude" {
		t.Fatalf("zero candidate row must name the missing runtime: %+v", bind)
	}
	if got := boundRuntimeOf(t, dst, name); got != "" {
		t.Fatalf("agent.runtime_id=%q, zero candidates must not write", got)
	}
}

func TestTransferRuntimeBind_DryRunWritesNothing(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	name := "XferDry-" + uuid.NewString()[:6]
	runtimeID := dbfx.Runtime(t, "Claude (mac)", testutil.Cols{
		"workspace_id": dst, "provider": "claude", "runtime_mode": "local",
	})
	body := linkedTransferBody(name, transferRuntimeHint("claude", "local"))
	body["dry_run"] = true
	report := importTransferConfig(t, dst, body)

	bind := singleBind(t, report)
	if bind.Action != service.TransferBindBound || bind.BoundRuntimeID != runtimeID {
		t.Fatalf("preview outcome=%+v", bind)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent WHERE workspace_id = $1 AND name = $2`, dst, name); n != 0 {
		t.Fatalf("agents=%d, dry run created rows", n)
	}
}

func TestTransferRuntimeBind_AutoBindCanBeTurnedOff(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	name := "XferOff-" + uuid.NewString()[:6]
	dbfx.Runtime(t, "Claude (mac)", testutil.Cols{
		"workspace_id": dst, "provider": "claude", "runtime_mode": "local",
	})
	body := linkedTransferBody(name, transferRuntimeHint("claude", "local"))
	body["auto_bind_runtimes"] = false
	report := importTransferConfig(t, dst, body)

	bind := singleBind(t, report)
	if bind.Action != service.TransferBindCandidates || bind.AutoBind {
		t.Fatalf("auto_bind_runtimes=false still bound: %+v", bind)
	}
	if got := boundRuntimeOf(t, dst, name); got != "" {
		t.Fatalf("agent.runtime_id=%q, auto-bind was turned off", got)
	}
}

func TestTransferRuntimeBind_KeepsAnExistingBinding(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	name := "XferKeep-" + uuid.NewString()[:6]
	chosen := dbfx.Runtime(t, "Claude (air)", testutil.Cols{
		"workspace_id": dst, "provider": "claude", "runtime_mode": "local",
	})
	dbfx.Runtime(t, "Claude (mac)", testutil.Cols{
		"workspace_id": dst, "provider": "claude", "runtime_mode": "local",
	})
	dbfx.Agent(t, name, chosen, testutil.Cols{"workspace_id": dst, "visibility": "workspace"})

	report := importTransferConfig(t, dst, linkedTransferBody(name, transferRuntimeHint("claude", "local")))

	bind := singleBind(t, report)
	if bind.Action != service.TransferBindAlreadyBound || bind.BoundRuntimeID != chosen {
		t.Fatalf("existing binding was moved: %+v", bind)
	}
	if got := boundRuntimeOf(t, dst, name); got != chosen {
		t.Fatalf("agent.runtime_id=%q want the human's pick %q", got, chosen)
	}
}

// The preview has to say what apply would do, including "already bound": a
// dry run resolves a new agent to a synthetic id, but an existing one still
// loads, so this row must not claim it will bind.
func TestTransferRuntimeBind_DryRunReportsAnExistingBinding(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	name := "XferDryKeep-" + uuid.NewString()[:6]
	chosen := dbfx.Runtime(t, "Claude (air)", testutil.Cols{
		"workspace_id": dst, "provider": "claude", "runtime_mode": "local",
	})
	dbfx.Agent(t, name, chosen, testutil.Cols{"workspace_id": dst, "visibility": "workspace"})
	body := linkedTransferBody(name, transferRuntimeHint("claude", "local"))
	body["dry_run"] = true

	bind := singleBind(t, importTransferConfig(t, dst, body))
	if bind.Action != service.TransferBindAlreadyBound || bind.BoundRuntimeID != chosen {
		t.Fatalf("dry-run outcome=%+v", bind)
	}
}

// A bundle exported before runtime_source_id existed has only the runtime's
// display name to join on; that fallback must still find the candidate.
func TestTransferRuntimeBind_LegacyBundleFallsBackToRuntimeName(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	name := "XferLegacy-" + uuid.NewString()[:6]
	runtimeID := dbfx.Runtime(t, name, testutil.Cols{
		"workspace_id": dst, "provider": "claude", "runtime_mode": "local",
	})
	hint := transferRuntimeHint("claude", "local")
	hint["display_name"] = name
	report := importTransferConfig(t, dst, transferAgentConfigBody(name, "", hint, nil))

	bind := singleBind(t, report)
	if bind.Action != service.TransferBindBound || bind.BoundRuntimeID != runtimeID {
		t.Fatalf("legacy name join did not bind: %+v", bind)
	}
}

// A bundle with no join at all must say so instead of reporting a bare row.
func TestTransferRuntimeBind_UnlinkedAgentReportsWhy(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	name := "XferNoJoin-" + uuid.NewString()[:6]
	dbfx.Runtime(t, "Claude (mac)", testutil.Cols{
		"workspace_id": dst, "provider": "claude", "runtime_mode": "local",
	})
	hint := transferRuntimeHint("claude", "local")
	hint["display_name"] = "somewhere-else"
	report := importTransferConfig(t, dst, transferAgentConfigBody(name, "", hint, nil))

	bind := singleBind(t, report)
	if bind.Action != service.TransferBindNoCandidate || bind.ReasonCode != service.TransferBindReasonSourceUnknown {
		t.Fatalf("unlinked agent outcome=%+v", bind)
	}
}

// The custom profile is part of the identity: the same provider on the same
// machine but a different command must not be offered as a candidate.
func TestTransferRuntimeBind_ProfileNameDecides(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	builtInAgent := "XferBuiltin-" + uuid.NewString()[:6]
	gatewayAgent := "XferGateway-" + uuid.NewString()[:6]
	builtIn := dbfx.Runtime(t, "Claude (mac)", testutil.Cols{
		"workspace_id": dst, "provider": "claude", "runtime_mode": "local",
	})
	profileID := dbfx.Insert(t, "runtime_profile", testutil.Cols{
		"workspace_id": dst, "display_name": "corp-gateway", "protocol_family": "claude",
		"command_name": "claude", "fixed_args": testutil.Raw("'[]'::jsonb"),
		"visibility": "workspace", "created_by": testUserID, "enabled": true,
	})
	gateway := dbfx.Runtime(t, "Claude gateway", testutil.Cols{
		"workspace_id": dst, "provider": "claude", "runtime_mode": "local", "profile_id": profileID,
	})

	builtInReport := importTransferConfig(t, dst, linkedTransferBody(builtInAgent, transferRuntimeHint("claude", "local")))
	if bind := singleBind(t, builtInReport); bind.BoundRuntimeID != builtIn {
		t.Fatalf("built-in hint bound %q want %q", bind.BoundRuntimeID, builtIn)
	}

	profileSourceID := uuid.NewString()
	hint := transferRuntimeHint("claude", "local")
	hint["profile_source_id"] = profileSourceID
	gatewayReport := importTransferConfig(t, dst, transferAgentConfigBody(gatewayAgent, hint["source_runtime_id"].(string), hint, []any{map[string]any{
		"source_id": profileSourceID, "display_name": "corp-gateway",
		"protocol_family": "claude", "command_name": "claude",
		"fixed_args": []string{}, "visibility": "workspace", "enabled": true,
	}}))
	bind := singleBind(t, gatewayReport)
	if bind.BoundRuntimeID != gateway {
		t.Fatalf("profile hint bound %q want %q (candidates=%+v)", bind.BoundRuntimeID, gateway, bind.Candidates)
	}
	if bind.ProfileName != "corp-gateway" {
		t.Fatalf("profile_name=%q", bind.ProfileName)
	}
}

func TestTransferRuntimeBind_IgnoresAnotherMembersPrivateRuntime(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	name := "XferPrivate-" + uuid.NewString()[:6]
	other := dbfx.User(t, "Other "+uuid.NewString()[:6], "other-"+uuid.NewString()[:6]+"@example.com")
	dbfx.Runtime(t, "Claude (someone else)", testutil.Cols{
		"workspace_id": dst, "provider": "claude", "runtime_mode": "local",
		"owner_id": other, "visibility": "private",
	})
	report := importTransferConfig(t, dst, linkedTransferBody(name, transferRuntimeHint("claude", "local")))

	bind := singleBind(t, report)
	if bind.Action != service.TransferBindNoCandidate || len(bind.Candidates) != 0 {
		t.Fatalf("another member's private runtime became a candidate: %+v", bind)
	}
	if got := boundRuntimeOf(t, dst, name); got != "" {
		t.Fatalf("agent.runtime_id=%q, private runtime was used", got)
	}
}

func TestTransferBindRuntimes_Endpoint(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	suffix := uuid.NewString()[:6]
	agentName := "XferPick-" + suffix
	agentID := dbfx.Agent(t, agentName, "", testutil.Cols{"workspace_id": dst, "visibility": "workspace"})
	mine := dbfx.Runtime(t, "Claude (mac)", testutil.Cols{
		"workspace_id": dst, "provider": "claude", "runtime_mode": "local",
	})
	other := dbfx.User(t, "Other "+suffix, "other-"+suffix+"@example.com")
	theirs := dbfx.Runtime(t, "Claude (theirs)", testutil.Cols{
		"workspace_id": dst, "provider": "claude", "runtime_mode": "local",
		"owner_id": other, "visibility": "private",
	})

	resp := testutil.Call(t, testHandler.BindWorkspaceTransferRuntimes,
		transferReq("POST", "/api/workspaces/"+dst+"/transfer/bind-runtimes", dst, map[string]any{
			"bindings": []map[string]any{
				{"agent_id": agentID, "runtime_id": mine},
				{"agent_id": agentID, "runtime_id": theirs},
				{"agent_id": agentID, "runtime_id": uuid.NewString()},
				{"agent_id": uuid.NewString(), "runtime_id": mine},
			},
		})).Want(http.StatusOK)
	var report service.TransferBindRuntimesReport
	resp.JSON(&report)

	if !report.Applied || report.Bound != 1 || report.Failed != 3 {
		t.Fatalf("bound=%d failed=%d body=%s", report.Bound, report.Failed, resp.Text())
	}
	if len(report.Results) != 4 {
		t.Fatalf("results=%d want one row per binding", len(report.Results))
	}
	if !report.Results[0].Ok || report.Results[0].RuntimeName != "Claude (mac)" {
		t.Fatalf("first result=%+v", report.Results[0])
	}
	if report.Results[1].ReasonCode != service.TransferBindReasonRuntimePrivate {
		t.Fatalf("private runtime reason=%q", report.Results[1].ReasonCode)
	}
	if report.Results[2].ReasonCode != service.TransferBindReasonRuntimeUnknown {
		t.Fatalf("unknown runtime reason=%q", report.Results[2].ReasonCode)
	}
	if report.Results[3].ReasonCode != service.TransferBindReasonAgentUnknown {
		t.Fatalf("unknown agent reason=%q", report.Results[3].ReasonCode)
	}
	if got := boundRuntimeOf(t, dst, agentName); got != mine {
		t.Fatalf("agent.runtime_id=%q want %q", got, mine)
	}
}

func TestTransferBindRuntimes_AgentActorForbidden(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	agentID := dbfx.Agent(t, "XferBindActor-"+uuid.NewString()[:6], "", testutil.Cols{"workspace_id": dst})
	runtimeID := dbfx.Runtime(t, "Claude (mac)", testutil.Cols{
		"workspace_id": dst, "provider": "claude", "runtime_mode": "local",
	})
	req := transferReq("POST", "/api/workspaces/"+dst+"/transfer/bind-runtimes", dst, map[string]any{
		"bindings": []map[string]any{{"agent_id": agentID, "runtime_id": runtimeID}},
	})
	actor := dbfx.Agent(t, "XferBindCaller-"+uuid.NewString()[:6], "", testutil.Cols{"workspace_id": dst})
	req.Header.Set("X-Agent-ID", actor)
	req.Header.Set("X-Actor-Source", "task_token")
	testutil.Call(t, testHandler.BindWorkspaceTransferRuntimes, req).Want(http.StatusForbidden)
}

func TestTransferBindRuntimes_MemberForbidden(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	suffix := uuid.NewString()[:6]
	agentID := dbfx.Agent(t, "XferBindMember-"+suffix, "", testutil.Cols{"workspace_id": dst})
	runtimeID := dbfx.Runtime(t, "Claude (mac)", testutil.Cols{
		"workspace_id": dst, "provider": "claude", "runtime_mode": "local",
	})
	member := dbfx.User(t, "Member "+suffix, "member-"+suffix+"@example.com")
	dbfx.Member(t, dst, member, "member")

	body := map[string]any{"bindings": []map[string]any{{"agent_id": agentID, "runtime_id": runtimeID}}}
	req := testutil.WithURLParams(
		testutil.WithHeaders(testutil.JSONRequest("POST", "/api/workspaces/"+dst+"/transfer/bind-runtimes", body), "X-User-ID", member),
		"id", dst,
	)
	testutil.Call(t, testHandler.BindWorkspaceTransferRuntimes, req).Want(http.StatusForbidden)
}
