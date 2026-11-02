package daemon

import (
	"fmt"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// idleStopFacts is what the idle watchdog knew when it stopped a run.
type idleStopFacts struct {
	budget time.Duration
	// commandRunning reports that a tool call was still in flight, so the
	// in-flight-tool budget ran out rather than the no-command one.
	commandRunning bool
	// capped reports that a command ran past the command cap, counted from
	// its start; otherwise the run went silent past the budget.
	capped      bool
	command     string        // the call it stopped on; "" when unknown
	commandAge  time.Duration // how long that call had run (awake time)
	lastStep    string        // the run's latest tool call; "" when it made none
	lastOutput  string        // the run's last output line; "" when none
	recentSteps []string      // the run's latest tool calls, oldest first
}

// idleStopNote is the issue comment a watchdog stop leaves (DENE-2900): which
// budget ran out, and the command it stopped on or the run's last step, so
// whoever continues the work knows where to pick it up. Every text in facts
// is already redacted and folded to one line.
func idleStopNote(f idleStopFacts) string {
	var b strings.Builder
	if f.commandRunning {
		b.WriteString("命令看门狗停了这一轮：")
		switch {
		case f.capped && f.command != "":
			fmt.Fprintf(&b, "命令 %s 已跑约 %s 还没结束，超过单条命令上限 %s。",
				noteCode(f.command), zhDuration(f.commandAge), zhDuration(f.budget))
		case f.capped:
			fmt.Fprintf(&b, "有命令跑了 %s 还没结束，超过单条命令上限。", zhDuration(f.budget))
		case f.command != "":
			fmt.Fprintf(&b, "命令 %s 还没结束，已经 %s 没有任何新消息，超过单条命令上限。",
				noteCode(f.command), zhDuration(f.budget))
		default:
			fmt.Fprintf(&b, "有命令还在跑，已经 %s 没有任何新消息，超过单条命令上限。", zhDuration(f.budget))
		}
		writeRecentSteps(&b, f.recentSteps)
		b.WriteString("续跑时别原样重跑这条命令：加超时、放后台，或换个办法。")
		return b.String()
	}
	fmt.Fprintf(&b, "空闲看门狗停了这一轮：%s 没有新消息，也没有在跑的命令。", zhDuration(f.budget))
	if f.lastStep != "" {
		fmt.Fprintf(&b, "最后一步：%s。", noteCode(f.lastStep))
	} else {
		b.WriteString("这一轮还没调用过工具。")
	}
	if f.lastOutput != "" {
		fmt.Fprintf(&b, "最后输出：%s", f.lastOutput)
		if !strings.HasSuffix(f.lastOutput, "。") {
			b.WriteString("。")
		}
	}
	writeRecentSteps(&b, f.recentSteps)
	b.WriteString("续跑时从这里接着做。")
	return b.String()
}

const (
	// stopMaxSteps and stopStepMaxRunes bound the step summary a stop note
	// carries into the resumed or fresh session.
	stopMaxSteps     = 8
	stopStepMaxRunes = 120
)

// writeRecentSteps lists the steps before the last one, which the note
// already names, so whoever continues sees what the run had done.
func writeRecentSteps(b *strings.Builder, steps []string) {
	if len(steps) < 2 {
		return
	}
	earlier := steps[:len(steps)-1]
	parts := make([]string, len(earlier))
	for i, step := range earlier {
		parts[i] = noteCode(step)
	}
	fmt.Fprintf(b, "之前几步（从早到晚）：%s。", strings.Join(parts, " → "))
}

// cutRunes shortens s to at most n runes, marking a cut with "…".
func cutRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}

// codexSemanticStopNote is the stop note for a turn Codex's own semantic
// timer ended, or "" when the result is anything else. With a command running
// and a command cap set, that timer stopped the command at the cap, counted
// from its start; otherwise the model was silent past the semantic timeout.
func codexSemanticStopNote(result agent.Result, opts agent.ExecOptions, live *liveTracker, commandRunning bool) string {
	if result.Status != "timeout" || !strings.Contains(result.Error, agent.CodexSemanticInactivityMarker) {
		return ""
	}
	if commandRunning && opts.SemanticCommandTimeout > 0 {
		return idleStopNote(live.stopFacts(opts.SemanticCommandTimeout, true, true))
	}
	budget := opts.SemanticInactivityTimeout
	if budget <= 0 {
		budget = DefaultCodexSemanticInactivityTimeout
	}
	return idleStopNote(live.stopFacts(budget, commandRunning, false))
}

// noteCode renders a command as inline code. A backtick inside it would end
// the span early, so it becomes a quote.
func noteCode(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "'") + "`"
}

// zhDuration renders a duration the way a person says it: "45 秒",
// "10 分钟", "1 小时 5 分钟". Seconds are dropped once there are minutes.
func zhDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return d.String()
	}
	d = d.Round(time.Second)
	hours := int(d / time.Hour)
	minutes := int(d % time.Hour / time.Minute)
	switch {
	case hours > 0 && minutes > 0:
		return fmt.Sprintf("%d 小时 %d 分钟", hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%d 小时", hours)
	case minutes > 0:
		return fmt.Sprintf("%d 分钟", minutes)
	default:
		return fmt.Sprintf("%d 秒", int(d/time.Second))
	}
}
