//go:build !linux && !darwin

package agent

// codexStopsTurnCommands is false here: nothing ends an interrupted turn's
// commands, so Codex runs do not offer interrupt and keep supplements.
const codexStopsTurnCommands = false

// stopCodexTurnCommands is Unix-only: elsewhere an interrupted turn's command
// runs until Codex itself ends it.
func stopCodexTurnCommands(int) int { return 0 }
