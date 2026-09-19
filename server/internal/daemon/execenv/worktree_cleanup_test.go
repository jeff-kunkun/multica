package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The cleanup policy is a table of reasons to KEEP. These tests are the
// canonical statement of it (DENE-617 invariants 6-9); the scan and the daemon
// endpoint above it only supply the measurements.

func TestEvaluateWorktreeCleanupKeepsUnlessEveryConditionHolds(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	settings := WorktreeCleanupSettings{Enabled: true, MinAgeDays: 14}
	// The qualifying shape: ours, idle, clean, merged, and long finished.
	eligible := WorktreeCandidate{
		Multica:   true,
		Merged:    true,
		LastRunAt: now.Add(-30 * 24 * time.Hour),
	}

	if got := EvaluateWorktreeCleanup(eligible, settings, now); got != "" {
		t.Fatalf("a copy satisfying every condition was kept because %q", got)
	}

	tests := []struct {
		name  string
		mutit func(*WorktreeCandidate)
		want  WorktreeKeepReason
	}{
		{
			// Invariant 8: a worktree the user made with `git worktree add`
			// carries no ownership record, and is never Multica's to delete.
			name:  "a worktree Multica did not create",
			mutit: func(c *WorktreeCandidate) { c.Multica = false },
			want:  KeepNotMulticaCreated,
		},
		{
			name:  "a task is running in it",
			mutit: func(c *WorktreeCandidate) { c.InUse = true },
			want:  KeepInUse,
		},
		{
			// Invariant 7. Note this case is merged AND long finished: the two
			// conditions that would otherwise qualify it are both satisfied,
			// and the uncommitted work still wins.
			name:  "uncommitted changes outrank merged and aged out",
			mutit: func(c *WorktreeCandidate) { c.Dirty = true },
			want:  KeepUncommittedChanges,
		},
		{
			name:  "the branch is not in trunk",
			mutit: func(c *WorktreeCandidate) { c.Merged = false },
			want:  KeepBranchNotMerged,
		},
		{
			name:  "its last run was recent",
			mutit: func(c *WorktreeCandidate) { c.LastRunAt = now.Add(-2 * 24 * time.Hour) },
			want:  KeepTooRecent,
		},
		{
			// "We could not measure it" is not permission to delete it.
			name:  "git could not be asked",
			mutit: func(c *WorktreeCandidate) { c.Unknown = true },
			want:  KeepStatusUnknown,
		},
		{
			// A copy with no recorded run is not infinitely old.
			name:  "no last run recorded",
			mutit: func(c *WorktreeCandidate) { c.LastRunAt = time.Time{} },
			want:  KeepTooRecent,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := eligible
			tc.mutit(&c)
			if got := EvaluateWorktreeCleanup(c, settings, now); got != tc.want {
				t.Fatalf("keep reason = %q, want %q", got, tc.want)
			}
		})
	}
}

// Invariant 7 again, from the other side: age never overrides dirt. A copy
// with uncommitted work is kept at any age, including a year later.
func TestUncommittedWorkIsKeptAtAnyAge(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	c := WorktreeCandidate{Multica: true, Merged: true, Dirty: true, LastRunAt: now.Add(-365 * 24 * time.Hour)}
	if got := EvaluateWorktreeCleanup(c, WorktreeCleanupSettings{Enabled: true, MinAgeDays: 1}, now); got != KeepUncommittedChanges {
		t.Fatalf("keep reason = %q, want %q", got, KeepUncommittedChanges)
	}
}

// An unset retention window must read as the default, not as "delete
// immediately": a zero value that means the most destructive policy available
// is a bug waiting for a truncated settings file.
func TestZeroMinAgeDaysFallsBackToTheDefault(t *testing.T) {
	if got := (WorktreeCleanupSettings{}).MinAge(); got != DefaultWorktreeCleanupMinAgeDays*24*time.Hour {
		t.Fatalf("MinAge() = %v, want the %d-day default", got, DefaultWorktreeCleanupMinAgeDays)
	}
	if got := (WorktreeCleanupSettings{MinAgeDays: -3}).MinAge(); got != DefaultWorktreeCleanupMinAgeDays*24*time.Hour {
		t.Fatalf("MinAge() with a negative setting = %v, want the default", got)
	}
}

