package issuewatchdog

import (
	"strings"

	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/util"
)

// Reviews in the office are mostly prose ("审查结论：需要修改，暂不通过",
// "复审通过 PR #14"), not the `verdict:` line, so both are recognized. The
// line wins when present.
var (
	reviewConclusionMarkers = []string{"审查结论", "复审结论", "审核结论", "审查意见", "复审意见"}
	reviewLeads             = []string{"审查", "复审", "审核"}
	// Checked before the pass words: "暂不通过" also contains "通过".
	reviewHoldWords = []string{"不通过", "未通过", "需要修改", "需修改", "要修改", "需要修正", "需修正",
		"暂不交", "打回", "退回", "驳回", "hold", "request changes", "changes requested"}
	reviewPassWords    = []string{"通过", "pass", "approve", "lgtm"}
	reviewRequestWords = []string{"审查", "复审", "审核", "review"}
)

// reviewVerdict reports "hold", "pass", or "" for a comment. Prose counts only
// when the comment's first line names a conclusion or opens with the review
// itself, so a plan that merely mentions 审查 is not a verdict.
func reviewVerdict(content string) string {
	if v := blockwait.Verdict(content); v != "" {
		return v
	}
	text := strings.ReplaceAll(content, "**", "")
	text = stripSignature(strings.TrimSpace(text))
	firstLine, _, _ := strings.Cut(text, "\n")
	clause := ""
	for _, marker := range reviewConclusionMarkers {
		if i := strings.Index(firstLine, marker); i >= 0 {
			clause = strings.TrimLeft(firstLine[i+len(marker):], " ：:")
			break
		}
	}
	if clause == "" {
		for _, lead := range reviewLeads {
			if strings.HasPrefix(firstLine, lead) {
				clause = firstLine
				break
			}
		}
	}
	if clause == "" {
		return ""
	}
	if end := strings.IndexAny(clause, "。；;"); end >= 0 {
		clause = clause[:end]
	}
	clause = strings.ToLower(clause)
	for _, word := range reviewHoldWords {
		if strings.Contains(clause, word) {
			return "hold"
		}
	}
	for _, word := range reviewPassWords {
		if strings.Contains(clause, word) {
			return "pass"
		}
	}
	return ""
}

// stripSignature drops a leading 【…】 or […] role signature.
func stripSignature(text string) string {
	for _, pair := range [][2]string{{"【", "】"}, {"[", "]"}} {
		if strings.HasPrefix(text, pair[0]) && !strings.HasPrefix(text, "[@") {
			if end := strings.Index(text, pair[1]); end > 0 {
				return strings.TrimSpace(text[end+len(pair[1]):])
			}
		}
	}
	return text
}

type actor struct {
	Type string
	ID   string
}

func (a actor) empty() bool { return a.ID == "" }

// mentionedActors lists the agents and members a comment @mentions.
func mentionedActors(content string) []actor {
	var out []actor
	for _, m := range util.ParseMentions(content) {
		if m.Type == "agent" || m.Type == "member" {
			out = append(out, actor{Type: m.Type, ID: m.ID})
		}
	}
	return out
}

func mentions(content string, target actor) bool {
	for _, a := range mentionedActors(content) {
		if a == target {
			return true
		}
	}
	return false
}

// isReviewRequest is a developer's comment that @mentions someone and asks
// for a review or re-review.
func isReviewRequest(content string) bool {
	lower := strings.ToLower(util.MentionText(content))
	for _, word := range reviewRequestWords {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}
