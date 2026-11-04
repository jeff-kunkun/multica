package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	keptWorkspaceID     = "11111111-1111-4111-8111-111111111111"
	excludedWorkspaceID = "22222222-2222-4222-8222-222222222222"
)

// The excluded workspace gets no Register call — neither its built-ins nor its
// custom runtime profile — while the other workspace registers exactly as
// before. With no runtime there, the claim payload (allRuntimeIDs) can never
// name one of its tasks.
func TestSyncWorkspaces_SkipsExcludedWorkspace(t *testing.T) {
	fx := newBatchFixture(t)
	stubLookPath(t, map[string]string{"company-codex": "/opt/bin/company-codex"})
	d := fx.daemon
	d.cfg.Agents = map[string]AgentEntry{"claude": {Path: "/fake/claude"}}
	d.cfg.ExcludeWorkspaceIDs = []string{excludedWorkspaceID}
	fx.setWorkspaces(
		WorkspaceInfo{ID: keptWorkspaceID, Name: "kept"},
		WorkspaceInfo{ID: excludedWorkspaceID, Name: "excluded"},
	)
	fx.profiles[excludedWorkspaceID] = []RuntimeProfile{{
		ID: "prof-wd", WorkspaceID: excludedWorkspaceID, DisplayName: "WD Codex",
		ProtocolFamily: "codex", CommandName: "company-codex",
		Visibility: "workspace", Enabled: true,
	}}

	if err := d.syncWorkspacesFromAPI(context.Background(), false); err != nil {
		t.Fatalf("syncWorkspacesFromAPI: %v", err)
	}

	if types, calls := fx.registrationFor(excludedWorkspaceID); calls != 0 {
		t.Fatalf("excluded workspace registered %d time(s) with %v, want none", calls, types)
	}
	if types, calls := fx.registrationFor(keptWorkspaceID); calls != 1 || len(types) != 1 || types[0] != "claude" {
		t.Fatalf("kept workspace registered %v in %d call(s), want [claude] once", types, calls)
	}

	d.mu.Lock()
	_, tracked := d.workspaces[excludedWorkspaceID]
	keptState := d.workspaces[keptWorkspaceID]
	d.mu.Unlock()
	if tracked {
		t.Fatal("excluded workspace is tracked; the daemon would heartbeat and claim for it")
	}
	if keptState == nil {
		t.Fatal("kept workspace is not tracked")
	}
	if got, want := len(d.allRuntimeIDs()), len(keptState.runtimeIDs); got != want || got == 0 {
		t.Fatalf("allRuntimeIDs has %d runtime(s), want only the kept workspace's %d", got, want)
	}

	// A later periodic sync keeps leaving it alone.
	if err := d.syncWorkspacesFromAPI(context.Background(), false); err != nil {
		t.Fatalf("second syncWorkspacesFromAPI: %v", err)
	}
	if _, calls := fx.registrationFor(excludedWorkspaceID); calls != 0 {
		t.Fatalf("excluded workspace registered on the second sync")
	}
}

// IDs are matched case-insensitively, so an upper-case entry still excludes
// the workspace the server names in lower case.
func TestSyncWorkspaces_ExclusionIgnoresCase(t *testing.T) {
	fx := newBatchFixture(t)
	d := fx.daemon
	d.cfg.Agents = map[string]AgentEntry{"claude": {Path: "/fake/claude"}}
	d.cfg.ExcludeWorkspaceIDs = []string{strings.ToUpper(excludedWorkspaceID)}
	fx.setWorkspaces(
		WorkspaceInfo{ID: keptWorkspaceID, Name: "kept"},
		WorkspaceInfo{ID: excludedWorkspaceID, Name: "excluded"},
	)

	if err := d.syncWorkspacesFromAPI(context.Background(), false); err != nil {
		t.Fatalf("syncWorkspacesFromAPI: %v", err)
	}
	if _, calls := fx.registrationFor(excludedWorkspaceID); calls != 0 {
		t.Fatal("upper-case exclusion entry did not exclude the workspace")
	}
}

