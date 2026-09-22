package repocache

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCreateWorktreeSparsePathsLeavesTheRestOffDisk(t *testing.T) {
	t.Parallel()
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"package.json":      "{}\n",
		"apps/web/index.ts": "web\n",
		"server/main.go":    "package main\n",
	}
	for name, body := range files {
		path := filepath.Join(source, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "server", "big.bin"), bytes.Repeat([]byte("q"), 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main", source},
		{"-C", source, "config", "user.email", "t@t"},
		{"-C", source, "config", "user.name", "t"},
		{"-C", source, "add", "-A"},
		{"-C", source, "commit", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}

	cache := New(t.TempDir(), testLogger())
	if err := cache.Sync("ws-sparse", []RepoInfo{{URL: source}}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	result, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-sparse",
		RepoURL:     source,
		WorkDir:     t.TempDir(),
		AgentName:   "sparse",
		TaskID:      "task-sparse",
		SparsePaths: "apps/web",
	})
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if result.SparsePaths != "apps/web" || result.SparseSkipped != "" {
		t.Fatalf("result sparse = %q skipped %q", result.SparsePaths, result.SparseSkipped)
	}
	if _, err := os.Stat(filepath.Join(result.Path, "apps", "web", "index.ts")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(result.Path, "package.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(result.Path, "server", "big.bin")); !os.IsNotExist(err) {
		t.Fatalf("cache worktree wrote the excluded blob: %v", err)
	}
	if _, err := os.Stat(filepath.Join(result.Path, "server", "MULTICA_SPARSE_EXCLUDED.txt")); err != nil {
		t.Fatalf("excluded directory has no marker: %v", err)
	}
}
