package execenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureWorkspacesRootMarker covers the root-level daemon marker that
// protects the whole workspaces tree. Regression for the confirmed escape
// where a sandboxed subprocess lost every MULTICA_* env var and ran
// `multica` from the workdir's *parent* directory: the per-workdir marker
// sits below cwd, so the CLI's upward walk found no daemon signal and fell
// back to the user's config PAT, misattributing agent writes to a member.
func TestEnsureWorkspacesRootMarker(t *testing.T) {
	t.Run("writes a valid marker into an empty root", func(t *testing.T) {
		root := t.TempDir()
		if err := EnsureWorkspacesRootMarker(root); err != nil {
			t.Fatalf("EnsureWorkspacesRootMarker: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(root, TaskContextMarkerRelPath))
		if err != nil {
			t.Fatalf("read root marker: %v", err)
		}
		var marker struct {
			ManagedBy string `json:"managed_by"`
		}
		if err := json.Unmarshal(data, &marker); err != nil {
			t.Fatalf("unmarshal root marker: %v\n%s", err, string(data))
		}
		if marker.ManagedBy != TaskContextMarkerManagedBy {
			t.Fatalf("managed_by = %q, want %q", marker.ManagedBy, TaskContextMarkerManagedBy)
		}
	})

	t.Run("is idempotent when a matching marker exists", func(t *testing.T) {
		root := t.TempDir()
		if err := EnsureWorkspacesRootMarker(root); err != nil {
			t.Fatalf("first EnsureWorkspacesRootMarker: %v", err)
		}
		if err := EnsureWorkspacesRootMarker(root); err != nil {
			t.Fatalf("second EnsureWorkspacesRootMarker: %v", err)
		}
	})

	t.Run("refuses to overwrite a foreign file", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, TaskContextMarkerRelPath)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		foreign := []byte(`{"managed_by":"someone-else"}`)
		if err := os.WriteFile(path, foreign, 0o644); err != nil {
			t.Fatalf("write foreign file: %v", err)
		}
		if err := EnsureWorkspacesRootMarker(root); err == nil {
			t.Fatal("expected error for foreign file at marker path, got nil")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("re-read foreign file: %v", err)
		}
		if string(data) != string(foreign) {
			t.Fatalf("foreign file was clobbered: %s", string(data))
		}
	})

	t.Run("self-heals a corrupt marker from a torn write", func(t *testing.T) {
		// A daemon killed mid os.WriteFile leaves an empty or truncated marker.
		// The old code treated any unparseable file like a foreign one and
		// refused it forever, leaving the whole node without a root guard. The
		// unparseable content is our own torn write, so it must be reclaimed.
		for _, corrupt := range []string{"", "{", "not-json", `{"managed_by":`} {
			root := t.TempDir()
			path := filepath.Join(root, TaskContextMarkerRelPath)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(path, []byte(corrupt), 0o644); err != nil {
				t.Fatalf("seed corrupt marker %q: %v", corrupt, err)
			}
			if err := EnsureWorkspacesRootMarker(root); err != nil {
				t.Fatalf("reclaim corrupt marker %q: unexpected error %v", corrupt, err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read reclaimed marker: %v", err)
			}
			var marker struct {
				ManagedBy string `json:"managed_by"`
			}
			if err := json.Unmarshal(data, &marker); err != nil {
				t.Fatalf("reclaimed marker not valid JSON for input %q: %v\n%s", corrupt, err, string(data))
			}
			if marker.ManagedBy != TaskContextMarkerManagedBy {
				t.Fatalf("managed_by = %q, want %q (input %q)", marker.ManagedBy, TaskContextMarkerManagedBy, corrupt)
			}
		}
	})

	t.Run("rejects empty root", func(t *testing.T) {
		if err := EnsureWorkspacesRootMarker(""); err == nil {
			t.Fatal("expected error for empty workspaces root, got nil")
		}
	})
}

// TestPrepare_WritesWorkspacesRootMarker verifies Prepare self-heals the
// root-level marker on every task start, so a marker deleted while the
// daemon is running is restored before the next agent spawns.
func TestPrepare_WritesWorkspacesRootMarker(t *testing.T) {
	root := t.TempDir()
	env, err := Prepare(PrepareParams{
		WorkspacesRoot: root,
		WorkspaceID:    "ws-test-001",
		TaskID:         "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
		AgentName:      "Test Agent",
		Task: TaskContextForEnv{
			IssueID: "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
		},
	}, testLogger())
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	defer env.Cleanup(true)

	data, err := os.ReadFile(filepath.Join(root, TaskContextMarkerRelPath))
	if err != nil {
		t.Fatalf("read root marker after Prepare: %v", err)
	}
	if !strings.Contains(string(data), TaskContextMarkerManagedBy) {
		t.Fatalf("root marker missing managed_by discriminator: %s", string(data))
	}
}

// TestReuse_SelfHealsWorkspacesRootMarker verifies the root marker is restored
// on the reuse path too: a marker deleted while the daemon runs must be back
// before a reused task spawns, not only after the next fresh Prepare.
func TestReuse_SelfHealsWorkspacesRootMarker(t *testing.T) {
	root := t.TempDir()
	env, err := Prepare(PrepareParams{
		WorkspacesRoot: root,
		WorkspaceID:    "ws-reuse-001",
		TaskID:         "b2c3d4e5-f6a7-8901-bcde-f23456789012",
		AgentName:      "Reuse Agent",
		Task:           TaskContextForEnv{IssueID: "b2c3d4e5-f6a7-8901-bcde-f23456789012"},
	}, testLogger())
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	defer env.Cleanup(true)

	rootMarker := filepath.Join(root, TaskContextMarkerRelPath)
	if err := os.Remove(rootMarker); err != nil {
		t.Fatalf("remove root marker: %v", err)
	}

	reused := Reuse(ReuseParams{
		WorkspacesRoot: root,
		WorkDir:        env.WorkDir,
		Task:           TaskContextForEnv{IssueID: "b2c3d4e5-f6a7-8901-bcde-f23456789012"},
	}, testLogger())
	if reused == nil {
		t.Fatal("Reuse returned nil for an existing workdir")
	}
	if _, err := os.Stat(rootMarker); err != nil {
		t.Fatalf("Reuse did not restore the root marker: %v", err)
	}
}

// TestWriteTaskContextMarkerPreExisting covers the decision table for a
// pre-existing file at the reserved per-workdir marker path (DENE-2142): a
// daemon-owned marker — whatever agent or issue it names, which subsumes
// same-issue retry, reassignment, and stale takeover — is overwritten
// atomically, torn bytes from a crashed write are reclaimed, and only a
// parseable file NOT owned by the daemon is refused, with the occupant's
// identity in the error instead of a bare refuse line.
func TestWriteTaskContextMarkerPreExisting(t *testing.T) {
	seed := func(t *testing.T, content string) (workDir, markerPath string) {
		t.Helper()
		workDir = t.TempDir()
		markerPath = filepath.Join(workDir, TaskContextMarkerRelPath)
		if err := os.MkdirAll(filepath.Dir(markerPath), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if content == "" {
			return workDir, markerPath
		}
		if err := os.WriteFile(markerPath, []byte(content), 0o644); err != nil {
			t.Fatalf("seed marker: %v", err)
		}
		return workDir, markerPath
	}
	next := TaskContextForEnv{AgentID: "agent-new", IssueID: "issue-new"}

	t.Run("overwrites a daemon-owned marker from another agent and issue", func(t *testing.T) {
		workDir, markerPath := seed(t, `{"managed_by":"multica-daemon-task","agent_id":"agent-old","issue_id":"issue-old"}`)
		if err := writeTaskContextMarker(workDir, next, nil); err != nil {
			t.Fatalf("writeTaskContextMarker: %v", err)
		}
		data, err := os.ReadFile(markerPath)
		if err != nil {
			t.Fatalf("read marker: %v", err)
		}
		var marker taskContextMarkerFile
		if err := json.Unmarshal(data, &marker); err != nil {
			t.Fatalf("marker not valid JSON after takeover: %v\n%s", err, string(data))
		}
		if marker.AgentID != next.AgentID || marker.IssueID != next.IssueID {
			t.Fatalf("marker = %+v, want agent %q issue %q", marker, next.AgentID, next.IssueID)
		}
	})

	t.Run("refuses torn bytes by default (old fail-closed behaviour)", func(t *testing.T) {
		for _, torn := range []string{"{", "not-json", `{"managed_by":"multica-daemon-task","agent_id":`} {
			workDir, markerPath := seed(t, torn)
			if err := writeTaskContextMarker(workDir, next, nil); err == nil {
				t.Fatalf("torn marker %q must be refused while MarkerSelfHeal is off", torn)
			}
			data, err := os.ReadFile(markerPath)
			if err != nil {
				t.Fatalf("read torn marker: %v", err)
			}
			if string(data) != torn {
				t.Fatalf("torn marker %q was rewritten to %q with MarkerSelfHeal off", torn, string(data))
			}
		}
	})

	t.Run("reclaims torn bytes from a crashed write when self-heal is on", func(t *testing.T) {
		heal := next
		heal.MarkerSelfHeal = true
		for _, torn := range []string{"", "{", "not-json", `{"managed_by":"multica-daemon-task","agent_id":`} {
			workDir, markerPath := seed(t, torn)
			if err := writeTaskContextMarker(workDir, heal, nil); err != nil {
				t.Fatalf("reclaim torn marker %q: unexpected error %v", torn, err)
			}
			data, err := os.ReadFile(markerPath)
			if err != nil {
				t.Fatalf("read reclaimed marker: %v", err)
			}
			var marker taskContextMarkerFile
			if err := json.Unmarshal(data, &marker); err != nil {
				t.Fatalf("reclaimed marker not valid JSON for input %q: %v\n%s", torn, err, string(data))
			}
			if marker.ManagedBy != TaskContextMarkerManagedBy {
				t.Fatalf("managed_by = %q, want %q (input %q)", marker.ManagedBy, TaskContextMarkerManagedBy, torn)
			}
		}
	})

	t.Run("refuses a parseable foreign file and names the occupant", func(t *testing.T) {
		foreign := `{"managed_by":"someone-else","agent_id":"agent-foreign","issue_id":"issue-foreign"}`
		workDir, markerPath := seed(t, foreign)
		err := writeTaskContextMarker(workDir, next, nil)
		if err == nil {
			t.Fatal("expected error for foreign file at marker path, got nil")
		}
		for _, want := range []string{"someone-else", "agent-foreign", "issue-foreign", markerPath} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not name occupant detail %q", err, want)
			}
		}
		data, readErr := os.ReadFile(markerPath)
		if readErr != nil {
			t.Fatalf("re-read foreign file: %v", readErr)
		}
		if string(data) != foreign {
			t.Fatalf("foreign file was clobbered: %s", string(data))
		}
	})

	t.Run("records the takeover in the sidecar manifest for end-of-task cleanup", func(t *testing.T) {
		workDir, markerPath := seed(t, `{"managed_by":"multica-daemon-task","agent_id":"agent-old","issue_id":"issue-old"}`)
		manifest := &sidecarManifest{}
		if err := writeTaskContextMarker(workDir, next, manifest); err != nil {
			t.Fatalf("writeTaskContextMarker: %v", err)
		}
		found := false
		for _, f := range manifest.Files {
			if f == markerPath {
				found = true
			}
		}
		if !found {
			t.Fatalf("refreshed marker not recorded in manifest: %+v", manifest.Files)
		}
	})

	t.Run("write leaves no torn bytes for a concurrent reader", func(t *testing.T) {
		// The atomic-write contract: after writeTaskContextMarker returns,
		// every byte visible at the path parses. A truncate-then-write here
		// (the pre-fix behavior) could be observed mid-flight by another
		// task's prepare on a shared directory and refused the whole prepare.
		workDir, markerPath := seed(t, "")
		if err := writeTaskContextMarker(workDir, next, nil); err != nil {
			t.Fatalf("writeTaskContextMarker: %v", err)
		}
		data, err := os.ReadFile(markerPath)
		if err != nil {
			t.Fatalf("read marker: %v", err)
		}
		var marker taskContextMarkerFile
		if err := json.Unmarshal(data, &marker); err != nil {
			t.Fatalf("marker not valid JSON: %v\n%s", err, string(data))
		}
		if marker.ManagedBy != TaskContextMarkerManagedBy {
			t.Fatalf("managed_by = %q, want %q", marker.ManagedBy, TaskContextMarkerManagedBy)
		}
	})
}
