// Package blockwait keeps only the acceptance verdict marker and the list of
// legacy block.* metadata keys. The timed re-check patrol that owned this
// package is gone; the callers left here belong to acceptance and quota relay.
package blockwait

import (
	"fmt"
	"strings"
)

const (
	// VerdictPassLine and VerdictHoldLine are the only acceptance markers that
	// may merge or close an issue. They must be a line by themselves.
	VerdictPassLine = "verdict: pass"
	VerdictHoldLine = "verdict: hold"
)

// WaitKeys are the legacy structured-wait metadata keys. Nothing writes them
// any more; a quota relay still clears stale ones off the issue it hands over.
func WaitKeys() []string {
	return []string{
		"block.blocked_by",
		"block.wake_at",
		"block.wait_condition",
		"block.wait_probe",
		"block.wait_timeout",
		"block.wait_probe_status",
		"block.wait_probe_at",
		"block.wait_probe_output",
		"block.wait_probe_notified",
		"block.needs_human",
		"block.woken_by",
		"block.patrol_at",
		"block.segment_nudged",
		"close.waiting_on",
	}
}

// Verdict reports a standalone acceptance marker. "hold" wins when both lines
// are present. Prose that merely contains the words is not a verdict.
func Verdict(body string) string {
	pass, hold := false, false
	for _, line := range strings.Split(body, "\n") {
		switch strings.ToLower(strings.TrimSpace(line)) {
		case VerdictPassLine:
			pass = true
		case VerdictHoldLine:
			hold = true
		}
	}
	if hold {
		return "hold"
	}
	if pass {
		return "pass"
	}
	return ""
}

// AppendVerdict adds the standalone marker line. An empty verdict leaves the
// body unchanged. The marker is not repeated when it is already its own line.
func AppendVerdict(body, verdict string) (string, error) {
	verdict = strings.ToLower(strings.TrimSpace(verdict))
	if verdict == "" {
		return body, nil
	}
	if verdict != "pass" && verdict != "hold" {
		return "", fmt.Errorf("verdict 只能是 pass 或 hold")
	}
	if Verdict(body) == verdict {
		return body, nil
	}
	line := "verdict: " + verdict
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return line + "\n", nil
	}
	return body + "\n\n" + line + "\n", nil
}