// A daemon whose every workspace is excluded registered nothing on purpose; that
// must not read as the "no runtime registered" startup failure.
func TestSyncWorkspaces_AllExcludedIsNotAStartupFailure(t *testing.T) {
	fx := newBatchFixture(t)
	d := fx.daemon
	d.cfg.Agents = map[string]AgentEntry{"claude": {Path: "/fake/claude"}}
	d.cfg.ExcludeWorkspaceIDs = []string{excludedWorkspaceID}
	fx.setWorkspaces(WorkspaceInfo{ID: excludedWorkspaceID, Name: "excluded"})

	if err := d.syncWorkspacesFromAPI(context.Background(), false); err != nil {
		t.Fatalf("syncWorkspacesFromAPI = %v, want nil when every workspace is excluded", err)
	}
	if got := fx.registerCallCount(); got != 0 {
		t.Fatalf("got %d Register calls, want 0", got)
	}
}

// A workspace that was tracked when the exclusion took effect is dropped, with
// its runtimes, like a workspace the account left.
func TestSyncWorkspaces_DropsTrackedWorkspaceOnceExcluded(t *testing.T) {
	fx := newBatchFixture(t)
	d := fx.daemon
	d.cfg.Agents = map[string]AgentEntry{"claude": {Path: "/fake/claude"}}
	fx.setWorkspaces(
		WorkspaceInfo{ID: keptWorkspaceID, Name: "kept"},
		WorkspaceInfo{ID: excludedWorkspaceID, Name: "excluded"},
	)
	if err := d.syncWorkspacesFromAPI(context.Background(), false); err != nil {
		t.Fatalf("first syncWorkspacesFromAPI: %v", err)
	}
	d.mu.Lock()
	dropped := append([]string(nil), d.workspaces[excludedWorkspaceID].runtimeIDs...)
	d.mu.Unlock()
	if len(dropped) == 0 {
		t.Fatal("setup: excluded workspace registered no runtime before the exclusion")
	}

	d.cfg.ExcludeWorkspaceIDs = []string{excludedWorkspaceID}
	if err := d.syncWorkspacesFromAPI(context.Background(), false); err != nil {
		t.Fatalf("second syncWorkspacesFromAPI: %v", err)
	}

	d.mu.Lock()
	_, tracked := d.workspaces[excludedWorkspaceID]
	_, keptTracked := d.workspaces[keptWorkspaceID]
	var stillIndexed []string
	for _, rid := range dropped {
		if _, ok := d.runtimeIndex[rid]; ok {
			stillIndexed = append(stillIndexed, rid)
		}
	}
	d.mu.Unlock()
	if tracked || len(stillIndexed) > 0 {
		t.Fatalf("excluded workspace still tracked=%v, indexed runtimes %v", tracked, stillIndexed)
	}
	if !keptTracked {
		t.Fatal("kept workspace was dropped too")
	}
}

