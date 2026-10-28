package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAgentPermissionLogFieldsAreFullAccess(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	want := map[string]string{
		"codex_sandbox_mode":     "danger-full-access",
		"codex_approvals":        "auto-accept",
		"claude_permission_mode": "bypassPermissions",
		"opencode_permissions":   "--dangerously-skip-permissions",
	}
	for _, goos := range []string{"darwin", "linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			assertPermissionLogFields(t, agentPermissionLogFields(goos), want)
		})
	}
}

// A Windows user who opted into Codex's native sandbox gets workspace-write
// tasks, and the startup line must say so rather than the full-access default.
func TestAgentPermissionLogFieldsReportWindowsNativeSandbox(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[windows]\nsandbox = \"unelevated\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", home)
	assertPermissionLogFields(t, agentPermissionLogFields("windows"), map[string]string{
		"codex_sandbox_mode":     "workspace-write",
		"codex_approvals":        "auto-accept",
		"claude_permission_mode": "bypassPermissions",
		"opencode_permissions":   "--dangerously-skip-permissions",
	})
	// macOS and Linux ignore windows.sandbox, as their tasks do.
	if got := agentPermissionLogFields("darwin")[1]; got != "danger-full-access" {
		t.Errorf("darwin codex_sandbox_mode = %v, want danger-full-access", got)
	}
}

func assertPermissionLogFields(t *testing.T, fields []any, want map[string]string) {
	t.Helper()
	if len(fields) != 2*len(want) {
		t.Fatalf("got %d fields, want %d: %v", len(fields), 2*len(want), fields)
	}
	for i := 0; i < len(fields); i += 2 {
		key, _ := fields[i].(string)
		value, _ := fields[i+1].(string)
		if want[key] != value {
			t.Errorf("%s = %q, want %q", key, value, want[key])
		}
	}
}
