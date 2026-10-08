package execenv

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// addTaskWorktree forks a task branch off from in its own worktree, the way
// a run's checkout sits next to the user's directory.
func addTaskWorktree(t *testing.T, repo, branch, from string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "wt")
	gitRun(t, repo, "worktree", "add", "-b", branch, dir, from)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return dir
}

func TestDeliveredFilesListsWhatTheBranchAddsAgainstTheMainline(t *testing.T) {
	repo := newTestRepo(t)
	wt := addTaskWorktree(t, repo, "agent/agent/dene-1", "main")
	commitIn(t, wt, "CONTEXT.md", "# terms\n", "DENE-1: context")
	if err := os.MkdirAll(filepath.Join(wt, "docs", "adr"), 0o755); err != nil {
		t.Fatal(err)
	}
	commitIn(t, wt, "docs/adr/0001-x.md", "adr\n", "DENE-1: adr")
	gitRun(t, wt, "rm", "-q", "keep.txt")
	gitRun(t, wt, "commit", "-m", "DENE-1: drop keep")

	files, base, err := DeliveredFiles(wt)
	if err != nil {
		t.Fatal(err)
	}
	if base != "main" || !reflect.DeepEqual(files, []string{"CONTEXT.md", "docs/adr/0001-x.md"}) {
		t.Fatalf("files = %v base = %q; want the two added files against main, the deletion left out", files, base)
	}
}

func TestDeliveredFilesPrefersTheDeliveryLine(t *testing.T) {
	repo := newTestRepo(t)
	gitRun(t, repo, "branch", testDeliveryBranch, "main")
	sibling := addTaskWorktree(t, repo, "agent/agent/sibling", testDeliveryBranch)
	commitIn(t, sibling, "sibling.txt", "s\n", "sibling work")
	gitRun(t, repo, "branch", "-f", testDeliveryBranch, "agent/agent/sibling")

	wt := addTaskWorktree(t, repo, "agent/agent/dene-2", testDeliveryBranch)
	commitIn(t, wt, "CONTEXT.md", "# terms\n", "DENE-2: context")

	files, base, err := DeliveredFiles(wt, "no-such-branch", testDeliveryBranch)
	if err != nil {
		t.Fatal(err)
	}
	if base != testDeliveryBranch || !reflect.DeepEqual(files, []string{"CONTEXT.md"}) {
		t.Fatalf("files = %v base = %q; the sibling's file is the line's, not this delivery's", files, base)
	}
}

func TestDeliveredFilesSaysNothingWhenHeadIsOnTheMainline(t *testing.T) {
	repo := newTestRepo(t)
	files, base, err := DeliveredFiles(repo)
	if err != nil || files != nil || base != "" {
		t.Fatalf("files = %v base = %q err = %v; want no answer", files, base, err)
	}
}

func TestMergeIntoMainlineMergesTheChatBranchIntoTheProjectDirectory(t *testing.T) {
	repo := newTestRepo(t)
	wt := addTaskWorktree(t, repo, "agent/chat/abc", "main")
	commitIn(t, wt, "CONTEXT.md", "# terms\n", "context term")

	res, err := MergeIntoMainline(wt, "Chat abcd1234: 沉淀 context", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Mainline != "main" || res.Source != "agent/chat/abc" || !reflect.DeepEqual(res.Files, []string{"CONTEXT.md"}) {
		t.Fatalf("res = %+v", res)
	}
	if got := subjects(res.Commits); !reflect.DeepEqual(got, []string{"context term", "Chat abcd1234: 沉淀 context"}) {
		t.Fatalf("commits = %v", got)
	}
	if got := gitRun(t, repo, "log", "-1", "--format=%s"); got != "Chat abcd1234: 沉淀 context" {
		t.Fatalf("project directory HEAD = %q; want the chat's merge commit", got)
	}
	if got := readFile(t, filepath.Join(repo, "CONTEXT.md")); got != "# terms\n" {
		t.Fatalf("project directory CONTEXT.md = %q", got)
	}
}

func TestMergeIntoMainlineRefusesADirtyProjectDirectory(t *testing.T) {
	repo := newTestRepo(t)
	wt := addTaskWorktree(t, repo, "agent/chat/abc", "main")
	commitIn(t, wt, "CONTEXT.md", "# terms\n", "context term")
	writeFile(t, filepath.Join(repo, "tracked.txt"), "the user's edit\n")
	before := gitRun(t, repo, "rev-parse", "HEAD")

	_, err := MergeIntoMainline(wt, "Chat abcd1234: 沉淀 context", "")
	if !errors.Is(err, ErrDeliveryMergeRefused) || !strings.Contains(err.Error(), "tracked.txt") {
		t.Fatalf("err = %v; want a refusal naming the dirty file", err)
	}
	if after := gitRun(t, repo, "rev-parse", "HEAD"); after != before {
		t.Fatalf("main moved %s -> %s on a refused merge", before, after)
	}
	if got := readFile(t, filepath.Join(repo, "tracked.txt")); got != "the user's edit\n" {
		t.Fatalf("the user's edit was touched: %q", got)
	}
}

func TestMergeIntoMainlineRecordsWorkAlreadyOnTheMainline(t *testing.T) {
	repo := newTestRepo(t)
	commitIn(t, repo, "CONTEXT.md", "# terms\n", "Chat abcd1234: context term")
	before := gitRun(t, repo, "rev-parse", "HEAD")

	res, err := MergeIntoMainline(repo, "Chat abcd1234: 沉淀 context", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Tip != before || res.Source != "main" {
		t.Fatalf("res = %+v; a shared directory's work is recorded, not merged again", res)
	}
	// Without a session start or an upstream, the last commits stand in.
	if !strings.Contains(strings.Join(res.Files, ","), "CONTEXT.md") {
		t.Fatalf("files = %v", res.Files)
	}
}
