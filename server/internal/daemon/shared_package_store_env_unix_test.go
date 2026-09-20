//go:build !windows

package daemon

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// runTaskCapturingEnv runs one task through the real runTask path with a fake
// agent binary that dumps its own environment, and returns that environment.
//
// The assertion this supports cannot be made anywhere cheaper: the point of the
// shared store is that the process the agent actually spawns `pnpm install`
// from sees the store, and the environment reaches that process only through
// runTask's assembly. Removing the injection from runTask must fail a test —
// a unit test over PreparePackageStore alone would stay green.
func runTaskCapturingEnv(t *testing.T, enabled bool, customEnv map[string]string) map[string]string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	envDump := filepath.Join(t.TempDir(), "env.txt")
	fakeBin := filepath.Join(t.TempDir(), "pi")
	script := `#!/bin/sh
cat > /dev/null
env > ` + envDump + `
printf '%s\n' '{"type":"agent_start"}'
printf '%s\n' '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"done"}}'
printf '%s\n' '{"type":"turn_end","message":{"role":"assistant","model":"test","usage":{"input":1,"output":1}}}'
`
	if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake agent: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := &Daemon{
		client:         NewClient(srv.URL),
		logger:         logger,
		workspaces:     make(map[string]*workspaceState),
		runtimeIndex:   map[string]Runtime{"rt-pkg": {ID: "rt-pkg", Provider: "pi"}},
		activeEnvRoots: make(map[string]int),
		cfg: Config{
			WorkspacesRoot:            t.TempDir(),
			AgentTimeout:              15 * time.Second,
			ServerBaseURL:             srv.URL,
			SharedPackageStoreEnabled: enabled,
			Agents:                    map[string]AgentEntry{"pi": {Path: fakeBin}},
		},
	}
	task := Task{
		ID:          "task-pkg-store",
		WorkspaceID: "ws-pkg",
		RuntimeID:   "rt-pkg",
		IssueID:     "issue-pkg",
		AgentID:     "agent-pkg",
		AuthToken:   "mat_pkg_store",
		Agent:       &AgentData{ID: "agent-pkg", Name: "pkg-agent", CustomEnv: customEnv},
	}

	if _, err := d.runTask(context.Background(), task, "pi", 0, logger); err != nil {
		t.Fatalf("runTask: %v", err)
	}

	raw, err := os.ReadFile(envDump)
	if err != nil {
		t.Fatalf("agent never recorded its environment: %v", err)
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			env[key] = value
		}
	}
	// Keep the root reachable for the caller's path assertions.
	env["__workspaces_root"] = d.cfg.WorkspacesRoot
	return env
}

func TestTaskEnvironmentPointsPackageManagersAtTheSharedStore(t *testing.T) {
	env := runTaskCapturingEnv(t, true, nil)
	root := env["__workspaces_root"]

	storeRoot := execenv.PackageStoreRoot(root)
	for _, key := range execenv.PackageStoreEnvKeys() {
		got := env[key]
		if got == "" {
			t.Errorf("%s is unset in the agent environment; that ecosystem still installs a private copy per task", key)
			continue
		}
		if !strings.HasPrefix(got, storeRoot+string(os.PathSeparator)) {
			t.Errorf("%s = %q, want a path under the shared store %q", key, got, storeRoot)
		}
	}
	if got := env["npm_config_store_dir"]; got != execenv.PnpmStoreDir(root) {
		t.Errorf("npm_config_store_dir = %q, want %q", got, execenv.PnpmStoreDir(root))
	}
}

// The store is a convenience, not a security boundary: an agent that genuinely
// needs its own is allowed to say so, which is why the injection happens before
// custom_env is layered on.
func TestAgentCustomEnvCanOverrideTheSharedStore(t *testing.T) {
	private := filepath.Join(t.TempDir(), "private-store")
	env := runTaskCapturingEnv(t, true, map[string]string{"npm_config_store_dir": private})

	if got := env["npm_config_store_dir"]; got != private {
		t.Errorf("npm_config_store_dir = %q, want the agent's own %q", got, private)
	}
	// Everything the agent did not override still points at the shared store.
	if got := env["GOMODCACHE"]; !strings.HasPrefix(got, execenv.PackageStoreRoot(env["__workspaces_root"])) {
		t.Errorf("GOMODCACHE = %q, want it left on the shared store", got)
	}
}

// MULTICA_SHARED_PACKAGE_STORE=0 has to be a real kill switch: with the store
// off, no task environment may name it and the directory is never created.
func TestSharedPackageStoreCanBeTurnedOff(t *testing.T) {
	env := runTaskCapturingEnv(t, false, nil)
	root := env["__workspaces_root"]

	for _, key := range execenv.PackageStoreEnvKeys() {
		if value, ok := env[key]; ok && strings.HasPrefix(value, execenv.PackageStoreRoot(root)) {
			t.Errorf("%s = %q, want the shared store left out entirely", key, value)
		}
	}
	if _, err := os.Stat(execenv.PackageStoreRoot(root)); !os.IsNotExist(err) {
		t.Errorf("store directory created while disabled (stat err = %v)", err)
	}
}
