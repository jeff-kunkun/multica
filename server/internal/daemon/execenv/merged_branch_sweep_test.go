package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// commitOnBranch makes a branch off HEAD with one commit adding name.
func commitOnBranch(t *testing.T, repo, branch, name string) {
	t.Helper()
	gitRun(t, repo, "branch", branch, "HEAD")
	wt := filepath.Join(t.TempDir(), "w")
	gitRun(t, repo, "worktree", "add", "-q", wt, branch)
	if err := os.WriteFile(filepath.Join(wt, name), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, wt, "add", name)
	gitRun(t, wt, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", name)
	gitRun(t, repo, "worktree", "remove", "--force", wt)
}

func branchExists(t *testing.T, repo, branch string) bool {
	t.Helper()
	return gitRun(t, repo, "branch", "--list", branch) != ""
}

// Merged branches go, unmerged ones stay, and a branch outside agent/ is never
// touched whatever it holds (DENE-1666).
func TestSweepMergedTaskBranchesDeletesOnlyDelivered(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	commitOnBranch(t, repo, "agent/a/dene-1", "one.txt")   // will be merged by ff
	commitOnBranch(t, repo, "agent/a/dene-2", "two.txt")   // will be squash-merged
	commitOnBranch(t, repo, "agent/a/dene-3", "three.txt") // stays unmerged (a failed run's partial work)
	gitRun(t, repo, "branch", "feature/mine", "HEAD")      // user's own, at trunk
	gitRun(t, repo, "update-ref", userStateRef("agent/a/dene-1"), "HEAD")

	gitRun(t, repo, "merge", "-q", "--ff-only", "agent/a/dene-1")
	gitRun(t, repo, "merge", "--squash", "agent/a/dene-2")
	gitRun(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "squash dene-2")

	got := SweepMergedTaskBranches(repo, nil, worktreeTestLogger())
	if len(got) != 2 {
		t.Fatalf("deleted = %v, want dene-1 and dene-2", got)
	}
	for _, b := range []string{"agent/a/dene-1", "agent/a/dene-2"} {
		if branchExists(t, repo, b) {
			t.Errorf("%s should be deleted", b)
		}
	}
	for _, b := range []string{"agent/a/dene-3", "feature/mine"} {
		if !branchExists(t, repo, b) {
			t.Errorf("%s must be kept", b)
		}
	}
	if refs := gitRun(t, repo, "for-each-ref", localStateRefPrefix); refs != "" {
		t.Errorf("state ref of a swept branch should go with it: %q", refs)
	}
	common := gitRun(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	manifest := readFile(t, filepath.Join(common, sweepManifestDir, "daemon.tsv"))
	if !strings.Contains(manifest, "agent/a/dene-1") || !strings.Contains(manifest, "agent/a/dene-2") || strings.Contains(manifest, "dene-3") {
		t.Errorf("manifest should list exactly the deleted branches:\n%s", manifest)
	}
}

// A merged branch some worktree still has checked out is in use, not stale.
func TestSweepMergedTaskBranchesKeepsCheckedOutBranch(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	commitOnBranch(t, repo, "agent/a/dene-4", "four.txt")
	gitRun(t, repo, "merge", "-q", "--ff-only", "agent/a/dene-4")
	busy := filepath.Join(t.TempDir(), "busy")
	gitRun(t, repo, "worktree", "add", "-q", busy, "agent/a/dene-4")

	if got := SweepMergedTaskBranches(repo, nil, worktreeTestLogger()); len(got) != 0 {
		t.Fatalf("swept a checked-out branch: %v", got)
	}
	if !branchExists(t, repo, "agent/a/dene-4") {
		t.Fatal("checked-out branch was deleted")
	}
	if SweepMergedTaskBranch(repo, "agent/a/dene-4", nil, nil) {
		t.Fatal("single-branch sweep deleted a checked-out branch")
	}
}

// An interrupted ref keeps work the branch does not contain, and it is pruned
// together with the branch, so the branch has to stay.
func TestSweepMergedTaskBranchesKeepsBranchWithRescuedWork(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	commitOnBranch(t, repo, "agent/a/dene-5", "five.txt")
	gitRun(t, repo, "merge", "-q", "--ff-only", "agent/a/dene-5")
	gitRun(t, repo, "update-ref", localInterruptedRefPrefix+"agent/a/dene-5", "HEAD")

	if got := SweepMergedTaskBranches(repo, nil, worktreeTestLogger()); len(got) != 0 {
		t.Fatalf("swept a branch holding an interrupted run's work: %v", got)
	}
}

// Merged into a development line that is not the repo's default branch counts
// when the caller names it as trunk.
func TestSweepMergedTaskBranchesHonoursGivenTrunk(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	main := gitRun(t, repo, "rev-parse", "--abbrev-ref", "HEAD")
	commitOnBranch(t, repo, "agent/a/dene-6", "six.txt")
	gitRun(t, repo, "branch", "kun", main)
	gitRun(t, repo, "worktree", "add", "-q", filepath.Join(t.TempDir(), "kun"), "kun")
	// Merge into kun through its worktree, leaving HEAD (main) behind.
	var kunPath string
	for _, line := range strings.Split(gitRun(t, repo, "worktree", "list", "--porcelain"), "\n\n") {
		if strings.Contains(line, "refs/heads/kun") {
			kunPath = strings.TrimPrefix(strings.SplitN(line, "\n", 2)[0], "worktree ")
		}
	}
	gitRun(t, kunPath, "merge", "-q", "--ff-only", "agent/a/dene-6")

	if got := SweepMergedTaskBranches(repo, nil, worktreeTestLogger()); len(got) != 0 {
		t.Fatalf("unmerged into HEAD's trunk, must be kept: %v", got)
	}
	if got := SweepMergedTaskBranches(repo, []string{"kun"}, worktreeTestLogger()); len(got) != 1 {
		t.Fatalf("merged into the named trunk, want deleted: %v", got)
	}
}

// The thing the ticket is for: starting a task in a repo reclaims what earlier
// tasks left behind, without touching the branch the new task uses.
func TestPrepareLocalWorktreeSweepsMergedTaskBranches(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	commitOnBranch(t, repo, "agent/a/dene-7", "seven.txt")
	gitRun(t, repo, "merge", "-q", "--ff-only", "agent/a/dene-7")
	commitOnBranch(t, repo, "agent/a/dene-8", "eight.txt")

	wt := prepareForTest(t, repo)
	t.Cleanup(func() { wt.Discard(worktreeTestLogger()) })

	if branchExists(t, repo, "agent/a/dene-7") {
		t.Error("merged task branch survived the next prepare")
	}
	if !branchExists(t, repo, "agent/a/dene-8") {
		t.Error("unmerged task branch was swept")
	}
	if !branchExists(t, repo, wt.Branch) {
		t.Errorf("the new task's own branch %s is gone", wt.Branch)
	}
}

// Reclaiming a preserved copy takes its delivered branch with it, but a copy
// that cleanup refuses leaves the branch alone (DENE-1666).
func TestRemoveCleanableWorktreeAlsoDropsTheDeliveredBranch(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	commitOnBranch(t, repo, "agent/a/dene-9", "nine.txt")
	commitOnBranch(t, repo, "agent/a/dene-10", "ten.txt")
	gitRun(t, repo, "merge", "-q", "--ff-only", "agent/a/dene-9")

	root := t.TempDir()
	old := time.Now().Add(-90 * 24 * time.Hour)
	settings := WorktreeCleanupSettings{Enabled: true, MinAgeDays: 1}
	for _, branch := range []string{"agent/a/dene-9", "agent/a/dene-10"} {
		path := filepath.Join(root, filepath.Base(branch))
		gitRun(t, repo, "worktree", "add", "-q", path, branch)
		if err := writeWorktreeRecord(root, WorktreeRecord{Path: path, GitRoot: repo, Branch: branch, CreatedAt: old, LastRunAt: old}); err != nil {
			t.Fatal(err)
		}
	}

	merged := filepath.Join(root, "dene-9")
	if err := RemoveCleanableWorktree(root, merged, settings, nil, GitWorktreeProbe{}, time.Now()); err != nil {
		t.Fatalf("remove merged copy: %v", err)
	}
	if branchExists(t, repo, "agent/a/dene-9") {
		t.Error("the delivered branch outlived its reclaimed copy")
	}

	unmerged := filepath.Join(root, "dene-10")
	if err := RemoveCleanableWorktree(root, unmerged, settings, nil, GitWorktreeProbe{}, time.Now()); err == nil {
		t.Fatal("removed a copy whose branch is not merged")
	}
	if !branchExists(t, repo, "agent/a/dene-10") {
		t.Error("an unmerged branch was deleted")
	}
}
