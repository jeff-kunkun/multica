package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestDaemonWebSocketPATRevalidation(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	for _, mutation := range []struct{ name, sql string }{
		{"revoked", "UPDATE personal_access_token SET revoked = true WHERE id = $1"},
		{"expired", "UPDATE personal_access_token SET expires_at = now() - interval '1 second' WHERE id = $1"},
		{"deleted", "UPDATE personal_access_token SET deleted_at = now() WHERE id = $1"},
		{"rotated", "UPDATE personal_access_token SET token_hash = token_hash || '-rotated' WHERE id = $1"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			token := "mul_" + uuid.NewString()
			id := dbfx.Insert(t, "personal_access_token", testutil.Cols{"user_id": testUserID, "name": "WS revalidation", "token_hash": auth.HashToken(token), "token_prefix": token[:12]})
			req := newRequest(http.MethodGet, "/api/daemon/ws", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			identity, ok := testHandler.buildDaemonWebSocketIdentity(httptest.NewRecorder(), req, nil, testUserID)
			if !ok || identity.Revalidate == nil {
				t.Fatal("PAT connection has no credential revalidation")
			}
			if err := identity.Revalidate(context.Background()); err != nil {
				t.Fatalf("valid PAT: %v", err)
			}
			if _, err := testPool.Exec(context.Background(), mutation.sql, id); err != nil {
				t.Fatal(err)
			}
			if err := identity.Revalidate(context.Background()); !errors.Is(err, daemonws.ErrAuthExpired) {
				t.Fatalf("%s PAT still authorizes the old connection: %v", mutation.name, err)
			}
		})
	}
}

func TestDaemonWebSocketMissingHubIsInternalError(t *testing.T) {
	h := &Handler{}
	w := httptest.NewRecorder()
	h.DaemonWebSocket(w, httptest.NewRequest(http.MethodGet, "/api/daemon/ws", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["code"] != "daemon_websocket_misconfigured" {
		t.Fatalf("code = %q, want daemon_websocket_misconfigured", body["code"])
	}
}

func TestBuildDaemonWebSocketIdentitySeedsBatchRuntimeLeases(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	const daemonID = "lease-daemon"
	runtimeIDs := []string{
		dbfx.Runtime(t, "WS lease runtime 1", testutil.Cols{
			"workspace_id": testWorkspaceID,
			"daemon_id":    daemonID,
			"provider":     "ws-lease-1",
			"device_info":  "WS lease runtime 1",
		}),
		dbfx.Runtime(t, "WS lease runtime 2", testutil.Cols{
			"workspace_id": testWorkspaceID,
			"daemon_id":    daemonID,
			"provider":     "ws-lease-2",
			"device_info":  "WS lease runtime 2",
		}),
	}
	req := newDaemonTokenRequest(http.MethodGet, "/api/daemon/ws", nil, testWorkspaceID, daemonID)
	req.Header.Set("X-Client-Version", "0.9.0")
	w := httptest.NewRecorder()

	identity, ok := testHandler.buildDaemonWebSocketIdentity(w, req, runtimeIDs, "")
	if !ok {
		t.Fatalf("buildDaemonWebSocketIdentity rejected valid runtimes: %d %s", w.Code, w.Body.String())
	}
	if identity.DaemonID != daemonID || identity.WorkspaceID != testWorkspaceID {
		t.Fatalf("identity scope = daemon %q workspace %q", identity.DaemonID, identity.WorkspaceID)
	}
	if identity.ClientVersion != "0.9.0" {
		t.Fatalf("client version = %q, want 0.9.0", identity.ClientVersion)
	}
	if len(identity.RuntimeLeases) != len(runtimeIDs) {
		t.Fatalf("runtime leases = %d, want %d", len(identity.RuntimeLeases), len(runtimeIDs))
	}
	for _, runtimeID := range runtimeIDs {
		lease := identity.RuntimeLeases[runtimeID]
		if lease == nil {
			t.Fatalf("missing lease for runtime %s", runtimeID)
		}
		state := lease.Snapshot()
		if state.WorkspaceID != testWorkspaceID || state.Status != "online" || !state.LastSeenAtValid || state.Provider == "" {
			t.Fatalf("lease %s = %+v", runtimeID, state)
		}
		if time.Since(state.LastSeenAt) > time.Minute {
			t.Fatalf("lease %s last_seen_at unexpectedly stale: %s", runtimeID, state.LastSeenAt)
		}
	}
}

func TestBuildDaemonWebSocketIdentityFailsClosedForMissingRuntime(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	req := newOwnerDaemonRequest(http.MethodGet, "/api/daemon/ws", nil)
	w := httptest.NewRecorder()

	if _, ok := testHandler.buildDaemonWebSocketIdentity(w, req, []string{uuid.NewString()}, ""); ok {
		t.Fatal("missing runtime unexpectedly authorized")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
}

func TestBuildDaemonWebSocketIdentityRejectsWrongDaemon(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	runtimeID := dbfx.Runtime(t, "WS lease daemon scope", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"daemon_id":    "owner-daemon",
		"device_info":  "WS lease daemon scope",
	})
	req := newDaemonTokenRequest(http.MethodGet, "/api/daemon/ws", nil, testWorkspaceID, "other-daemon")
	w := httptest.NewRecorder()

	if _, ok := testHandler.buildDaemonWebSocketIdentity(w, req, []string{runtimeID}, ""); ok {
		t.Fatal("runtime owned by another daemon unexpectedly authorized")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
}

func TestBuildDaemonWebSocketIdentityRejectsCrossWorkspaceRuntime(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	workspaceID := dbfx.Insert(t, "workspace", testutil.Cols{
		"name":         "WS Lease Other Workspace",
		"slug":         "ws-lease-" + uuid.NewString(),
		"description":  "Cross-workspace lease test",
		"issue_prefix": "HWL",
	})
	runtimeID := dbfx.Runtime(t, "WS lease cross workspace", testutil.Cols{
		"workspace_id": workspaceID,
		"daemon_id":    "lease-daemon",
		"device_info":  "WS lease cross workspace",
	})
	req := newDaemonTokenRequest(http.MethodGet, "/api/daemon/ws", nil, testWorkspaceID, "lease-daemon")
	w := httptest.NewRecorder()

	if _, ok := testHandler.buildDaemonWebSocketIdentity(w, req, []string{runtimeID}, ""); ok {
		t.Fatal("cross-workspace runtime unexpectedly authorized")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
}