// Invariant 6: with cleanup switched off the verdicts are still computed, so
// the settings screen can show what WOULD be removed before the user agrees to
// it. Eligible() answers the policy question; Enabled answers whether anything
// acts on it, and they are deliberately separate values.
func TestDisabledPolicyStillProducesVerdicts(t *testing.T) {
	now := time.Now()
	settings := WorktreeCleanupSettings{Enabled: false, MinAgeDays: 14}
	c := WorktreeCandidate{Multica: true, Merged: true, LastRunAt: now.Add(-30 * 24 * time.Hour)}
	if got := EvaluateWorktreeCleanup(c, settings, now); got != "" {
		t.Fatalf("a disabled policy changed the verdict to %q; it must only change whether anything acts on it", got)
	}
}

// --- placement -------------------------------------------------------------

// Invariant 4: the default location is the repository's sibling, on the user's
// disk, not anywhere inside the Multica workspace.
func TestDefaultWorktreeRootIsTheRepositorySibling(t *testing.T) {
	got := DefaultWorktreeRoot("/Users/me/code/app")
	if want := filepath.Join("/Users/me/code", "app.multica-worktrees"); got != want {
		t.Fatalf("DefaultWorktreeRoot = %q, want %q", got, want)
	}
}

// Invariant 5: never inside the repository's working tree. A copy there shows
// up in the user's own `git status`, in ripgrep and in editor indexes, and
// would need a .gitignore entry to behave.
func TestResolveWorktreeRootRefusesAPlacementInsideTheRepository(t *testing.T) {
	repo := t.TempDir()
	for _, inside := range []string{repo, filepath.Join(repo, ".worktrees"), filepath.Join(repo, "a", "b")} {
		if _, err := ResolveWorktreeRoot(repo, inside); err == nil {
			t.Fatalf("ResolveWorktreeRoot accepted %q, which is inside the repository %q", inside, repo)
		}
	}
}

func TestResolveWorktreeRootAcceptsAnExplicitSiblingAndRejectsRelative(t *testing.T) {
	repo := t.TempDir()
	sibling := filepath.Join(filepath.Dir(repo), "copies")
	got, err := ResolveWorktreeRoot(repo, sibling)
	if err != nil {
		t.Fatalf("ResolveWorktreeRoot(%q) errored: %v", sibling, err)
	}
	if got != filepath.Clean(sibling) {
		t.Fatalf("ResolveWorktreeRoot = %q, want %q", got, sibling)
	}
	if _, err := ResolveWorktreeRoot(repo, "copies"); err == nil {
		t.Fatal("a relative worktree_root was accepted; it would resolve against the daemon's cwd")
	}
	got, err = ResolveWorktreeRoot(repo, "")
	if err != nil {
		t.Fatalf("an empty worktree_root errored: %v", err)
	}
	if got != DefaultWorktreeRoot(repo) {
		t.Fatalf("an empty worktree_root resolved to %q, want the default %q", got, DefaultWorktreeRoot(repo))
	}
}

// --- ownership records -----------------------------------------------------

func TestWorktreeRecordRoundTripAndMissingRecordMeansNotOurs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "dene-617-abc123")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeWorktreeRecord(root, WorktreeRecord{Path: path, GitRoot: "/repo", Branch: "agent/x"}); err != nil {
		t.Fatalf("writeWorktreeRecord: %v", err)
	}
	rec, err := readWorktreeRecord(root, path)
	if err != nil {
		t.Fatalf("readWorktreeRecord: %v", err)
	}
	if rec.Branch != "agent/x" || rec.GitRoot != "/repo" {
		t.Fatalf("record round-tripped as %+v", rec)
	}
	if rec.CreatedAt.IsZero() || rec.LastRunAt.IsZero() {
		t.Fatalf("record has no timestamps: %+v — the age rule reads them", rec)
	}

	// The record lives OUTSIDE the working copy: inside, it would appear as an
	// untracked file in the copy's own `git status` and be swept into the
	// delivered branch.
	if strings.HasPrefix(worktreeRecordPath(root, path), path+string(filepath.Separator)) {
		t.Fatalf("the ownership record %q is inside the working copy %q", worktreeRecordPath(root, path), path)
	}

	removeWorktreeRecord(root, path)
	if _, err := readWorktreeRecord(root, path); err == nil {
		t.Fatal("the record survived removal")
	}
}

// --- scanning --------------------------------------------------------------

type stubProbe struct {
	dirty  map[string]bool
	merged map[string]bool
	trunk  string
	err    error
}

func (s stubProbe) Dirty(path string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return s.dirty[filepath.Base(path)], nil
}

func (s stubProbe) MergedInto(_, branch, _ string) (bool, error) { return s.merged[branch], nil }
func (s stubProbe) CurrentBranch(path string) (string, error)    { return filepath.Base(path), nil }
func (s stubProbe) DefaultBranch(string) (string, error)         { return s.trunk, nil }

