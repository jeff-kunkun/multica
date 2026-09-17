package handler

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// Configuration inheritance at the API boundary (DENE-470). A specialisation
// owns its identity and its own prompt; every configuration field belongs to its
// base role. These tests drive the real handlers and then read the COLUMNS back,
// because the write-time half of the rule is about what the row holds — the
// read-time half is covered by daemon_agent_inheritance_test.go, which asserts
// the claim payload a daemon actually boots from.

// inheritedColumns is the row slice these tests read back. It is deliberately a
// struct rather than a map: a field that stops being selected is a compile
// error, not a silently missing assertion.
type inheritedColumns struct {
	RuntimeID        pgtype.UUID
	RuntimeMode      string
	Visibility       string
	PermissionMode   string
	Model            pgtype.Text
	ThinkingLevel    pgtype.Text
	ServiceTier      pgtype.Text
	MaxConcurrent    int32
	CustomEnv        []byte
	CustomArgs       []byte
	McpConfig        []byte
	RuntimeConfig    []byte
	SwitchableModels []byte
	Starters         []byte
	DisabledSkills   []byte
	Allowlist        []string
}

func persistedInheritedColumns(t *testing.T, agentID string) inheritedColumns {
	t.Helper()

	var row inheritedColumns
	dbfx.QueryRow(t, `
		SELECT runtime_id, runtime_mode, visibility, permission_mode, model,
		       thinking_level, service_tier, max_concurrent_tasks, custom_env,
		       custom_args, mcp_config, runtime_config, switchable_models,
		       conversation_starters, disabled_runtime_skills,
		       composio_toolkit_allowlist
		FROM agent WHERE id = $1`, agentID,
	).Scan(
		&row.RuntimeID, &row.RuntimeMode, &row.Visibility, &row.PermissionMode, &row.Model,
		&row.ThinkingLevel, &row.ServiceTier, &row.MaxConcurrent, &row.CustomEnv,
		&row.CustomArgs, &row.McpConfig, &row.RuntimeConfig, &row.SwitchableModels,
		&row.Starters, &row.DisabledSkills, &row.Allowlist,
	)
	return row
}

// configuredBaseRole inserts a base role whose EVERY inherited column differs
// from the fixture defaults, so "the child copied the base role" and "the child
// kept its own row" can never be confused for one another.
func configuredBaseRole(t *testing.T, name, runtimeID string) string {
	t.Helper()

	return dbfx.Agent(t, name, runtimeID, testutil.Cols{
		"instructions":               "base role rules",
		"model":                      "base-model",
		"thinking_level":             "high",
		"service_tier":               "priority",
		"max_concurrent_tasks":       4,
		"visibility":                 "workspace",
		"permission_mode":            "public_to",
		"custom_env":                 testutil.Raw(`'{"BASE_KEY":"base-value"}'::jsonb`),
		"custom_args":                testutil.Raw(`'["--base-flag"]'::jsonb`),
		"mcp_config":                 testutil.Raw(`'{"mcpServers":{"base":{}}}'::jsonb`),
		"runtime_config":             testutil.Raw(`'{"base":true}'::jsonb`),
		"conversation_starters":      testutil.Raw(`'[{"label":"base","prompt":"base"}]'::jsonb`),
		"switchable_models":          testutil.Raw(`'[{"model":"base-model","role":"default","note":""}]'::jsonb`),
		"disabled_runtime_skills":    testutil.Raw(`'["base-disabled-skill"]'::jsonb`),
		"composio_toolkit_allowlist": testutil.Raw(`ARRAY['base-toolkit']`),
	})
}

