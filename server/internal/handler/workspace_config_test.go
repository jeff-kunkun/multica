package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
)

const (
	canaryEnv    = "CANARY_ENV_9f3a"
	canaryMCP    = "CANARY_MCP_7b1c"
	canaryGW     = "CANARY_GW_2e8d"
	canarySig    = "CANARY_SIG_4c6f"
	canaryMCPLib = "CANARY_MCPLIB_5d2a"
)

func configReq(method, path, wsID string, body any) *http.Request {
	return testutil.WithURLParams(
		testutil.WithHeaders(testutil.JSONRequest(method, path, body), "X-User-ID", testUserID),
		"id", wsID,
	)
}

func setupConfigWorkspaces(t *testing.T) (src, dst string) {
	t.Helper()
	suf := uuid.NewString()[:8]
	src = dbfx.Workspace(t, "CfgSrc "+suf, "cfgsrc-"+suf)
	dst = dbfx.Workspace(t, "CfgDst "+suf, "cfgdst-"+suf)
	dbfx.Member(t, src, testUserID, "owner")
	dbfx.Member(t, dst, testUserID, "owner")
	if err := testHandler.Queries.SeedIssueStatusEntries(context.Background(), util.MustParseUUID(src)); err != nil {
		t.Fatalf("seed src statuses: %v", err)
	}
	if err := testHandler.Queries.SeedIssueStatusEntries(context.Background(), util.MustParseUUID(dst)); err != nil {
		t.Fatalf("seed dst statuses: %v", err)
	}
	return src, dst
}

func exportBundle(t *testing.T, wsID string) service.ConfigBundle {
	t.Helper()
	var bundle service.ConfigBundle
	testutil.Call(t, testHandler.ExportWorkspaceConfig, configReq("GET", "/api/workspaces/"+wsID+"/config/export", wsID, nil)).
		Want(http.StatusOK).JSON(&bundle)
	return bundle
}

func importReport(t *testing.T, wsID string, body any, want int) service.ConfigImportReport {
	t.Helper()
	resp := testutil.Call(t, testHandler.ImportWorkspaceConfig, configReq("POST", "/api/workspaces/"+wsID+"/config/import", wsID, body))
	if resp.Code != want {
		var wrapped struct {
			Code   string                     `json:"code"`
			Error  string                     `json:"error"`
			Report service.ConfigImportReport `json:"report"`
		}
		_ = json.Unmarshal(resp.Body.Bytes(), &wrapped)
		if wrapped.Report.BundleID != "" {
			if want == http.StatusConflict || want == http.StatusUnprocessableEntity {
				return wrapped.Report
			}
		}
	}
	resp.Want(want)
	if want != http.StatusOK {
		var wrapped struct {
			Report service.ConfigImportReport `json:"report"`
		}
		resp.JSON(&wrapped)
		return wrapped.Report
	}
	var report service.ConfigImportReport
	resp.JSON(&report)
	return report
}

func TestWorkspaceConfigExport_CanarySecretsOmitted(t *testing.T) {
	src, _ := setupConfigWorkspaces(t)
	agentName := "CanaryBot-" + uuid.NewString()[:8]
	agentID := dbfx.Agent(t, agentName, "", testutil.Cols{
		"workspace_id":   src,
		"runtime_config": testutil.Raw(fmt.Sprintf(`'{"mode":"gateway","gateway":{"url":"http://127.0.0.1:18789","token":"%s"}}'::jsonb`, canaryGW)),
		"custom_env":     testutil.Raw(fmt.Sprintf(`'{"%s":"secret-value"}'::jsonb`, canaryEnv)),
		"mcp_config":     testutil.Raw(fmt.Sprintf(`'{"env":{"KEY":"%s"}}'::jsonb`, canaryMCP)),
		"visibility":     "workspace",
	})
	mcpName := "canary-mcp-" + uuid.NewString()[:8]
	dbfx.Insert(t, "workspace_mcp_server", testutil.Cols{
		"workspace_id": src,
		"name":         mcpName,
		"config":       testutil.Raw(fmt.Sprintf(`'{"type":"stdio","command":"npx","env":{"KEY":"%s"}}'::jsonb`, canaryMCPLib)),
		"created_by":   testUserID,
	})
	apID := dbfx.Insert(t, "autopilot", testutil.Cols{
		"workspace_id":    src,
		"title":           "Canary AP " + uuid.NewString()[:6],
		"assignee_type":   "agent",
		"assignee_id":     agentID,
		"status":          "paused",
		"execution_mode":  "create_issue",
		"created_by_type": "member",
		"created_by_id":   testUserID,
	})
	dbfx.Insert(t, "autopilot_trigger", testutil.Cols{
		"autopilot_id":    apID,
		"kind":            "webhook",
		"enabled":         true,
		"label":           "CI",
		"webhook_token":   "awt_canarytokenvalue",
		"signing_secret":  canarySig,
		"created_by_type": "member",
		"created_by_id":   testUserID,
	})

	resp := testutil.Call(t, testHandler.ExportWorkspaceConfig, configReq("GET", "/api/workspaces/"+src+"/config/export", src, nil)).Want(http.StatusOK)
	body := resp.Text()
	for _, needle := range []string{canaryEnv, canaryMCP, canaryGW, canarySig, canaryMCPLib, "***"} {
		if strings.Contains(body, needle) {
			t.Errorf("export leaked %q", needle)
		}
	}

	var bundle service.ConfigBundle
	resp.JSON(&bundle)
	wantFields := map[string]bool{
		"custom_env": false, "mcp_config": false, "runtime_config.gateway.token": false,
		"webhook_token": false, "signing_secret": false, "config": false,
	}
	for _, s := range bundle.SecretsOmitted {
		wantFields[s.Field] = true
	}
	for field, ok := range wantFields {
		if !ok {
			t.Errorf("secrets_omitted missing field %s", field)
		}
	}
	if bundle.Format != service.ConfigBundleFormat {
		t.Errorf("format = %q", bundle.Format)
	}
	if cd := resp.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("missing Content-Disposition attachment, got %q", cd)
	}
	if resp.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", resp.Header().Get("Cache-Control"))
	}
}

