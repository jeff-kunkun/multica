package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWorkspaceLinkViewerUpdateAndAuditHTTP(t *testing.T) {
	t.Chdir(t.TempDir())
	patched := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/workspace-links":
			_, _ = w.Write([]byte(`{"links":[{"id":"link-1","side":"viewer","source":{"slug":"source"}}]}`))
		case r.URL.Path == "/api/workspace-links/lookup":
			if r.URL.Query().Get("target") != "source" {
				t.Error("lookup did not target source")
			}
			_, _ = w.Write([]byte(`{"pull":{"allowed":true,"projects":[{"id":"source-project","title":"Roadmap"}]}}`))
		case r.Method == "PATCH":
			var body struct {
				ProjectIDs []string `json:"project_ids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body.ProjectIDs) != 1 || body.ProjectIDs[0] != "source-project" {
				t.Errorf("projects=%v", body.ProjectIDs)
			}
			patched = true
			_, _ = w.Write([]byte(`{"id":"link-1"}`))
		case r.URL.Path == "/api/workspace-links/audit":
			if r.URL.Query().Get("cursor") != "next+page" || r.URL.Query().Get("limit") != "2" {
				t.Errorf("query=%s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"entries":[{"peer_workspace_name":"source","route":"issue.update","object_label":"WL-1"}],"next_cursor":null}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			http.Error(w, "unexpected", 404)
		}
	}))
	defer srv.Close()
	clearAgentContext(t)
	t.Setenv("MULTICA_DAEMON_PORT", "")
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "viewer")
	t.Setenv("MULTICA_TOKEN", "test-token")
	c := workspaceLinkUpdateCmd
	_ = c.Flags().Set("project", "Roadmap")
	_ = c.Flags().Set("output", "json")
	defer c.Flags().Set("project", "")
	if err := runWorkspaceLinkUpdate(c, []string{"link-1"}); err != nil {
		t.Fatal(err)
	}
	if !patched {
		t.Fatal("no PATCH")
	}
	a := workspaceLinkAuditCmd
	_ = a.Flags().Set("cursor", "next+page")
	_ = a.Flags().Set("limit", "2")
	_ = a.Flags().Set("output", "json")
	defer a.Flags().Set("cursor", "")
	defer a.Flags().Set("limit", "50")
	if err := runWorkspaceLinkAudit(a, nil); err != nil {
		t.Fatal(err)
	}
}
