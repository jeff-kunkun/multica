package blockwait

import (
	"fmt"
	"strings"
	"time"
)

// Patrol actions. Hold means the wait is still legitimate.
const (
	ActionHold    = "hold"
	ActionWake    = "wake"
	ActionRelease = "release"
)

// BlockerView is the live state of one issue this record waits on.
type BlockerView struct {
	Ref      string
	Status   string
	Accepted bool
}

// Cleared reports whether the blocker no longer holds the waiter.
func (b BlockerView) Cleared() bool {
	switch b.Status {
	case "done", "cancelled":
		return true
	default:
		return b.Accepted
	}
}

// PatrolInput is one quiet blocked or in-review issue.
type PatrolInput struct {
	Status         string
	Quiet          time.Duration
	Record         Record
	Blockers       []BlockerView
	Now            time.Time
	LastPatrol     time.Time
	HasLastPatrol  bool
	HasPassComment bool
	ReleasedPass   bool
}

// Decision is what the patrol or the acceptance hook should do, plus the
// sentence to leave on the issue.
type Decision struct {
	Action string
	Reason string
	Record Record
}

// DecidePatrol picks a single next step for one stalled issue.
func DecidePatrol(in PatrolInput) Decision {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	recentPatrol := in.HasLastPatrol && in.Now.Sub(in.LastPatrol) < QuietAfter
	// A wake that came due after the last patrol is a new event. Anything else
	// we already said stays said until the quiet window passes again.
	if recentPatrol {
		if in.Record.HasWakeAt && in.LastPatrol.Before(in.Record.WakeAt) && !in.Now.Before(in.Record.WakeAt) {
			return Decision{Action: ActionWake, Reason: "到了预定的复查时间，叫醒执行人。"}
		}
		return Decision{Action: ActionHold}
	}

	for _, blocker := range in.Blockers {
		if blocker.Cleared() && !recentPatrol {
			return Decision{Action: ActionWake, Reason: fmt.Sprintf("挡路的 %s 已经解除（%s），叫醒等待方继续。", blocker.Ref, blocker.Status)}
		}
	}
	if in.Record.HasWakeAt && !in.Now.Before(in.Record.WakeAt) && !recentPatrol {
		return Decision{Action: ActionWake, Reason: "到了预定的复查时间，叫醒执行人。"}
	}
	if in.Record.HasWaitTimeout && !in.Now.Before(in.Record.WaitTimeout) && !recentPatrol {
		what := in.Record.WaitCondition
		if what == "" {
			what = "外部条件"
		}
		return Decision{Action: ActionWake, Reason: fmt.Sprintf("等「%s」已经过了截止时间，叫醒执行人复查。", what)}
	}
	if in.Status == "in_review" && (in.HasPassComment || in.ReleasedPass) && !recentPatrol {
		return Decision{Action: ActionRelease, Reason: "验收已经通过，但票还停在待验收。平台按通过收口。"}
	}
	if in.Quiet >= QuietAfter && !recentPatrol {
		if in.Status == "in_review" {
			return Decision{Action: ActionWake, Reason: "待验收超过 30 分钟没有运行，叫醒验收人。"}
		}
		if in.Status == "blocked" && !in.Record.Structured() {
			return Decision{Action: ActionWake, Reason: "这张票标了阻塞，但没写在等什么，也没有运行。平台把它接回来。"}
		}
		if in.Status == "blocked" && waitingOnOpen(in) {
			return Decision{Action: ActionHold, Reason: "挡路的票还没结束。"}
		}
	}
	return Decision{Action: ActionHold}
}

func waitingOnOpen(in PatrolInput) bool {
	if len(in.Blockers) == 0 {
		return false
	}
	for _, blocker := range in.Blockers {
		if !blocker.Cleared() {
			return true
		}
	}
	return false
}

