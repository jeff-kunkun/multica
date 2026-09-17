package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// A target that carries /transfer/* but predates the V3 bundle reader refuses
// a schema_version 2 bundle with its own wording — "unsupported transfer
// schema_version" — which names neither the version it got nor the upgrade
// that fixes it. The CLI is the only side running new code in that pairing, so
// it is where the sentence has to be repaired (DENE-240).
func TestTransferImport_OutdatedTargetRejectsV3Bundle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.URL.Path == "/api/workspaces":
			writeJSONTest(w, []map[string]any{{"id": "ws-1", "slug": "tgt", "name": "Tgt"}})
		case req.URL.Path == "/api/workspaces/ws-1":
			writeJSONTest(w, map[string]any{"id": "ws-1", "slug": "tgt", "name": "Tgt"})
		case strings.HasSuffix(req.URL.Path, "/transfer/config"):
			_, _ = io.ReadAll(req.Body)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"unsupported transfer schema_version","code":"transfer_bundle_version_unsupported"}`))
		default:
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "v3.zip")
	writeTransferZipFiles(t, path, func(add func(string, any)) {
		add("manifest.json", map[string]any{"format": "multica.workspace-transfer", "schema_version": 2, "bundle_id": "b-1"})
		add("config.json", map[string]any{"format": "multica.workspace-config", "schema_version": 1, "bundle_id": "c-1", "entities": map[string]any{}})
	})

	cmd := newTransferImportTestCmd()
	_ = cmd.Flags().Set("server-url", srv.URL)
	_ = cmd.Flags().Set("workspace", "tgt")
	_ = cmd.Flags().Set("in", path)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader(""))

	err := cmd.Execute()
	if err == nil {
		t.Fatal("import against an outdated target succeeded, want a refusal")
	}
	msg := err.Error()
	for _, want := range []string{"target_outdated", "schema_version 2", "upgrade the target"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not contain %q — the user cannot tell the target is the thing to fix", msg, want)
		}
	}
}

// A 404 on /transfer/config still reports target_unsupported: that target has
// no transfer routes at all, and upgrading a bundle group would not help.
func TestTransferImport_TargetWithoutTransferRoutes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/api/workspaces":
			writeJSONTest(w, []map[string]any{{"id": "ws-1", "slug": "tgt", "name": "Tgt"}})
		case "/api/workspaces/ws-1":
			writeJSONTest(w, map[string]any{"id": "ws-1", "slug": "tgt", "name": "Tgt"})
		default:
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "v2.zip")
	writeTransferZipFiles(t, path, func(add func(string, any)) {
		add("manifest.json", map[string]any{"format": "multica.workspace-transfer", "schema_version": 1, "bundle_id": "b-1"})
		add("config.json", map[string]any{"format": "multica.workspace-config", "schema_version": 1, "bundle_id": "c-1", "entities": map[string]any{}})
	})

	cmd := newTransferImportTestCmd()
	_ = cmd.Flags().Set("server-url", srv.URL)
	_ = cmd.Flags().Set("workspace", "tgt")
	_ = cmd.Flags().Set("in", path)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader(""))

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "target_unsupported") {
		t.Fatalf("import error = %v, want target_unsupported", err)
	}
}
