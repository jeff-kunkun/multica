package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func qwenDeclaredCatalog() []ModelEntry {
	return []ModelEntry{
		{ID: "nexus-coder", Default: true, Thinking: &ModelThinking{
			SupportedLevels: []ThinkingLevel{{Value: "low"}, {Value: "medium"}, {Value: "xhigh"}, {Value: "none"}},
			Source:          "~/.qwen/settings.json · modelProviders.openai[nexus-coder].capabilities.reasoning",
		}},
		{ID: "nexus-image"},
	}
}

// TestDeclaredThinkingRejection pins DENE-1950: a Qwen level is accepted only
// when the catalog the runtime reported declares it for the model the agent
// will run. No catalog, an undeclared model or an undeclared level are refused
// with a reason, never stored to be dropped at run time.
func TestDeclaredThinkingRejection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	runtimeID := parseUUID("22222222-2222-2222-2222-222222222222")
	cache := NewInMemoryModelCatalogCache()
	if err := cache.Put(ctx, uuidToString(runtimeID), qwenDeclaredCatalog(), nil, true); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := &Handler{ModelCatalogCache: cache}
	empty := &Handler{ModelCatalogCache: NewInMemoryModelCatalogCache()}

	for _, tc := range []struct {
		name, model, level, want string
		h                        *Handler
	}{
		{name: "declared level", h: h, model: "nexus-coder", level: "low"},
		{name: "off is declared", h: h, model: "nexus-coder", level: "none"},
		{name: "empty model uses the default entry", h: h, model: "", level: "xhigh"},
		{name: "clear needs no catalog", h: empty, model: "nexus-coder", level: ""},
		{name: "undeclared level", h: h, model: "nexus-coder", level: "high", want: "declared: low, medium, xhigh, none"},
		{name: "model declares nothing", h: h, model: "nexus-image", level: "low", want: "declares no thinking levels"},
		{name: "model not in catalog", h: h, model: "gone", level: "low", want: "not in the catalog"},
		{name: "offline or never reported", h: empty, model: "nexus-coder", level: "low", want: "has not reported its models yet, or is offline"},
	} {
		got := tc.h.declaredThinkingRejection(ctx, "qwen", runtimeID, tc.model, tc.level)
		if tc.want == "" && got != "" {
			t.Errorf("%s: rejected with %q", tc.name, got)
		}
		if tc.want != "" && !strings.Contains(got, tc.want) {
			t.Errorf("%s: got %q, want it to mention %q", tc.name, got, tc.want)
		}
	}
	if got := empty.declaredThinkingRejection(ctx, "codex", runtimeID, "gpt-5.5", "high"); got != "" {
		t.Errorf("other dynamic providers keep their daemon-side check, got %q", got)
	}
}

func TestUpdateAgent_QwenDeclaredThinkingLevel(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createProviderRuntime(t, "qwen")

	patch := func(t *testing.T, agentID string, body map[string]any) (int, string) {
		t.Helper()
		w := httptest.NewRecorder()
		testHandler.UpdateAgent(w, withURLParam(newRequest(http.MethodPatch, "/api/agents/"+agentID, body), "id", agentID))
		var resp map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if w.Code != http.StatusOK {
			msg, _ := resp["error"].(string)
			return w.Code, msg
		}
		level, _ := resp["thinking_level"].(string)
		return w.Code, level
	}

	created := httptest.NewRecorder()
	testHandler.CreateAgent(created, newRequest(http.MethodPost, "/api/agents", map[string]any{
		"name": "qwen-declared-thinking", "runtime_id": runtimeID, "visibility": "private",
		"max_concurrent_tasks": 1, "model": "nexus-coder",
	}))
	if created.Code != http.StatusCreated {
		t.Fatalf("seed agent: %d %s", created.Code, created.Body.String())
	}
	var seeded map[string]any
	_ = json.NewDecoder(created.Body).Decode(&seeded)
	agentID, _ := seeded["id"].(string)
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent WHERE id = $1`, agentID) })

	// Before the runtime reports its models the level cannot be confirmed.
	if code, msg := patch(t, agentID, map[string]any{"thinking_level": "low"}); code != http.StatusBadRequest || !strings.Contains(msg, "has not reported its models yet") {
		t.Fatalf("undiscovered: %d %q", code, msg)
	}

	if err := testHandler.ModelCatalogCache.Put(ctx, runtimeID, qwenDeclaredCatalog(), nil, true); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	t.Cleanup(func() { _ = testHandler.ModelCatalogCache.Invalidate(ctx, runtimeID) })

	for _, level := range []string{"low", "medium", "xhigh", "none"} {
		if code, got := patch(t, agentID, map[string]any{"thinking_level": level}); code != http.StatusOK || got != level {
			t.Fatalf("save %s: %d %q", level, code, got)
		}
	}
	if code, msg := patch(t, agentID, map[string]any{"thinking_level": "high"}); code != http.StatusBadRequest || !strings.Contains(msg, "not declared") {
		t.Fatalf("undeclared high: %d %q", code, msg)
	}
	// Switching to a model that declares nothing must not keep the level.
	if code, msg := patch(t, agentID, map[string]any{"model": "nexus-image"}); code != http.StatusBadRequest || !strings.Contains(msg, `thinking_level=""`) {
		t.Fatalf("model switch carrying none: %d %q", code, msg)
	}
	// The client's model-change cleanup sends both; that is accepted.
	if code, got := patch(t, agentID, map[string]any{"model": "nexus-image", "thinking_level": ""}); code != http.StatusOK || got != "" {
		t.Fatalf("model switch with clear: %d %q", code, got)
	}
}
