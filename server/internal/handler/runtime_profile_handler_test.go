package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// insertRuntimeProfileFixture creates a runtime_profile in testWorkspaceID and
// returns its id, registering cleanup.
func insertRuntimeProfileFixture(t *testing.T, ctx context.Context, displayName, protocolFamily, commandName string) string {
	t.Helper()
	var profileID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO runtime_profile (workspace_id, display_name, protocol_family, command_name, created_by)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id
	`, testWorkspaceID, displayName, protocolFamily, commandName, testUserID).Scan(&profileID); err != nil {
		t.Fatalf("insert runtime_profile fixture: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM runtime_profile WHERE id = $1`, profileID)
	})
	return profileID
}

// insertProfileRuntimeFixture creates an agent_runtime instance bound to the
// given profile (so profile_id is set), returning its id.
func insertProfileRuntimeFixture(t *testing.T, ctx context.Context, profileID, name, provider string) string {
	t.Helper()
	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, owner_id, profile_id, last_seen_at
		)
		VALUES ($1, NULL, $2, 'local', $3, 'online', $4, '{}'::jsonb, $5, $6, now())
		RETURNING id
	`, testWorkspaceID, name, provider, name+" device", testUserID, profileID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert profile runtime fixture: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent WHERE runtime_id = $1`, runtimeID)
		testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
	})
	return runtimeID
}

