package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/coderesolve"
)

// newDefaultDirTestDaemon runs a fake Claude that records its cwd and the
// files it can see there, the way newLeaderReuseTestDaemon records args.
func newDefaultDirTestDaemon(t *testing.T) (*Daemon, string) {
	t.Helper()
	testDir := t.TempDir()
	fakeBin := filepath.Join(testDir, "claude")
	cwdFile := filepath.Join(testDir, "cwd.txt")
	script := `#!/bin/sh
pwd -P > "` + cwdFile + `"
IFS= read -r _
printf '%s\n' '{"type":"system","session_id":"session-default-dir"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"session-default-dir","result":"done"}'
`
	writeTestExecutable(t, fakeBin, []byte(script))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	d := &Daemon{
		client:         NewClient(srv.URL),
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		workspaces:     make(map[string]*workspaceState),
		runtimeIndex:   map[string]Runtime{"rt-default": {ID: "rt-default", Provider: "claude"}},
		activeEnvRoots: make(map[string]int),
		cfg: Config{
			WorkspacesRoot: t.TempDir(),
			AgentTimeout:   5 * time.Second,
			ServerBaseURL:  srv.URL,
			Agents:         map[string]AgentEntry{"claude": {Path: fakeBin}},
		},
	}
	return d, cwdFile
}

func defaultDirTestTask(id, defaultDir string, decision coderesolve.Decision) Task {
	return Task{
		ID:             id,
		WorkspaceID:    "ws-default",
		RuntimeID:      "rt-default",
		IssueID:        "issue-default",
		AgentID:        "agent-default",
		AuthToken:      "mat_default_dir",
		CodeDecision:   &decision,
		DefaultWorkDir: defaultDir,
		Agent:          &AgentData{ID: "agent-default", Name: "default-agent"},
	}
}

func scratchDecision() coderesolve.Decision {
	return coderesolve.NewSharedScratch(coderesolve.ScratchTarget{SessionID: "task"})
}