// The runtime-profiles WS hint for an excluded workspace is ignored outright:
// no request reaches the server for it, while the same hint for another
// workspace still fetches that workspace's profiles.
func TestRuntimeProfilesHintIgnoresExcludedWorkspace(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"runtime_profiles":[]}`))
	}))
	t.Cleanup(srv.Close)
	d := freshDaemon(srv.URL)
	d.cfg.ExcludeWorkspaceIDs = []string{excludedWorkspaceID}

	d.handleRuntimeProfilesChanged(protocol.RuntimeProfilesChangedPayload{WorkspaceID: excludedWorkspaceID})
	mu.Lock()
	excludedRequests := len(paths)
	mu.Unlock()
	if excludedRequests != 0 {
		t.Fatalf("hint for the excluded workspace sent %d request(s) %v, want none", excludedRequests, paths)
	}

	d.handleRuntimeProfilesChanged(protocol.RuntimeProfilesChangedPayload{WorkspaceID: keptWorkspaceID})
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 || !strings.Contains(paths[0], keptWorkspaceID) {
		t.Fatalf("hint for a served workspace sent %v, want one profile fetch for it", paths)
	}
}

func TestNormalizeWorkspaceIDList(t *testing.T) {
	t.Parallel()

	got, err := NormalizeWorkspaceIDList([]string{
		" " + strings.ToUpper(excludedWorkspaceID) + " ", "", keptWorkspaceID, excludedWorkspaceID,
	})
	if err != nil {
		t.Fatalf("NormalizeWorkspaceIDList: %v", err)
	}
	if len(got) != 2 || got[0] != excludedWorkspaceID || got[1] != keptWorkspaceID {
		t.Fatalf("NormalizeWorkspaceIDList = %v, want [%s %s]", got, excludedWorkspaceID, keptWorkspaceID)
	}

	for _, bad := range []string{"wd-game", "{" + keptWorkspaceID + "}", strings.ReplaceAll(keptWorkspaceID, "-", "")} {
		if _, err := NormalizeWorkspaceIDList([]string{bad}); err == nil {
			t.Errorf("NormalizeWorkspaceIDList(%q) accepted a non-UUID", bad)
		}
	}

	if got, err := NormalizeWorkspaceIDList(nil); err != nil || got != nil {
		t.Fatalf("NormalizeWorkspaceIDList(nil) = %v, %v; want nil, nil", got, err)
	}
}

// config.json supplies the list; the env var replaces it; a blank env var
// counts as unset; a malformed entry from either source fails LoadConfig.
func TestLoadConfig_ExcludeWorkspaceIDsPrecedence(t *testing.T) {
	stageFakeAgent(t)
	redirectTestHome(t, t.TempDir())
	t.Setenv("SHELL", filepath.Join(t.TempDir(), "missing-shell"))
	t.Setenv(ExcludeWorkspaceIDsEnv, "")
	base := Overrides{ServerURL: "http://localhost:0", WorkspacesRoot: t.TempDir(), Profile: "office"}

	cfg, err := LoadConfig(base)
	if err != nil {
		t.Fatalf("LoadConfig default: %v", err)
	}
	if len(cfg.ExcludeWorkspaceIDs) != 0 {
		t.Fatalf("default ExcludeWorkspaceIDs = %v, want none", cfg.ExcludeWorkspaceIDs)
	}

	if err := cli.SaveCLIConfigForProfile(cli.CLIConfig{
		ServerURL:           "http://localhost:0",
		ExcludeWorkspaceIDs: []string{strings.ToUpper(excludedWorkspaceID)},
	}, "office"); err != nil {
		t.Fatalf("SaveCLIConfigForProfile: %v", err)
	}
	cfg, err = LoadConfig(base)
	if err != nil {
		t.Fatalf("LoadConfig file: %v", err)
	}
	if len(cfg.ExcludeWorkspaceIDs) != 1 || cfg.ExcludeWorkspaceIDs[0] != excludedWorkspaceID {
		t.Fatalf("file ExcludeWorkspaceIDs = %v, want [%s]", cfg.ExcludeWorkspaceIDs, excludedWorkspaceID)
	}

	t.Setenv(ExcludeWorkspaceIDsEnv, "  ")
	cfg, err = LoadConfig(base)
	if err != nil {
		t.Fatalf("LoadConfig blank env: %v", err)
	}
	if len(cfg.ExcludeWorkspaceIDs) != 1 || cfg.ExcludeWorkspaceIDs[0] != excludedWorkspaceID {
		t.Fatalf("blank env ExcludeWorkspaceIDs = %v, want the config.json list", cfg.ExcludeWorkspaceIDs)
	}

	t.Setenv(ExcludeWorkspaceIDsEnv, keptWorkspaceID+", ")
	cfg, err = LoadConfig(base)
	if err != nil {
		t.Fatalf("LoadConfig env: %v", err)
	}
	if len(cfg.ExcludeWorkspaceIDs) != 1 || cfg.ExcludeWorkspaceIDs[0] != keptWorkspaceID {
		t.Fatalf("env ExcludeWorkspaceIDs = %v, want [%s] (env replaces config.json)", cfg.ExcludeWorkspaceIDs, keptWorkspaceID)
	}

	t.Setenv(ExcludeWorkspaceIDsEnv, "wd-game")
	if _, err := LoadConfig(base); err == nil || !strings.Contains(err.Error(), ExcludeWorkspaceIDsEnv) {
		t.Fatalf("LoadConfig bad env = %v, want an error naming %s", err, ExcludeWorkspaceIDsEnv)
	}

	t.Setenv(ExcludeWorkspaceIDsEnv, "")
	if err := cli.SaveCLIConfigForProfile(cli.CLIConfig{
		ServerURL:           "http://localhost:0",
		ExcludeWorkspaceIDs: []string{"not-a-uuid"},
	}, "office"); err != nil {
		t.Fatalf("SaveCLIConfigForProfile: %v", err)
	}
	if _, err := LoadConfig(base); err == nil || !strings.Contains(err.Error(), "exclude_workspace_ids") {
		t.Fatalf("LoadConfig bad config.json = %v, want an error naming exclude_workspace_ids", err)
	}
}
