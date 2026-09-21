// Package logexport builds the task log bundle: one redacted zip holding the
// structured run logs, the run metadata and a summary written to be pasted
// straight into an AI conversation.
//
// The package is pure — it takes rows the caller already loaded and returns
// bytes — so the HTTP endpoint, the report action and the CLI (which calls the
// endpoint) cannot drift into producing different artifacts.
package logexport

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Scope selects which runs and which time window a bundle covers.
type Scope string

const (
	// ScopeRun covers only the anchor run.
	ScopeRun Scope = "run"
	// ScopeHours covers every run on the anchor's issue, limited to entries
	// from the last N hours.
	ScopeHours Scope = "hours"
	// ScopeTask covers every run on the anchor's issue.
	ScopeTask Scope = "task"
)

const (
	// DefaultHours is the window used when ScopeHours carries no value.
	DefaultHours = 6
	// MaxHours bounds the window; anything longer is ScopeTask's job.
	MaxHours = 24 * 30
	// FormatVersion is bumped when the bundle layout changes incompatibly.
	FormatVersion = 1
)

// ErrNoLogs means the selected scope holds no log entries. It is a normal
// outcome, not a failure: the caller offers a wider scope instead.
var ErrNoLogs = errors.New("no log entries in the selected scope")

// ParseScope validates a scope string. The empty string means ScopeRun.
func ParseScope(s string) (Scope, error) {
	switch Scope(strings.TrimSpace(s)) {
	case "", ScopeRun:
		return ScopeRun, nil
	case ScopeHours:
		return ScopeHours, nil
	case ScopeTask:
		return ScopeTask, nil
	}
	return "", fmt.Errorf("invalid scope %q (want run, hours or task)", s)
}

// NormalizeHours clamps an hours value into the supported window.
func NormalizeHours(h int) int {
	if h <= 0 {
		return DefaultHours
	}
	if h > MaxHours {
		return MaxHours
	}
	return h
}

// Entry is one structured log line.
type Entry struct {
	TaskID          string         `json:"task_id"`
	Seq             int            `json:"seq"`
	Type            string         `json:"type"`
	Tool            string         `json:"tool,omitempty"`
	Content         string         `json:"content,omitempty"`
	Input           map[string]any `json:"input,omitempty"`
	Output          string         `json:"output,omitempty"`
	OutputTruncated bool           `json:"output_truncated,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
}

// Run is the metadata of one run plus its log entries.
type Run struct {
	TaskID        string     `json:"task_id"`
	AgentID       string     `json:"agent_id"`
	AgentName     string     `json:"agent_name,omitempty"`
	Status        string     `json:"status"`
	FailureReason string     `json:"failure_reason,omitempty"`
	Error         string     `json:"error,omitempty"`
	ExitCode      *int       `json:"exit_code"`
	Attempt       int        `json:"attempt"`
	BranchName    string     `json:"branch_name,omitempty"`
	WorkDir       string     `json:"work_dir,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	StartedAt     *time.Time `json:"started_at"`
	CompletedAt   *time.Time `json:"completed_at"`
	EntryCount    int        `json:"entry_count"`

	Entries []Entry `json:"-"`
}

// Input is everything Build needs.
type Input struct {
	WorkspaceID     string
	IssueID         string
	IssueIdentifier string
	IssueTitle      string
	AnchorTaskID    string
	Scope           Scope
	Hours           int
	GeneratedAt     time.Time
	Runs            []Run
	// Secrets are literal values (agent environment values, stored tokens)
	// masked wherever they appear, on top of the pattern-based redaction.
	Secrets []string
	// Warnings records what could not be collected. A non-empty list marks
	// the bundle partial.
	Warnings []string
	// MaxBytes caps the zip size. Zero means no cap. When the full bundle is
	// larger, the oldest entries are dropped until it fits.
	MaxBytes int
}

// Window is the time span the bundle covers.
type Window struct {
	From *time.Time `json:"from"`
	To   *time.Time `json:"to"`
}

// Meta is meta.json, and the preview the UI renders before download.
type Meta struct {
	FormatVersion   int       `json:"format_version"`
	GeneratedAt     time.Time `json:"generated_at"`
	WorkspaceID     string    `json:"workspace_id"`
	IssueID         string    `json:"issue_id,omitempty"`
	IssueIdentifier string    `json:"issue_identifier,omitempty"`
	IssueTitle      string    `json:"issue_title,omitempty"`
	AnchorTaskID    string    `json:"anchor_task_id"`
	Scope           Scope     `json:"scope"`
	Hours           int       `json:"hours,omitempty"`
	Window          Window    `json:"window"`
	RunCount        int       `json:"run_count"`
	EntryCount      int       `json:"entry_count"`
	DroppedEntries  int       `json:"dropped_entries"`
	Partial         bool      `json:"partial"`
	Warnings        []string  `json:"warnings"`
	Redacted        bool      `json:"redacted"`
	Runs            []Run     `json:"runs"`
}

// Bundle is a built log package.
type Bundle struct {
	Filename string
	Zip      []byte
	Summary  string
	Meta     Meta
}

