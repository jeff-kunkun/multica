package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const magpieTestWorkspaceID = "55555555-5555-5555-5555-555555555555"

func newWorkspaceMagpieTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "magpie"}
	cmd.Flags().String("set", "", "")
	cmd.Flags().String("output", "table", "")
	return cmd
}

func TestRunWorkspaceMagpieSetOnPutsSwitch(t *testing.T) {
	var gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspaces/"+magpieTestWorkspaceID+"/magpie" {
			http.NotFound(w, r)
			return
		}
		gotMethod = r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(map[string]any{"enabled": true})
	}))
	defer srv.Close()

	redirectTestHome(t, t.TempDir())
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_TOKEN", "test-token")

	cmd := newWorkspaceMagpieTestCmd()
	_ = cmd.Flags().Set("set", "on")
	_ = cmd.Flags().Set("output", "json")
	out, err := captureStdout(t, func() error {
		return runWorkspaceMagpie(cmd, []string{magpieTestWorkspaceID})
	})
	if err != nil {
		t.Fatalf("runWorkspaceMagpie: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Fatalf("method = %s, want PUT", gotMethod)
	}
	if gotBody["enabled"] != true {
		t.Fatalf("body = %v, want enabled=true", gotBody)
	}
	if !strings.Contains(out, `"enabled": true`) {
		t.Fatalf("output %q does not report enabled", out)
	}
}

func TestRunWorkspaceMagpieShowsSwitch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/workspaces/"+magpieTestWorkspaceID+"/magpie" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"enabled": false})
	}))
	defer srv.Close()

	redirectTestHome(t, t.TempDir())
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_TOKEN", "test-token")

	out, err := captureStdout(t, func() error {
		return runWorkspaceMagpie(newWorkspaceMagpieTestCmd(), []string{magpieTestWorkspaceID})
	})
	if err != nil {
		t.Fatalf("runWorkspaceMagpie: %v", err)
	}
	if !strings.Contains(out, "MAGPIE") || !strings.Contains(out, "off") {
		t.Fatalf("table output %q does not show the switch as off", out)
	}
}

func TestRunWorkspaceMagpieRejectsBadSet(t *testing.T) {
	cmd := newWorkspaceMagpieTestCmd()
	_ = cmd.Flags().Set("set", "yes")
	err := runWorkspaceMagpie(cmd, []string{magpieTestWorkspaceID})
	if err == nil || !strings.Contains(err.Error(), "on or off") {
		t.Fatalf("err = %v, want --set must be on or off", err)
	}
}
