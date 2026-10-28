package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func waitSupplementReady(t *testing.T, ctx context.Context, session *Session) {
	t.Helper()
	for !session.SupplementReady() {
		select {
		case result := <-session.Result:
			t.Fatalf("ended before ready: %+v", result)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// The fake app-server checks the exact wire contract: turn/interrupt names the
// running turn, and the next turn/start carries the instruction on the same
// thread. The run then ends with the new turn's answer.
func TestCodexInterruptStartsNextTurnOnSameThread(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	fakePath := writeFakeCodexAppServer(t, ""+
		`read line`+"\n"+
		`echo '{"jsonrpc":"2.0","id":1,"result":{}}'`+"\n"+
		`read line`+"\n"+
		`read line`+"\n"+
		`echo '{"jsonrpc":"2.0","id":2,"result":{"thread":{"id":"thr-1"}}}'`+"\n"+
		`read line`+"\n"+
		`echo '{"jsonrpc":"2.0","id":3,"result":{"turn":{"id":"turn-1"}}}'`+"\n"+
		`echo '{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"thr-1","turn":{"id":"turn-1"}}}'`+"\n"+
		`echo '{"jsonrpc":"2.0","method":"item/started","params":{"threadId":"thr-1","turnId":"turn-1","item":{"type":"commandExecution","id":"cmd-1","command":"sleep 600"}}}'`+"\n"+
		`read line`+"\n"+
		`case "$line" in *'"turn/interrupt"'*'"turn-1"'*) ;; *) echo "bad interrupt: $line" >&2; exit 41;; esac`+"\n"+
		`echo '{"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":"thr-1","turn":{"id":"turn-1","status":"interrupted"}}}'`+"\n"+
		`echo '{"jsonrpc":"2.0","id":4,"result":{}}'`+"\n"+
		`read line`+"\n"+
		`case "$line" in *'"turn/start"'*'write hello'*'"thr-1"'*) ;; *) echo "bad next turn: $line" >&2; exit 42;; esac`+"\n"+
		`echo '{"jsonrpc":"2.0","id":5,"result":{"turn":{"id":"turn-2"}}}'`+"\n"+
		`echo '{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"thr-1","turn":{"id":"turn-2"}}}'`+"\n"+
		`echo '{"jsonrpc":"2.0","method":"item/completed","params":{"threadId":"thr-1","turnId":"turn-2","item":{"type":"agentMessage","id":"msg-2","text":"wrote hello","phase":"final_answer"}}}'`+"\n"+
		`echo '{"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":"thr-1","turn":{"id":"turn-2","status":"completed"}}}'`+"\n"+
		`cat >/dev/null`+"\n")
	backend, err := New("codex", Config{ExecutablePath: fakePath, Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	session, err := backend.Execute(ctx, "run sleep 600", ExecOptions{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for range session.Messages {
		}
	}()
	if session.Interrupt == nil {
		t.Fatal("codex session has no Interrupt")
	}
	waitSupplementReady(t, ctx, session)
	if err := session.Interrupt(ctx, "change of plan: write hello"); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	result := <-session.Result
	if result.Status != "completed" || result.Output != "wrote hello" || result.SessionID != "thr-1" {
		t.Fatalf("result: %+v", result)
	}
	if err := session.Interrupt(ctx, "late"); err == nil {
		t.Fatal("interrupt accepted after the run ended")
	}
}

// runFakeClaudeInterrupt plays Claude Code's stream-json side of an interrupt:
// the control request ends the turn with a result, and the next user frame on
// the same stdin starts the next turn of the same session.
func runFakeClaudeInterrupt() {
	reader := bufio.NewReader(os.Stdin)
	read := func() map[string]any {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(30)
		}
		var frame map[string]any
		if json.Unmarshal(line, &frame) != nil {
			os.Exit(31)
		}
		return frame
	}
	hook := func(event string) {
		_ = json.NewEncoder(os.Stdout).Encode(claudeSupplementHook(event, ""))
		if read()["type"] != "control_response" {
			os.Exit(32)
		}
	}
	init := read()
	_ = writeClaudeFrame(os.Stdout, map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": init["request_id"], "response": map[string]any{}}})
	if read()["type"] != "user" {
		os.Exit(33)
	}
	hook("UserPromptSubmit")
	fmt.Println(`{"type":"system","subtype":"init","session_id":"claude-interrupt"}`)
	frame := read()
	request, _ := frame["request"].(map[string]any)
	if frame["type"] != "control_request" || request["subtype"] != "interrupt" {
		os.Exit(34)
	}
	_ = writeClaudeFrame(os.Stdout, map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": frame["request_id"], "response": map[string]any{}}})
	fmt.Println(`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"claude-interrupt","result":""}`)
	next := read()
	message, _ := next["message"].(map[string]any)
	content, _ := json.Marshal(message["content"])
	if next["type"] != "user" || !strings.Contains(string(content), "write hello") {
		os.Exit(35)
	}
	hook("UserPromptSubmit")
	fmt.Println(`{"type":"result","subtype":"success","session_id":"claude-interrupt","result":"wrote hello"}`)
	if _, err := reader.ReadBytes('\n'); err != io.EOF {
		os.Exit(36)
	}
}

func TestClaudeInterruptContinuesSameSession(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b := &claudeBackend{cfg: Config{ExecutablePath: self, Logger: slog.Default(), Env: map[string]string{"CLAUDE_FAKE_MODE": "interrupt", "IS_SANDBOX": "1"}}}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	session, err := b.Execute(ctx, "run sleep 600", ExecOptions{EnableTaskSupplement: true})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for range session.Messages {
		}
	}()
	if session.Interrupt == nil {
		t.Fatal("claude session has no Interrupt")
	}
	waitSupplementReady(t, ctx, session)
	if err := session.Interrupt(ctx, "change of plan: write hello"); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	result := <-session.Result
	if result.Status != "completed" || result.Output != "wrote hello" || result.SessionID != "claude-interrupt" {
		t.Fatalf("result: %+v", result)
	}
	if err := session.Interrupt(ctx, "late"); err == nil {
		t.Fatal("interrupt accepted after the run ended")
	}
}

func TestClaudeInterrupterRefusalAndEnd(t *testing.T) {
	var out strings.Builder
	c := newClaudeInterrupter(&out)
	if err := c.interrupt(t.Context(), "x"); err == nil || !strings.Contains(err.Error(), "not started") {
		t.Fatalf("before start: %v", err)
	}
	c.started()

	done := make(chan error, 1)
	go func() { done <- c.interrupt(t.Context(), "x") }()
	waitPending := func() string {
		for range 200 {
			c.mu.Lock()
			pending := c.pending
			c.mu.Unlock()
			if pending != nil {
				return pending.requestID
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("interrupt never pending")
		return ""
	}
	id := waitPending()
	c.controlResponse(json.RawMessage(fmt.Sprintf(`{"subtype":"error","request_id":%q,"error":"busy"}`, id)))
	if err := <-done; err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("refusal: %v", err)
	}

	go func() { done <- c.interrupt(t.Context(), "x") }()
	waitPending()
	c.end()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("end: %v", err)
	}
	if next := c.resultSeen(); next != nil {
		t.Fatal("a result after end must end the run")
	}
}

// The daemon offers one steer tier per run; an interrupt provider that also
// matched restart or handoff would never offer steer-interrupt-v1.
func TestSteersByInterruptOnlyForProbedProviders(t *testing.T) {
	for _, provider := range []string{"codex", "claude", "opencode", "pi", "grok", "cursor", "hermes"} {
		// Codex only where its interrupted turn's commands can be stopped.
		want := provider == "claude" || (provider == "codex" && (runtime.GOOS == "linux" || runtime.GOOS == "darwin"))
		if got := SteersByInterrupt(provider); got != want {
			t.Errorf("SteersByInterrupt(%q) = %v, want %v", provider, got, want)
		}
		if want && (SteersByRestart(provider) || SteersByHandoff(provider)) {
			t.Errorf("%s is also in another steer tier", provider)
		}
	}
}
