//go:build !windows

package processtree

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A group whose only member is an exited, not-yet-reaped process is going
// away, not foreign: darwin answers kill(-pgid) on it with EPERM, and under
// load the same state briefly outlives Wait reaping the leader. finish must
// wait for the group to disappear instead of failing.
func TestFinishWaitsOutAGroupOfOnlyZombies(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	c, err := newController(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, _ := exec.Command("ps", "-o", "stat=", "-p", pid).Output()
		if strings.HasPrefix(strings.TrimSpace(string(out)), "Z") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("leader never became a zombie")
		}
		time.Sleep(10 * time.Millisecond)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = cmd.Wait()
	}()
	if err := c.finish(cmd); err != nil {
		t.Fatalf("finish on a zombie-only group = %v, want nil once it is reaped", err)
	}
}

// EPERM also answers a group whose live members we may not signal. The
// tests below fake that for a running leader: every group signal is refused,
// while signals to the leader's own pid go through.
func startRefusingGroup(t *testing.T) (*exec.Cmd, *controller) {
	t.Helper()
	cmd := exec.Command("/bin/sleep", "30")
	c, err := newController(cmd)
	if err != nil {
		t.Fatal(err)
	}
	c.kill = func(pid int, sig syscall.Signal) error {
		if pid < 0 {
			return syscall.EPERM
		}
		return syscall.Kill(pid, sig)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd, c
}

func TestInterruptReportsALiveGroupThatRefusesSignals(t *testing.T) {
	t.Parallel()

	cmd, c := startRefusingGroup(t)
	if err := c.interrupt(cmd); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("interrupt = %v, want EPERM", err)
	}
}

func TestStopKillsTheLeaderWhenALiveGroupRefusesSignals(t *testing.T) {
	t.Parallel()

	cmd, c := startRefusingGroup(t)
	if err := c.stop(cmd); err != nil {
		t.Fatalf("stop = %v, want the leader killed directly", err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		<-waitDone
		t.Fatal("leader still running after stop; Wait would block")
	}
}

func TestFinishFailsInBoundedTimeWhenALiveGroupRefusesSignals(t *testing.T) {
	t.Parallel()

	cmd, c := startRefusingGroup(t)
	started := time.Now()
	err := c.finish(cmd)
	if !errors.Is(err, syscall.EPERM) {
		t.Fatalf("finish = %v, want an EPERM failure", err)
	}
	if elapsed := time.Since(started); elapsed > processTreeFinishTimeout+2*time.Second {
		t.Fatalf("finish took %s, want it bounded by %s", elapsed, processTreeFinishTimeout)
	}
}
