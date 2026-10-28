package handler

import (
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// DENE-2667 PR 1: issue detail carries the watchdog's project-group reminders
// so `multica issue get` and the issue header can say the group was told.
// Failed attempts and other watchdog events do not count.
func TestIssueDetailCarriesWatchdogGroupReminder(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	issueID := dbfx.Issue(t, "watchdog group reminder visible")
	quietID := dbfx.Issue(t, "no watchdog reminder")
	first := time.Now().Add(-45 * time.Minute).UTC().Truncate(time.Second)
	last := first.Add(20 * time.Minute)
	attempt := func(key, eventType, outcome string, at time.Time) {
		t.Helper()
		dbfx.Exec(t, `
INSERT INTO office_issue_watchdog_attempt
    (workspace_id, issue_id, event_key, event_type, target, outcome, attempted_at)
VALUES ($1, $2, $3, $4, 'project_group', $5, $6)`, testWorkspaceID, issueID, key, eventType, outcome, at)
	}
	const taskID = "01a17d29-0000-7000-8000-000000000001"
	attempt("running-silent:"+taskID+":1", "employee_running_silent", "sent", first)
	attempt("running-silent:"+taskID+":2", "employee_running_silent", "sent", last)
	attempt("running-silent:"+taskID+":3", "employee_running_silent", "failed", last.Add(20*time.Minute))
	attempt("status:"+issueID+":1:done", "status_done", "sent", last.Add(time.Minute))

	var issue IssueResponse
	testutil.Call(t, testHandler.GetIssue,
		withURLParam(newRequest(http.MethodGet, "/api/issues/"+issueID, nil), "id", issueID),
	).Want(http.StatusOK).JSON(&issue)
	got := issue.WatchdogReminder
	if got == nil || got.GroupReminderCount != 2 || got.TaskID != taskID {
		t.Fatalf("watchdog_reminder = %+v, want 2 delivered reminders for %s", got, taskID)
	}
	if at, err := time.Parse(time.RFC3339, got.GroupRemindedAt); err != nil || !at.Equal(last) {
		t.Fatalf("group_reminded_at = %q, want %s", got.GroupRemindedAt, last)
	}

	var quiet IssueResponse
	testutil.Call(t, testHandler.GetIssue,
		withURLParam(newRequest(http.MethodGet, "/api/issues/"+quietID, nil), "id", quietID),
	).Want(http.StatusOK).JSON(&quiet)
	if quiet.WatchdogReminder != nil {
		t.Fatalf("issue without reminders carries watchdog_reminder %+v", quiet.WatchdogReminder)
	}
}
