package main

import (
	"strings"
	"testing"
	"time"
)

func TestWatchdogReminderLine(t *testing.T) {
	at := time.Date(2026, 10, 27, 12, 5, 0, 0, time.UTC)
	issue := map[string]any{"watchdog_reminder": map[string]any{
		"group_reminded_at":    at.Format(time.RFC3339),
		"group_reminder_count": float64(3),
		"task_id":              "t-1",
	}}
	got := watchdogReminderLine(issue)
	want := "Group reminded: " + at.Local().Format("2006-01-02 15:04") + " (3 total)"
	if got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}

	// Older servers omit the field; nothing is printed.
	for _, issue := range []map[string]any{{}, {"watchdog_reminder": nil}, {"watchdog_reminder": map[string]any{}}} {
		if line := watchdogReminderLine(issue); strings.TrimSpace(line) != "" {
			t.Fatalf("line for %v = %q, want empty", issue, line)
		}
	}
}
