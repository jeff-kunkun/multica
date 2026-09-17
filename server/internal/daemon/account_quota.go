package daemon

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// accountQuotaProviderCLI maps a runtime provider onto the CLI family whose
// account directory one of its runs burns.
//
// Antigravity is deliberately absent: its quota accounting runs inside
// agyQuotaFailover, which also picks the next slot and retries. Recording the
// same hit twice would move one deadline through two code paths.
//
// Codex and cursor ARE here even though they cannot be rebound today (their
// account directory is chosen by CODEX_HOME / CURSOR_DATA_DIR, which the task
// environment rewrites per task). "This account is out of quota until T" is
// still true for them and still worth showing — what they lack is the switch,
// not the fact.
var accountQuotaProviderCLI = map[string]string{
	"dsh":    "dsh",
	"claude": "claude",
	"codex":  "codex",
	"cursor": "cursor",
}

// accountLeverEnvKey returns the environment variable an `env:NAME` lever
// writes, or "" for any other lever shape. It is the daemon-side twin of the
// frontend's parseAccountLever and reads the same strings the probe table
// advertises, so the two cannot drift about which key binds a CLI.
func accountLeverEnvKey(lever string) string {
	trimmed := strings.TrimSpace(lever)
	rest, ok := strings.CutPrefix(trimmed, "env:")
	if !ok {
		return ""
	}
	return strings.TrimSpace(rest)
}

// resolveRunAccountHome returns the account directory a run of cli used, or ""
// when this daemon cannot say.
//
// The answer comes from the same probe table the account report is built from:
// the CLI's env lever read out of the agent's custom_env when it is set, and
// the CLI's own directory under home otherwise. Both branches produce a path
// that compares equal to a reported row's `home`, which is what lets the stamp
// find the row it belongs to.
func resolveRunAccountHome(cli string, customEnv map[string]string, home string) string {
	home = strings.TrimSpace(home)
	for _, probe := range agentCLIProbes {
		if probe.CLI != cli {
			continue
		}
		if key := accountLeverEnvKey(probe.Lever); key != "" {
			if value := strings.TrimSpace(customEnv[key]); value != "" {
				if home != "" {
					value = expandAccountHomePrefix(value, home)
				}
				// A relative override cannot be matched against the absolute
				// paths the probe reports, so claiming a directory would be a
				// guess. Say nothing instead.
				if !filepath.IsAbs(value) {
					return ""
				}
				return agent.NormalizeAgyDir(value)
			}
		}
		if home == "" || probe.BaseDir == "" {
			return ""
		}
		return agent.NormalizeAgyDir(filepath.Join(home, probe.BaseDir))
	}
	return ""
}

// expandAccountHomePrefix resolves a leading ~ against the host home. Anything
// else is returned untouched.
func expandAccountHomePrefix(path, home string) string {
	path = strings.TrimSpace(path)
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return filepath.Join(home, path[2:])
	}
	return path
}

// accountQuotaResetAt reports the deadline a finished run proves its account is
// exhausted until, and whether the run proves that at all.
//
// Two signals, in order, because they answer the question with different
// confidence:
//
//  1. Result.PlanLimits with status "exhausted" — the provider's own verdict,
//     already the source the runtime availability UI trusts. Reading it here
//     rather than re-deriving keeps the account pill and the runtime health
//     badge from disagreeing about the same run.
//  2. taskfailure.Classify landing in provider_quota_limit — the classifier
//     every failed task already goes through, so a phrasing it learns is a
//     phrasing this path learns with it.
//
// Only Result.Error is classified, never Result.Output: the output is the
// agent's own prose, and an agent that writes the word "quota" in an answer
// must not burn its account.
//
// The deadline itself prefers the provider's soonest reported window, falls
// back to a "Resets in …" hint in the error text, and lands on a short default
// when neither exists — see agent.DefaultQuotaResetAt for why short.
func accountQuotaResetAt(result agent.Result, now time.Time) (time.Time, bool) {
	planExhausted := result.PlanLimits != nil &&
		result.PlanLimits.Status == protocol.PlanLimitsStatusExhausted
	if !planExhausted && taskfailure.Classify(result.Error) != taskfailure.ReasonAgentProviderQuotaLimit {
		return time.Time{}, false
	}
	if at := soonestPlanLimitReset(result.PlanLimits, now); !at.IsZero() {
		return at, true
	}
	if at := agent.ParseQuotaResetHint(result.Error, now); !at.IsZero() {
		return at, true
	}
	return agent.DefaultQuotaResetAt(now, time.Time{}), true
}

// soonestPlanLimitReset returns the nearest still-future window reset in a
// snapshot. A window that already reset is not a deadline; a snapshot with no
// windows (the common shape for a CLI that only reported a 429) has none.
func soonestPlanLimitReset(snapshot *protocol.PlanLimitsSnapshot, now time.Time) time.Time {
	if snapshot == nil {
		return time.Time{}
	}
	var soonest time.Time
	for _, window := range snapshot.Windows {
		if window.ResetsAt == nil || *window.ResetsAt <= 0 {
			continue
		}
		at := time.Unix(*window.ResetsAt, 0)
		if !at.After(now) {
			continue
		}
		if soonest.IsZero() || at.Before(soonest) {
			soonest = at
		}
	}
	return soonest
}

// recordRunAccountQuota stamps the account directory a finished run burned when
// that run failed on quota.
//
// Called once per task on the terminal result, after every retry the run may
// have taken: an intermediate failure that a retry recovered from is not an
// exhausted account, and recording it would hide a usable account behind a
// deadline nothing else would clear.
func (d *Daemon) recordRunAccountQuota(provider string, customEnv map[string]string, result agent.Result, now time.Time) {
	cli, ok := accountQuotaProviderCLI[strings.TrimSpace(provider)]
	if !ok {
		return
	}
	resetAt, exhausted := accountQuotaResetAt(result, now)
	if !exhausted {
		return
	}
	home, err := agyQuotaHomeFn()
	if err != nil {
		home = ""
	}
	dir := resolveRunAccountHome(cli, customEnv, home)
	if dir == "" {
		return
	}
	d.markAccountQuotaExhausted(dir, resetAt)
}
