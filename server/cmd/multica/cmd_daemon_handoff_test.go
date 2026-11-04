package main

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

func useTempDaemonProfileRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv(cli.TaskConfigRootEnv, root)
	if err := os.MkdirAll(daemonDirForProfile(""), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDaemonPredecessorEnvRoundTrip(t *testing.T) {
	rec := daemonPortRecord{PID: 4242, Port: 19514}
	got, ok := parseDaemonPredecessor(formatDaemonPredecessor(rec))
	if !ok || got != rec {
		t.Fatalf("round trip = %+v, %v", got, ok)
	}
	for _, bad := range []string{"", "4242", "x:1", "1:x", "0:1", "1:0", "1:70000", "-1:5"} {
		if _, ok := parseDaemonPredecessor(bad); ok {
			t.Errorf("parseDaemonPredecessor(%q) accepted", bad)
		}
	}
}

func TestSwappableWriterRedirectsExistingLoggers(t *testing.T) {
	var first, second bytes.Buffer
	w := &swappableWriter{w: &first}
	_, _ = w.Write([]byte("a"))
	if old := w.swap(&second); old != &first {
		t.Fatal("swap did not return the previous writer")
	}
	_, _ = w.Write([]byte("b"))
	if first.String() != "a" || second.String() != "b" {
		t.Fatalf("first=%q second=%q", first.String(), second.String())
	}
}

// The retiring marker is what tells a successor its predecessor is gone: the
// OS drops the lock when the process dies, however it dies.
func TestPredecessorGoneFollowsRetiringMarker(t *testing.T) {
	useTempDaemonProfileRoot(t)
	pid := 4242
	if !predecessorGone("", pid) {
		t.Fatal("no marker at all should read as gone")
	}
	h := &daemonProcessHandoff{profile: "", pid: pid}
	if err := h.release(nil); err != nil {
		t.Fatalf("release: %v", err)
	}
	if predecessorGone("", pid) {
		t.Fatal("held marker read as gone")
	}
	h.closeAll()
	if !predecessorGone("", pid) {
		t.Fatal("released marker not read as gone")
	}
	if _, err := os.Stat(daemonRetiringMarkerPath("", pid)); !os.IsNotExist(err) {
		t.Fatalf("marker file left behind: %v", err)
	}
}

func TestDaemonProcessHandoffReleaseAndReclaimProfileLock(t *testing.T) {
	useTempDaemonProfileRoot(t)
	lock, acquired, err := tryLockDaemonProfile(daemonProfileLockPath(""))
	if err != nil || !acquired {
		t.Fatalf("initial lock: %v %v", acquired, err)
	}
	h := &daemonProcessHandoff{profile: "", pid: os.Getpid(), port: 19999, lock: lock}
	t.Cleanup(h.closeAll)
	if err := h.release(nil); err != nil {
		t.Fatalf("release: %v", err)
	}
	// A successor can now take the profile.
	succ, acquired, err := tryLockDaemonProfile(daemonProfileLockPath(""))
	if err != nil || !acquired {
		t.Fatalf("successor could not lock the released profile: %v %v", acquired, err)
	}
	if err := h.reclaim(); err == nil {
		t.Fatal("reclaimed a profile the successor holds")
	}
	succ.Close()
	if err := h.reclaim(); err != nil {
		t.Fatalf("reclaim after successor left: %v", err)
	}
	rec, ok := readDaemonPortRecord("")
	if !ok || rec.PID != os.Getpid() || rec.Port != 19999 {
		t.Fatalf("port record after reclaim = %+v %v", rec, ok)
	}
	if data, _ := os.ReadFile(daemonPIDPathForProfile("")); strings.TrimSpace(string(data)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("pid file after reclaim = %q", data)
	}
	if _, err := os.Stat(daemonRetiringMarkerPath("", os.Getpid())); !os.IsNotExist(err) {
		t.Fatalf("marker kept after reclaim: %v", err)
	}
}

func TestRemoveDaemonPIDIfOwnedKeepsSuccessorPID(t *testing.T) {
	useTempDaemonProfileRoot(t)
	path := daemonPIDPathForProfile("")
	if err := os.WriteFile(path, []byte("777"), 0o644); err != nil {
		t.Fatal(err)
	}
	removeDaemonPIDIfOwned("", 555)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("removed the successor's pid file")
	}
	removeDaemonPIDIfOwned("", 777)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("kept its own pid file")
	}
}

