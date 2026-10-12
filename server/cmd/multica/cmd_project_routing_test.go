package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"
)

// DENE-1966: `multica project update <id> --routing on|off` writes the same
// routing_disabled field the project settings switch writes.
func TestProjectUpdateRoutingFlag(t *testing.T) {
	const projectID = "33333333-3333-3333-3333-333333333333"
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/projects/"+projectID {
			http.NotFound(w, r)
			return
		}
		got = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": projectID, "routing_disabled": got["routing_disabled"]})
	}))
	defer srv.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "11111111-1111-1111-1111-111111111111")

	newCmd := func(value string) *cobra.Command {
		cmd := &cobra.Command{}
		cmd.Flags().String("routing", "", "")
		cmd.Flags().String("output", "json", "")
		_ = cmd.Flags().Set("routing", value)
		return cmd
	}
	for value, want := range map[string]bool{"off": true, "on": false} {
		got = nil
		if err := runProjectUpdate(newCmd(value), []string{projectID}); err != nil {
			t.Fatalf("--routing %s: %v", value, err)
		}
		if got["routing_disabled"] != want || len(got) != 1 {
			t.Fatalf("--routing %s sent %v", value, got)
		}
	}
	got = nil
	if err := runProjectUpdate(newCmd("maybe"), []string{projectID}); err == nil || got != nil {
		t.Fatalf("--routing maybe must be refused before any write: err=%v body=%v", err, got)
	}
}