// TestDeleteRuntimeProfile_ArchivedAgentUnbindCascade is the regression guard for
// the FK-RESTRICT 500: a profile whose only remaining agent is ARCHIVED must
// still delete cleanly. agent.runtime_id is ON DELETE RESTRICT, so without the
// per-runtime teardown the DELETE on agent_runtime would raise a raw FK error and
// the handler would 500.
//
// Since MUL-5559 the teardown unbinds that agent instead of hard-deleting it, so
// this test also pins that the agent survives as an unbound agent.
func TestDeleteRuntimeProfile_ArchivedAgentUnbindCascade(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	profileID := insertRuntimeProfileFixture(t, ctx, "Cascade Profile Archived", "codex", "company-codex-arch")
	runtimeID := insertProfileRuntimeFixture(t, ctx, profileID, "Cascade Profile Runtime", "codex")
	agentID := createCascadeFixtureAgent(t, ctx, runtimeID, "Cascade Profile Archived Agent")

	// Archive the agent — the active-agent guard passes, but the FK still pins
	// the runtime row until the teardown unbinds it.
	if _, err := testPool.Exec(ctx, `UPDATE agent SET archived_at = now() WHERE id = $1`, agentID); err != nil {
		t.Fatalf("archive agent: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("DELETE", "/api/workspaces/"+testWorkspaceID+"/runtime-profiles/"+profileID, nil)
	req = withURLParams(req, "id", testWorkspaceID, "profileId", profileID)
	notifier := &recordingRuntimeGoneNotifier{}
	h := *testHandler
	h.DaemonRuntimeGone = notifier
	h.DeleteRuntimeProfile(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	var profileRows, rtRows, agentRows int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM runtime_profile WHERE id = $1`, profileID).Scan(&profileRows); err != nil {
		t.Fatalf("count profile rows: %v", err)
	}
	if profileRows != 0 {
		t.Fatalf("expected profile deleted, found %d", profileRows)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&rtRows); err != nil {
		t.Fatalf("count runtime rows: %v", err)
	}
	if rtRows != 0 {
		t.Fatalf("expected runtime row deleted by cascade, found %d", rtRows)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent WHERE id = $1`, agentID).Scan(&agentRows); err != nil {
		t.Fatalf("count agent rows: %v", err)
	}
	if agentRows != 1 {
		t.Fatalf("expected the archived agent to survive as an unbound agent, found %d rows", agentRows)
	}
	var stillBound bool
	if err := testPool.QueryRow(ctx,
		`SELECT runtime_id IS NOT NULL FROM agent WHERE id = $1`, agentID).Scan(&stillBound); err != nil {
		t.Fatalf("read agent binding: %v", err)
	}
	if stillBound {
		t.Fatalf("surviving agent must be unbound (runtime_id IS NULL)")
	}

	if len(notifier.runtimeIDs) != 1 || notifier.runtimeIDs[0] != runtimeID {
		t.Fatalf("runtime-gone notifications = %v, want [%s]", notifier.runtimeIDs, runtimeID)
	}
}

// TestDeleteRuntimeProfile_ActiveAgentBlocks confirms the guard still refuses
// (409) while an ACTIVE agent is bound to one of the profile's runtimes, and
// leaves the profile + runtime intact.
func TestDeleteRuntimeProfile_ActiveAgentBlocks(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	profileID := insertRuntimeProfileFixture(t, ctx, "Cascade Profile Active", "codex", "company-codex-active")
	runtimeID := insertProfileRuntimeFixture(t, ctx, profileID, "Cascade Profile Active Runtime", "codex")
	_ = createCascadeFixtureAgent(t, ctx, runtimeID, "Cascade Profile Active Agent")

	w := httptest.NewRecorder()
	req := newRequest("DELETE", "/api/workspaces/"+testWorkspaceID+"/runtime-profiles/"+profileID, nil)
	req = withURLParams(req, "id", testWorkspaceID, "profileId", profileID)
	notifier := &recordingRuntimeGoneNotifier{}
	h := *testHandler
	h.DaemonRuntimeGone = notifier
	h.DeleteRuntimeProfile(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}

	var profileRows, rtRows int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM runtime_profile WHERE id = $1`, profileID).Scan(&profileRows); err != nil {
		t.Fatalf("count profile rows: %v", err)
	}
	if profileRows != 1 {
		t.Fatalf("expected profile to survive 409, found %d", profileRows)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&rtRows); err != nil {
		t.Fatalf("count runtime rows: %v", err)
	}
	if rtRows != 1 {
		t.Fatalf("expected runtime to survive 409, found %d", rtRows)
	}
	if len(notifier.runtimeIDs) != 0 {
		t.Fatalf("rollback/refusal emitted runtime-gone notifications: %v", notifier.runtimeIDs)
	}
}

func TestDeleteRuntimeProfile_WorkspaceMismatchReturnsStructuredConflict(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	profileID := insertRuntimeProfileFixture(t, ctx, "Workspace Mismatch Profile", "codex", "workspace-mismatch-codex")
	runtimeID := insertProfileRuntimeFixture(t, ctx, profileID, "Workspace Mismatch Runtime", "codex")
	agentID := createArchivedWorkspaceMismatchedAgent(t, runtimeID, "Profile Workspace Mismatch Agent")

	w := httptest.NewRecorder()
	req := newRequest("DELETE", "/api/workspaces/"+testWorkspaceID+"/runtime-profiles/"+profileID, nil)
	req = withURLParams(req, "id", testWorkspaceID, "profileId", profileID)
	testHandler.DeleteRuntimeProfile(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Code != "runtime_delete_workspace_mismatch" {
		t.Fatalf("code = %q, want runtime_delete_workspace_mismatch", body.Code)
	}

	var profileRows, runtimeRows int
	var boundRuntime string
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM runtime_profile WHERE id = $1`, profileID).Scan(&profileRows); err != nil {
		t.Fatalf("count profile rows: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&runtimeRows); err != nil {
		t.Fatalf("count runtime rows: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT runtime_id::text FROM agent WHERE id = $1`, agentID).Scan(&boundRuntime); err != nil {
		t.Fatalf("read mismatched agent binding: %v", err)
	}
	if profileRows != 1 || runtimeRows != 1 || boundRuntime != runtimeID {
		t.Fatalf("workspace mismatch mutated data: profile rows=%d runtime rows=%d agent runtime=%q", profileRows, runtimeRows, boundRuntime)
	}
}

func TestDeleteRuntimeProfile_MissingProfileWithOrphanRuntimesCleansUp(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	profileID := insertRuntimeProfileFixture(t, ctx, "Orphaned Profile Cleanup", "codex", "orphaned-profile-codex")
	runtimeID := insertProfileRuntimeFixture(t, ctx, profileID, "Orphaned Profile Runtime", "codex")
	if _, err := testPool.Exec(ctx, `DELETE FROM runtime_profile WHERE id = $1`, profileID); err != nil {
		t.Fatalf("delete profile row: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("DELETE", "/api/workspaces/"+testWorkspaceID+"/runtime-profiles/"+profileID, nil)
	req = withURLParams(req, "id", testWorkspaceID, "profileId", profileID)
	testHandler.DeleteRuntimeProfile(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	var rtRows int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&rtRows); err != nil {
		t.Fatalf("count runtime rows: %v", err)
	}
	if rtRows != 0 {
		t.Fatalf("expected orphaned runtime row deleted, found %d", rtRows)
	}
}

func TestDeleteRuntimeProfile_MissingProfileNoOrphansStillReturns404(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	missingProfileID := "00000000-0000-0000-0000-000000415800"
	w := httptest.NewRecorder()
	req := newRequest("DELETE", "/api/workspaces/"+testWorkspaceID+"/runtime-profiles/"+missingProfileID, nil)
	req = withURLParams(req, "id", testWorkspaceID, "profileId", missingProfileID)
	testHandler.DeleteRuntimeProfile(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRuntimeProfileDeleteLockSerializesRegistration(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	profileID := insertRuntimeProfileFixture(
		t,
		ctx,
		"Profile Registration Lock",
		"codex",
		"profile-lock-codex",
	)
	params := db.LockRuntimeProfileForDeleteParams{
		ID:          parseUUID(profileID),
		WorkspaceID: parseUUID(testWorkspaceID),
	}

	deleteTx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin delete transaction: %v", err)
	}
	defer deleteTx.Rollback(ctx)
	if _, err := testHandler.Queries.WithTx(deleteTx).LockRuntimeProfileForDelete(ctx, params); err != nil {
		t.Fatalf("lock profile for delete: %v", err)
	}

	registrationTx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin registration transaction: %v", err)
	}
	defer registrationTx.Rollback(ctx)
	if _, err := registrationTx.Exec(ctx, `SET LOCAL lock_timeout = '50ms'`); err != nil {
		t.Fatalf("set lock timeout: %v", err)
	}
	_, err = testHandler.Queries.WithTx(registrationTx).LockRuntimeProfileForRegistration(
		ctx,
		db.LockRuntimeProfileForRegistrationParams(params),
	)
	if err == nil {
		t.Fatal("registration lock unexpectedly bypassed the profile delete lock")
	}
}

// TestCreateRuntimeProfile_ForcesWorkspaceVisibility is the regression guard
// for the visibility leak: visibility=private is not user-settable in v1
// because the read paths don't enforce it. A client that POSTs
// visibility:"private" must get a profile stored as 'workspace' — never
// private — so a "private" profile can't leak to other members or be
// registered by other daemons. Belt-and-suspenders: also assert the row in
// the DB is 'workspace'.
func TestCreateRuntimeProfile_ForcesWorkspaceVisibility(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/workspaces/"+testWorkspaceID+"/runtime-profiles", map[string]any{
		"display_name":    "Visibility Forced Profile",
		"protocol_family": "codex",
		"command_name":    "vis-forced-codex",
		"visibility":      "private", // must be ignored
	})
	req = withURLParam(req, "id", testWorkspaceID)
	testHandler.CreateRuntimeProfile(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp RuntimeProfileResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM runtime_profile WHERE id = $1`, resp.ID)
	})

	if resp.Visibility != "workspace" {
		t.Fatalf("response visibility = %q, want workspace (private must be forced to workspace)", resp.Visibility)
	}
	var dbVis string
	if err := testPool.QueryRow(ctx, `SELECT visibility FROM runtime_profile WHERE id = $1`, resp.ID).Scan(&dbVis); err != nil {
		t.Fatalf("read stored visibility: %v", err)
	}
	if dbVis != "workspace" {
		t.Fatalf("stored visibility = %q, want workspace", dbVis)
	}
}

func TestCreateRuntimeProfile_ValidatesCommandAndFixedArgs(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cases := []struct {
		name        string
		commandName string
		fixedArgs   []string
		wantStatus  int
	}{
		{
			name:        "split command and args accepted",
			commandName: "agent",
			fixedArgs:   []string{"--model", "composer-2.5"},
			wantStatus:  http.StatusCreated,
		},
		{
			name:        "command line rejected",
			commandName: "agent --model composer-2.5",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "nul arg rejected",
			commandName: "agent",
			fixedArgs:   []string{"bad\x00arg"},
			wantStatus:  http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := newRequest("POST", "/api/workspaces/"+testWorkspaceID+"/runtime-profiles", map[string]any{
				"display_name":    "Validation " + tc.name,
				"protocol_family": "codex",
				"command_name":    tc.commandName,
				"fixed_args":      tc.fixedArgs,
			})
			req = withURLParam(req, "id", testWorkspaceID)
			testHandler.CreateRuntimeProfile(w, req)
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.wantStatus, w.Body.String())
			}
			if w.Code == http.StatusCreated {
				var resp RuntimeProfileResponse
				if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				t.Cleanup(func() {
					testPool.Exec(context.Background(), `DELETE FROM runtime_profile WHERE id = $1`, resp.ID)
				})
				if got := strings.Join(resp.FixedArgs, " "); got != strings.Join(tc.fixedArgs, " ") {
					t.Fatalf("fixed_args = %v, want %v", resp.FixedArgs, tc.fixedArgs)
				}
			}
		})
	}
}

func TestCreateRuntimeProfile_RuntimeIdentity(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	for _, tc := range []struct {
		name, target, family string
		status               int
	}{
		{"runtime type", "opencode", "", http.StatusCreated},
		{"legacy family", "", "opencode", http.StatusCreated},
		{"mismatch", "opencode", "codex", http.StatusBadRequest},
		{"removed engine", "pi", "", http.StatusBadRequest},
		{"unknown", "unknown", "", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := withURLParam(newRequest("POST", "/", map[string]any{
				"display_name": "Identity " + tc.name, "runtime_type": tc.target,
				"protocol_family": tc.family, "command_name": "wrapper", "fixed_args": []string{"launch"},
			}), "id", testWorkspaceID)
			w := httptest.NewRecorder()
			testHandler.CreateRuntimeProfile(w, req)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if tc.status != http.StatusCreated {
				return
			}
			var resp RuntimeProfileResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM runtime_profile WHERE id = $1`, resp.ID) })
			want := tc.target
			if want == "" {
				want = tc.family
			}
			if resp.RuntimeType != want || resp.ProtocolFamily != "opencode" || resp.CommandName != "wrapper" || len(resp.FixedArgs) != 1 {
				t.Fatalf("incorrect identity: %+v", resp)
			}
		})
	}
}

func TestUpdateRuntimeProfile_RejectsIdentityChange(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	id := insertRuntimeProfileFixture(t, context.Background(), "Immutable OpenCode", "opencode", "wrapper")
	for _, field := range []string{"runtime_type", "protocol_family"} {
		// withURLParams, not two nested withURLParam calls: the singular helper
		// installs a fresh chi route context each time, so the outer call would
		// drop "id" and the handler would reject on workspace id before ever
		// reaching the immutability check this test exists for.
		req := withURLParams(newRequest("PATCH", "/", map[string]any{field: "codex"}), "id", testWorkspaceID, "profileId", id)
		w := httptest.NewRecorder()
		testHandler.UpdateRuntimeProfile(w, req)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "immutable") {
			t.Fatalf("identity update: %d %s", w.Code, w.Body.String())
		}
	}
}
