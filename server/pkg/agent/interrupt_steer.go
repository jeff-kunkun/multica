package agent

// SteersByInterrupt reports whether a provider's runs can stop the step they
// are in, a running command included, and continue the same session with a
// message (DENE-2667). Codex interrupts its turn and starts a new one on the
// same thread; Claude takes an interrupt control request on its open
// stream-json input and then the message as the next user turn. OpenCode 1.x
// ends `opencode run` when its session is aborted, so it is not listed and
// its messages stay supplements. Codex is listed only where the daemon can
// stop the interrupted turn's commands (codexStopsTurnCommands); elsewhere they
// would keep running beside the next turn, so its messages stay supplements.
func SteersByInterrupt(provider string) bool {
	switch provider {
	case "codex":
		return codexStopsTurnCommands
	case "claude":
		return true
	}
	return false
}
