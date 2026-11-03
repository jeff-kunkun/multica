package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

func TestIdleWatchdogBudgetsOfficePolicy(t *testing.T) {
	legacy := Config{AgentIdleWatchdog: 2 * time.Hour, AgentToolWatchdog: 2 * time.Hour, CommandWatchdog: DefaultCommandWatchdog}
	office := func(cfg Config, unlimited bool, base agent.ExecOptions) agent.ExecOptions {
		applyOfficeIdlePolicy(&base, unlimited, narrowedToolWatchdog(cfg.AgentToolWatchdog, cfg.CommandWatchdog))
		return base
	}
	noCap := Config{AgentIdleWatchdog: 2 * time.Hour, AgentToolWatchdog: 2 * time.Hour}
	neverDuringTool := Config{AgentIdleWatchdog: 2 * time.Hour, CommandWatchdog: DefaultCommandWatchdog}
	cases := []struct {
		name       string
		cfg        Config
		opts       agent.ExecOptions
		idle, tool time.Duration
	}{
		{"legacy unchanged", legacy, agent.ExecOptions{}, 2 * time.Hour, 2 * time.Hour},
		{"legacy opencode unchanged", legacy, agent.ExecOptions{IdleWatchdogTimeout: 10 * time.Minute}, 10 * time.Minute, 2 * time.Hour},
		// DENE-2900: 10 minutes with no command, the command cap with one.
		{"office claude", legacy, office(legacy, false, agent.ExecOptions{}), 10 * time.Minute, time.Hour},
		{"office opencode", legacy, office(legacy, false, agent.ExecOptions{IdleWatchdogTimeout: 10 * time.Minute}), 10 * time.Minute, time.Hour},
		{"office without a command cap keeps the tool budget", noCap, office(noCap, false, agent.ExecOptions{}), 10 * time.Minute, 2 * time.Hour},
		{"office keeps a daemon-wide never-stop during a tool", neverDuringTool, office(neverDuringTool, false, agent.ExecOptions{}), 10 * time.Minute, 0},
		{"office 长任务 has no watchdog", legacy, office(legacy, true, agent.ExecOptions{}), 0, 2 * time.Hour},
		{"global zero still disables", Config{}, office(Config{}, false, agent.ExecOptions{}), 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idle, tool := idleWatchdogBudgets(tc.cfg, tc.opts)
			if idle != tc.idle || tool != tc.tool {
				t.Fatalf("budgets = %s/%s, want %s/%s", idle, tool, tc.idle, tc.tool)
			}
		})
	}
	if opts := office(legacy, true, agent.ExecOptions{}); opts.SemanticInactivityTimeout != officeUnlimitedSemanticInactivity {
		t.Fatalf("长任务 Codex semantic timeout = %s, want it lifted", opts.SemanticInactivityTimeout)
	}
}

// officeWatchdog runs the real watchdog with the office budgets on
// synctest's fake clock. output stamps the CLI's last line; tools sets the
// number of commands running; run and done start and finish commands the
// command cap measures, through a real liveTracker.
type officeWatchdog struct {
	fired  atomic.Bool
	capped atomic.Bool
	tools  atomic.Int32
	line   atomic.Int64
	start  time.Time
	thresh atomic.Int64
	live   *liveTracker
	// hidden is what the backend reports as running while its stream shows
	// nothing yet (OpenCode).
	hidden atomic.Pointer[[]agent.RunningTool]
	// queue is the backend's message buffer. The test's drain loop is its only
	// reader: it takes a message when the test lets one through (let), and
	// answers the cap between two messages, as the daemon's drain loop does.
	queue chan agent.Message
	drain *drainProgress
	let   chan chan struct{}
	// mute makes the drain loop take the cap's next question and stop there,
	// never replying.
	mute atomic.Bool
}

func (w *officeWatchdog) hiddenCalls() []agent.RunningTool {
	if calls := w.hidden.Load(); calls != nil {
		return *calls
	}
	return nil
}

