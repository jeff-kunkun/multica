package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentSeriesRoundTrip(t *testing.T) {
	if testHandler == nil {
		t.Fatal("database required")
	}
	runtimeID := createClaudeProviderRuntime(t)
	w := httptest.NewRecorder()
	testHandler.CreateAgent(w, newRequest(http.MethodPost, "/api/agents", map[string]any{"name": "series-test", "runtime_id": runtimeID}))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var a AgentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), "DELETE FROM agent WHERE id = $1", a.ID) })
	if a.Series != "Claude" || a.SeriesSource != "auto" {
		t.Fatalf("new CLI default: %+v", a)
	}
	for _, value := range []string{"破甲系列", ""} {
		w = httptest.NewRecorder()
		testHandler.UpdateAgent(w, withURLParam(newRequest(http.MethodPut, "/api/agents/"+a.ID, map[string]any{"series": value}), "id", a.ID))
		if w.Code != http.StatusOK {
			t.Fatalf("update: %d %s", w.Code, w.Body.String())
		}
		var updated AgentResponse
		json.Unmarshal(w.Body.Bytes(), &updated)
		want, source := value, "manual"
		if value == "" {
			want, source = "Claude", "auto"
		}
		if updated.Series != want || updated.SeriesSource != source || updated.AutomaticSeries != "Claude" {
			t.Fatalf("unexpected series: %+v", updated)
		}
		t.Logf("series=%s source=%s automatic=%s", updated.Series, updated.SeriesSource, updated.AutomaticSeries)
	}
	for _, value := range []string{strings.Repeat("界", 65), "bad\nname"} {
		w = httptest.NewRecorder()
		testHandler.UpdateAgent(w, withURLParam(newRequest(http.MethodPut, "/api/agents/"+a.ID, map[string]any{"series": value}), "id", a.ID))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid accepted: %d %s", w.Code, w.Body.String())
		}
	}
}