// assertColumnsMatchBaseRole is the shared assertion: every inherited column on
// the agent equals the base role's, field by field.
func assertColumnsMatchBaseRole(t *testing.T, agentID, baseID string) {
	t.Helper()

	got := persistedInheritedColumns(t, agentID)
	want := persistedInheritedColumns(t, baseID)
	fields := []struct {
		name     string
		got, exp any
	}{
		{"runtime_id", got.RuntimeID, want.RuntimeID},
		{"runtime_mode", got.RuntimeMode, want.RuntimeMode},
		{"visibility", got.Visibility, want.Visibility},
		{"permission_mode", got.PermissionMode, want.PermissionMode},
		{"model", got.Model, want.Model},
		{"thinking_level", got.ThinkingLevel, want.ThinkingLevel},
		{"service_tier", got.ServiceTier, want.ServiceTier},
		{"max_concurrent_tasks", got.MaxConcurrent, want.MaxConcurrent},
		{"custom_env", string(got.CustomEnv), string(want.CustomEnv)},
		{"custom_args", string(got.CustomArgs), string(want.CustomArgs)},
		{"mcp_config", string(got.McpConfig), string(want.McpConfig)},
		{"runtime_config", string(got.RuntimeConfig), string(want.RuntimeConfig)},
		{"switchable_models", string(got.SwitchableModels), string(want.SwitchableModels)},
		{"conversation_starters", string(got.Starters), string(want.Starters)},
		{"disabled_runtime_skills", string(got.DisabledSkills), string(want.DisabledSkills)},
		{"composio_toolkit_allowlist", fmt.Sprint(got.Allowlist), fmt.Sprint(want.Allowlist)},
	}
	for _, f := range fields {
		if f.got != f.exp {
			t.Errorf("%s = %v, want the base role's %v", f.name, f.got, f.exp)
		}
	}
}

// workspaceInvocationTargetCount counts the allow-list rows that grant the
// whole workspace, which is the shape the create test seeds on the base role.
func workspaceInvocationTargetCount(t *testing.T, agentID string) int {
	t.Helper()

	return dbfx.Count(t,
		`SELECT count(*) FROM agent_invocation_target WHERE agent_id = $1 AND target_type = 'workspace'`,
		agentID)
}

// TestCreateSpecialisationInheritsBaseRoleConfiguration is the create arm: the
// derive request supplies identity only — no runtime at all, which is exactly
// how the production bug happened — and the child still comes out configured.
func TestCreateSpecialisationInheritsBaseRoleConfiguration(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	baseID := configuredBaseRole(t, "cfg-create-base", testRuntimeID)
	dbfx.InsertNoID(t, "agent_invocation_target",
		testutil.Cols{
			"agent_id":    baseID,
			"target_type": "workspace",
			"target_id":   testWorkspaceID,
			"created_by":  testUserID,
		},
		"agent_id = $1 AND target_type = 'workspace'", baseID)

	req := specRequest(http.MethodPost, "/api/agents", map[string]any{
		"name":            "cfg-create-child",
		"parent_agent_id": baseID,
	})
	resp := testutil.Decode[AgentResponse](t, testHandler.CreateAgent, req, http.StatusCreated)

	assertColumnsMatchBaseRole(t, resp.ID, baseID)
	if got := workspaceInvocationTargetCount(t, resp.ID); got != 1 {
		t.Errorf("child workspace invocation targets = %d, want the base role's 1", got)
	}
	// Identity stays the child's own: the inherited columns above must not have
	// dragged the base role's prompt or name along.
	if resp.Instructions != "" {
		t.Errorf("child instructions = %q, want its own (empty) value", resp.Instructions)
	}
	if resp.Name != "cfg-create-child" {
		t.Errorf("child name = %q", resp.Name)
	}
}

// TestCreateSpecialisationIgnoresRequestConfiguration pins "ignore", not
// "merge": a client that fills the derive form with its own defaults must not
// be able to fork the child's configuration away from the base role's.
func TestCreateSpecialisationIgnoresRequestConfiguration(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	otherRuntimeID := createClaimReclaimRuntime(t, t.Context(), "cfg-create-other-runtime")
	baseID := configuredBaseRole(t, "cfg-ignore-base", testRuntimeID)

	req := specRequest(http.MethodPost, "/api/agents", map[string]any{
		"name":                 "cfg-ignore-child",
		"parent_agent_id":      baseID,
		"runtime_id":           otherRuntimeID,
		"model":                "request-model",
		"thinking_level":       "low",
		"max_concurrent_tasks": 2,
		"visibility":           "private",
		"custom_args":          []string{"--request-flag"},
		"conversation_starters": []map[string]any{
			{"label": "request", "prompt": "request"},
		},
	})
	resp := testutil.Decode[AgentResponse](t, testHandler.CreateAgent, req, http.StatusCreated)

	assertColumnsMatchBaseRole(t, resp.ID, baseID)
	if resp.RuntimeID == otherRuntimeID {
		t.Error("the child was created on the request's runtime instead of the base role's")
	}
	if resp.Model != "base-model" {
		t.Errorf("child model = %q, want the base role's", resp.Model)
	}
}