// The acceptance case for DENE-2667 PR 8: an issue with no folder runs in the
// machine's default directory, and the brief sits in the sidecar beside it.
func TestRunTaskWithNoFolderRunsInTheDefaultDirectory(t *testing.T) {
	t.Parallel()
	d, cwdFile := newDefaultDirTestDaemon(t)
	defaultDir := t.TempDir() // not a git work tree
	if err := os.WriteFile(filepath.Join(defaultDir, "mine.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := d.runTask(context.Background(), defaultDirTestTask("task-default-dir", defaultDir, scratchDecision()), "claude", 0, d.logger)
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if !sameDir(t, result.WorkDir, defaultDir) {
		t.Fatalf("WorkDir = %q, want the default directory %q", result.WorkDir, defaultDir)
	}
	cwd, err := os.ReadFile(cwdFile)
	if err != nil {
		t.Fatalf("read cwd: %v", err)
	}
	if !sameDir(t, strings.TrimSpace(string(cwd)), defaultDir) {
		t.Fatalf("agent started in %q, want %q", strings.TrimSpace(string(cwd)), defaultDir)
	}
	entries, err := os.ReadDir(defaultDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "mine.txt" {
			t.Errorf("default directory gained %q; instruction files belong in the sidecar", e.Name())
		}
	}
	sidecar := filepath.Join(result.EnvRoot, "sidecar")
	if _, err := os.Stat(filepath.Join(sidecar, ".multica")); err != nil {
		t.Fatalf(".multica missing from the isolated sidecar %s: %v", sidecar, err)
	}
	if _, err := os.Stat(filepath.Join(result.EnvRoot, "workdir")); !os.IsNotExist(err) {
		t.Fatalf("a scratch workdir was created next to the default-directory run: %v", err)
	}
}

// A default directory the run cannot use sends it to scratch, and the folder
// that was named is left exactly as it was. Not parallel: it moves HOME and
// the working directory so the blacklisted and relative cases point at
// throwaway folders.
func TestRunTaskFallsBackToScratchForAnUnusableDefaultDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	t.Chdir(cwd)
	for _, dir := range []string{home, filepath.Join(cwd, "work")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "mine.txt"), []byte("mine"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	missing := filepath.Join(t.TempDir(), "gone")

	for _, tc := range []struct{ name, defaultDir, folder string }{
		{"missing", missing, missing},
		{"relative", "work", filepath.Join(cwd, "work")},
		{"blacklisted home directory", home, home},
	} {
		d, _ := newDefaultDirTestDaemon(t)
		result, err := d.runTask(context.Background(), defaultDirTestTask("task-unusable-"+strings.ReplaceAll(tc.name, " ", "-"), tc.defaultDir, scratchDecision()), "claude", 0, d.logger)
		if err != nil {
			t.Fatalf("%s: runTask: %v", tc.name, err)
		}
		if !strings.HasPrefix(result.WorkDir, result.EnvRoot) {
			t.Fatalf("%s: WorkDir = %q, want a scratch directory under %q", tc.name, result.WorkDir, result.EnvRoot)
		}
		entries, err := os.ReadDir(tc.folder)
		if tc.folder == missing {
			if !os.IsNotExist(err) {
				t.Fatalf("%s: the missing default directory was created: %v", tc.name, err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Name() != "mine.txt" {
				t.Errorf("%s: %s gained %q", tc.name, tc.folder, e.Name())
			}
		}
	}
}

func TestDefaultDirAssignmentRules(t *testing.T) {
	dir := t.TempDir()
	remote := coderesolve.NewRemoteCache(coderesolve.RemoteTarget{})

	cases := []struct {
		name     string
		task     Task
		provider string
		use      bool
		skipped  bool
	}{
		{"scratch run", defaultDirTestTask("t", dir, scratchDecision()), "claude", true, false},
		{"no default set", defaultDirTestTask("t", "", scratchDecision()), "claude", false, false},
		{"run with a repository keeps its checkout", defaultDirTestTask("t", dir, remote), "claude", false, true},
		{"runtime that cannot isolate its brief", defaultDirTestTask("t", dir, scratchDecision()), "hermes", false, true},
		{"relative path", defaultDirTestTask("t", "work", scratchDecision()), "claude", false, true},
		{"protected system root", defaultDirTestTask("t", "/", scratchDecision()), "claude", false, true},
		{"missing directory", defaultDirTestTask("t", filepath.Join(dir, "gone"), scratchDecision()), "claude", false, true},
	}
	leader := defaultDirTestTask("t", dir, scratchDecision())
	leader.IsLeaderTask = true
	cases = append(cases, struct {
		name     string
		task     Task
		provider string
		use      bool
		skipped  bool
	}{"squad leader", leader, "claude", false, false})

	for _, tc := range cases {
		a, skipped := defaultDirAssignment(tc.task, tc.provider)
		if (a != nil) != tc.use || (skipped != "") != tc.skipped {
			t.Errorf("%s: assignment=%+v skipped=%q, want use=%v skipped=%v", tc.name, a, skipped, tc.use, tc.skipped)
		}
		if a != nil && (!a.DefaultDir || a.projectFolder() != nil) {
			t.Errorf("%s: default directory treated as the project's folder", tc.name)
		}
	}
}

func TestSyncDefaultDirReportsTheNewPathAtOnce(t *testing.T) {
	existing := t.TempDir()
	var mu sync.Mutex
	setting := existing
	var reports []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/daemon/runtimes/rt-1/default-dir" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		reports = append(reports, body)
		current := setting
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"path": current})
	}))
	defer srv.Close()
	d := &Daemon{
		client:     NewClient(srv.URL),
		logger:     discardLogger(),
		workspaces: map[string]*workspaceState{"ws": {runtimeIDs: []string{"rt-1"}}},
	}

	path, exists := d.syncDefaultDir(context.Background(), "", nil)
	if path != existing || exists == nil || !*exists {
		t.Fatalf("first round = %q %v, want %q reported existing", path, exists, existing)
	}
	if len(reports) != 2 || reports[1]["path"] != existing || reports[1]["exists"] != true {
		t.Fatalf("reports = %v, want the new path reported in the same round", reports)
	}

	mu.Lock()
	setting = filepath.Join(existing, "missing")
	mu.Unlock()
	path, exists = d.syncDefaultDir(context.Background(), path, exists)
	if path != setting || exists == nil || *exists {
		t.Fatalf("second round = %q %v, want %q reported missing", path, exists, setting)
	}
	if last := reports[len(reports)-1]; last["path"] != setting || last["exists"] != false {
		t.Fatalf("last report = %v", last)
	}

	mu.Lock()
	setting = ""
	mu.Unlock()
	if path, exists = d.syncDefaultDir(context.Background(), path, exists); path != "" || exists != nil {
		t.Fatalf("cleared setting = %q %v, want nothing to report", path, exists)
	}
}

// `exists` says the folder is there; whether a run may use it is decided per
// run. A blocked folder that exists (here the home directory) reports true and
// its runs still go to scratch. Not parallel: it moves HOME.
func TestBlockedDefaultDirectoryReportsExistingButRunsInScratch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "mine.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	var reports []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		reports = append(reports, body)
		_ = json.NewEncoder(w).Encode(map[string]string{"path": home})
	}))
	defer srv.Close()
	syncer := &Daemon{
		client:     NewClient(srv.URL),
		logger:     discardLogger(),
		workspaces: map[string]*workspaceState{"ws": {runtimeIDs: []string{"rt-1"}}},
	}
	if _, exists := syncer.syncDefaultDir(context.Background(), "", nil); exists == nil || !*exists {
		t.Fatal("exists is not true for a home directory that is there")
	}
	if last := reports[len(reports)-1]; last["path"] != home || last["exists"] != true {
		t.Fatalf("last report = %v, want %q reported existing", last, home)
	}

	d, _ := newDefaultDirTestDaemon(t)
	result, err := d.runTask(context.Background(), defaultDirTestTask("task-blocked-home", home, scratchDecision()), "claude", 0, d.logger)
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if !strings.HasPrefix(result.WorkDir, result.EnvRoot) {
		t.Fatalf("WorkDir = %q, want a scratch directory under %q", result.WorkDir, result.EnvRoot)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "mine.txt" {
			t.Errorf("the home directory gained %q", e.Name())
		}
	}
}