func TestWorkspaceConfigImport_RejectsSecret(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	dry := true
	body := map[string]any{
		"dry_run":     dry,
		"on_conflict": "skip",
		"bundle": map[string]any{
			"format":         service.ConfigBundleFormat,
			"schema_version": 1,
			"bundle_id":      uuid.NewString(),
			"exported_at":    "2026-09-15T00:00:00Z",
			"source":         map[string]any{"workspace_id": uuid.NewString(), "slug": "x", "name": "x", "exported_by": testUserID},
			"entities": map[string]any{
				"agents": []map[string]any{{
					"source_id":  uuid.NewString(),
					"name":       "leaky",
					"custom_env": map[string]string{"K": "v"},
				}},
			},
		},
	}
	resp := testutil.Call(t, testHandler.ImportWorkspaceConfig, configReq("POST", "/api/workspaces/"+dst+"/config/import", dst, body))
	resp.Want(http.StatusBadRequest)
	got := resp.Map()
	if got["code"] != "config_bundle_contains_secret" {
		t.Fatalf("code = %v body=%s", got["code"], resp.Text())
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent WHERE workspace_id = $1 AND name = 'leaky'`, dst); n != 0 {
		t.Fatalf("reject-secret wrote an agent")
	}
}

func TestWorkspaceConfigImport_OverwritePreservesCustomEnv(t *testing.T) {
	src, dst := setupConfigWorkspaces(t)
	name := "KeepEnv-" + uuid.NewString()[:8]
	dbfx.Agent(t, name, "", testutil.Cols{
		"workspace_id": src,
		"custom_env":   testutil.Raw(`'{"SRC":"1"}'::jsonb`),
		"visibility":   "workspace",
	})
	dstAgent := dbfx.Agent(t, name, "", testutil.Cols{
		"workspace_id": dst,
		"custom_env":   testutil.Raw(`'{"DST_A":"1","DST_B":"2"}'::jsonb`),
		"visibility":   "workspace",
	})
	bundle := exportBundle(t, src)
	dry := false
	_ = importReport(t, dst, map[string]any{
		"bundle": bundle, "dry_run": dry, "on_conflict": "overwrite", "include": []string{"agents"},
	}, http.StatusOK)
	var keyCount int
	dbfx.QueryRow(t, `SELECT (SELECT count(*) FROM jsonb_object_keys(COALESCE(custom_env, '{}'::jsonb))) FROM agent WHERE id = $1`, dstAgent).Scan(&keyCount)
	if keyCount != 2 {
		t.Fatalf("overwrite clobbered custom_env; key_count=%d want 2", keyCount)
	}
}

func TestWorkspaceConfigRoundTripAndConflicts(t *testing.T) {
	src, dst := setupConfigWorkspaces(t)
	suf := uuid.NewString()[:6]
	labelName := "builder-" + suf
	skillName := "review-" + suf
	agentName := "Builder-" + suf
	squadName := "Squad-" + suf
	projectTitle := "Proj-" + suf
	apTitle := "Nightly-" + suf
	qaName := "Please review-" + suf
	propName := "Tier-" + suf
	statusKey := "verifying_" + suf

	dbfx.Insert(t, "issue_label", testutil.Cols{
		"workspace_id": src, "resource_type": "agent", "name": labelName, "color": "#3b82f6", "description": "",
	})
	skillID := dbfx.Insert(t, "skill", testutil.Cols{
		"workspace_id": src, "name": skillName, "description": "review", "content": "# Review\n", "created_by": testUserID,
	})
	dbfx.Insert(t, "skill_file", testutil.Cols{
		"skill_id": skillID, "path": "references/a.md", "content": "# A\n",
	})
	agentID := dbfx.Agent(t, agentName, "", testutil.Cols{
		"workspace_id": src, "instructions": "build things", "visibility": "workspace",
	})
	dbfx.InsertNoID(t, "agent_skill", testutil.Cols{"agent_id": agentID, "skill_id": skillID, "enabled": true},
		"agent_id = $1 AND skill_id = $2", agentID, skillID)
	dbfx.Squad(t, squadName, agentID, testutil.Cols{"workspace_id": src})
	dbfx.Project(t, projectTitle, testutil.Cols{"workspace_id": src, "status": "in_progress"})
	dbfx.Insert(t, "autopilot", testutil.Cols{
		"workspace_id": src, "title": apTitle, "assignee_type": "agent", "assignee_id": agentID,
		"status": "active", "execution_mode": "create_issue", "created_by_type": "member", "created_by_id": testUserID,
	})
	dbfx.Insert(t, "quick_action", testutil.Cols{
		"workspace_id": src, "name": qaName, "assignee_type": "agent", "assignee_id": agentID,
		"prompt": "review", "visibility": "public", "created_by_type": "member", "created_by_id": testUserID,
	})
	dbfx.Insert(t, "issue_property", testutil.Cols{
		"workspace_id": src, "name": propName, "type": "select", "description": "", "icon": "layers",
		"config":   testutil.Raw(`'{"options":[{"id":"11111111-1111-1111-1111-111111111111","name":"T3","color":"#ef4444"}]}'::jsonb`),
		"position": 1,
	})
	dbfx.Insert(t, "issue_status", testutil.Cols{
		"workspace_id": src, "key": statusKey, "name": "Verifying", "description": "",
		"category": "in_review", "color": "#a855f7", "is_system": false, "position": 4.5,
	})

	bundle := exportBundle(t, src)
	if bundle.Stats["agents"] < 1 || bundle.Stats["skills"] < 1 || bundle.Stats["labels"] < 1 {
		t.Fatalf("export stats too small: %+v", bundle.Stats)
	}

	dryTrue := true
	preview := importReport(t, dst, map[string]any{"bundle": bundle, "dry_run": dryTrue, "on_conflict": "skip"}, http.StatusOK)
	if preview.Applied {
		t.Fatal("dry_run report should have applied=false")
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent WHERE workspace_id = $1 AND name = $2`, dst, agentName); n != 0 {
		t.Fatal("dry_run wrote an agent")
	}

	dryFalse := false
	applied := importReport(t, dst, map[string]any{"bundle": bundle, "dry_run": dryFalse, "on_conflict": "skip"}, http.StatusOK)
	if !applied.Applied {
		t.Fatal("apply report should have applied=true")
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent WHERE workspace_id = $1 AND name = $2`, dst, agentName); n != 1 {
		t.Fatalf("expected 1 imported agent, got %d", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM skill WHERE workspace_id = $1 AND name = $2`, dst, skillName); n != 1 {
		t.Fatalf("expected 1 imported skill, got %d", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_label WHERE workspace_id = $1 AND name = $2`, dst, labelName); n != 1 {
		t.Fatalf("expected 1 imported label, got %d", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM squad WHERE workspace_id = $1 AND name = $2`, dst, squadName); n != 1 {
		t.Fatalf("expected 1 imported squad, got %d", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM project WHERE workspace_id = $1 AND title = $2`, dst, projectTitle); n != 1 {
		t.Fatalf("expected 1 imported project, got %d", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM autopilot WHERE workspace_id = $1 AND title = $2`, dst, apTitle); n != 1 {
		t.Fatalf("expected 1 imported autopilot, got %d", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM quick_action WHERE workspace_id = $1 AND name = $2`, dst, qaName); n != 1 {
		t.Fatalf("expected 1 imported quick action, got %d", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_property WHERE workspace_id = $1 AND name = $2`, dst, propName); n != 1 {
		t.Fatalf("expected 1 imported property, got %d", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_status WHERE workspace_id = $1 AND key = $2`, dst, statusKey); n != 1 {
		t.Fatalf("expected 1 imported custom status, got %d", n)
	}

	again := importReport(t, dst, map[string]any{"bundle": bundle, "dry_run": dryFalse, "on_conflict": "skip"}, http.StatusOK)
	if again.Stats.Created != 0 {
		t.Fatalf("second skip import created %d entities", again.Stats.Created)
	}
	if again.Stats.Skipped == 0 {
		t.Fatalf("second skip import expected skipped > 0, stats=%+v", again.Stats)
	}

	rename := importReport(t, dst, map[string]any{"bundle": bundle, "dry_run": dryFalse, "on_conflict": "rename", "include": []string{"agents"}}, http.StatusOK)
	if n := dbfx.Count(t, `SELECT count(*) FROM agent WHERE workspace_id = $1 AND name LIKE $2`, dst, agentName+"%"); n < 2 {
		t.Fatalf("rename should create a copy, agent count=%d report=%+v", n, rename.Stats)
	}
}

func TestWorkspaceConfigImport_UnmappedMemberDropped(t *testing.T) {
	src, dst := setupConfigWorkspaces(t)
	outsider := dbfx.User(t, "Outsider", "out-"+uuid.NewString()[:8]+"@example.com")
	dbfx.Member(t, src, outsider, "member")
	agentID := dbfx.Agent(t, "Lead-"+uuid.NewString()[:6], "", testutil.Cols{"workspace_id": src, "visibility": "workspace"})
	squadID := dbfx.Squad(t, "WithOutsider", agentID, testutil.Cols{"workspace_id": src})
	dbfx.SquadMember(t, squadID, "member", outsider, testutil.Cols{"role": "决策人"})

	bundle := exportBundle(t, src)
	dry := false
	report := importReport(t, dst, map[string]any{"bundle": bundle, "dry_run": dry, "on_conflict": "skip", "include": []string{"agents", "squads"}}, http.StatusOK)
	found := false
	for _, u := range report.UnmappedRefs {
		if u.RefType == "member" && u.RefID == outsider {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected unmapped member ref, got %+v", report.UnmappedRefs)
	}
}

func TestWorkspaceConfigImport_InvalidVersion(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	dry := true
	resp := testutil.Call(t, testHandler.ImportWorkspaceConfig, configReq("POST", "/api/workspaces/"+dst+"/config/import", dst, map[string]any{
		"dry_run": dry,
		"bundle": map[string]any{
			"format": service.ConfigBundleFormat, "schema_version": 99,
			"source":   map[string]any{"workspace_id": uuid.NewString()},
			"entities": map[string]any{},
		},
	}))
	resp.Want(http.StatusBadRequest)
	if resp.Map()["code"] != "config_bundle_version_unsupported" {
		t.Fatalf("code=%v", resp.Map()["code"])
	}
}

func TestWorkspaceConfigExport_InvalidInclude(t *testing.T) {
	src, _ := setupConfigWorkspaces(t)
	req := configReq("GET", "/api/workspaces/"+src+"/config/export?include=not_a_type", src, nil)
	testutil.Call(t, testHandler.ExportWorkspaceConfig, req).Want(http.StatusBadRequest)
}

func TestWorkspaceConfigExport_AgentActorForbidden(t *testing.T) {
	src, _ := setupConfigWorkspaces(t)
	agentID := dbfx.Agent(t, "Actor-"+uuid.NewString()[:6], "", testutil.Cols{"workspace_id": src})
	req := configReq("GET", "/api/workspaces/"+src+"/config/export", src, nil)
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Actor-Source", "task_token")
	testutil.Call(t, testHandler.ExportWorkspaceConfig, req).Want(http.StatusForbidden)
}

func TestWorkspaceConfigImport_SameWorkspaceRejected(t *testing.T) {
	src, _ := setupConfigWorkspaces(t)
	bundle := exportBundle(t, src)
	dry := true
	resp := testutil.Call(t, testHandler.ImportWorkspaceConfig, configReq("POST", "/api/workspaces/"+src+"/config/import", src, map[string]any{
		"bundle": bundle, "dry_run": dry, "on_conflict": "skip",
	}))
	resp.Want(http.StatusBadRequest)
	if resp.Map()["code"] != "config_import_same_workspace" {
		t.Fatalf("code=%v body=%s", resp.Map()["code"], resp.Text())
	}
}
