package logexport

import (
	"fmt"
	"strings"
	"time"
)

const (
	// summaryTailEntries is how many trailing entries of each run the summary
	// quotes. The end of a run is where a failure shows.
	summaryTailEntries = 12
	// summaryErrorEntries bounds the quoted error entries per run.
	summaryErrorEntries = 8
	// summaryLineLimit clips one quoted line.
	summaryLineLimit = 400
)

// buildSummary renders summary.md: a self-contained brief a reader can paste
// into an AI conversation without opening the archive.
func buildSummary(m Meta, runs []Run) string {
	var b strings.Builder
	b.WriteString("# Multica task log export\n\n")
	b.WriteString("This is a redacted export of agent run logs from Multica. ")
	b.WriteString("Tokens, passwords and environment variable values were removed; ")
	b.WriteString("`[REDACTED ...]` marks where. Full entries are in `logs.jsonl`, run metadata in `meta.json`.\n\n")

	b.WriteString("## Context\n\n")
	if m.IssueIdentifier != "" || m.IssueTitle != "" {
		fmt.Fprintf(&b, "- Issue: %s %s\n", m.IssueIdentifier, m.IssueTitle)
	}
	fmt.Fprintf(&b, "- Anchor run (task id): `%s`\n", m.AnchorTaskID)
	fmt.Fprintf(&b, "- Scope: %s\n", scopeLabel(m))
	fmt.Fprintf(&b, "- Time window: %s → %s\n", fmtTime(m.Window.From), fmtTime(m.Window.To))
	fmt.Fprintf(&b, "- Runs: %d, log entries: %d\n", m.RunCount, m.EntryCount)
	if m.DroppedEntries > 0 {
		fmt.Fprintf(&b, "- Trimmed: the %d oldest entries were dropped to keep the package small\n", m.DroppedEntries)
	}
	if m.Partial {
		b.WriteString("- PARTIAL EXPORT — not everything could be collected:\n")
		for _, w := range m.Warnings {
			fmt.Fprintf(&b, "  - %s\n", w)
		}
	}
	fmt.Fprintf(&b, "- Generated at: %s\n", m.GeneratedAt.Format(time.RFC3339))

	b.WriteString("\n## Runs\n\n")
	b.WriteString("| task id | agent | status | exit code | failure | started | completed | entries |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, r := range runs {
		agent := r.AgentName
		if agent == "" {
			agent = r.AgentID
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %s | %s | %d |\n",
			r.TaskID, cell(agent), r.Status, fmtExit(r.ExitCode), cell(r.FailureReason),
			fmtTime(r.StartedAt), fmtTime(r.CompletedAt), len(r.Entries))
	}

	for _, r := range runs {
		fmt.Fprintf(&b, "\n## Run `%s` (%s)\n\n", r.TaskID, r.Status)
		if r.Error != "" {
			fmt.Fprintf(&b, "Recorded error:\n\n```\n%s\n```\n\n", clip(r.Error, 4*summaryLineLimit))
		}
		if errs := errorEntries(r.Entries); len(errs) > 0 {
			b.WriteString("Error entries:\n\n```\n")
			for _, e := range errs {
				b.WriteString(entryLine(e))
			}
			b.WriteString("```\n\n")
		}
		tail := r.Entries
		if len(tail) > summaryTailEntries {
			tail = tail[len(tail)-summaryTailEntries:]
		}
		if len(tail) == 0 {
			b.WriteString("No log entries in this window.\n")
			continue
		}
		fmt.Fprintf(&b, "Last %d entries:\n\n```\n", len(tail))
		for _, e := range tail {
			b.WriteString(entryLine(e))
		}
		b.WriteString("```\n")
	}
	return b.String()
}

func scopeLabel(m Meta) string {
	switch m.Scope {
	case ScopeHours:
		return fmt.Sprintf("last %d hours, all runs on the issue", m.Hours)
	case ScopeTask:
		return "entire task (all runs on the issue)"
	}
	return "this run only"
}

func errorEntries(entries []Entry) []Entry {
	var out []Entry
	for _, e := range entries {
		if e.Type == "error" {
			out = append(out, e)
		}
	}
	if len(out) > summaryErrorEntries {
		out = out[len(out)-summaryErrorEntries:]
	}
	return out
}

func entryLine(e Entry) string {
	body := e.Content
	if body == "" {
		body = e.Output
	}
	label := e.Type
	if e.Tool != "" {
		label += ":" + e.Tool
	}
	return fmt.Sprintf("[%s #%d %s] %s\n", e.CreatedAt.UTC().Format("15:04:05"), e.Seq, label,
		clip(strings.ReplaceAll(strings.TrimSpace(body), "\n", " ⏎ "), summaryLineLimit))
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func cell(s string) string {
	if s == "" {
		return "—"
	}
	return strings.ReplaceAll(s, "|", "\\|")
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return t.UTC().Format(time.RFC3339)
}

func fmtExit(code *int) string {
	if code == nil {
		return "—"
	}
	return fmt.Sprintf("%d", *code)
}
