package agent

// The permission settings the daemon pins on every run. Custom args cannot
// override them (both flags are blocked in the providers' arg filters), so
// these values are what each CLI actually runs with. The daemon logs them at
// startup.
const (
	// ClaudePermissionMode is the --permission-mode value on every Claude run.
	ClaudePermissionMode = "bypassPermissions"
	// OpenCodeSkipPermissionsFlag is passed on every OpenCode run so no
	// permission prompt can stall a non-interactive task.
	OpenCodeSkipPermissionsFlag = "--dangerously-skip-permissions"
	// CodexApprovals is how the daemon answers Codex approval requests: every
	// command, patch, and permission request is accepted (see
	// handleServerRequest in codex.go).
	CodexApprovals = "auto-accept"
)
