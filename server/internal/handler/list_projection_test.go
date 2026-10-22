package handler

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/listshape"
)

func TestListProjection(t *testing.T) {
	rows := []AgentResponse{{ID: "a", Name: "Agent", Instructions: strings.Repeat("private prompt", 10000), McpConfig: json.RawMessage(`null`), WorkEnabled: false}}
	for _, tc := range []struct {
		query string
		want  []string
		bad   bool
	}{
		{"?compact=true", []string{"id", "name", "work_enabled"}, false},
		{"?compact=true&fields=id,name", []string{"id", "name"}, false},
		{"?fields=id,custom_env", nil, true},
		{"?fields=id,,name", nil, true},
	} {
		t.Run(tc.query, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeProjectedList(w, httptest.NewRequest("GET", "/api/agents"+tc.query, nil), rows, listshape.Agent)
			if tc.bad {
				if w.Code != 400 {
					t.Fatalf("status %d: %s", w.Code, w.Body.String())
				}
				return
			}
			var got []map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			for _, name := range tc.want {
				if _, ok := got[0][name]; !ok {
					t.Fatalf("missing %s", name)
				}
			}
			if strings.Contains(w.Body.String(), "private prompt") {
				t.Fatal("full prompt leaked")
			}
		})
	}
	full, err := projectList(httptest.NewRequest("GET", "/api/agents", nil), rows, listshape.Agent)
	if err != nil {
		t.Fatal(err)
	}
	if got := full.([]AgentResponse)[0].Instructions; got != rows[0].Instructions {
		t.Fatal("default shape changed")
	}
	redacted, err := projectList(httptest.NewRequest("GET", "/api/agents?fields=mcp_config", nil), rows, listshape.Agent)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(redacted)
	if string(data) != `[{"mcp_config":null}]` {
		t.Fatalf("redaction lost: %s", data)
	}
}

func TestCompactFiftyAgentsUnderThirtyKB(t *testing.T) {
	rows := make([]AgentResponse, 50)
	for i := range rows {
		rows[i] = AgentResponse{ID: fmt.Sprintf("00000000-0000-0000-0000-%012d", i), Name: "特兰克斯 · 中档智能体", Status: "idle", RuntimeID: "11111111-1111-1111-1111-111111111111", RuntimeMode: "codex", WorkEnabled: true, Instructions: strings.Repeat("长提示", 10000)}
	}
	w := httptest.NewRecorder()
	writeProjectedList(w, httptest.NewRequest("GET", "/api/agents?compact=true", nil), rows, listshape.Agent)
	if w.Code != 200 || w.Body.Len() > 30*1024 {
		t.Fatalf("status %d, bytes %d", w.Code, w.Body.Len())
	}
	t.Logf("50 agents: %d bytes", w.Body.Len())
}

func TestChatRuntimeAndProjectFields(t *testing.T) {
	chat, err := projectList(httptest.NewRequest("GET", "/?compact=true", nil), []ChatDirectoryItem{{ID: "c", Title: "Chat", Summary: "large message", Originator: "Member"}}, listshape.Chat)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(chat)
	if strings.Contains(string(data), "summary") || strings.Contains(string(data), "originator") {
		t.Fatalf("chat: %s", data)
	}
	runtime, err := projectList(httptest.NewRequest("GET", "/?compact=true", nil), []AgentRuntimeResponse{{ID: "r", Name: "Runtime", Metadata: map[string]any{"large": "config"}}}, listshape.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(runtime)
	if strings.Contains(string(data), "metadata") {
		t.Fatalf("runtime: %s", data)
	}
	project, err := projectList(httptest.NewRequest("GET", "/?fields=id,title,status", nil), []ProjectResponse{{ID: "p", Title: "Project"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(project)
	if strings.Contains(string(data), "description") {
		t.Fatalf("project: %s", data)
	}
	_, err = projectList(httptest.NewRequest("GET", "/?fields=not_a_field", nil), []ChatDirectoryItem{}, listshape.Chat)
	if err == nil {
		t.Fatal("empty result must still validate fields")
	}
}
