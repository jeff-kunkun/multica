package handler

import (
	"context"
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBotWakeChainLimitSettingValidation(t *testing.T) {
	for _, raw := range []string{`{}`, `{"bot_wake_chain_limit":1}`, `{"bot_wake_chain_limit":4}`, `{"bot_wake_chain_limit":100}`} {
		var value any
		_ = json.Unmarshal([]byte(raw), &value)
		if err := validateBotWakeChainSettings(value); err != nil {
			t.Fatalf("%s rejected: %v", raw, err)
		}
	}
	for _, raw := range []string{`{"bot_wake_chain_limit":null}`, `{"bot_wake_chain_limit":0}`, `{"bot_wake_chain_limit":101}`, `{"bot_wake_chain_limit":4.5}`, `{"bot_wake_chain_limit":"4"}`, `{"bot_wake_chain_limit":true}`} {
		var value any
		_ = json.Unmarshal([]byte(raw), &value)
		if err := validateBotWakeChainSettings(value); err == nil {
			t.Fatalf("%s accepted", raw)
		}
	}
}

func TestTaskSettingsCannotRaiseOrRemoveBotWakeGuard(t *testing.T) {
	for _, incoming := range []map[string]any{
		{"bot_wake_chain_limit": float64(100), "theme": "dark"},
		{"theme": "dark"},
	} {
		got := keepStoredBotWakeChainLimit(incoming, []byte(`{"bot_wake_chain_limit":4}`)).(map[string]any)
		if got["bot_wake_chain_limit"] != float64(4) || got["theme"] != "dark" {
			t.Fatalf("task modified guard: %+v", got)
		}
	}
	got := keepStoredBotWakeChainLimit(map[string]any{"bot_wake_chain_limit": float64(100)}, []byte(`{}`)).(map[string]any)
	if _, present := got["bot_wake_chain_limit"]; present {
		t.Fatalf("task modified default guard: %+v", got)
	}
}

func TestWorkspaceRejectsInvalidPartialBotWakeLimitBeforeWriting(t *testing.T) {
	for _, raw := range []string{`null`, `0`, `101`, `4.5`, `"4"`, `true`} {
		r := newRequest("PATCH", "/workspaces/"+testWorkspaceID, map[string]any{})
		r.Body = io.NopCloser(strings.NewReader(`{"bot_wake_chain_limit":` + raw + `}`))
		route := chi.NewRouteContext()
		route.URLParams.Add("id", testWorkspaceID)
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
		w := httptest.NewRecorder()
		testHandler.UpdateWorkspace(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d body=%s", raw, w.Code, w.Body)
		}
	}
}
