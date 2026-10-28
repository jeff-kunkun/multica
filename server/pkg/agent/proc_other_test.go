//go:build !windows

package agent

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestKillProcessGroupUntilGoneReachesAMemberTheFirstKillMissed(t *testing.T) {
	leader := exec.Command("/bin/sleep", "30")
	configureProcessGroup(leader)
	if err := leader.Start(); err != nil {
		t.Fatalf("start leader: %v", err)
	}
	// The first kill. The leader stays unreaped, so its group lives on.
	signalProcessGroup(leader, syscall.SIGKILL)

	// Stands in for a child forked while that kill was delivered: a live
	// member of the group that the kill never reached.
	missed := exec.Command("/bin/sleep", "30")
	missed.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: leader.Process.Pid}
	if err := missed.Start(); err != nil {
		_ = leader.Wait()
		t.Fatalf("join the leader's group: %v", err)
	}
	missedDone := make(chan struct{})
	go func() {
		_ = missed.Wait()
		close(missedDone)
	}()

	killed := make(chan struct{})
	go func() {
		killProcessGroupUntilGone(leader, 5*time.Second)
		close(killed)
	}()

	select {
	case <-missedDone:
	case <-time.After(2 * time.Second):
		_ = missed.Process.Kill()
		<-missedDone
		_ = leader.Wait()
		<-killed
		t.Fatal("a group member the first kill missed is still running")
	}
	_ = leader.Wait()
	select {
	case <-killed:
	case <-time.After(2 * time.Second):
		t.Fatal("still killing after the group was gone")
	}
}

func TestKillProcessGroupUntilGoneSendsNothingAfterTheTimeout(t *testing.T) {
	leader := exec.Command("/bin/sleep", "30")
	configureProcessGroup(leader)
	if err := leader.Start(); err != nil {
		t.Fatalf("start leader: %v", err)
	}
	done := make(chan struct{})
	go func() {
		_ = leader.Wait()
		close(done)
	}()
	defer func() {
		_ = leader.Process.Kill()
		<-done
	}()

	// The timeout ends inside the first 10ms sleep, so no send is allowed.
	killProcessGroupUntilGone(leader, 5*time.Millisecond)

	select {
	case <-done:
		t.Fatal("killed the group after the timeout had passed")
	case <-time.After(200 * time.Millisecond):
	}
}
