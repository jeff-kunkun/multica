//go:build linux || darwin

package agent

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// The test process stands in for the Codex app-server: a command runs in its
// own session, an MCP server only in its own process group.
func TestStopCodexTurnCommandsStopsCommandsAndSparesMCPServers(t *testing.T) {
	start := func(attr *syscall.SysProcAttr) *exec.Cmd {
		cmd := exec.Command("sleep", "30")
		cmd.SysProcAttr = attr
		if err := cmd.Start(); err != nil {
			t.Fatalf("start: %v", err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd
	}
	command := start(&syscall.SysProcAttr{Setsid: true})
	mcpServer := start(&syscall.SysProcAttr{Setpgid: true})
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()

	if n := stopCodexTurnCommands(os.Getpid()); n != 1 {
		t.Fatalf("stopped %d commands, want 1", n)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn's command is still running")
	}
	if err := mcpServer.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("the MCP server was stopped too: %v", err)
	}
}
