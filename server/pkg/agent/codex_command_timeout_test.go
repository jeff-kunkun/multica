package agent

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

// fastCodexShutdown keeps a stopped fixture from waiting out the graceful
// shutdown budget. It sets a package global, so these tests are not parallel.
func fastCodexShutdown(t *testing.T) {
	codexGracefulShutdownTimeoutNanos.Store(int64(100 * time.Millisecond))
	t.Cleanup(func() { codexGracefulShutdownTimeoutNanos.Store(0) })
}

// codexTurnScript is a fake app-server that completes the handshake, starts
// a turn and then plays body.
func codexTurnScript(body string) string {
	return "" +
		`read line` + "\n" +
		`echo '{"jsonrpc":"2.0","id":1,"result":{}}'` + "\n" +
		`read line` + "\n" +
		`read line` + "\n" +
		`echo '{"jsonrpc":"2.0","id":2,"result":{"thread":{"id":"thr-cmd"}}}'` + "\n" +
		`read line` + "\n" +
		`echo '{"jsonrpc":"2.0","id":3,"result":{}}'` + "\n" +
		`echo '{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"thr-cmd","turn":{"id":"turn-cmd"}}}'` + "\n" +
		body
}

const (
	codexCommandStarted   = `echo '{"jsonrpc":"2.0","method":"item/started","params":{"threadId":"thr-cmd","item":{"type":"commandExecution","id":"cmd-1","command":"make build"}}}'` + "\n"
	codexCommandCompleted = `echo '{"jsonrpc":"2.0","method":"item/completed","params":{"threadId":"thr-cmd","item":{"type":"commandExecution","id":"cmd-1","aggregatedOutput":"built"}}}'` + "\n"
	codexTurnCompleted    = `echo '{"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":"thr-cmd","turn":{"id":"turn-cmd","status":"completed"}}}'` + "\n"
)

// A command that prints nothing for longer than the semantic timeout is not a
// stalled model: the timer waits the command budget while it runs (DENE-2900).
func TestCodexSemanticTimerWaitsForSilentCommand(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	fakePath := writeFakeCodexAppServer(t, codexTurnScript(
		codexCommandStarted+`sleep 0.4`+"\n"+codexCommandCompleted+codexTurnCompleted))

	result := executeFakeCodex(t, fakePath, ExecOptions{
		Timeout:                   10 * time.Second,
		SemanticInactivityTimeout: 100 * time.Millisecond,
		SemanticCommandTimeout:    3 * time.Second,
	})
	if result.Status != "completed" {
		t.Fatalf("a silent 0.4s command under a 3s command budget was stopped: status=%q error=%q", result.Status, result.Error)
	}
}

// A command silent past the command budget still ends the run, and the error
// says a command was running and which budget ran out.
func TestCodexSemanticTimerStopsCommandPastItsBudget(t *testing.T) {
	fastCodexShutdown(t)
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	fakePath := writeFakeCodexAppServer(t, codexTurnScript(codexCommandStarted+`sleep 3`+"\n"))

	started := time.Now()
	result := executeFakeCodex(t, fakePath, ExecOptions{
		Timeout:                   15 * time.Second,
		SemanticInactivityTimeout: 100 * time.Millisecond,
		SemanticCommandTimeout:    400 * time.Millisecond,
	})
	if result.Status != "timeout" {
		t.Fatalf("status = %q, error = %q; want timeout", result.Status, result.Error)
	}
	for _, want := range []string{CodexSemanticInactivityMarker + " after 400ms", "a command was still running"} {
		if !strings.Contains(result.Error, want) {
			t.Fatalf("error = %q, want it to contain %q", result.Error, want)
		}
	}
	if elapsed := time.Since(started); elapsed > 2500*time.Millisecond {
		t.Fatalf("stop took %s", elapsed)
	}
}

// Once the command finished, a silent model is stopped on the semantic
// timeout again, not the command budget.
func TestCodexSemanticTimerStopsSilentModelAfterCommand(t *testing.T) {
	fastCodexShutdown(t)
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	fakePath := writeFakeCodexAppServer(t, codexTurnScript(
		codexCommandStarted+codexCommandCompleted+`sleep 3`+"\n"))

	started := time.Now()
	result := executeFakeCodex(t, fakePath, ExecOptions{
		Timeout:                   15 * time.Second,
		SemanticInactivityTimeout: 150 * time.Millisecond,
		SemanticCommandTimeout:    8 * time.Second,
	})
	if result.Status != "timeout" || !strings.Contains(result.Error, CodexSemanticInactivityMarker+" after 150ms") {
		t.Fatalf("status = %q, error = %q; want the semantic timeout", result.Status, result.Error)
	}
	if strings.Contains(result.Error, "a command was still running") {
		t.Fatalf("a finished command was reported as running: %q", result.Error)
	}
	if elapsed := time.Since(started); elapsed > 2500*time.Millisecond {
		t.Fatalf("a silent model waited the command budget: %s", elapsed)
	}
}