// TestUpdateBaseRolePropagatesConfigurationToSpecialisations is the propagation
// arm: a base role's runtime and access are one fact shared with its
// specialisations, and they move together inside the update's transaction.
func TestUpdateBaseRolePropagatesConfigurationToSpecialisations(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	// Deliberately not configuredBaseRole: this test moves the base role to
	// another runtime, and the fixture provider rejects a per-agent reasoning
	// effort when the runtime changes, which would mask what is under test.
	baseID := dbfx.Agent(t, "cfg-propagate-base", testRuntimeID, testutil.Cols{
		"instructions":         "base role rules",
		"permission_mode":      "public_to",
		"visibility":           "workspace",
		"max_concurrent_tasks": 4,
		"model":                "base-model",
	})
	childID := createSpecialisationViaAPI(t, "cfg-propagate-child", baseID)

	otherRuntimeID := createClaimReclaimRuntime(t, t.Context(), "cfg-propagate-runtime")

	req := testutil.WithURLParams(
		specRequest(http.MethodPut, "/api/agents/"+baseID, map[string]any{
			"runtime_id":      otherRuntimeID,
			"permission_mode": "private",
		}), "id", baseID)
	testutil.Call(t, testHandler.UpdateAgent, req).Want(http.StatusOK)

	assertColumnsMatchBaseRole(t, childID, baseID)
	var mirrored pgtype.UUID
	dbfx.QueryRow(t, `SELECT runtime_id FROM agent WHERE id = $1`, childID).Scan(&mirrored)
	if mirrored != parseUUID(otherRuntimeID) {
		t.Fatalf("child runtime_id = %v, want the base role's new runtime %s", mirrored, otherRuntimeID)
	}
}

// TestUpdateSpecialisationMirrorsOnAttach covers an EXISTING agent being
// attached: the mirror runs in the same transaction as the parent link, so the
// row is never visible with a base role and a stale runtime.
func TestUpdateSpecialisationMirrorsOnAttach(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	baseID := configuredBaseRole(t, "cfg-attach-base", testRuntimeID)
	otherRuntimeID := createClaimReclaimRuntime(t, t.Context(), "cfg-attach-runtime")
	// A fully independent base role with a runtime of its own, about to become
	// somebody's specialisation.
	orphanID := dbfx.Agent(t, "cfg-attach-orphan", otherRuntimeID, testutil.Cols{
		"model": "orphan-model",
	})

	req := testutil.WithURLParams(
		specRequest(http.MethodPut, "/api/agents/"+orphanID, map[string]any{
			"parent_agent_id": baseID,
		}), "id", orphanID)
	testutil.Call(t, testHandler.UpdateAgent, req).Want(http.StatusOK)

	got := persistedInheritedColumns(t, orphanID)
	if got.RuntimeID != parseUUID(testRuntimeID) {
		t.Errorf("attached runtime_id = %v, want the base role's %s", got.RuntimeID, testRuntimeID)
	}
	if got.RuntimeMode != "cloud" {
		t.Errorf("attached runtime_mode = %q, want the base role's", got.RuntimeMode)
	}
	if got.PermissionMode != "public_to" || got.Visibility != "workspace" {
		t.Errorf("attached permission = (%q, %q), want the base role's", got.PermissionMode, got.Visibility)
	}
}

// TestUpdateSpecialisationCannotForkInheritedConfiguration pins the "not
// editable" half at the API boundary. A PATCH-as-PUT client that echoes the
// whole payload back is tolerated, but it cannot move the child off its base
// role's configuration — the mirror is re-asserted before the transaction
// commits.
func TestUpdateSpecialisationCannotForkInheritedConfiguration(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	baseID := configuredBaseRole(t, "cfg-fork-base", testRuntimeID)
	childID := createSpecialisationViaAPI(t, "cfg-fork-child", baseID)

	req := testutil.WithURLParams(
		specRequest(http.MethodPut, "/api/agents/"+childID, map[string]any{
			"model":                "forked-model",
			"max_concurrent_tasks": 2,
			"permission_mode":      "private",
		}), "id", childID)
	testutil.Call(t, testHandler.UpdateAgent, req).Want(http.StatusOK)

	assertColumnsMatchBaseRole(t, childID, baseID)
}

