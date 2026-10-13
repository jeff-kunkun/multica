package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/spf13/cobra"
)

const (
	testShareProjectID = "11111111-1111-1111-1111-111111111111"
	testShareUserID    = "22222222-2222-2222-2222-222222222222"
)

func newProjectShareTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "share"}
	cmd.Flags().String("workspace-id", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().String("output", "table", "")
	return cmd
}

// newProjectShareServer serves the project and member lists the CLI resolves
// against and records every write it receives.
func newProjectShareServer(t *testing.T) (*httptest.Server, *[]string, *map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var writes []string
	gotBody := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			json.NewEncoder(w).Encode(map[string]any{"projects": []map[string]any{{"id": testShareProjectID, "title": "Alpha"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/workspaces/ws-1/members":
			json.NewEncoder(w).Encode([]map[string]any{{"user_id": testShareUserID, "name": "Ada", "email": "ada@example.com"}})
		case r.Method == http.MethodPost || r.Method == http.MethodDelete:
			mu.Lock()
			writes = append(writes, r.Method+" "+r.URL.Path)
			mu.Unlock()
			if r.Method == http.MethodPost {
				json.NewDecoder(r.Body).Decode(&gotBody)
				w.WriteHeader(http.StatusCreated)
				w.Write([]byte(`{}`))
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	redirectTestHome(t, t.TempDir())
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	return srv, &writes, &gotBody
}

func TestRunProjectShareResolvesMemberAndPosts(t *testing.T) {
	_, writes, body := newProjectShareServer(t)

	if err := runProjectShare(newProjectShareTestCmd(), []string{testShareProjectID, "ada@example.com"}); err != nil {
		t.Fatalf("runProjectShare: %v", err)
	}
	if len(*writes) != 1 || (*writes)[0] != "POST /api/projects/"+testShareProjectID+"/members" {
		t.Fatalf("writes = %v", *writes)
	}
	if (*body)["user_id"] != testShareUserID {
		t.Fatalf("body = %v, want user_id %s", *body, testShareUserID)
	}
}

func TestRunProjectUnshareDeletesMember(t *testing.T) {
	_, writes, _ := newProjectShareServer(t)

	if err := runProjectUnshare(newProjectShareTestCmd(), []string{testShareProjectID, "Ada"}); err != nil {
		t.Fatalf("runProjectUnshare: %v", err)
	}
	want := "DELETE /api/projects/" + testShareProjectID + "/members/" + testShareUserID
	if len(*writes) != 1 || (*writes)[0] != want {
		t.Fatalf("writes = %v, want [%s]", *writes, want)
	}
}

func TestRunProjectShareRejectsUnknownMember(t *testing.T) {
	_, writes, _ := newProjectShareServer(t)

	if err := runProjectShare(newProjectShareTestCmd(), []string{testShareProjectID, "nobody"}); err == nil {
		t.Fatal("expected an error for an unknown member")
	}
	if len(*writes) != 0 {
		t.Fatalf("writes = %v, want none", *writes)
	}
}
