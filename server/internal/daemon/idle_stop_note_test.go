package daemon

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// hiddenToolBackend models OpenCode 1.x: the stream shows a tool only once it
// finished, and RunningTools reports the command while it runs. It stays
// silent for silence, then prints the call and completes.
type hiddenToolBackend struct {
	silence time.Duration
}

func (b hiddenToolBackend) Execute(ctx context.Context, _ string, _ agent.ExecOptions) (*agent.Session, error) {
	msgCh := make(chan agent.Message, 4)
	resCh := make(chan agent.Result, 1)
	var mu sync.Mutex
	running := []agent.RunningTool{{
		CallID:    "oc-1",
		Tool:      "bash",
		Input:     map[string]any{"command": "npm run build"},
		StartedAt: time.Now(),
	}}
	msgCh <- agent.Message{Type: agent.MessageText, Content: "building"}
	go func() {
		select {
		case <-time.After(b.silence):
		case <-ctx.Done():
			resCh <- agent.Result{Status: "aborted", Error: ctx.Err().Error()}
			close(msgCh)
			close(resCh)
			return
		}
		mu.Lock()
		running = nil
		mu.Unlock()
		msgCh <- agent.Message{Type: agent.MessageToolUse, Tool: "bash", CallID: "oc-1", Input: map[string]any{"command": "npm run build"}}
		msgCh <- agent.Message{Type: agent.MessageToolResult, Tool: "bash", CallID: "oc-1", Output: "built"}
		close(msgCh)
		resCh <- agent.Result{Status: "completed", Output: "built"}
		close(resCh)
	}()
	return &agent.Session{
		Messages: msgCh,
		Result:   resCh,
		RunningTools: func() []agent.RunningTool {
			mu.Lock()
			defer mu.Unlock()
			return append([]agent.RunningTool(nil), running...)
		},
	}, nil
}

// A command the stream does not show yet still counts as running: a short
// no-command window does not stop it (DENE-2900, "long command not killed").
func TestExecuteAndDrain_HiddenRunningCommandIsNotIdle(t *testing.T) {
	t.Parallel()
	d := newTestDaemon(t)
	d.cfg.AgentIdleWatchdog = 5 * time.Second
	d.cfg.AgentToolWatchdog = 5 * time.Second

	result, _, err := d.executeAndDrain(context.Background(), hiddenToolBackend{silence: 400 * time.Millisecond}, "p",
		agent.ExecOptions{IdleWatchdogTimeout: 50 * time.Millisecond, ToolWatchdogTimeout: 3 * time.Second},
		slog.Default(), "t-hidden-tool", "", new(atomic.Int32))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" {
		t.Fatalf("a running command was stopped as idle: status=%q error=%q", result.Status, result.Error)
	}
}

// Past the command cap the run is stopped, and the note names the command.
func TestExecuteAndDrain_HiddenCommandPastCapIsStoppedWithNote(t *testing.T) {
	t.Parallel()
	d := newTestDaemon(t)
	d.cfg.AgentIdleWatchdog = 5 * time.Second
	d.cfg.AgentToolWatchdog = 5 * time.Second

	start := time.Now()
	result, _, err := d.executeAndDrain(context.Background(), hiddenToolBackend{silence: time.Minute}, "p",
		agent.ExecOptions{IdleWatchdogTimeout: 50 * time.Millisecond, ToolWatchdogTimeout: 200 * time.Millisecond},
		slog.Default(), "t-hidden-tool-cap", "", new(atomic.Int32))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "idle_watchdog" {
		t.Fatalf("status = %q, error = %q; want idle_watchdog", result.Status, result.Error)
	}
	for _, want := range []string{"命令看门狗停了这一轮：命令 `npm run build` 已跑约", "超过单条命令上限 200ms", "续跑时别原样重跑"} {
		if !strings.Contains(result.Error, want) {
			t.Fatalf("note = %q, want it to contain %q", result.Error, want)
		}
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("the cap took %s", elapsed)
	}
}

