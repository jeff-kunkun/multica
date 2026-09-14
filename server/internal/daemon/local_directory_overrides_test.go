package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"log/slog"
)

func writeOverrideFile(t *testing.T, dir, daemonID, localPath string) string {
	t.Helper()
	path := filepath.Join(dir, localSharedOverridesFileName)
	raw, err := json.Marshal(localSharedOverrideFile{
		Overrides: []localSharedOverride{{
			DaemonID:      daemonID,
			LocalPath:     localPath,
			SkipPathMutex: true,
		}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestLocalSharedOverrideStoreHasPersistsAcrossReload(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const daemonID = "d-1"
	target := filepath.Join(dir, "pg-game")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := writeOverrideFile(t, dir, daemonID, target)

	first := newLocalSharedOverrideStore(path)
	if !first.Has(daemonID, target, target) {
		t.Fatal("fresh store did not see the on-disk override")
	}

	second := newLocalSharedOverrideStore(path)
	if !second.Has(daemonID, target, target) {
		t.Fatal("reopened store lost the override; it must survive daemon restart")
	}
	if second.Has("other-daemon", target, target) {
		t.Fatal("override leaked across daemon_id")
	}
}

func TestLocalSharedOverrideStoreReloadsAfterWrite(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const daemonID = "d-1"
	target := filepath.Join(dir, "umbrella")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, localSharedOverridesFileName)
	store := newLocalSharedOverrideStore(path)
	if store.Has(daemonID, target, target) {
		t.Fatal("empty store reported an override")
	}

	writeOverrideFile(t, dir, daemonID, target)
	// Some filesystems have 1s mtime granularity; the store keys on mtime+size.
	time.Sleep(10 * time.Millisecond)
	if !store.Has(daemonID, target, target) {
		t.Fatal("store did not pick up a file written after construction")
	}
}

func TestAcquireLocalDirectoryLockSkipsInPlaceWithLocalOverride(t *testing.T) {
	t.Parallel()

	const daemonID = "d-mine"
	dir := t.TempDir()
	target := filepath.Join(dir, "pg-game")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	overrides := writeOverrideFile(t, dir, daemonID, target)

	raw, err := json.Marshal(localDirectoryRef{
		LocalPath:     target,
		DaemonID:      daemonID,
		ExecutionMode: localDirectoryModeInPlace,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resources := []ProjectResourceData{
		{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: raw},
	}

	d := &Daemon{
		cfg:                  Config{DaemonID: daemonID},
		localPathLocks:       NewLocalPathLocker(),
		logger:               slog.Default(),
		localSharedOverrides: newLocalSharedOverrideStore(overrides),
	}

	assignment, err := d.resolveLocalDirectoryAssignment(Task{ID: "t1", ProjectResources: resources})
	if err != nil {
		t.Fatalf("assignment: %v", err)
	}
	if !assignment.IsShared() || !assignment.SkipsPathMutex() {
		t.Fatalf("IsShared()=%v SkipsPathMutex()=%v, want both true after local override",
			assignment.IsShared(), assignment.SkipsPathMutex())
	}
	if assignment.UsesWorktree() {
		t.Fatal("UsesWorktree() = true; override must not turn in_place into worktree")
	}
	if !assignment.RunsInUserDirectory() {
		t.Fatal("RunsInUserDirectory() = false; cwd must stay the user's directory")
	}

	for _, taskID := range []string{"task-a", "task-b"} {
		release, abort := d.acquireLocalDirectoryLockIfNeeded(
			context.Background(),
			Task{ID: taskID, IssueID: "issue-" + taskID, ProjectResources: resources},
			slog.Default(),
			nil,
		)
		if abort {
			t.Fatalf("%s: acquisition aborted", taskID)
		}
		if release != nil {
			t.Fatalf("%s: got a release callback, so the path mutex was taken", taskID)
		}
	}
	if got := d.localPathLocks.Holder(assignment.RealPath); got != "" {
		t.Fatalf("holder = %q, want empty: local override must not lock the path", got)
	}
}
