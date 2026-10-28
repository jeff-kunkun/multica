package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// --default-dir alone saves the setting and never starts a CLI update.
func TestRuntimeUpdateDefaultDirPatchesWithoutUpdating(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	var sent []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/runtimes/rt-1" || r.Method != http.MethodPatch {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		sent = append(sent, body)
		json.NewEncoder(w).Encode(map[string]any{"id": "rt-1"})
	}))
	defer srv.Close()

	for _, dir := range []string{" /Users/me/work ", ""} {
		cmd := newRuntimeDeleteTestCmd(srv.URL)
		cmd.Flags().String("target-version", "", "")
		cmd.Flags().String("default-dir", "", "")
		cmd.Flags().Set("default-dir", dir)
		if _, err := captureRuntimeStdout(t, func() error { return runRuntimeUpdate(cmd, []string{"rt-1"}) }); err != nil {
			t.Fatalf("%q: %v", dir, err)
		}
	}
	if len(sent) != 2 || sent[0]["default_work_dir"] != "/Users/me/work" || sent[1]["default_work_dir"] != "" {
		t.Fatalf("PATCH bodies = %v, want the trimmed path, then a clear", sent)
	}
	if len(sent[0]) != 1 {
		t.Fatalf("PATCH carried other fields: %v", sent[0])
	}

	cmd := newRuntimeDeleteTestCmd(srv.URL)
	cmd.Flags().String("target-version", "", "")
	cmd.Flags().String("default-dir", "", "")
	if err := runRuntimeUpdate(cmd, []string{"rt-1"}); err == nil {
		t.Fatal("neither flag given, want --target-version required")
	}
}
