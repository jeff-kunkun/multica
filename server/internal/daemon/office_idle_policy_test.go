package daemon

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

func TestIdleWatchdogBudgetsOfficePolicy(t *testing.T) {
	legacy := Config{AgentIdleWatchdog: 2 * time.Hour, AgentToolWatchdog: 2 * time.Hour}
	office := func(provider string, unlimited bool, base agent.ExecOptions) agent.ExecOptions {
		applyOfficeIdlePolicy(&base, provider, unlimited)
		return base
	}
	cases := []struct {
		name       string
		cfg        Config
		opts       agent.ExecOptions
		idle, tool time.Duration
	}{
		{"legacy unchanged", legacy, agent.ExecOptions{}, 2 * time.Hour, 2 * time.Hour},
		{"legacy opencode unchanged", legacy, agent.ExecOptions{IdleWatchdogTimeout: 10 * time.Minute}, 10 * time.Minute, 2 * time.Hour},
		{"office claude", legacy, office("claude", false, agent.ExecOptions{}), 3 * time.Minute, 30 * time.Minute},
		{"office codex", legacy, office("codex", false, agent.ExecOptions{}), 3 * time.Minute, 30 * time.Minute},
		{"office unverified CLI gets 5 minutes", legacy, office("opencode", false, agent.ExecOptions{IdleWatchdogTimeout: 10 * time.Minute}), 5 * time.Minute, 30 * time.Minute},
		{"office 长任务 has no watchdog", legacy, office("claude", true, agent.ExecOptions{}), 0, 2 * time.Hour},
		{"global zero still disables", Config{}, office("claude", false, agent.ExecOptions{}), 0, 30 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idle, tool := idleWatchdogBudgets(tc.cfg, tc.opts)
			if idle != tc.idle || tool != tc.tool {
				t.Fatalf("budgets = %s/%s, want %s/%s", idle, tool, tc.idle, tc.tool)
			}
		})
	}
	if opts := office("codex", true, agent.ExecOptions{}); opts.SemanticInactivityTimeout != officeUnlimitedSemanticInactivity {
		t.Fatalf("长任务 Codex semantic timeout = %s, want it lifted", opts.SemanticInactivityTimeout)
	}
}

// officeWatchdog runs the real watchdog with the office budgets for provider on
// synctest's fake clock. output stamps the CLI's last line; tools sets the
// number of commands running.
type officeWatchdog struct {
	fired  atomic.Bool
	tools  atomic.Int32
	line   atomic.Int64
	start  time.Time
	thresh atomic.Int64
}

func startOfficeWatchdog(t *testing.T, provider string, unlimited bool) *officeWatchdog {
	t.Helper()
	w := &officeWatchdog{start: time.Now()}
	now := func() time.Duration { return time.Since(w.start) }
	opts := agent.ExecOptions{}
	applyOfficeIdlePolicy(&opts, provider, unlimited)
	idle, tool := idleWatchdogBudgets(Config{AgentIdleWatchdog: 2 * time.Hour, AgentToolWatchdog: 2 * time.Hour}, opts)
	if idle <= 0 {
		// executeAndDrainDetectingSkills starts no watchdog at all.
		return w
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var last atomic.Int64
	last.Store(int64(now()))
	countTools := countingOutputActivity(w.tools.Load, &last, func() time.Duration { return time.Duration(w.line.Load()) })
	go new(Daemon).runIdleWatchdogWithClock(ctx, idle, tool, &last, countTools, &w.fired, &w.thresh,
		cancel, make(chan agent.Message), nil, nil, slog.Default(), now)
	synctest.Wait()
	return w
}

func (w *officeWatchdog) output() { w.line.Store(int64(time.Since(w.start))) }

// The four cases DENE-2667 names, on a fake clock.
func TestOfficeIdlePolicyStopsOnlyRealSilence(t *testing.T) {
	t.Run("3 minutes with no output and no command stops the run", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := startOfficeWatchdog(t, "claude", false)
			time.Sleep(2*time.Minute + 50*time.Second)
			synctest.Wait()
			if w.fired.Load() {
				t.Fatal("stopped before 3 minutes of silence")
			}
			time.Sleep(2 * time.Minute)
			synctest.Wait()
			if !w.fired.Load() || time.Duration(w.thresh.Load()) != 3*time.Minute {
				t.Fatalf("fired=%v threshold=%s, want a stop at 3m", w.fired.Load(), time.Duration(w.thresh.Load()))
			}
		})
	})
	t.Run("streamed output and thinking keep a long turn alive", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := startOfficeWatchdog(t, "codex", false)
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
			time.Sleep(4 * time.Minute)
			synctest.Wait()
			if !w.fired.Load() {
				t.Fatal("silence after the output ended was never stopped")
			}
		})
	})
	t.Run("a running command may stay silent up to 30 minutes", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := startOfficeWatchdog(t, "claude", false)
			w.tools.Store(1)
			time.Sleep(29 * time.Minute)
			synctest.Wait()
			if w.fired.Load() {
				t.Fatal("a silent command was stopped before 30 minutes")
			}
			time.Sleep(3 * time.Minute)
			synctest.Wait()
			if !w.fired.Load() || time.Duration(w.thresh.Load()) != 30*time.Minute {
				t.Fatalf("fired=%v threshold=%s, want a stop at 30m", w.fired.Load(), time.Duration(w.thresh.Load()))
			}
		})
	})
	t.Run("长任务 is never stopped for silence", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := startOfficeWatchdog(t, "claude", true)
			time.Sleep(24 * time.Hour)
			synctest.Wait()
			if w.fired.Load() {
				t.Fatal("a 长任务 run was stopped for silence")
			}
		})
	})
	t.Run("an unverified CLI gets 5 minutes", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := startOfficeWatchdog(t, "opencode", false)
			time.Sleep(4*time.Minute + 50*time.Second)
			synctest.Wait()
			if w.fired.Load() {
				t.Fatal("stopped before 5 minutes of silence")
			}
			time.Sleep(3 * time.Minute)
			synctest.Wait()
			if !w.fired.Load() || time.Duration(w.thresh.Load()) != 5*time.Minute {
				t.Fatalf("fired=%v threshold=%s, want a stop at 5m", w.fired.Load(), time.Duration(w.thresh.Load()))
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