// Without a command budget the timer behaves as before: a running command
// gets the semantic timeout like everything else.
func TestCodexSemanticTimerWithoutCommandBudgetKeepsOldBehaviour(t *testing.T) {
	fastCodexShutdown(t)
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	fakePath := writeFakeCodexAppServer(t, codexTurnScript(codexCommandStarted+`sleep 3`+"\n"))

	result := executeFakeCodex(t, fakePath, ExecOptions{
		Timeout:                   15 * time.Second,
		SemanticInactivityTimeout: 150 * time.Millisecond,
	})
	if result.Status != "timeout" || !strings.Contains(result.Error, CodexSemanticInactivityMarker+" after 150ms") {
		t.Fatalf("status = %q, error = %q; want the semantic timeout", result.Status, result.Error)
	}
}

// The cap counts from the command's start: other calls starting and finishing
// beside it do not give it more time (Codex's DENE-2900 review).
func TestCodexSemanticTimerCapsCommandBesideOtherActivity(t *testing.T) {
	fastCodexShutdown(t)
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	otherCalls := `for i in $(seq 1 40); do` + "\n" +
		`echo '{"jsonrpc":"2.0","method":"item/started","params":{"threadId":"thr-cmd","item":{"type":"commandExecution","id":"other-'"$i"'","command":"git status"}}}'` + "\n" +
		`echo '{"jsonrpc":"2.0","method":"item/completed","params":{"threadId":"thr-cmd","item":{"type":"commandExecution","id":"other-'"$i"'","aggregatedOutput":"clean"}}}'` + "\n" +
		`sleep 0.1` + "\n" +
		`done` + "\n"
	fakePath := writeFakeCodexAppServer(t, codexTurnScript(codexCommandStarted+otherCalls))

	started := time.Now()
	result := executeFakeCodex(t, fakePath, ExecOptions{
		Timeout:                   15 * time.Second,
		SemanticInactivityTimeout: 100 * time.Millisecond,
		SemanticCommandTimeout:    600 * time.Millisecond,
	})
	if result.Status != "timeout" || !strings.Contains(result.Error, CodexSemanticInactivityMarker+" after 600ms") ||
		!strings.Contains(result.Error, "a command was still running") {
		t.Fatalf("status = %q, error = %q; want the command cap", result.Status, result.Error)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("the other calls kept the long command alive for %s", elapsed)
	}
}

// A cap below the semantic timeout is still the cap.
func TestCodexSemanticTimerCapBelowSemanticTimeout(t *testing.T) {
	fastCodexShutdown(t)
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	fakePath := writeFakeCodexAppServer(t, codexTurnScript(codexCommandStarted+`sleep 4`+"\n"))

	started := time.Now()
	result := executeFakeCodex(t, fakePath, ExecOptions{
		Timeout:                   15 * time.Second,
		SemanticInactivityTimeout: 10 * time.Second,
		SemanticCommandTimeout:    300 * time.Millisecond,
	})
	if result.Status != "timeout" || !strings.Contains(result.Error, CodexSemanticInactivityMarker+" after 300ms") ||
		!strings.Contains(result.Error, "a command was still running") {
		t.Fatalf("status = %q, error = %q; want the command cap", result.Status, result.Error)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("stop took %s", elapsed)
	}
}

func TestCodexRunningCallsTracksToolMessages(t *testing.T) {
	t.Parallel()
	var r codexRunningCalls
	running := func() bool { _, ok := r.oldest(); return ok }
	r.observe(Message{Type: MessageToolUse}) // no call id: ignored
	if running() {
		t.Fatal("a call without an id was tracked")
	}
	r.observe(Message{Type: MessageToolUse, CallID: "a"})
	aStart, _ := r.oldest()
	time.Sleep(2 * time.Millisecond)
	r.observe(Message{Type: MessageToolUse, CallID: "b"})
	r.observe(Message{Type: MessageToolUse, CallID: "a"}) // a repeat keeps the start
	if start, _ := r.oldest(); !start.Equal(aStart) {
		t.Fatalf("oldest = %s, want a's first start %s", start, aStart)
	}
	r.observe(Message{Type: MessageText, CallID: "b"})
	r.observe(Message{Type: MessageToolResult, CallID: "a"})
	if start, ok := r.oldest(); !ok || !start.After(aStart) {
		t.Fatalf("oldest = %s/%v, want b, still running", start, ok)
	}
	r.observe(Message{Type: MessageToolResult, CallID: "b"})
	if running() {
		t.Fatal("every call finished")
	}
	r.observe(Message{Type: MessageToolUse, CallID: "c"})
	r.reset()
	if running() {
		t.Fatal("reset kept a call")
	}
}