// PRSnapshot is the slice of pull-request state the release decision needs.
type PRSnapshot struct {
	Number    int
	State     string
	Mergeable string
	Checks    string
	URL       string
}

// Release actions.
const (
	ReleaseDone  = "done"
	ReleaseMerge = "merge"
	ReleaseBlock = "block"
)

// DecideRelease says what an acceptance pass should do with the linked PRs.
// A pass never stays in in_review: no open PR closes the issue, a clean PR is
// merged, and a conflict or a red check becomes a structured block.
func DecideRelease(prs []PRSnapshot, now time.Time) Decision {
	if now.IsZero() {
		now = time.Now()
	}
	var open []PRSnapshot
	for _, pr := range prs {
		if strings.EqualFold(pr.State, "open") {
			open = append(open, pr)
		}
	}
	if len(open) == 0 {
		return Decision{Action: ReleaseDone, Reason: "验收已经通过，没有还开着的 PR，这张票可以关了。"}
	}
	for _, pr := range open {
		if prBlocked(pr) {
			rec := Record{
				WaitCondition:  prBlockReason(pr),
				HasWaitTimeout: true,
				WaitTimeout:    now.Add(QuietAfter),
				HasWakeAt:      true,
				WakeAt:         now.Add(QuietAfter),
			}
			return Decision{
				Action: ReleaseBlock,
				Reason: fmt.Sprintf("验收已经通过，但 %s。先标成阻塞，到点再看，不继续停在待验收。", rec.WaitCondition),
				Record: rec,
			}
		}
	}
	label := prLabel(open[0])
	return Decision{
		Action: ReleaseMerge,
		Reason: fmt.Sprintf("验收已经通过，平台合并 %s 并关票。", label),
		Record: Record{HasWakeAt: true, WakeAt: now.Add(QuietAfter), WaitCondition: "验收已通过，等待合并 " + label},
	}
}

func prBlocked(pr PRSnapshot) bool {
	switch strings.ToLower(pr.Mergeable) {
	case "dirty", "blocked", "behind":
		return true
	}
	switch strings.ToLower(pr.Checks) {
	case "failure", "error", "failing", "cancelled":
		return true
	}
	return false
}

func prBlockReason(pr PRSnapshot) string {
	label := prLabel(pr)
	switch strings.ToLower(pr.Mergeable) {
	case "dirty":
		return label + " 有合并冲突"
	case "behind":
		return label + " 分支落后，合不进去"
	case "blocked":
		return label + " 被分支保护挡住"
	}
	switch strings.ToLower(pr.Checks) {
	case "failure", "error", "failing":
		return label + " 的检查是红的"
	case "cancelled":
		return label + " 的检查被取消了"
	}
	return label + " 现在合不进去"
}

func prLabel(pr PRSnapshot) string {
	if pr.URL != "" {
		return pr.URL
	}
	if pr.Number > 0 {
		return fmt.Sprintf("PR #%d", pr.Number)
	}
	return "PR"
}

var (
	passPhrases = []string{"验收通过", "通过验收", "等待合并", "待合并", "verdict: pass", "verdict=pass"}
	holdPhrases = []string{"验收不通过", "不通过", "needs-work", "需要修改", "打回", "暂不合并"}
)

// IsAcceptancePass reports whether a reviewer comment is a pass. A hold phrase
// wins, so "验收不通过" is not a pass.
func IsAcceptancePass(body string) bool {
	if IsAcceptanceHold(body) {
		return false
	}
	lower := strings.ToLower(body)
	for _, phrase := range passPhrases {
		if strings.Contains(lower, strings.ToLower(phrase)) {
			return true
		}
	}
	return false
}

// IsAcceptanceHold reports whether the comment refuses the pass.
func IsAcceptanceHold(body string) bool {
	lower := strings.ToLower(body)
	for _, phrase := range holdPhrases {
		if strings.Contains(lower, strings.ToLower(phrase)) {
			return true
		}
	}
	return false
}
