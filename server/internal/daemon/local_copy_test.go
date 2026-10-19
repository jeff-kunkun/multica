package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func newLocalCopyFixture(t *testing.T, files map[string]string) (orig string, logger *slog.Logger) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	orig = filepath.Join(t.TempDir(), "folder")
	for rel, body := range files {
		writeCopyTestFile(t, filepath.Join(orig, rel), body)
	}
	if err := os.MkdirAll(orig, 0o755); err != nil {
		t.Fatal(err)
	}
	return orig, slog.New(slog.NewTextHandler(io.Discard, nil))
}

func writeCopyTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mustPrepareLocalRunCopy(t *testing.T, task Task, orig string, logger *slog.Logger) *localRunCopy {
	t.Helper()
	c, err := prepareLocalRunCopy(task, orig, logger)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if c == nil {
		t.Skip("volume cannot clone; local_directory runs in place here")
	}
	return c
}

// findBackup returns the content of rel in the single backup taken so far.
func findBackup(t *testing.T, c *localRunCopy, rel string) string {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(c.RunsRoot, "_backup", "*", rel))
	if len(matches) != 1 {
		t.Fatalf("backups of %s: %v", rel, matches)
	}
	return readTestFile(t, matches[0])
}

func TestLocalRunCopyMergesBackAlongsideConcurrentChanges(t *testing.T) {
	orig, logger := newLocalCopyFixture(t, map[string]string{
		"a.txt":   "a\n",
		"b.txt":   "b\n",
		"old.txt": "old\n",
		"doc.md":  "one\ntwo\nthree\nfour\nfive\n",
	})
	c := mustPrepareLocalRunCopy(t, Task{ID: "task-1", AgentID: "agent-1", IssueID: "issue-1"}, orig, logger)

	// The run edits its copy...
	writeCopyTestFile(t, filepath.Join(c.Work, "a.txt"), "a by run\n")
	writeCopyTestFile(t, filepath.Join(c.Work, "new", "made.txt"), "made\n")
	if err := os.Remove(filepath.Join(c.Work, "old.txt")); err != nil {
		t.Fatal(err)
	}
	writeCopyTestFile(t, filepath.Join(c.Work, "doc.md"), "ONE\ntwo\nthree\nfour\nfive\n")
	// ...while someone else edits the original folder.
	writeCopyTestFile(t, filepath.Join(orig, "b.txt"), "b by other\n")
	writeCopyTestFile(t, filepath.Join(orig, "doc.md"), "one\ntwo\nthree\nfour\nFIVE\n")

	c.Finish(logger)

	for rel, want := range map[string]string{
		"a.txt":        "a by run\n",
		"b.txt":        "b by other\n",
		"new/made.txt": "made\n",
		"doc.md":       "ONE\ntwo\nthree\nfour\nFIVE\n",
	} {
		if got := readTestFile(t, filepath.Join(orig, rel)); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(orig, "old.txt")); !os.IsNotExist(err) {
		t.Errorf("old.txt should be deleted, stat err = %v", err)
	}
	if got := findBackup(t, c, "old.txt"); got != "old\n" {
		t.Errorf("backup of deleted file = %q", got)
	}
	if _, err := os.Stat(c.Dir); !os.IsNotExist(err) {
		t.Errorf("copy should be removed after merge-back, stat err = %v", err)
	}
}