// silentAfterStepBackend ran one command, printed a line, then hangs with
// nothing running: a real hang.
type silentAfterStepBackend struct{}

func (silentAfterStepBackend) Execute(_ context.Context, _ string, _ agent.ExecOptions) (*agent.Session, error) {
	msgCh := make(chan agent.Message, 4)
	msgCh <- agent.Message{Type: agent.MessageToolUse, Tool: "bash", CallID: "c1", Input: map[string]any{"command": "git status"}}
	msgCh <- agent.Message{Type: agent.MessageToolResult, Tool: "bash", CallID: "c1", Output: "clean"}
	msgCh <- agent.Message{Type: agent.MessageText, Content: "checked the tree"}
	return &agent.Session{
		Messages:     msgCh,
		Result:       make(chan agent.Result),
		RunningTools: func() []agent.RunningTool { return nil },
	}, nil
}

// A real hang is still stopped on the no-command window, and the note gives
// the last step and output to resume from ("real hang still stopped").
func TestExecuteAndDrain_RealHangStillStoppedWithLastStep(t *testing.T) {
	t.Parallel()
	d := newTestDaemon(t)
	d.cfg.AgentIdleWatchdog = 5 * time.Second
	d.cfg.AgentToolWatchdog = 5 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	start := time.Now()
	result, _, err := d.executeAndDrain(ctx, silentAfterStepBackend{}, "p",
		agent.ExecOptions{IdleWatchdogTimeout: 100 * time.Millisecond, ToolWatchdogTimeout: 3 * time.Second},
		slog.Default(), "t-real-hang", "", new(atomic.Int32))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "idle_watchdog" {
		t.Fatalf("status = %q, error = %q; want idle_watchdog", result.Status, result.Error)
	}
	for _, want := range []string{"空闲看门狗停了这一轮：100ms 没有新消息，也没有在跑的命令。", "最后一步：`git status`", "最后输出：checked the tree", "续跑时从这里接着做"} {
		if !strings.Contains(result.Error, want) {
			t.Fatalf("note = %q, want it to contain %q", result.Error, want)
		}
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("a real hang waited %s, the command cap rather than the idle window", elapsed)
	}
}

// codexTimeoutBackend ends the way Codex does when its own semantic timer
// fires while a command runs: a timeout result, the command never finished.
type codexTimeoutBackend struct{}

func (codexTimeoutBackend) Execute(_ context.Context, _ string, _ agent.ExecOptions) (*agent.Session, error) {
	msgCh := make(chan agent.Message, 2)
	resCh := make(chan agent.Result, 1)
	msgCh <- agent.Message{Type: agent.MessageToolUse, Tool: "exec_command", CallID: "cmd-1", Input: map[string]any{"command": "mvn package"}}
	close(msgCh)
	resCh <- agent.Result{Status: "timeout", Error: agent.CodexSemanticInactivityMarker + " after 1h0m0s without agent progress (a command was still running; last activity: tool_use:exec_command)"}
	close(resCh)
	return &agent.Session{Messages: msgCh, Result: resCh}, nil
}

// A Codex semantic timeout becomes a watchdog stop and leaves the same note in front of Codex's own
// diagnostic, which stays for the failure classifiers.
func TestExecuteAndDrain_CodexSemanticTimeoutGetsStopNote(t *testing.T) {
	t.Parallel()
	d := newTestDaemon(t)
	result, _, err := d.executeAndDrain(context.Background(), codexTimeoutBackend{}, "p",
		agent.ExecOptions{SemanticInactivityTimeout: 10 * time.Minute, SemanticCommandTimeout: time.Hour},
		slog.Default(), "t-codex-semantic", "", new(atomic.Int32))
	if err != nil {
		t.Fatal(err)
	}
	// Handled as a watchdog stop: resumed once, never retried fresh by the server.
	if result.Status != "idle_watchdog" {
		t.Fatalf("status = %q, want idle_watchdog", result.Status)
	}
	if !strings.HasPrefix(result.Error, "命令看门狗停了这一轮：命令 `mvn package`") || !strings.Contains(result.Error, "超过单条命令上限 1 小时") {
		t.Fatalf("error = %q, want the note naming the command first", result.Error)
	}
	if !strings.Contains(result.Error, "\n\n"+agent.CodexSemanticInactivityMarker) {
		t.Fatalf("error = %q, want Codex's diagnostic kept after the note", result.Error)
	}
}