// TestDetachSnapshotsInheritedConfiguration covers the moment inheritance
// stops. While the link exists the claim path resolves these columns from the
// base role live; once the link is gone nothing does, so the row has to carry
// them or the freed agent silently reverts to whatever it happened to hold.
func TestDetachSnapshotsInheritedConfiguration(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	baseID := configuredBaseRole(t, "cfg-detach-base", testRuntimeID)
	childID := createSpecialisationViaAPI(t, "cfg-detach-child", baseID)

	// Move the base role AFTER the child was created: a snapshot taken at
	// attach time would carry the old values and fail here. thinking_level is
	// written straight to the column because the fixture runtime exposes no
	// reasoning catalog to send a new value through the API.
	dbfx.Exec(t, `UPDATE agent SET thinking_level = 'medium' WHERE id = $1`, baseID)
	updateReq := testutil.WithURLParams(
		specRequest(http.MethodPut, "/api/agents/"+baseID, map[string]any{
			"model":                "base-model-v2",
			"max_concurrent_tasks": 9,
		}), "id", baseID)
	testutil.Call(t, testHandler.UpdateAgent, updateReq).Want(http.StatusOK)

	detachReq := testutil.WithURLParams(
		specRequest(http.MethodPut, "/api/agents/"+childID, map[string]any{
			"parent_agent_id": "",
		}), "id", childID)
	testutil.Call(t, testHandler.UpdateAgent, detachReq).Want(http.StatusOK)

	got := persistedInheritedColumns(t, childID)
	if got.Model.String != "base-model-v2" {
		t.Errorf("detached model = %q, want the base role's live value", got.Model.String)
	}
	if got.ThinkingLevel.String != "medium" {
		t.Errorf("detached thinking_level = %q, want the base role's live value", got.ThinkingLevel.String)
	}
	if got.MaxConcurrent != 9 {
		t.Errorf("detached max_concurrent_tasks = %d, want the base role's live value", got.MaxConcurrent)
	}
	// The runtime columns were mirrored at attach and must survive the detach.
	if got.RuntimeID != parseUUID(testRuntimeID) {
		t.Errorf("detached runtime_id = %v, want the value it ran with", got.RuntimeID)
	}
}

// TestSolidifySnapshotsInheritedConfiguration is the same moment reached through
// Agent.solidify, which is the path the product points users at.
func TestSolidifySnapshotsInheritedConfiguration(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	baseID := configuredBaseRole(t, "cfg-solidify-base", testRuntimeID)
	childID := createSpecialisationViaAPI(t, "cfg-solidify-child", baseID)

	updateReq := testutil.WithURLParams(
		specRequest(http.MethodPut, "/api/agents/"+baseID, map[string]any{
			"model": "base-model-v2",
		}), "id", baseID)
	testutil.Call(t, testHandler.UpdateAgent, updateReq).Want(http.StatusOK)

	solidifyReq := testutil.WithURLParams(
		specRequest(http.MethodPost, "/api/agents/"+childID+"/solidify", nil), "id", childID)
	testutil.Call(t, testHandler.SolidifyAgent, solidifyReq).Want(http.StatusOK)

	got := persistedInheritedColumns(t, childID)
	if got.Model.String != "base-model-v2" {
		t.Errorf("solidified model = %q, want the base role's live value", got.Model.String)
	}
	if got.RuntimeID != parseUUID(testRuntimeID) {
		t.Errorf("solidified runtime_id = %v, want the value it ran with", got.RuntimeID)
	}
}

// createSpecialisationViaAPI derives a specialisation through the real handler,
// so the fixture exercises the same path the product does.
func createSpecialisationViaAPI(t *testing.T, name, baseID string) string {
	t.Helper()

	req := specRequest(http.MethodPost, "/api/agents", map[string]any{
		"name":            name,
		"parent_agent_id": baseID,
	})
	resp := testutil.Decode[AgentResponse](t, testHandler.CreateAgent, req, http.StatusCreated)
	return resp.ID
}
