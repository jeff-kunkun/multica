package logexport

import (
	"regexp"
	"sort"
	"strings"

	"github.com/multica-ai/multica/server/pkg/redact"
)

// minSecretLen keeps short literals out of the literal mask. A two-character
// environment value such as "1" or "on" would otherwise blank out half the log.
const minSecretLen = 6

const (
	envPlaceholder    = "[REDACTED ENV]"
	secretPlaceholder = "[REDACTED SECRET]"
)

// envAssignRe matches a shell-style environment assignment at the start of a
// line: `NAME=value`, `export NAME=value`, `declare -x NAME="value"`. The
// export drops every such value, not only the ones whose name looks secret —
// `env` and `printenv` dumps are the usual way a credential with an innocent
// name ends up in a transcript. The name is kept: it is what a reader needs to
// reason about the environment.
var envAssignRe = regexp.MustCompile(`(?m)^(\s*(?:export\s+|declare\s+-x\s+)?[A-Z][A-Z0-9_]{1,}=)(\S.*)$`)

// masker applies the export's redaction: the shared pattern table first, then
// environment assignments, then the literal secret values of this workspace.
type masker struct {
	literals []string
}

func newMasker(secrets []string) *masker {
	seen := make(map[string]struct{}, len(secrets))
	lits := make([]string, 0, len(secrets))
	for _, s := range secrets {
		s = strings.TrimSpace(s)
		if len(s) < minSecretLen {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		lits = append(lits, s)
	}
	// Longest first so a value that contains another is replaced whole.
	sort.Slice(lits, func(i, j int) bool { return len(lits[i]) > len(lits[j]) })
	return &masker{literals: lits}
}

func (m *masker) text(s string) string {
	if s == "" {
		return s
	}
	// Literals run first: once a pattern rewrites part of a value, the literal
	// no longer matches and its remainder would survive.
	for _, lit := range m.literals {
		s = strings.ReplaceAll(s, lit, secretPlaceholder)
	}
	s = redact.Text(s)
	return envAssignRe.ReplaceAllString(s, "${1}"+envPlaceholder)
}

func (m *masker) value(v any, depth int) any {
	if depth > 32 {
		return secretPlaceholder
	}
	switch t := v.(type) {
	case string:
		return m.text(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = m.value(val, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = m.value(val, depth+1)
		}
		return out
	}
	return v
}

func (m *masker) entry(e Entry) Entry {
	e.Content = m.text(e.Content)
	e.Output = m.text(e.Output)
	if e.Input != nil {
		e.Input, _ = m.value(e.Input, 0).(map[string]any)
	}
	return e
}
