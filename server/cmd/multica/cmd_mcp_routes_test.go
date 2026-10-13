package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMCPRoutesUseSharedAPI(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, key := range []string{"MULTICA_TASK_ID", "MULTICA_AGENT_ID", "MULTICA_DAEMON_PORT"} {
		t.Setenv(key, "")
	}
	const project = "019ef39a-1488-7000-8000-000000000001"
	const bot = "019ef39a-1488-7000-8000-000000000002"
	for _, tc := range []struct {
		name          string
		args          []string
		method, query string
		project       any
	}{
		{"list", []string{"list"}, "GET", "", nil},
		{"project", []string{"set", "--project", project, "--bot", bot, "--channel-ref", "Multica-group-1"}, "PUT", "", project},
		{"fallback", []string{"set", "--bot", bot, "--channel-ref", "Multica-group-1"}, "PUT", "", nil},
		{"remove project", []string{"remove", "--project", project}, "DELETE", "project_id=" + project, project},
		{"remove fallback", []string{"remove"}, "DELETE", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count++
				if r.Method != tc.method || r.URL.Path != "/api/workspaces/ws-1/office-bot-routes" || r.URL.RawQuery != tc.query {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
				}
				switch r.Method {
				case "GET":
					fmt.Fprint(w, `[]`)
				case "DELETE":
					w.WriteHeader(204)
				case "PUT":
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if body["project_id"] != tc.project || body["bot_id"] != bot || body["channel_ref"] != "Multica-group-1" || len(body) != 3 {
						t.Errorf("unexpected route body %v", body)
					}
					_ = json.NewEncoder(w).Encode(body)
				}
			}))
			defer server.Close()
			t.Setenv("MULTICA_SERVER_URL", server.URL)
			t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
			t.Setenv("MULTICA_TOKEN", "test-pat")
			cmd := newMCPRoutesCommand()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(append(tc.args, "--output", "json"))
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("requests %d", count)
			}
			if !json.Valid(out.Bytes()) {
				t.Fatal("output is not JSON")
			}
		})
	}
}
func TestMCPRoutesRejectAmbiguousScope(t *testing.T) {
	for _, args := range [][]string{{"remove", "--project", ""}, {"remove", "--project", "unknown"}, {"set", "--bot", "bad", "--channel-ref", "group"}, {"set", "--bot", "019ef39a-1488-7000-8000-000000000002"}} {
		cmd := newMCPRoutesCommand()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("accepted incomplete route %v", args)
		}
	}
}
