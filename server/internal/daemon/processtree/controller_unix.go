//go:build !windows

package processtree

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const processTreeFinishTimeout = 5 * time.Second

type controller struct {
	// kill is syscall.Kill; tests replace it to fake a group that refuses
	// signals.
	kill func(pid int, sig syscall.Signal) error
}

func newController(cmd *exec.Cmd) (*controller, error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	return &controller{kill: syscall.Kill}, nil
}

func (*controller) attach(_ *exec.Cmd) error { return nil }

func (c *controller) interrupt(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	if err := c.kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	return nil
}

func (c *controller) stop(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	if err := c.kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return cmd.Process.Kill()
	}
	return nil
}

func (c *controller) finish(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	if err := c.kill(-pid, 0); errors.Is(err, syscall.ESRCH) {
		return nil
	}
	// A normally-exited leader can still leave a descendant holding inherited
	// pipes or repository locks. Kill the remaining group before returning
	// ownership of the repository to another operation.
	//
	// Darwin answers EPERM, not ESRCH, while every member is an exited process
	// awaiting its reap, and under load that state briefly outlives Wait
	// reaping the leader. EPERM can equally mean a live member we may not
	// signal, so it only passes if the poll below sees the group disappear.
	killErr := c.kill(-pid, syscall.SIGKILL)
	if killErr != nil && !errors.Is(killErr, syscall.ESRCH) && !errors.Is(killErr, syscall.EPERM) {
		return killErr
	}
	deadline := time.Now().Add(processTreeFinishTimeout)
	for time.Now().Before(deadline) {
		if err := c.kill(-pid, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	if errors.Is(killErr, syscall.EPERM) {
		return fmt.Errorf("process group %d still refuses signals after %s: %w", pid, processTreeFinishTimeout, killErr)
	}
	return fmt.Errorf("process group %d still active after %s", pid, processTreeFinishTimeout)
}

func (*controller) close() {}
