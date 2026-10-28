package daemon

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// The office idle policy (DENE-2667) stops a quiet run much sooner than the
// legacy two-hour watchdog, and counts more as "not quiet":
//
//   - every line the CLI writes is activity: Claude streams its reply word by
//     word, and Codex reports its thinking;
//   - with no command running, 3 minutes of silence stops the run;
//   - with a command running, the run may stay silent up to 30 minutes;
//   - an issue a person labelled 长任务 has no limit;
//   - a run stopped for silence resumes the same session once.
//
// A CLI whose output has not been verified to stream while it works gets 5
// minutes instead of 3. The daemon-wide MULTICA_AGENT_IDLE_WATCHDOG=0 still
// switches every watchdog off.
const (
	idleStopPolicyOffice = "office"

	officeIdleWindow           = 3 * time.Minute
	officeUnverifiedIdleWindow = 5 * time.Minute
	officeToolWindow           = 30 * time.Minute
	// officeUnlimitedSemanticInactivity lifts Codex's own no-progress timer
	// for a 长任务 run. Zero would mean the backend default, so "no limit" is a
	// span no run reaches.
	officeUnlimitedSemanticInactivity = 10 * 365 * 24 * time.Hour
)

// officeIdleStreamVerified lists the CLIs whose output was verified to keep
// streaming while they work, a long reply or a long think (DENE-2667 real runs).
var officeIdleStreamVerified = map[string]bool{
	"claude": true,
	"codex":  true,
}

func idleStopPolicyFromEnv() (string, error) {
	switch value := strings.TrimSpace(os.Getenv("MULTICA_IDLE_STOP_POLICY")); value {
	case "", "legacy":
		return "", nil
	case idleStopPolicyOffice:
		return idleStopPolicyOffice, nil
	default:
		return "", fmt.Errorf("MULTICA_IDLE_STOP_POLICY: unknown policy %q (want \"office\" or \"legacy\")", value)
	}
}

// applyOfficeIdlePolicy sets one execution's office idle budgets.
func applyOfficeIdlePolicy(opts *agent.ExecOptions, provider string, idleUnlimited bool) {
	opts.OfficeIdleStop = true
	if idleUnlimited {
		opts.IdleUnlimited = true
		opts.SemanticInactivityTimeout = officeUnlimitedSemanticInactivity
		return
	}
	opts.IdleWatchdogTimeout = officeUnverifiedIdleWindow
	if officeIdleStreamVerified[provider] {
		opts.IdleWatchdogTimeout = officeIdleWindow
	}
	opts.ToolWatchdogTimeout = officeToolWindow
}

// shouldResumeAfterIdleStop reports whether a run the office policy stopped
// for silence gets its one resume of the same session.
func shouldResumeAfterIdleStop(result agent.Result, opts agent.ExecOptions) bool {
	return opts.OfficeIdleStop && result.Status == "idle_watchdog" && result.SessionID != ""
}

// officeIdleResumePrompt is the next instruction of the resumed session.
const officeIdleResumePrompt = "Your previous step went quiet for too long, so Multica stopped it. " +
	"Continue the same task from where you left off. If a command hung, do not rerun it unchanged: " +
	"give it a timeout, run it in the background, or take another route."