func TestScanWorktreeRootReportsOneVerdictPerCopy(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	old := now.Add(-30 * 24 * time.Hour)

	mk := func(name string, record bool, branch string) string {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if record {
			if err := writeWorktreeRecord(root, WorktreeRecord{
				Path: path, GitRoot: "/repo", Branch: branch, CreatedAt: old, LastRunAt: old,
			}); err != nil {
				t.Fatal(err)
			}
		}
		return path
	}
	removable := mk("merged-and-clean", true, "merged-and-clean")
	dirty := mk("dirty", true, "dirty")
	unmerged := mk("unmerged", true, "unmerged")
	theirs := mk("user-made", false, "")

	probe := stubProbe{
		dirty:  map[string]bool{"dirty": true},
		merged: map[string]bool{"merged-and-clean": true, "dirty": true},
		trunk:  "main",
	}
	items, err := ScanWorktreeRoot(root, WorktreeCleanupSettings{Enabled: true, MinAgeDays: 14}, nil, probe, now)
	if err != nil {
		t.Fatalf("ScanWorktreeRoot: %v", err)
	}
	got := map[string]WorktreeKeepReason{}
	for _, item := range items {
		got[item.Path] = item.KeepReason
	}
	want := map[string]WorktreeKeepReason{
		removable: "",
		dirty:     KeepUncommittedChanges,
		unmerged:  KeepBranchNotMerged,
		theirs:    KeepNotMulticaCreated,
	}
	for path, wantReason := range want {
		if got[path] != wantReason {
			t.Errorf("%s: keep reason = %q, want %q", filepath.Base(path), got[path], wantReason)
		}
	}
	// The record directory is bookkeeping, not a working copy.
	if _, listed := got[filepath.Join(root, worktreeRecordDirName)]; listed {
		t.Error("the scan listed its own record directory as a working copy")
	}
}

// A copy a task is running in is never a candidate, whatever its git state.
func TestScanWorktreeRootMarksBusyCopiesInUse(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	path := filepath.Join(root, "busy")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeWorktreeRecord(root, WorktreeRecord{
		Path: path, GitRoot: "/repo", Branch: "busy",
		CreatedAt: now.Add(-90 * 24 * time.Hour), LastRunAt: now.Add(-90 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	items, err := ScanWorktreeRoot(root,
		WorktreeCleanupSettings{Enabled: true, MinAgeDays: 1},
		map[string]bool{path: true},
		stubProbe{merged: map[string]bool{"busy": true}, trunk: "main"},
		now)
	if err != nil {
		t.Fatalf("ScanWorktreeRoot: %v", err)
	}
	if len(items) != 1 || items[0].KeepReason != KeepInUse {
		t.Fatalf("items = %+v, want one item kept as %q", items, KeepInUse)
	}
}

// A root that was never created is not a failure: a repository whose parallel
// mode has never run simply has no copies.
func TestScanWorktreeRootTreatsAMissingRootAsEmpty(t *testing.T) {
	items, err := ScanWorktreeRoot(filepath.Join(t.TempDir(), "never-created"), WorktreeCleanupSettings{}, nil, stubProbe{}, time.Now())
	if err != nil {
		t.Fatalf("a missing worktree root errored: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("items = %+v, want none", items)
	}
}

// RemoveCleanableWorktree re-checks before acting. A preview is a snapshot;
// between rendering it and the click, a run can start or the user can edit.
func TestRemoveCleanableWorktreeRefusesWhatTheCurrentStateKeeps(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "dirty-now")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-90 * 24 * time.Hour)
	if err := writeWorktreeRecord(root, WorktreeRecord{Path: path, GitRoot: "/repo", Branch: "b", CreatedAt: old, LastRunAt: old}); err != nil {
		t.Fatal(err)
	}
	err := RemoveCleanableWorktree(root, path,
		WorktreeCleanupSettings{Enabled: true, MinAgeDays: 1},
		nil,
		stubProbe{dirty: map[string]bool{"dirty-now": true}, merged: map[string]bool{"b": true}, trunk: "main"},
		time.Now())
	if err == nil {
		t.Fatal("removed a copy that has uncommitted changes right now")
	}
	if !strings.Contains(err.Error(), string(KeepUncommittedChanges)) {
		t.Fatalf("error = %v, want it to name %q", err, KeepUncommittedChanges)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("the copy was removed anyway: %v", statErr)
	}
}