// Build assembles the bundle. It returns ErrNoLogs when the scope is empty.
func Build(in Input) (Bundle, error) {
	mask := newMasker(in.Secrets)
	runs := selectRuns(in, mask)

	total := 0
	for _, r := range runs {
		total += len(r.Entries)
	}
	if total == 0 {
		return Bundle{}, ErrNoLogs
	}

	dropped := 0
	for {
		b, err := assemble(in, runs, dropped)
		if err != nil {
			return Bundle{}, err
		}
		if in.MaxBytes <= 0 || len(b.Zip) <= in.MaxBytes {
			return b, nil
		}
		remaining := total - dropped
		if remaining <= 1 {
			// A single entry that still does not fit: ship it anyway rather
			// than produce an empty package.
			return b, nil
		}
		// Halve what is left each round: a handful of rebuilds even for a
		// bundle many times over the cap.
		step := remaining / 2
		dropOldest(runs, step)
		dropped += step
	}
}

// selectRuns applies the scope, redacts, and returns runs oldest first.
func selectRuns(in Input, mask *masker) []Run {
	var cutoff time.Time
	if in.Scope == ScopeHours {
		cutoff = in.GeneratedAt.Add(-time.Duration(NormalizeHours(in.Hours)) * time.Hour)
	}
	out := make([]Run, 0, len(in.Runs))
	for _, r := range in.Runs {
		if in.Scope == ScopeRun && r.TaskID != in.AnchorTaskID {
			continue
		}
		entries := make([]Entry, 0, len(r.Entries))
		for _, e := range r.Entries {
			if !cutoff.IsZero() && e.CreatedAt.Before(cutoff) {
				continue
			}
			entries = append(entries, mask.entry(e))
		}
		if len(entries) == 0 && r.TaskID != in.AnchorTaskID {
			continue
		}
		r.Error = mask.text(r.Error)
		r.WorkDir = mask.text(r.WorkDir)
		r.Entries = entries
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// dropOldest removes n entries, oldest first across runs.
func dropOldest(runs []Run, n int) {
	for i := range runs {
		if n <= 0 {
			return
		}
		k := len(runs[i].Entries)
		if k > n {
			k = n
		}
		runs[i].Entries = runs[i].Entries[k:]
		n -= k
	}
}

func assemble(in Input, runs []Run, dropped int) (Bundle, error) {
	meta := Meta{
		FormatVersion:   FormatVersion,
		GeneratedAt:     in.GeneratedAt.UTC(),
		WorkspaceID:     in.WorkspaceID,
		IssueID:         in.IssueID,
		IssueIdentifier: in.IssueIdentifier,
		IssueTitle:      in.IssueTitle,
		AnchorTaskID:    in.AnchorTaskID,
		Scope:           in.Scope,
		DroppedEntries:  dropped,
		Partial:         len(in.Warnings) > 0,
		Warnings:        append([]string{}, in.Warnings...),
		Redacted:        true,
		Runs:            make([]Run, 0, len(runs)),
	}
	if in.Scope == ScopeHours {
		meta.Hours = NormalizeHours(in.Hours)
	}

	var logs bytes.Buffer
	enc := json.NewEncoder(&logs)
	enc.SetEscapeHTML(false)
	for _, r := range runs {
		r.EntryCount = len(r.Entries)
		meta.EntryCount += r.EntryCount
		for _, e := range r.Entries {
			at := e.CreatedAt
			if meta.Window.From == nil || at.Before(*meta.Window.From) {
				meta.Window.From = &at
			}
			if meta.Window.To == nil || at.After(*meta.Window.To) {
				meta.Window.To = &at
			}
			if err := enc.Encode(e); err != nil {
				return Bundle{}, fmt.Errorf("encode log entry: %w", err)
			}
		}
		meta.Runs = append(meta.Runs, r)
	}
	meta.RunCount = len(meta.Runs)

	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return Bundle{}, fmt.Errorf("encode meta: %w", err)
	}
	summary := buildSummary(meta, runs)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range []struct {
		name string
		data []byte
	}{
		{"summary.md", []byte(summary)},
		{"meta.json", metaJSON},
		{"logs.jsonl", logs.Bytes()},
	} {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.name, Method: zip.Deflate, Modified: meta.GeneratedAt})
		if err != nil {
			return Bundle{}, fmt.Errorf("zip %s: %w", f.name, err)
		}
		if _, err := w.Write(f.data); err != nil {
			return Bundle{}, fmt.Errorf("zip %s: %w", f.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return Bundle{}, fmt.Errorf("close zip: %w", err)
	}

	return Bundle{
		Filename: filename(meta),
		Zip:      buf.Bytes(),
		Summary:  summary,
		Meta:     meta,
	}, nil
}

func filename(m Meta) string {
	label := strings.ToLower(m.IssueIdentifier)
	if label == "" {
		label = "task"
	}
	short := m.AnchorTaskID
	if len(short) > 8 {
		short = short[len(short)-8:]
	}
	return fmt.Sprintf("multica-logs-%s-%s-%s-%s.zip", label, short, m.Scope, m.GeneratedAt.Format("20060102T150405Z"))
}
