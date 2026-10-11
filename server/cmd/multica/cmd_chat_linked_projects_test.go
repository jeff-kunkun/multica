package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

const referenceLink = "22222222-2222-4222-8222-222222222222"
const referenceProject = "33333333-3333-4333-8333-333333333333"
const referenceSession = "11111111-1111-4111-8111-111111111111"

func TestChatLinkedProjectsLifecycle(t *testing.T) {
	t.Chdir(t.TempDir())
	saved := []map[string]any{}
	live := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/workspace-links/projects":
			if r.Method != "GET" {
				t.Errorf("method: %s", r.Method)
			}
			json.NewEncoder(w).Encode(map[string]any{"projects": []map[string]any{{"link_id": referenceLink, "id": referenceProject, "title": "Shared", "source": map[string]string{"name": "Source"}}}})
		case "/api/chat/sessions/" + referenceSession:
			if r.Method == "PATCH" {
				var body map[string][]map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if len(body) != 1 || body["linked_projects"] == nil {
					t.Errorf("patch must contain only linked_projects array: %v", body)
				}
				saved = body["linked_projects"]
			} else if r.Method != "GET" {
				t.Errorf("method: %s", r.Method)
			}
			for _, row := range saved {
				row["title"] = "Shared"
				row["source_name"] = "Source"
				row["available"] = live
			}
			json.NewEncoder(w).Encode(map[string]any{"linked_projects": saved})
		default:
			http.Error(w, "unexpected path", 404)
		}
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_TOKEN", "mul_test")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_CHAT_SESSION_ID", referenceSession)
	run := func(args ...string) []map[string]any {
		t.Helper()
		cmd := newChatLinkedProjectsCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		var result struct {
			LinkedProjects []map[string]any `json:"linked_projects"`
		}
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatalf("%s: %v", out.String(), err)
		}
		t.Log(out.String())
		return result.LinkedProjects
	}
	rows := run("--available")
	if len(rows) != 1 || rows[0]["source_name"] != "Source" || rows[0]["project_id"] != referenceProject || rows[0]["available"] != true {
		t.Fatalf("options: %v", rows)
	}
	rows = run(referenceSession, "--reference", referenceLink+":"+referenceProject)
	if len(rows) != 1 || rows[0]["available"] != true {
		t.Fatalf("attach: %v", rows)
	}
	live = false
	rows = run()
	if len(rows) != 1 || rows[0]["available"] != false || rows[0]["unavailable_reason"] == "" {
		t.Fatalf("stale: %v", rows)
	}
	if rows = run("--clear"); len(rows) != 0 {
		t.Fatalf("clear: %v", rows)
	}
}

func TestChatLinkedProjectsInvalidInput(t *testing.T) {
	for _, args := range [][]string{{"--available", "--clear"}, {"--reference", "bad"}, {"--output", "yaml"}, {"--available", referenceSession}, {"--clear", "--reference", referenceLink + ":" + referenceProject}} {
		cmd := newChatLinkedProjectsCommand()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}
