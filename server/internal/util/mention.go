package util

import (
	"regexp"
	"strings"
)

// Mention represents a parsed @mention from markdown content.
type Mention struct {
	Type string // "member", "agent", "issue", or "all"
	ID   string // user_id, agent_id, issue_id, or "all"
}

// MentionRe matches [@Label](mention://type/id) or [Label](mention://issue/id) in markdown.
// The @ prefix is optional to support issue mentions which use [MUL-123](mention://issue/...).
// Uses .+? (non-greedy) instead of [^\]]* so labels containing square brackets
// (e.g. "David[TF]") are matched correctly — the ](mention:// anchor is specific
// enough to prevent over-matching.
var MentionRe = regexp.MustCompile(`\[@?(.+?)\]\(mention://(member|agent|bot|squad|issue|all)/([0-9a-fA-F-]+|all)\)`)

// IsMentionAll returns true if the mention is an @all mention.
func (m Mention) IsMentionAll() bool {
	return m.Type == "all"
}

// ParseMentions extracts deduplicated mentions from markdown content.
func ParseMentions(content string) []Mention {
	matches := MentionRe.FindAllStringSubmatch(MentionText(content), -1)
	seen := make(map[string]bool)
	var result []Mention
	for _, m := range matches {
		key := m[2] + ":" + m[3]
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, Mention{Type: m[2], ID: m[3]})
	}
	return result
}

// HasMentionAll returns true if any mention in the slice is an @all mention.
func HasMentionAll(mentions []Mention) bool {
	for _, m := range mentions {
		if m.IsMentionAll() {
			return true
		}
	}
	return false
}

// MentionText defines the conservative notification grammar shared with
// bot_wake_mention_text in SQL. Code/quote lines and inline backtick spans never
// create notifications; unmatched inline backticks suppress the rest of a line.
func MentionText(content string) string {
	var out strings.Builder
	fence := ""
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = trimmed[:3]
			continue
		}
		if strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") || strings.HasPrefix(trimmed, ">") {
			continue
		}
		for len(line) > 0 {
			start := strings.IndexByte(line, '`')
			if start < 0 {
				out.WriteString(line)
				break
			}
			out.WriteString(line[:start])
			line = line[start:]
			n := 0
			for n < len(line) && line[n] == '`' {
				n++
			}
			marker := line[:n]
			line = line[n:]
			end := strings.Index(line, marker)
			if end < 0 {
				break
			}
			line = line[end+n:]
		}
		out.WriteByte('\n')
	}
	return out.String()
}
