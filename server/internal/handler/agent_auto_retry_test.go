package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentAutoRetryEnabledGetAndUpdate(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	runtimeID := createClaudeProviderRuntime(t)
	agentID := createAgentOnRuntime(t, "auto-retry-switch-test", runtimeID, "")
	secret := "gateway-token-keep-me"

	if _, err := testPool.Exec(ctx, `
		UPDATE agent
		SET runtime_config = jsonb_build_object(
			'mode', 'gateway',
			'gateway', jsonb_build_object('host', 'gw.internal', 'token', $2::text)
		)
		WHERE id = $1
	`, agentID, secret); err != nil {
		t.Fatalf("seed gateway token: %v", err)
	}

	t.Run("get defaults to true and masks gateway token", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := withURLParam(newRequest(http.MethodGet, "/api/agents/"+agentID, nil), "id", agentID)
		testHandler.GetAgent(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp map[string]any
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode GET: %v", err)
		}
		if resp["auto_retry_enabled"] != true {
			t.Errorf("auto_retry_enabled = %v, want true", resp["auto_retry_enabled"])
		}
		rc, _ := resp["runtime_config"].(map[string]any)
		gw, _ := rc["gateway"].(map[string]any)
		if gw["token"] != runtimeConfigGatewayTokenMask {
			t.Errorf("GET token = %v, want masked", gw["token"])
		}
	})

	t.Run("put false then true without touching runtime_config", func(t *testing.T) {
		for _, enabled := range []bool{false, true} {
			w := httptest.NewRecorder()
			req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID, map[string]any{
				"auto_retry_enabled": enabled,
			}), "id", agentID)
			testHandler.UpdateAgent(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("PUT auto_retry_enabled=%v: expected 200, got %d: %s", enabled, w.Code, w.Body.String())
			}
			var resp map[string]any
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("decode PUT: %v", err)
			}
			if resp["auto_retry_enabled"] != enabled {
				t.Errorf("auto_retry_enabled = %v, want %v", resp["auto_retry_enabled"], enabled)
			}
			rc, _ := resp["runtime_config"].(map[string]any)
			gw, _ := rc["gateway"].(map[string]any)
			if gw["token"] != runtimeConfigGatewayTokenMask {
				t.Errorf("PUT response token = %v, want masked", gw["token"])
			}
		}

		var persisted string
		if err := testPool.QueryRow(ctx, `SELECT runtime_config #>> '{gateway,token}' FROM agent WHERE id = $1`, agentID).Scan(&persisted); err != nil {
			t.Fatalf("read persisted token: %v", err)
		}
		if persisted != secret {
			t.Errorf("persisted gateway token = %q, want original %q", persisted, secret)
		}
	})

	t.Run("omitted field leaves the switch alone", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID, map[string]any{
			"description": "name-only-adjacent",
		}), "id", agentID)
		testHandler.UpdateAgent(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("omitted switch: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp map[string]any
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode omitted PUT: %v", err)
		}
		if resp["auto_retry_enabled"] != true {
			t.Errorf("omitted auto_retry_enabled changed the switch: got %v", resp["auto_retry_enabled"])
		}
	})
}