func TestCodexSemanticStopNote(t *testing.T) {
	t.Parallel()
	timeout := agent.Result{Status: "timeout", Error: agent.CodexSemanticInactivityMarker + " after 10m0s"}
	live := &liveTracker{}
	if note := codexSemanticStopNote(timeout, agent.ExecOptions{}, live, false); !strings.HasPrefix(note, "空闲看门狗停了这一轮：10 分钟 没有新消息") {
		t.Fatalf("default budget note = %q", note)
	}
	if note := codexSemanticStopNote(timeout, agent.ExecOptions{SemanticInactivityTimeout: 3 * time.Minute, SemanticCommandTimeout: time.Hour}, live, true); !strings.Contains(note, "超过单条命令上限") || !strings.Contains(note, "1 小时") {
		t.Fatalf("command note = %q", note)
	}
	// A cap below the semantic timeout is still the cap (Codex's review).
	if note := codexSemanticStopNote(timeout, agent.ExecOptions{SemanticInactivityTimeout: time.Hour, SemanticCommandTimeout: 5 * time.Minute}, live, true); !strings.Contains(note, "有命令跑了 5 分钟 还没结束") {
		t.Fatalf("short cap note = %q", note)
	}
	// Without a cap, a running command was silent past the semantic timeout.
	if note := codexSemanticStopNote(timeout, agent.ExecOptions{SemanticInactivityTimeout: 3 * time.Minute}, live, true); !strings.Contains(note, "已经 3 分钟 没有任何新消息") {
		t.Fatalf("no-cap note = %q", note)
	}
	for _, other := range []agent.Result{
		{Status: "completed"},
		{Status: "timeout", Error: "codex first-turn no progress"},
		{Status: "failed", Error: agent.CodexSemanticInactivityMarker},
	} {
		if note := codexSemanticStopNote(other, agent.ExecOptions{}, live, false); note != "" {
			t.Fatalf("result %+v got a note %q", other, note)
		}
	}
}

func TestIdleStopNoteVariants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		facts idleStopFacts
		want  string
	}{
		{"command past the cap", idleStopFacts{budget: time.Hour, commandRunning: true, capped: true, command: "sleep `x`", commandAge: 65 * time.Minute},
			"命令看门狗停了这一轮：命令 `sleep 'x'` 已跑约 1 小时 5 分钟 还没结束，超过单条命令上限 1 小时。续跑时别原样重跑这条命令：加超时、放后台，或换个办法。"},
		{"unnamed command past the cap", idleStopFacts{budget: time.Hour, commandRunning: true, capped: true},
			"命令看门狗停了这一轮：有命令跑了 1 小时 还没结束，超过单条命令上限。续跑时别原样重跑这条命令：加超时、放后台，或换个办法。"},
		{"silent command", idleStopFacts{budget: 30 * time.Minute, commandRunning: true, command: "tail -f log"},
			"命令看门狗停了这一轮：命令 `tail -f log` 还没结束，已经 30 分钟 没有任何新消息，超过单条命令上限。续跑时别原样重跑这条命令：加超时、放后台，或换个办法。"},
		{"unknown command", idleStopFacts{budget: 2 * time.Hour, commandRunning: true},
			"命令看门狗停了这一轮：有命令还在跑，已经 2 小时 没有任何新消息，超过单条命令上限。续跑时别原样重跑这条命令：加超时、放后台，或换个办法。"},
		{"idle with step", idleStopFacts{budget: 10 * time.Minute, lastStep: "git push", lastOutput: "rejected。"},
			"空闲看门狗停了这一轮：10 分钟 没有新消息，也没有在跑的命令。最后一步：`git push`。最后输出：rejected。续跑时从这里接着做。"},
		{"idle with earlier steps", idleStopFacts{budget: 10 * time.Minute, lastStep: "go test ./...", recentSteps: []string{"git pull", "rg TODO", "go test ./..."}},
			"空闲看门狗停了这一轮：10 分钟 没有新消息，也没有在跑的命令。最后一步：`go test ./...`。之前几步（从早到晚）：`git pull` → `rg TODO`。续跑时从这里接着做。"},
		{"idle without step", idleStopFacts{budget: 45 * time.Second},
			"空闲看门狗停了这一轮：45 秒 没有新消息，也没有在跑的命令。这一轮还没调用过工具。续跑时从这里接着做。"},
	}
	for _, c := range cases {
		if got := idleStopNote(c.facts); got != c.want {
			t.Errorf("%s:\n got  %s\n want %s", c.name, got, c.want)
		}
	}
}

