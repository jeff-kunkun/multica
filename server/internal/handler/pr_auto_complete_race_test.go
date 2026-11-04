package handler

import (
	"context"
	"encoding/json"
	"testing"
)

// The decision→write race test (MUL-7429 review) went with the status write
// itself in DENE-2697: a merge no longer opens a transaction to race against.

// fireTimedPRWebhook delivers a pull_request event that closes identifier,
// with an explicit updated_at.
func fireTimedPRWebhook(t *testing.T, inst int64, identifier, state, timestamp string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"action":       "edited",
		"installation": map[string]any{"id": inst},
		"repository":   map[string]any{"name": "widget", "owner": map[string]any{"login": "acme"}},
		"pull_request": map[string]any{"number": 1, "html_url": "https://github.com/acme/widget/pull/1", "title": "Closes " + identifier, "state": state, "merged": state == "closed", "created_at": "2026-09-22T00:00:00Z", "updated_at": timestamp, "head": map[string]any{"ref": "fix/a"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	testHandler.handlePullRequestEvent(context.Background(), raw)
}

// A merged PR records its mirror state but leaves issue acceptance to its
// manager.
func TestPRMergeLeavesIssueStatusForManagerAcceptance(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	const inst int64 = 8758003
	issue := prAutoCompleteTestIssue(t, "PR merge keeps status", inst)
	fireTimedPRWebhook(t, inst, issue.Identifier, "open", "2026-09-22T01:00:00Z")
	fireTimedPRWebhook(t, inst, issue.Identifier, "closed", "2026-09-22T02:00:00Z")
	if got := issueStatusForTest(t, issue.ID); got != "in_progress" {
		t.Fatalf("merged PR changed issue status: got %s, want in_progress", got)
	}
}
