package daemon

import (
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/pkg/agent"
)

// agentPermissionLogFields lists the permissions the daemon pins for Codex,
// Claude, and OpenCode on goos, so one startup line shows what every task of
// those CLIs runs with. Codex's sandbox mode reflects the shared Codex config,
// so a Windows windows.sandbox opt-in shows as workspace-write.
func agentPermissionLogFields(goos string) []any {
	return []any{
		"codex_sandbox_mode", execenv.CodexStartupSandboxMode(goos),
		"codex_approvals", agent.CodexApprovals,
		"claude_permission_mode", agent.ClaudePermissionMode,
		"opencode_permissions", agent.OpenCodeSkipPermissionsFlag,
	}
}
