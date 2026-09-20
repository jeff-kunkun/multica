package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Where a parallel-mode working copy actually lands, proven against real git
// (DENE-617 invariants 4, 5, 9).

// Invariant 4: the copy is on the user's disk, beside their repository — not
// inside the env root, which the workspace GC reclaims on its own schedule.
func TestPrepareLocalWorktreePlacesTheCopyBesideTheRepository(t *testing.T) {
	repo := newTestRepo(t)
	envRoot := t.TempDir()
	wt, err := PrepareLocalWorktree(LocalWorktreeParams{
		LocalPath: repo,
		EnvRoot:   envRoot,
		AgentName: "J",
		TaskID:    "11112222-3333-4444-5555-666677778888",
	}, worktreeTestLogger())
	if err != nil {
		t.Fatalf("PrepareLocalWorktree: %v", err)
	}
	t.Cleanup(func() { wt.Discard(worktreeTestLogger()) })

	wantRoot := DefaultWorktreeRoot(repo)
	if filepath.Dir(wt.Path) != wantRoot {
		t.Fatalf("worktree at %q, want it under the repository's sibling %q", wt.Path, wantRoot)
	}
	if strings.HasPrefix(wt.Path, filepath.Clean(envRoot)+string(filepath.Separator)) {
		t.Fatalf("worktree at %q is still inside the env root %q, where the workspace GC reclaims it", wt.Path, envRoot)
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatalf("worktree directory missing: %v", err)
	}

	// Invariant 5: not inside the user's working tree, so it never shows up in
	// their `git status` and needs no .gitignore entry.
	if rel, relErr := filepath.Rel(repo, wt.Path); relErr == nil && !strings.HasPrefix(rel, "..") {
		t.Fatalf("worktree at %q is inside the repository %q", wt.Path, repo)
	}
	if status := gitRun(t, repo, "status", "--porcelain"); strings.Contains(status, worktreeRootSuffix) {
		t.Fatalf("the worktree root appears in the repository's own `git status`:\n%s", status)
	}

	// Invariant 8's precondition: the copy is recorded as Multica's, or
	// cleanup would never be allowed to touch it.
	rec, err := readWorktreeRecord(wantRoot, wt.Path)
	if err != nil {
		t.Fatalf("no ownership record for the copy Multica just created: %v", err)
	}
	if rec.Branch != wt.Branch || rec.GitRoot != wt.GitRoot {
		t.Fatalf("record = %+v, want branch %q in %q", rec, wt.Branch, wt.GitRoot)
	}
}

// A configured worktree_root is honoured, and the copy is still recorded there.
func TestPrepareLocalWorktreeHonoursAConfiguredRoot(t *testing.T) {
	repo := newTestRepo(t)
	custom := filepath.Join(t.TempDir(), "my-copies")
	wt, err := PrepareLocalWorktree(LocalWorktreeParams{
		LocalPath:    repo,
		EnvRoot:      t.TempDir(),
		WorktreeRoot: custom,
		AgentName:    "J",
		TaskID:       "11112222-3333-4444-5555-666677778888",
	}, worktreeTestLogger())
	if err != nil {
		t.Fatalf("PrepareLocalWorktree: %v", err)
	}
	t.Cleanup(func() { wt.Discard(worktreeTestLogger()) })
	if filepath.Dir(wt.Path) != filepath.Clean(custom) {
		t.Fatalf("worktree at %q, want it under the configured root %q", wt.Path, custom)
	}
	if _, err := readWorktreeRecord(custom, wt.Path); err != nil {
		t.Fatalf("no ownership record under the configured root: %v", err)
	}
}

// A root inside the repository is refused rather than silently relocated: the
// user asked for something that cannot work, and a task that quietly does
// something else is how a setting becomes a lie.
func TestPrepareLocalWorktreeRefusesARootInsideTheRepository(t *testing.T) {
	repo := newTestRepo(t)
	_, err := PrepareLocalWorktree(LocalWorktreeParams{
		LocalPath:    repo,
		EnvRoot:      t.TempDir(),
		WorktreeRoot: filepath.Join(repo, ".worktrees"),
		AgentName:    "J",
		TaskID:       "11112222-3333-4444-5555-666677778888",
	}, worktreeTestLogger())
	if err == nil {
		t.Fatal("a worktree root inside the repository was accepted")
	}
	if !strings.Contains(err.Error(), "inside the repository") {
		t.Fatalf("error = %v, want it to say the root is inside the repository", err)
	}
}

// Invariant 9: removal goes through `git worktree remove`, so the repository
// is left with no orphan metadata — and the same directory name can be used
// again, which an `rm -rf` would have made impossible.
func TestCleanupRemovalLeavesNoOrphanWorktreeMetadata(t *testing.T) {
	repo := newTestRepo(t)
	envRoot := filepath.Join(t.TempDir(), "task-one")
	if err := os.MkdirAll(envRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	wt, err := PrepareLocalWorktree(LocalWorktreeParams{
		LocalPath: repo,
		EnvRoot:   envRoot,
		AgentName: "J",
		TaskID:    "11112222-3333-4444-5555-666677778888",
	}, worktreeTestLogger())
	if err != nil {
		t.Fatalf("PrepareLocalWorktree: %v", err)
	}
	root := filepath.Dir(wt.Path)
	name := filepath.Base(wt.Path)

	// Age the record past the retention window and mark the branch merged, so
	// the copy qualifies. Trunk is pinned so the test does not depend on how
	// the repository names its default branch.
	rec, err := readWorktreeRecord(root, wt.Path)
	if err != nil {
		t.Fatalf("readWorktreeRecord: %v", err)
	}
	rec.LastRunAt = time.Now().Add(-90 * 24 * time.Hour)
	rec.CreatedAt = rec.LastRunAt
	if err := writeWorktreeRecord(root, *rec); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "merge", "--no-ff", "-m", "merge task branch", wt.Branch)

	settings := WorktreeCleanupSettings{Enabled: true, MinAgeDays: 14, TrunkBranch: "main"}
	if err := RemoveCleanableWorktree(root, wt.Path, settings, nil, GitWorktreeProbe{}, time.Now()); err != nil {
		t.Fatalf("RemoveCleanableWorktree: %v", err)
	}
	if _, statErr := os.Stat(wt.Path); !os.IsNotExist(statErr) {
		t.Fatalf("the working copy is still on disk: %v", statErr)
	}
	if list := gitRun(t, repo, "worktree", "list"); strings.Contains(list, wt.Path) {
		t.Fatalf("`git worktree list` still names the removed copy:\n%s", list)
	}
	if entries, readErr := os.ReadDir(filepath.Join(repo, ".git", "worktrees")); readErr == nil {
		for _, e := range entries {
			if e.Name() == name {
				t.Fatalf(".git/worktrees/%s survived removal; git will refuse that name again", name)
			}
		}
	}
	if _, recErr := readWorktreeRecord(root, wt.Path); recErr == nil {
		t.Fatal("the ownership record survived removal, so a later scan would look for a copy that is gone")
	}

	// The proof that no orphan is left: the same name works again.
	reused := filepath.Join(root, name)
	if out, addErr := runGit(repo, "worktree", "add", "-b", "reuse-check", reused, "HEAD"); addErr != nil {
		t.Fatalf("could not reuse the removed worktree name: %s: %v", strings.TrimSpace(out), addErr)
	}
	_, _ = runGit(repo, "worktree", "remove", "--force", reused)
}