// (1) Both sides edit the same line: the folder keeps its version, the run's
// is saved next to it, the copy is kept and the conflict is reported.
func TestLocalRunCopySameLineConflictKeepsBothVersions(t *testing.T) {
	orig, logger := newLocalCopyFixture(t, map[string]string{"note.md": "title\nbody\n"})
	const taskID = "019a0000-0000-7000-8000-00000000abcd"
	c := mustPrepareLocalRunCopy(t, Task{ID: taskID, AgentID: "agent-1", IssueID: "issue-1"}, orig, logger)

	writeCopyTestFile(t, filepath.Join(c.Work, "note.md"), "title by run\nbody\n")
	writeCopyTestFile(t, filepath.Join(orig, "note.md"), "title by other\nbody\n")
	out := c.Finish(logger)

	if got := readTestFile(t, filepath.Join(orig, "note.md")); got != "title by other\nbody\n" {
		t.Errorf("note.md = %q, the folder's version must stay untouched", got)
	}
	saved := "note.md" + localCopyConflictSuffix + "0000abcd"
	if got := readTestFile(t, filepath.Join(orig, saved)); got != "title by run\nbody\n" {
		t.Errorf("%s = %q, want the run's version", saved, got)
	}
	if len(out.Conflicts) != 1 || out.Conflicts[0].Path != "note.md" || out.Conflicts[0].SavedAs != saved {
		t.Fatalf("conflicts = %+v, want note.md saved as %s", out.Conflicts, saved)
	}
	if out.KeptCopy == "" || out.KeptCopy == c.Dir {
		t.Fatalf("kept copy = %q, want it set aside under its own name", out.KeptCopy)
	}
	if got := readTestFile(t, filepath.Join(out.KeptCopy, "work", "note.md")); got != "title by run\nbody\n" {
		t.Errorf("kept copy note.md = %q", got)
	}
	if _, err := os.Stat(c.Dir); !os.IsNotExist(err) {
		t.Errorf("the key's directory must be free for the next run, stat err = %v", err)
	}
	note := out.Note()
	for _, want := range []string{"note.md", saved, ".multica-runs/folder/"} {
		if !strings.Contains(note, want) {
			t.Errorf("note missing %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, orig) {
		t.Errorf("note must not carry the absolute folder path:\n%s", note)
	}
}

// (2) Both sides edit different lines: merged automatically, no conflict.
func TestLocalRunCopyDifferentLinesMergeWithoutConflict(t *testing.T) {
	orig, logger := newLocalCopyFixture(t, map[string]string{"doc.md": "one\ntwo\nthree\nfour\nfive\n"})
	c := mustPrepareLocalRunCopy(t, Task{ID: "task-1", AgentID: "agent-1", IssueID: "issue-1"}, orig, logger)

	writeCopyTestFile(t, filepath.Join(c.Work, "doc.md"), "ONE\ntwo\nthree\nfour\nfive\n")
	writeCopyTestFile(t, filepath.Join(orig, "doc.md"), "one\ntwo\nthree\nfour\nFIVE\n")
	out := c.Finish(logger)

	if got := readTestFile(t, filepath.Join(orig, "doc.md")); got != "ONE\ntwo\nthree\nfour\nFIVE\n" {
		t.Errorf("doc.md = %q, want both edits", got)
	}
	if len(out.Conflicts) != 0 || out.KeptCopy != "" || out.Note() != "" {
		t.Errorf("outcome = %+v, want no conflict", out)
	}
	if matches, _ := filepath.Glob(filepath.Join(orig, "*"+localCopyConflictSuffix+"*")); len(matches) != 0 {
		t.Errorf("unexpected conflict files: %v", matches)
	}
	if _, err := os.Stat(c.Dir); !os.IsNotExist(err) {
		t.Errorf("copy should be removed after a clean merge-back, stat err = %v", err)
	}
}

// (3) The folder was not touched: the run's version is written.
func TestLocalRunCopyUntouchedOriginalTakesRunVersion(t *testing.T) {
	orig, logger := newLocalCopyFixture(t, map[string]string{"note.md": "title\n"})
	c := mustPrepareLocalRunCopy(t, Task{ID: "task-1", AgentID: "agent-1", IssueID: "issue-1"}, orig, logger)

	writeCopyTestFile(t, filepath.Join(c.Work, "note.md"), "title by run\n")
	out := c.Finish(logger)

	if got := readTestFile(t, filepath.Join(orig, "note.md")); got != "title by run\n" {
		t.Errorf("note.md = %q, want the run's version", got)
	}
	if len(out.Conflicts) != 0 || out.KeptCopy != "" {
		t.Errorf("outcome = %+v, want no conflict", out)
	}
}

func TestLocalRunCopyRunDeletingAChangedFileIsAConflict(t *testing.T) {
	orig, logger := newLocalCopyFixture(t, map[string]string{"keep.md": "v1\n"})
	c := mustPrepareLocalRunCopy(t, Task{ID: "task-1", AgentID: "agent-1", IssueID: "issue-1"}, orig, logger)

	if err := os.Remove(filepath.Join(c.Work, "keep.md")); err != nil {
		t.Fatal(err)
	}
	writeCopyTestFile(t, filepath.Join(orig, "keep.md"), "v2 by other\n")
	out := c.Finish(logger)

	if got := readTestFile(t, filepath.Join(orig, "keep.md")); got != "v2 by other\n" {
		t.Errorf("keep.md = %q, the other side's change must survive", got)
	}
	if len(out.Conflicts) != 1 || out.Conflicts[0].SavedAs != "" {
		t.Errorf("conflicts = %+v, want one deletion conflict", out.Conflicts)
	}
}

func TestLocalRunCopyBothCreatingTheSameFileIsNotAConflict(t *testing.T) {
	orig, logger := newLocalCopyFixture(t, map[string]string{"a.txt": "a\n"})
	c := mustPrepareLocalRunCopy(t, Task{ID: "task-1", AgentID: "agent-1", IssueID: "issue-1"}, orig, logger)

	writeCopyTestFile(t, filepath.Join(c.Work, "gen.txt"), "same\n")
	writeCopyTestFile(t, filepath.Join(orig, "gen.txt"), "same\n")
	if out := c.Finish(logger); len(out.Conflicts) != 0 {
		t.Errorf("conflicts = %+v, identical results must not conflict", out.Conflicts)
	}
}

func TestApplyLocalCopyOutcomeBlocksACompletedRun(t *testing.T) {
	out := localCopyOutcome{Folder: "/u/folder", Conflicts: []localCopyConflict{{Path: "a.txt", SavedAs: "a.txt.multica-conflict-1234"}}}

	result := TaskResult{Status: "completed", Comment: "done"}
	var runErr error
	applyLocalCopyOutcome(&result, &runErr, out)
	if result.Status != "blocked" || result.FailureReason != "agent_blocked" || runErr != nil {
		t.Fatalf("result = %+v, err = %v; want blocked / agent_blocked", result, runErr)
	}
	if !strings.Contains(result.Comment, "a.txt.multica-conflict-1234") || !strings.HasSuffix(result.Comment, "done") {
		t.Errorf("comment = %q, want the note then the agent's output", result.Comment)
	}

	failed := TaskResult{}
	runErr = errors.New("agent crashed")
	applyLocalCopyOutcome(&failed, &runErr, out)
	if !strings.Contains(runErr.Error(), "agent crashed") || !strings.Contains(runErr.Error(), "a.txt") {
		t.Errorf("err = %v, want the run's error plus the note", runErr)
	}

	clean := TaskResult{Status: "completed"}
	runErr = nil
	applyLocalCopyOutcome(&clean, &runErr, localCopyOutcome{Folder: "/u/folder"})
	if clean.Status != "completed" || runErr != nil {
		t.Errorf("a clean merge-back must leave the result alone: %+v %v", clean, runErr)
	}
}

func TestLocalRunCopyIgnoresRewritesWithSameContent(t *testing.T) {
	orig, logger := newLocalCopyFixture(t, map[string]string{"CLAUDE.md": "rules\n"})
	c := mustPrepareLocalRunCopy(t, Task{ID: "task-1", AgentID: "agent-1", IssueID: "issue-1"}, orig, logger)

	// The brief is written into CLAUDE.md and restored on exit.
	writeCopyTestFile(t, filepath.Join(c.Work, "CLAUDE.md"), "rules\n")
	writeCopyTestFile(t, filepath.Join(orig, "CLAUDE.md"), "rules edited by user\n")
	c.Finish(logger)

	if got := readTestFile(t, filepath.Join(orig, "CLAUDE.md")); got != "rules edited by user\n" {
		t.Errorf("CLAUDE.md = %q, an unchanged copy must not overwrite the user's edit", got)
	}
}

func TestLocalRunCopyDisabledBySwitchFile(t *testing.T) {
	orig, logger := newLocalCopyFixture(t, map[string]string{"a.txt": "a\n"})
	home := os.Getenv("HOME")
	writeCopyTestFile(t, filepath.Join(home, ".multica", localCopyDisableFileName), "")

	c, err := prepareLocalRunCopy(Task{ID: "task-1", AgentID: "agent-1", IssueID: "issue-1"}, orig, logger)
	if err != nil || c != nil {
		t.Fatalf("prepare with switch file = (%v, %v), want (nil, nil)", c, err)
	}
}

func TestLocalRunCopyDisabledOnlyForListedFolders(t *testing.T) {
	orig, logger := newLocalCopyFixture(t, map[string]string{"a.txt": "a\n"})
	switchFile := filepath.Join(os.Getenv("HOME"), ".multica", localCopyDisableFileName)

	writeCopyTestFile(t, switchFile, "# too large to clone\n/elsewhere/big-folder\n")
	c := mustPrepareLocalRunCopy(t, Task{ID: "task-1", AgentID: "agent-1", IssueID: "issue-1"}, orig, logger)
	c.Finish(logger)

	writeCopyTestFile(t, switchFile, "/elsewhere/big-folder\n"+orig+"/\n")
	c, err := prepareLocalRunCopy(Task{ID: "task-2", AgentID: "agent-1", IssueID: "issue-1"}, orig, logger)
	if err != nil || c != nil {
		t.Fatalf("prepare for listed folder = (%v, %v), want (nil, nil)", c, err)
	}

	// Content that is not a folder list keeps the old meaning: off everywhere.
	writeCopyTestFile(t, switchFile, "yes\n")
	c, err = prepareLocalRunCopy(Task{ID: "task-3", AgentID: "agent-1", IssueID: "issue-1"}, orig, logger)
	if err != nil || c != nil {
		t.Fatalf("prepare with a non-list switch file = (%v, %v), want (nil, nil)", c, err)
	}
}

func TestLocalRunCopyConflictedLeftoverIsSetAside(t *testing.T) {
	orig, logger := newLocalCopyFixture(t, map[string]string{"a.txt": "a\n"})
	const crashed = "019a0000-0000-7000-8000-0000crash001"
	c := mustPrepareLocalRunCopy(t, Task{ID: crashed, AgentID: "agent-1", IssueID: "issue-1"}, orig, logger)
	writeCopyTestFile(t, filepath.Join(c.Work, "a.txt"), "a by crashed run\n")
	writeCopyTestFile(t, filepath.Join(orig, "a.txt"), "a by user\n")
	// The daemon died before Finish ran.
	localCopyActiveDirs.Delete(c.Dir)

	next := mustPrepareLocalRunCopy(t, Task{ID: "task-2", AgentID: "agent-1", IssueID: "issue-1"}, orig, logger)
	defer next.Finish(logger)
	if got := readTestFile(t, filepath.Join(orig, "a.txt")); got != "a by user\n" {
		t.Errorf("a.txt = %q, the user's version must stay", got)
	}
	if got := readTestFile(t, filepath.Join(orig, "a.txt"+localCopyConflictSuffix+idTail(crashed))); got != "a by crashed run\n" {
		t.Errorf("saved conflict = %q, want the crashed run's version named after it", got)
	}
	if got := readTestFile(t, filepath.Join(next.Work, "a.txt")); got != "a by user\n" {
		t.Errorf("next copy a.txt = %q, want a fresh snapshot of the folder", got)
	}
	if matches, _ := filepath.Glob(c.Dir + ".conflict-*"); len(matches) != 1 {
		t.Errorf("conflicted leftover copies kept: %v, want one", matches)
	}
}

func TestLocalRunCopyMergesLeftoverBeforeNextRun(t *testing.T) {
	orig, logger := newLocalCopyFixture(t, map[string]string{"a.txt": "a\n"})
	task := Task{ID: "task-1", AgentID: "agent-1", IssueID: "issue-1"}
	c := mustPrepareLocalRunCopy(t, task, orig, logger)
	writeCopyTestFile(t, filepath.Join(c.Work, "a.txt"), "a by crashed run\n")
	// The daemon died before Finish ran.
	localCopyActiveDirs.Delete(c.Dir)

	next := mustPrepareLocalRunCopy(t, Task{ID: "task-2", AgentID: "agent-1", IssueID: "issue-1"}, orig, logger)
	defer next.Finish(logger)
	if next.Work != c.Work {
		t.Errorf("next run copy %s, want the same stable path %s", next.Work, c.Work)
	}
	if got := readTestFile(t, filepath.Join(orig, "a.txt")); got != "a by crashed run\n" {
		t.Errorf("a.txt = %q, leftover work was not merged back", got)
	}
	if got := readTestFile(t, filepath.Join(next.Work, "a.txt")); got != "a by crashed run\n" {
		t.Errorf("next copy a.txt = %q, want it to start from the merged folder", got)
	}
}

func TestLocalRunCopyConcurrentTasksOnSameKeyGetSeparateCopies(t *testing.T) {
	orig, logger := newLocalCopyFixture(t, map[string]string{"a.txt": "a\n"})
	first := mustPrepareLocalRunCopy(t, Task{ID: "task-1", AgentID: "agent-1", IssueID: "issue-1"}, orig, logger)
	second := mustPrepareLocalRunCopy(t, Task{ID: "task-2", AgentID: "agent-1", IssueID: "issue-1"}, orig, logger)
	if first.Work == second.Work {
		t.Fatalf("both tasks got %s", first.Work)
	}
	writeCopyTestFile(t, filepath.Join(first.Work, "one.txt"), "1\n")
	writeCopyTestFile(t, filepath.Join(second.Work, "two.txt"), "2\n")
	first.Finish(logger)
	second.Finish(logger)
	for _, rel := range []string{"one.txt", "two.txt"} {
		if _, err := os.Stat(filepath.Join(orig, rel)); err != nil {
			t.Errorf("%s missing after both runs: %v", rel, err)
		}
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestLocalRunCopyCarriesBranchBackWithoutTouchingWorkingTree(t *testing.T) {
	orig, logger := newLocalCopyFixture(t, map[string]string{"app.txt": "v1\n"})
	runGit(t, orig, "init", "-q", "-b", "main")
	runGit(t, orig, "add", ".")
	runGit(t, orig, "commit", "-q", "-m", "init")

	c := mustPrepareLocalRunCopy(t, Task{ID: "task-1", AgentID: "agent-1", IssueID: "issue-1"}, orig, logger)
	runGit(t, c.Work, "checkout", "-q", "-b", "feature")
	writeCopyTestFile(t, filepath.Join(c.Work, "app.txt"), "v2 on feature\n")
	runGit(t, c.Work, "commit", "-q", "-am", "feature work")
	writeCopyTestFile(t, filepath.Join(c.Work, "report.md"), "untracked output\n")
	featureSHA := runGit(t, c.Work, "rev-parse", "HEAD")
	c.Finish(logger)

	if got := runGit(t, orig, "rev-parse", "refs/heads/feature"); got != featureSHA {
		t.Errorf("feature = %s, want %s", got, featureSHA)
	}
	if got := runGit(t, orig, "symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Errorf("checked-out branch moved to %s", got)
	}
	if got := readTestFile(t, filepath.Join(orig, "app.txt")); got != "v1\n" {
		t.Errorf("app.txt = %q, the branch's change must not land in main's working tree", got)
	}
	if got := readTestFile(t, filepath.Join(orig, "report.md")); got != "untracked output\n" {
		t.Errorf("report.md = %q", got)
	}
}

func TestLocalRunCopyCommitOnCheckedOutBranchLandsAsChanges(t *testing.T) {
	orig, logger := newLocalCopyFixture(t, map[string]string{"app.txt": "v1\n"})
	runGit(t, orig, "init", "-q", "-b", "main")
	runGit(t, orig, "add", ".")
	runGit(t, orig, "commit", "-q", "-m", "init")
	mainSHA := runGit(t, orig, "rev-parse", "HEAD")

	c := mustPrepareLocalRunCopy(t, Task{ID: "task-1", AgentID: "agent-1", IssueID: "issue-1"}, orig, logger)
	writeCopyTestFile(t, filepath.Join(c.Work, "app.txt"), "v2\n")
	runGit(t, c.Work, "commit", "-q", "-am", "run work")
	runSHA := runGit(t, c.Work, "rev-parse", "HEAD")
	c.Finish(logger)

	if got := runGit(t, orig, "rev-parse", "main"); got != mainSHA {
		t.Errorf("checked-out main moved to %s", got)
	}
	if got := readTestFile(t, filepath.Join(orig, "app.txt")); got != "v2\n" {
		t.Errorf("app.txt = %q", got)
	}
	kept := runGit(t, orig, "for-each-ref", "--format=%(objectname)", "refs/multica/runs/")
	if kept != runSHA {
		t.Errorf("kept run commit = %q, want %s", kept, runSHA)
	}
}

func TestLocalRunCopySkipsWorktreeCreatedInsideCopy(t *testing.T) {
	orig, logger := newLocalCopyFixture(t, map[string]string{"app.txt": "v1\n"})
	runGit(t, orig, "init", "-q", "-b", "main")
	runGit(t, orig, "add", ".")
	runGit(t, orig, "commit", "-q", "-m", "init")

	c := mustPrepareLocalRunCopy(t, Task{ID: "task-1", AgentID: "agent-1", IssueID: "issue-1"}, orig, logger)
	runGit(t, c.Work, "worktree", "add", "-q", ".worktrees/x", "-b", "x")
	c.Finish(logger)

	if _, err := os.Stat(filepath.Join(orig, ".worktrees", "x")); !os.IsNotExist(err) {
		t.Errorf("a worktree made inside the copy must not land in the folder, stat err = %v", err)
	}
}

func TestBuildLocalRunCopyBlock(t *testing.T) {
	block := buildLocalRunCopyBlock("/Users/u/.multica-runs/project/k/work", "/Users/u/project")
	for _, want := range []string{"Private copy", "/Users/u/project", "/Users/u/.multica-runs/project/k/work", "merged back"} {
		if !strings.Contains(block, want) {
			t.Errorf("block missing %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "git worktree add") {
		t.Errorf("a private copy must not be told to make worktrees:\n%s", block)
	}
}

func TestLocalRunCopyNoticeOnlyWithACopy(t *testing.T) {
	if got := buildLocalRunCopyBlock("", "/Users/u/project"); got != "" {
		t.Errorf("no copy must render no notice, got:\n%s", got)
	}
}

// Not parallel: it swaps the package-level probe and sets the environment.
// With the always-clone switch on, stored worktree and shared rows run on a
// private copy where one can be made, and keep their mode where it cannot;
// the stored value itself is never rewritten.
func TestStoredExecutionModeIgnoredWhereACopyCanBeMade(t *testing.T) {
	prev := localCopySupported
	defer func() { localCopySupported = prev }()
	t.Setenv(localDirectoryAlwaysCloneEnv, "1")

	for _, stored := range []string{"", localDirectoryModeInPlace, localDirectoryModeWorktree, localDirectoryModeShared} {
		a := &localDirectoryAssignment{AbsPath: "/u/folder", Ref: localDirectoryRef{ExecutionMode: stored}}

		localCopySupported = func(string) bool { return true }
		if a.UsesWorktree() || a.IsShared() || a.SkipsPathMutex() || !a.RunsInUserDirectory() || a.mode() != localDirectoryModeInPlace {
			t.Errorf("stored %q with a copy: mode = %q, want in_place on a private copy", stored, a.mode())
		}

		localCopySupported = func(string) bool { return false }
		want := stored
		if want == "" {
			want = localDirectoryModeInPlace
		}
		if a.mode() != want {
			t.Errorf("stored %q without a copy: mode = %q, want the stored mode", stored, a.mode())
		}
		if a.Ref.ExecutionMode != stored {
			t.Errorf("stored mode rewritten to %q", a.Ref.ExecutionMode)
		}
	}
}

// Not parallel: it swaps the package-level probe. Without the switch every
// stored mode keeps its old behaviour, clone or not.
func TestStoredExecutionModeKeptByDefault(t *testing.T) {
	prev := localCopySupported
	defer func() { localCopySupported = prev }()
	localCopySupported = func(string) bool { return true }
	t.Setenv(localDirectoryAlwaysCloneEnv, "")

	for _, stored := range []string{localDirectoryModeWorktree, localDirectoryModeShared} {
		a := &localDirectoryAssignment{AbsPath: "/u/folder", Ref: localDirectoryRef{ExecutionMode: stored}}
		if a.mode() != stored || !a.SkipsPathMutex() {
			t.Errorf("stored %q with the switch off: mode = %q, want the stored mode", stored, a.mode())
		}
	}
}

// Not parallel: it swaps the package-level probe, and Go runs sequential
// top-level tests before resuming any parallel ones.
func TestAcquireLocalDirectoryLockSkipsInPlaceWithPrivateCopy(t *testing.T) {
	prev := localCopySupported
	localCopySupported = func(string) bool { return true }
	defer func() { localCopySupported = prev }()

	const daemonID = "d-mine"
	raw, err := json.Marshal(localDirectoryRef{LocalPath: t.TempDir(), DaemonID: daemonID})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resources := []ProjectResourceData{{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: raw}}
	d := &Daemon{cfg: Config{DaemonID: daemonID}, localPathLocks: NewLocalPathLocker(), logger: slog.Default()}
	for _, taskID := range []string{"task-a", "task-b"} {
		release, abort := d.acquireLocalDirectoryLockIfNeeded(context.Background(),
			Task{ID: taskID, IssueID: "issue-" + taskID, ProjectResources: resources}, slog.Default(), nil)
		if abort {
			t.Fatalf("%s: acquisition aborted", taskID)
		}
		if release != nil {
			t.Fatalf("%s: got a release callback, so in-place tasks with a private copy still queue", taskID)
		}
	}
}