func TestZhDuration(t *testing.T) {
	t.Parallel()
	for d, want := range map[time.Duration]string{
		-time.Second:                    "0s",
		200 * time.Millisecond:          "200ms",
		45 * time.Second:                "45 秒",
		10*time.Minute + 20*time.Second: "10 分钟",
		time.Hour:                       "1 小时",
		time.Hour + 5*time.Minute:       "1 小时 5 分钟",
	} {
		if got := zhDuration(d); got != want {
			t.Errorf("zhDuration(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestNarrowedToolWatchdog(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ daemonWide, provider, want time.Duration }{
		{2 * time.Hour, time.Hour, time.Hour},
		{30 * time.Minute, time.Hour, 0}, // cannot extend the daemon-wide bound
		{0, time.Hour, 0},                // daemon-wide 0 = never stop a running tool
		{2 * time.Hour, 0, 0},            // no cap configured
	} {
		if got := narrowedToolWatchdog(c.daemonWide, c.provider); got != c.want {
			t.Errorf("narrowedToolWatchdog(%s, %s) = %s, want %s", c.daemonWide, c.provider, got, c.want)
		}
	}
}

func TestLoadConfig_CommandWatchdog(t *testing.T) {
	stageFakeAgent(t)
	for env, want := range map[string]time.Duration{"": DefaultCommandWatchdog, "45m": 45 * time.Minute, "0": 0} {
		t.Setenv("MULTICA_COMMAND_WATCHDOG", env)
		cfg, err := LoadConfig(Overrides{ServerURL: "http://localhost:8080", WorkspacesRoot: t.TempDir()})
		if err != nil {
			t.Fatalf("env %q: %v", env, err)
		}
		if cfg.CommandWatchdog != want {
			t.Fatalf("env %q: CommandWatchdog = %s, want %s", env, cfg.CommandWatchdog, want)
		}
	}
}

// The command cap's view of the calls (DENE-2900): starts on the awake clock,
// subagent hand-offs left out, and no call left behind by a result that
// answers none.
func TestLiveTrackerOldestCommand(t *testing.T) {
	t.Parallel()
	var clock atomic.Int64
	at := func(d time.Duration) { clock.Store(int64(d)) }
	var mu sync.Mutex
	var backend []agent.RunningTool
	live := &liveTracker{
		clock: func() time.Duration { return time.Duration(clock.Load()) },
		running: func() []agent.RunningTool {
			mu.Lock()
			defer mu.Unlock()
			return append([]agent.RunningTool(nil), backend...)
		},
	}
	setBackend := func(calls ...agent.RunningTool) {
		mu.Lock()
		defer mu.Unlock()
		backend = calls
	}
	use := func(id, tool string) {
		live.observe(agent.Message{Type: agent.MessageToolUse, CallID: id, Tool: tool, Input: map[string]any{"command": id}})
	}
	oldest := func() time.Duration {
		t.Helper()
		start, ok := live.oldestCommand()
		if !ok {
			t.Fatal("no command running")
		}
		return start
	}

	if _, ok := live.oldestCommand(); ok {
		t.Fatal("an idle tracker reported a command")
	}
	at(time.Minute)
	use("sub", "Task")
	if _, ok := live.oldestCommand(); ok {
		t.Fatal("a subagent hand-off was measured")
	}
	at(2 * time.Minute)
	use("a", "bash")
	at(3 * time.Minute)
	use("b", "bash")
	if got := oldest(); got != 2*time.Minute {
		t.Fatalf("oldest = %s, want a's start", got)
	}
	// A result that answers no call drops the oldest, as the in-flight count
	// drops one.
	live.observe(agent.Message{Type: agent.MessageToolResult, CallID: "unknown"})
	if got := oldest(); got != 2*time.Minute {
		t.Fatalf("oldest = %s; the unanswered result should have dropped the hand-off first", got)
	}
	live.observe(agent.Message{Type: agent.MessageToolResult, CallID: "a"})
	if got := oldest(); got != 3*time.Minute {
		t.Fatalf("oldest = %s, want b's start", got)
	}
	live.observe(agent.Message{Type: agent.MessageToolResult, CallID: "b"})
	if _, ok := live.oldestCommand(); ok {
		t.Fatal("every call finished")
	}

	// A backend-reported call starts when a reading first shows it, moved
	// back by the age the backend recorded, but at most one watchdog tick.
	at(time.Hour)
	setBackend(agent.RunningTool{CallID: "oc-1", Tool: "bash", StartedAt: time.Now().Add(-30 * time.Second)})
	if got := oldest(); got < time.Hour-31*time.Second || got > time.Hour-29*time.Second {
		t.Fatalf("backend call start = %s, want about 30s before the first reading", got)
	}
	at(2 * time.Hour)
	if got := oldest(); got > time.Hour-29*time.Second {
		t.Fatalf("a later reading moved the start to %s", got)
	}
	setBackend(agent.RunningTool{CallID: "oc-2", Tool: "bash", StartedAt: time.Now().Add(-3 * time.Hour)})
	if got := oldest(); got != 2*time.Hour-idleWatchdogMaxTick {
		t.Fatalf("a start from before a long gap = %s, want at most one tick back", got)
	}
	// A call without an id that finished and runs again is a new call.
	setBackend(agent.RunningTool{Tool: "bash", Input: map[string]any{"command": "make"}})
	first := oldest()
	setBackend()
	live.untrackedRunning()
	at(3 * time.Hour)
	setBackend(agent.RunningTool{Tool: "bash", Input: map[string]any{"command": "make"}})
	if got := oldest(); got == first || got != 3*time.Hour {
		t.Fatalf("the rerun started at %s, want its own start", got)
	}
}

func TestLiveTrackerKeepsRecentSteps(t *testing.T) {
	t.Parallel()
	live := &liveTracker{}
	for i := 0; i < stopMaxSteps+3; i++ {
		live.observe(agent.Message{Type: agent.MessageToolUse, Tool: "bash", CallID: strings.Repeat("c", i+1), Input: map[string]any{"command": "step " + strings.Repeat("x", i)}})
	}
	live.observe(agent.Message{Type: agent.MessageToolUse, Tool: "bash", CallID: "long", Input: map[string]any{"command": strings.Repeat("y", 400)}})
	facts := live.stopFacts(time.Minute, false, false)
	if len(facts.recentSteps) != stopMaxSteps {
		t.Fatalf("kept %d steps, want %d", len(facts.recentSteps), stopMaxSteps)
	}
	if last := facts.recentSteps[stopMaxSteps-1]; len([]rune(last)) != stopStepMaxRunes || !strings.HasSuffix(last, "…") {
		t.Fatalf("long step = %q, want it cut to %d runes", last, stopStepMaxRunes)
	}
	if facts.recentSteps[0] != "step "+strings.Repeat("x", 4) {
		t.Fatalf("oldest kept step = %q", facts.recentSteps[0])
	}
}