func TestRunDaemonHandoffSkipsDaemonThatCannotHandOver(t *testing.T) {
	cmd := &cobra.Command{}
	for _, health := range []map[string]any{
		{"pid": float64(1234)},
		{"pid": float64(1234), "handoff_ready": false},
		{"handoff_ready": true},
	} {
		// Port 1 is never a daemon; reaching it would be an error, not a skip.
		handled, err := runDaemonHandoff(cmd, 1, health)
		if handled || err != nil {
			t.Fatalf("health %v: handled=%v err=%v, want a silent fallback", health, handled, err)
		}
	}
}

func TestRunDaemonHandoffFallsBackWhenRetireRefused(t *testing.T) {
	cmd := &cobra.Command{}
	// Nothing listens on port 1, so the retire request fails before anything
	// changes and the caller keeps stop-then-start.
	handled, err := runDaemonHandoff(cmd, 1, map[string]any{"pid": float64(1234), "handoff_ready": true})
	if handled || err != nil {
		t.Fatalf("handled=%v err=%v, want fallback", handled, err)
	}
}

func TestPrintDaemonStatusShowsHandoff(t *testing.T) {
	t.Parallel()
	base := map[string]any{"status": "running", "pid": float64(1234), "uptime": "1m"}
	var idle bytes.Buffer
	printDaemonStatusReport(&idle, "Daemon", base)
	if strings.Contains(idle.String(), "Retiring") {
		t.Fatalf("status = %q, want no retire rows", idle.String())
	}

	retiring := map[string]any{"status": "running", "pid": float64(1234), "uptime": "1m", "retiring": true, "active_task_count": float64(2)}
	var r bytes.Buffer
	printDaemonStatusReport(&r, "Daemon", retiring)
	if !strings.Contains(r.String(), "Retiring:") || !strings.Contains(r.String(), "finishing 2 run(s)") {
		t.Fatalf("retiring status = %q", r.String())
	}

	succ := map[string]any{"status": "running", "pid": float64(5678), "uptime": "1m",
		"predecessor": map[string]any{"pid": float64(1234), "active_task_count": float64(1), "cli_version": "v0.6.7"}}
	var s bytes.Buffer
	printDaemonStatusReport(&s, "Daemon", succ)
	if !strings.Contains(s.String(), "Retiring predecessor:") || !strings.Contains(s.String(), "pid 1234 (v0.6.7) is finishing 1 run(s)") {
		t.Fatalf("successor status = %q", s.String())
	}
}

// Any daemon that starts while a retiring one still runs finds it, with the
// runs it recorded as it retired — not only the successor the handoff
// launched.
func TestFindRetiringPredecessorReadsRetireRecord(t *testing.T) {
	useTempDaemonProfileRoot(t)
	if _, ok := findRetiringPredecessor("", os.Getpid()); ok {
		t.Fatal("found a predecessor in an empty profile")
	}
	h := &daemonProcessHandoff{profile: "", pid: 4242, port: 19514}
	if err := h.release([]string{"run-1", "run-2"}); err != nil {
		t.Fatalf("release: %v", err)
	}
	rec, ok := findRetiringPredecessor("", os.Getpid())
	if !ok || rec.PID != 4242 || rec.Port != 19514 || strings.Join(rec.HeldTaskIDs, ",") != "run-1,run-2" {
		t.Fatalf("found %+v %v", rec, ok)
	}
	if _, ok := findRetiringPredecessor("", 4242); ok {
		t.Fatal("a daemon found itself as its own predecessor")
	}
	h.closeAll()
	if _, ok := findRetiringPredecessor("", os.Getpid()); ok {
		t.Fatal("found a predecessor that exited")
	}
	if _, err := os.Stat(daemonRetiringRecordPath("", 4242)); !os.IsNotExist(err) {
		t.Fatalf("retire record left behind: %v", err)
	}
}

// A retire with no runs still writes a record, so a successor knows the
// predecessor holds nothing rather than knowing nothing.
func TestRetireRecordWithNoRuns(t *testing.T) {
	useTempDaemonProfileRoot(t)
	h := &daemonProcessHandoff{profile: "", pid: 4243, port: 19515}
	t.Cleanup(h.closeAll)
	if err := h.release(nil); err != nil {
		t.Fatalf("release: %v", err)
	}
	rec, ok := readDaemonRetiringRecord("", 4243)
	if !ok || rec.HeldTaskIDs == nil || len(rec.HeldTaskIDs) != 0 {
		t.Fatalf("record = %+v %v", rec, ok)
	}
}
