package daemon

import "testing"

func TestApplyDshTaskEnvDefaultsToFullAccess(t *testing.T) {
	agentEnv := map[string]string{}
	applyDshTaskEnv(agentEnv, "/tmp/session-root")

	if got := agentEnv["DSH_PERMISSION_MODE"]; got != "danger-full-access" {
		t.Fatalf("DSH_PERMISSION_MODE = %q, want danger-full-access", got)
	}
	if got := agentEnv["MULTICA_DSH_SESSION_ROOT"]; got != "/tmp/session-root" {
		t.Fatalf("MULTICA_DSH_SESSION_ROOT = %q, want /tmp/session-root", got)
	}
	if got := agentEnv["DSH_TELEMETRY_DISABLED"]; got != "1" {
		t.Fatalf("DSH_TELEMETRY_DISABLED = %q, want 1", got)
	}
}

func TestApplyDshTaskEnvKeepsCustomPermissionMode(t *testing.T) {
	// custom_env is layered before applyDshTaskEnv runs, so an agent that
	// wants the sandbox back sets DSH_PERMISSION_MODE and must keep it.
	agentEnv := map[string]string{"DSH_PERMISSION_MODE": "workspace-write"}
	applyDshTaskEnv(agentEnv, "/tmp/session-root")

	if got := agentEnv["DSH_PERMISSION_MODE"]; got != "workspace-write" {
		t.Fatalf("DSH_PERMISSION_MODE = %q, want the custom_env value workspace-write", got)
	}
}

func TestApplyDshTaskEnvOverridesDaemonOwnedKeys(t *testing.T) {
	// The session root and telemetry switch are daemon-owned, not defaults.
	agentEnv := map[string]string{"DSH_TELEMETRY_DISABLED": "0"}
	applyDshTaskEnv(agentEnv, "/tmp/session-root")

	if got := agentEnv["DSH_TELEMETRY_DISABLED"]; got != "1" {
		t.Fatalf("DSH_TELEMETRY_DISABLED = %q, want daemon-owned 1", got)
	}
}

func TestDshPermissionModeIsNotBlocklisted(t *testing.T) {
	// A blocked key would be dropped from custom_env, making the override
	// in TestApplyDshTaskEnvKeepsCustomPermissionMode unreachable in prod.
	if isBlockedEnvKey("DSH_PERMISSION_MODE") {
		t.Fatal("DSH_PERMISSION_MODE is blocklisted; agents could not opt back into the sandbox")
	}
}
