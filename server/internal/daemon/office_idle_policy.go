package daemon

import (
	"context"
	"fmt"
	"log/slog"
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
//   - with no command running, 10 minutes of silence stops the run;
//   - a running command is activity; one command may run up to the command
//     cap (MULTICA_COMMAND_WATCHDOG, 60 minutes), counted from its own start,
//     whether it prints or not;
//   - an issue a person labelled 长任务 has no limit;
//   - a stopped run continues once: the same session, or a fresh one with the
//     task and the stop note when there is no session or it cannot resume.
//
// Kun set the 10 and 60 minutes on 2026-11-02 (DENE-2900); before, the policy
// stopped after 3 minutes (5 for a CLI not verified to stream) and allowed 30
// with a command. The daemon-wide MULTICA_AGENT_IDLE_WATCHDOG=0 still
// switches every watchdog off.
const (
	idleStopPolicyOffice = "office"

	officeIdleWindow = 10 * time.Minute
	// officeUnlimitedSemanticInactivity lifts Codex's own no-progress timer
	// for a 长任务 run. Zero would mean the backend default, so "no limit" is a
	// span no run reaches.
	officeUnlimitedSemanticInactivity = 10 * 365 * 24 * time.Hour
)

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

// applyOfficeIdlePolicy sets one execution's office idle budgets. toolWindow
// is the command cap already narrowed to the daemon-wide tool budget
// (narrowedToolWatchdog); zero keeps that budget.
func applyOfficeIdlePolicy(opts *agent.ExecOptions, idleUnlimited bool, toolWindow time.Duration) {
	opts.OfficeIdleStop = true
	if idleUnlimited {
		opts.IdleUnlimited = true
		opts.SemanticInactivityTimeout = officeUnlimitedSemanticInactivity
		return
	}
	opts.IdleWatchdogTimeout = officeIdleWindow
	opts.ToolWatchdogTimeout = toolWindow
}

// shouldResumeAfterIdleStop reports whether a run a watchdog stopped for
// silence gets its one continuation: the same session when it can be resumed,
// a fresh one with the stop note otherwise. Since DENE-2900 that is every such
// run, not only the office policy's: a server retry started from nothing and
// lost the context of dozens of steps.
func shouldResumeAfterIdleStop(result agent.Result) bool {
	return result.Status == "idle_watchdog"
}

// officeIdleResumePrompt is the next instruction of the resumed session.
const officeIdleResumePrompt = "Your previous step went quiet for too long, so Multica stopped it. " +
	"Continue the same task from where you left off. If a command hung, do not rerun it unchanged: " +
	"give it a timeout, run it in the background, or take another route."

// officeIdleResumePromptFor adds the stop note, which names the command the
// run stopped on or its last step (DENE-2900).
func officeIdleResumePromptFor(stopNote string) string {
	if stopNote == "" {
		return officeIdleResumePrompt
	}
	return officeIdleResumePrompt + "\n\nMultica's note on the stop: " + stopNote
}

// idleFreshPromptFor is the prompt of a fresh session after the stopped one
// could not be resumed: the task again, and what the stopped session did, so
// the work does not restart from nothing (DENE-2900).
func idleFreshPromptFor(taskPrompt, stopNote string) string {
	return taskPrompt + "\n\nAn earlier session on this task went quiet for too long and Multica " +
		"stopped it; it could not be resumed, so this is a new session. Do not redo finished work: " +
		"check the workspace and the issue first, then continue. If a command hung, do not rerun it " +
		"unchanged.\n\nMultica's note on the stop: " + stopNote
}

// idleStoppedTwiceNote heads the run's error when the resumed session was
// stopped again: nothing retries it automatically, a person decides.
const idleStoppedTwiceNote = "同一轮连续被停了 2 次（第一次停下后已在原会话接着跑过一次），不再自动重跑，这一轮记为阻塞。请人看一下卡在哪，再决定怎么继续。"

// idleNotContinuedNote heads the run's error when the continuation after the
// first stop could not even start: nothing reruns it, a person decides.
const idleNotContinuedNote = "这一轮被停下后，接着跑（原会话和新会话）都没能启动，不再自动重跑，这一轮记为阻塞。请人看一下下面的停止说明，再决定怎么继续。"

// The blocked issue's next step (at most 80 characters).
const (
	idleBlockAction        = "连停两次，不再自动重跑：看单里的停止说明，重派或补指示"
	idleNotContinuedAction = "停下后没能接着跑：看单里的停止说明，重派或补指示"
)

// blockIssueAfterIdleStops moves the run's issue to blocked once nothing will
// continue the run (DENE-2900): its continuation was stopped again or could
// not start. It writes as the run's agent, with the task's own token, and only
// while the issue is still in_progress with that agent assigned, so a status
// someone already set is never overwritten. It returns what the run's error
// adds: nothing once the issue is blocked or no longer this run's to move, and
// the failure with what to do when the move failed, so the issue does not
// read as in progress with no one running it.
func (d *Daemon) blockIssueAfterIdleStops(ctx context.Context, task Task, token, action string, taskLog *slog.Logger) string {
	if task.IssueID == "" || d.client == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	moved, err := d.client.BlockIssueAfterIdleStops(ctx, token, task.IssueID, task.AgentID, action)
	switch {
	case err != nil:
		taskLog.Warn("idle stop: could not move the issue to blocked", "issue_id", task.IssueID, "error", err)
		return "\n\n" + idleBlockFailedNote(err)
	case moved:
		taskLog.Info("idle stop: issue moved to blocked", "issue_id", task.IssueID)
	default:
		taskLog.Info("idle stop: issue left as is (not in_progress with this agent)", "issue_id", task.IssueID)
	}
	return ""
}

// idleBlockFailedNote tells whoever reads the issue that the move to blocked
// failed, why, and what to do instead.
func idleBlockFailedNote(err error) string {
	return "没能把单子挪到「已阻塞」（" + cutRunes(err.Error(), 200) + "）。单子可能还显示进行中，但已经没有员工在跑：请负责人手动挪到已阻塞，或重新派单。"
}