func startOfficeWatchdog(t *testing.T, unlimited bool) *officeWatchdog {
	t.Helper()
	w := &officeWatchdog{start: time.Now(), queue: make(chan agent.Message, 8), let: make(chan chan struct{})}
	w.drain = newDrainProgress(func() int { return len(w.queue) })
	now := func() time.Duration { return time.Since(w.start) }
	w.live = &liveTracker{clock: now, running: w.hiddenCalls}
	cfg := Config{AgentIdleWatchdog: 2 * time.Hour, AgentToolWatchdog: 2 * time.Hour, CommandWatchdog: DefaultCommandWatchdog}
	opts := agent.ExecOptions{}
	applyOfficeIdlePolicy(&opts, unlimited, narrowedToolWatchdog(cfg.AgentToolWatchdog, cfg.CommandWatchdog))
	idle, tool := idleWatchdogBudgets(cfg, opts)
	if idle <= 0 {
		// executeAndDrainDetectingSkills starts no watchdog at all.
		return w
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var last atomic.Int64
	last.Store(int64(now()))
	countTools := countingOutputActivity(w.tools.Load, &last, func() time.Duration { return time.Duration(w.line.Load()) })
	commands := commandCap{oldest: w.live.oldestCommand, drain: w.drain, stopped: &w.capped}
	go w.drainLoop(ctx)
	go new(Daemon).runIdleWatchdogWithClock(ctx, idle, tool, &last, countTools, &w.fired, &w.thresh,
		cancel, w.queue, nil, nil, commands, slog.Default(), now)
	synctest.Wait()
	return w
}

func (w *officeWatchdog) output() { w.line.Store(int64(time.Since(w.start))) }

// run starts a command, as its tool_use message would: the tracker records
// its start and the in-flight count rises, which is also activity.
func (w *officeWatchdog) run(id, tool, command string) {
	w.live.observe(agent.Message{Type: agent.MessageToolUse, CallID: id, Tool: tool, Input: map[string]any{"command": command}})
	w.tools.Add(1)
	w.output()
}

// done finishes a command, as its tool_result message would.
func (w *officeWatchdog) done(id string) {
	w.live.observe(agent.Message{Type: agent.MessageToolResult, CallID: id})
	w.tools.Add(-1)
	w.output()
}

// drainLoop is the test's drain loop. It takes the next queued message when
// the test lets one through; a hold channel sent with it keeps the message
// between taking it and handling it until the test closes the channel.
func (w *officeWatchdog) drainLoop(ctx context.Context) {
	defer w.drain.stopped()
	for {
		select {
		case <-ctx.Done():
			return
		case reply := <-w.drain.asks:
			if w.mute.Load() {
				<-ctx.Done()
				return
			}
			w.drain.answer(reply)
		case hold := <-w.let:
			var msg agent.Message
			select {
			case msg = <-w.queue:
			case <-ctx.Done():
				return
			}
			if hold != nil {
				select {
				case <-hold:
				case <-ctx.Done():
					return
				}
			}
			w.live.observe(msg)
			if msg.Type == agent.MessageToolResult {
				w.tools.Add(-1)
			}
			w.output()
			w.drain.took()
		}
	}
}

// pass lets the drain loop take and handle the next queued message.
func (w *officeWatchdog) pass() { w.let <- nil }

// flood keeps the message queue full until ctx ends: the backend prints
// faster than the drain loop takes it, one message a second.
func (w *officeWatchdog) flood(ctx context.Context) {
	for len(w.queue) < cap(w.queue) {
		w.queue <- agent.Message{Type: agent.MessageText, Content: "line"}
	}
	for ctx.Err() == nil {
		time.Sleep(time.Second)
		select {
		case w.let <- nil:
		case <-ctx.Done():
			return
		}
		select {
		case w.queue <- agent.Message{Type: agent.MessageText, Content: "line"}:
		case <-ctx.Done():
			return
		}
	}
}

// mustNotStopBy and mustStopBy sleep the fake clock to at, measured from the
// watchdog's start, and check whether it has fired.
func (w *officeWatchdog) mustNotStopBy(t *testing.T, at time.Duration, why string) {
	t.Helper()
	time.Sleep(at - time.Since(w.start))
	synctest.Wait()
	if w.fired.Load() {
		t.Fatalf("stopped by %s: %s", at, why)
	}
}

func (w *officeWatchdog) mustStopBy(t *testing.T, at time.Duration, byCap bool) {
	t.Helper()
	time.Sleep(at - time.Since(w.start))
	synctest.Wait()
	if !w.fired.Load() || w.capped.Load() != byCap {
		t.Fatalf("by %s: fired=%v by the command cap=%v, want fired by the cap=%v", at, w.fired.Load(), w.capped.Load(), byCap)
	}
}

// The cases DENE-2667 names, with the windows Kun set in DENE-2900, on a fake
// clock.
func TestOfficeIdlePolicyStopsOnlyRealSilence(t *testing.T) {
	t.Run("10 minutes with no output and no command stops the run", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := startOfficeWatchdog(t, false)
			time.Sleep(9*time.Minute + 50*time.Second)
			synctest.Wait()
			if w.fired.Load() {
				t.Fatal("stopped before 10 minutes of silence")
			}
			time.Sleep(2 * time.Minute)
			synctest.Wait()
			if !w.fired.Load() || time.Duration(w.thresh.Load()) != 10*time.Minute {
				t.Fatalf("fired=%v threshold=%s, want a stop at 10m", w.fired.Load(), time.Duration(w.thresh.Load()))
			}
		})
	})
	t.Run("streamed output and thinking keep a long turn alive", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := startOfficeWatchdog(t, false)
			// 40 minutes of word-by-word output or reasoning lines, none of
			// which are transcript messages.
			for i := 0; i < 40*6; i++ {
				time.Sleep(10 * time.Second)
				w.output()
			}
			synctest.Wait()
			if w.fired.Load() {
				t.Fatal("a turn that kept writing was stopped as idle")
			}
			time.Sleep(11 * time.Minute)
			synctest.Wait()
			if !w.fired.Load() {
				t.Fatal("silence after the output ended was never stopped")
			}
		})
	})
	t.Run("a running command may stay silent up to the 60-minute cap", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := startOfficeWatchdog(t, false)
			w.run("c1", "bash", "go test ./...")
			w.mustNotStopBy(t, 59*time.Minute, "a silent command before 60 minutes")
			w.mustStopBy(t, 62*time.Minute, true)
			if time.Duration(w.thresh.Load()) != time.Hour {
				t.Fatalf("threshold=%s, want the 60m cap", time.Duration(w.thresh.Load()))
			}
		})
	})
	// Codex's DENE-2900 review: the cap counts from the command's start, not
	// from the run's last activity.
	t.Run("a command that keeps printing still ends at the cap", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := startOfficeWatchdog(t, false)
			w.run("c1", "bash", "tail -f deploy.log")
			for time.Since(w.start) < 59*time.Minute {
				time.Sleep(10 * time.Second)
				w.output()
			}
			w.mustNotStopBy(t, 59*time.Minute, "a printing command before 60 minutes")
			for time.Since(w.start) < 62*time.Minute {
				time.Sleep(10 * time.Second)
				w.output()
			}
			w.mustStopBy(t, 62*time.Minute, true)
		})
	})
	t.Run("a command beside other work ends at its own cap", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := startOfficeWatchdog(t, false)
			w.run("long", "bash", "make release")
			for i := 1; time.Since(w.start) < 55*time.Minute; i++ {
				time.Sleep(2 * time.Minute)
				id := fmt.Sprintf("short-%d", i)
				w.run(id, "bash", "git status")
				time.Sleep(time.Minute)
				w.done(id)
			}
			w.mustNotStopBy(t, 59*time.Minute, "the long command before 60 minutes")
			w.mustStopBy(t, 62*time.Minute, true)
		})
	})
	t.Run("a later command gets its own full cap", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := startOfficeWatchdog(t, false)
			w.run("first", "bash", "npm ci")
			time.Sleep(50 * time.Minute)
			w.run("second", "bash", "npm run build")
			w.done("first")
			w.mustNotStopBy(t, 105*time.Minute, "the second command 55 minutes in")
			w.mustStopBy(t, 112*time.Minute, true)
		})
	})
	// Codex's second DENE-2900 review: output the drain loop is behind on
	// must not hold the cap off for ever, nor may the cap stop a command whose
	// result was already queued, and the 64 calls the live report shows must
	// not be the only ones the cap measures.
	t.Run("a queue kept full by output cannot hold off the cap", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := startOfficeWatchdog(t, false)
			ctx, stop := context.WithCancel(context.Background())
			flooding := make(chan struct{})
			w.run("c1", "bash", "yes")
			go func() {
				defer close(flooding)
				w.flood(ctx)
			}()
			// The bubble may end only once the flood has (a failed check
			// included).
			defer func() {
				stop()
				<-flooding
			}()
			w.mustNotStopBy(t, 59*time.Minute, "a flooding command before 60 minutes")
			w.mustStopBy(t, 66*time.Minute, true)
			if len(w.queue) == 0 {
				t.Fatal("the queue drained; the case did not hold it full")
			}
		})
	})
	t.Run("a result queued when the cap is reached ends the command, not the run", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := startOfficeWatchdog(t, false)
			w.run("c1", "bash", "go test ./...")
			// The result lands in the queue just before the cap; the drain
			// loop takes it only after the watchdog has seen the cap.
			time.Sleep(59*time.Minute + 50*time.Second)
			w.queue <- agent.Message{Type: agent.MessageText, Content: "ok"}
			w.queue <- agent.Message{Type: agent.MessageToolResult, CallID: "c1"}
			time.Sleep(time.Minute)
			w.pass()
			w.pass()
			w.mustNotStopBy(t, 69*time.Minute, "a command whose result was queued at the cap")
			w.mustStopBy(t, 77*time.Minute, false)
		})
	})
	// Codex's third DENE-2900 review: the last output is taken just after the
	// cap asked where the backlog ends, and nothing follows. It must be
	// waited for once, not twice, or the cap would never stop the command.
	t.Run("output taken just after the cap asked does not hold the cap off", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := startOfficeWatchdog(t, false)
			w.run("c1", "bash", "go test ./...")
			time.Sleep(59*time.Minute + 50*time.Second)
			w.queue <- agent.Message{Type: agent.MessageText, Content: "still running"}
			time.Sleep(40 * time.Second)
			w.pass()
			w.mustStopBy(t, 66*time.Minute, true)
			if len(w.queue) != 0 || w.drain.count() != 1 {
				t.Fatalf("queue %d, taken %d: the line was not taken after the cap asked", len(w.queue), w.drain.count())
			}
		})
	})
	// Codex's fourth DENE-2900 review: the result was queued long before the
	// cap, and the drain loop has taken it off the queue but not handled it
	// when the cap is reached. Neither the queue nor the count holds it then,
	// so only the drain loop can tell the cap it is busy with a message.
	t.Run("a result taken but not handled at the cap ends the command, not the run", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := startOfficeWatchdog(t, false)
			w.run("c1", "bash", "go test ./...")
			time.Sleep(50 * time.Minute)
			w.queue <- agent.Message{Type: agent.MessageToolResult, CallID: "c1"}
			time.Sleep(9*time.Minute + 30*time.Second)
			hold := make(chan struct{})
			w.let <- hold
			w.mustNotStopBy(t, 61*time.Minute, "a command whose result the drain loop held at the cap")
			close(hold)
			w.mustNotStopBy(t, 69*time.Minute, "a command whose result the drain loop held at the cap")
			w.mustStopBy(t, 77*time.Minute, false)
		})
	})
	// The wait for the drain loop stays bounded: past the cap the run is
	// stopped one tick later at most, wherever the drain loop stopped.
	t.Run("a drain loop stuck at the cap does not hold the cap off", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := startOfficeWatchdog(t, false)
			w.run("c1", "bash", "go test ./...")
			time.Sleep(59 * time.Minute)
			w.queue <- agent.Message{Type: agent.MessageText, Content: "still running"}
			w.let <- make(chan struct{})
			w.mustNotStopBy(t, 64*time.Minute, "the first tick the drain loop did not answer")
			w.mustStopBy(t, 66*time.Minute, true)
		})
	})
	// Codex's fifth DENE-2900 review: the drain loop answers where the backlog
	// ends, handles one of its two messages, then gets stuck on the other.
	t.Run("a drain loop stuck in the backlog after answering does not hold the cap off", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := startOfficeWatchdog(t, false)
			w.run("c1", "bash", "go test ./...")
			time.Sleep(59 * time.Minute)
			w.queue <- agent.Message{Type: agent.MessageText, Content: "line 1"}
			w.queue <- agent.Message{Type: agent.MessageText, Content: "line 2"}
			time.Sleep(90 * time.Second)
			w.pass()
			w.let <- make(chan struct{})
			if w.drain.count() != 1 {
				t.Fatalf("taken %d, want 1: the drain loop did not stop inside the backlog", w.drain.count())
			}
			w.mustNotStopBy(t, 64*time.Minute, "a backlog the drain loop was still taking")
			w.mustStopBy(t, 66*time.Minute, true)
		})
	})
	// Codex's fifth DENE-2900 review: the drain loop takes the cap's question
	// and stops before it replies.
	t.Run("a drain loop that takes the question and never replies does not hold the cap off", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := startOfficeWatchdog(t, false)
			w.run("c1", "bash", "go test ./...")
			w.mute.Store(true)
			w.mustNotStopBy(t, 64*time.Minute, "the first tick the drain loop did not reply")
			w.mustStopBy(t, 66*time.Minute, true)
		})
	})
	t.Run("the oldest of 80 calls at once still ends at its own cap", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := startOfficeWatchdog(t, false)
			w.run("long", "bash", "make release")
			for i := 0; i < 80; i++ {
				time.Sleep(10 * time.Second)
				w.run(fmt.Sprintf("p%d", i), "bash", "lint")
			}
			// They finish out of order: the odd ones newest first, then the
			// even ones oldest first. long keeps running.
			for i := 79; i >= 0; i -= 2 {
				w.done(fmt.Sprintf("p%d", i))
			}
			for i := 0; i < 80; i += 2 {
				w.done(fmt.Sprintf("p%d", i))
			}
			if start, ok := w.live.oldestCommand(); !ok || start != 0 {
				t.Fatalf("oldest = %s, %v; want long's start", start, ok)
			}
			w.mustNotStopBy(t, 59*time.Minute, "the long command before 60 minutes")
			w.mustStopBy(t, 62*time.Minute, true)
		})
	})
	t.Run("a command the stream does not show yet is not charged the silence before it", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := startOfficeWatchdog(t, false)
			time.Sleep(9 * time.Minute)
			w.hidden.Store(&[]agent.RunningTool{{CallID: "oc-1", Tool: "bash", Input: map[string]any{"command": "npm run build"}, StartedAt: time.Now()}})
			w.tools.Add(1)
			w.mustNotStopBy(t, 66*time.Minute, "the hidden command 57 minutes in")
			w.mustStopBy(t, 71*time.Minute, true)
		})
	})
	t.Run("a subagent hand-off is not capped, only its silence is", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := startOfficeWatchdog(t, false)
			w.run("sub", "Task", "explore the repo")
			for time.Since(w.start) < 90*time.Minute {
				time.Sleep(10 * time.Second)
				w.output()
			}
			w.mustNotStopBy(t, 90*time.Minute, "a subagent that kept working")
			w.mustStopBy(t, 156*time.Minute, false)
		})
	})
	t.Run("长任务 is never stopped for silence", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := startOfficeWatchdog(t, true)
			time.Sleep(24 * time.Hour)
			synctest.Wait()
			if w.fired.Load() {
				t.Fatal("a 长任务 run was stopped for silence")
			}
		})
	})
}

func TestIdleStopPolicyFromEnv(t *testing.T) {
	for value, want := range map[string]string{"": "", "legacy": "", "office": idleStopPolicyOffice} {
		t.Setenv("MULTICA_IDLE_STOP_POLICY", value)
		got, err := idleStopPolicyFromEnv()
		if err != nil || got != want {
			t.Fatalf("%q: got %q, %v; want %q", value, got, err, want)
		}
	}
	t.Setenv("MULTICA_IDLE_STOP_POLICY", "ofice")
	if _, err := idleStopPolicyFromEnv(); err == nil {
		t.Fatal("a misspelt policy was accepted")
	}
}
