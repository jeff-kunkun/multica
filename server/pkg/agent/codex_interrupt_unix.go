//go:build linux || darwin

package agent

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// codexStopsTurnCommands reports that this platform can end an interrupted
// turn's commands, so Codex runs may offer interrupt.
const codexStopsTurnCommands = true

// stopCodexTurnCommands ends the shell commands an interrupted Codex turn
// left running and returns how many it stopped (DENE-2667). Codex 0.164
// reports the turn interrupted but leaves its commands alive until the
// app-server exits. It starts every command in a session of its own, while its
// MCP servers are only process-group leaders, so the session leaders among the
// app-server's children are exactly its commands; the MCP servers survive.
func stopCodexTurnCommands(appServerPID int) int {
	all, err := listCursorUnixProcessInfos()
	if err != nil {
		return 0
	}
	stopped := 0
	for _, info := range all {
		if info.ppid != appServerPID || info.zombie || info.pgid != info.pid {
			continue
		}
		if sid, err := unix.Getsid(info.pid); err != nil || sid != info.pid {
			continue
		}
		// Re-read right before signalling: the snapshot's pid must still be
		// the same command, not a reused number.
		current, err := readCursorUnixProcessInfo(info.pid)
		if err != nil || current.start != info.start || current.ppid != appServerPID || current.pgid != info.pid {
			continue
		}
		if syscall.Kill(-info.pid, syscall.SIGKILL) == nil {
			stopped++
		}
	}
	return stopped
}
