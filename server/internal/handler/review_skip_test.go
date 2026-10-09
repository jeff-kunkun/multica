package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// DENE-1678: an executor sending merged, already-reviewed work to review does
// not wait for an acceptance seat. The ticket lands in done and says why.
func TestCloseInReviewSkipsReviewWhenMergedAndReviewed(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	for _, tc := range []struct {
		name   string
		seed   func(t *testing.T, issueID, prURL string)
		reason string
	}{
		{"github approve", func(t *testing.T, _, prURL string) {
			if _, err := testPool.Exec(context.Background(),
				`UPDATE github_pull_request SET approved_by = 'octo', approved_at = now() WHERE html_url = $1`, prURL); err != nil {
				t.Fatalf("approve: %v", err)
			}
		}, "octo 在 GitHub 上批准过"},
		{"platform verdict", func(t *testing.T, issueID, _ string) {
			seedComment(t, issueID, time.Now().Add(-time.Minute), "审过了\nverdict: pass", nil)
		}, "在票上给过审查通过"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issue := createIssueHTTP(t, "review skip "+tc.name, "in_progress")
			agentID := handlerTestAgentID(t)
			taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
			prURL := seedOpenPullForIssue(t, issue.ID, 167801)
			if _, err := testPool.Exec(context.Background(),
				`UPDATE github_pull_request SET state = 'merged', merged_at = now() WHERE html_url = $1`, prURL); err != nil {
				t.Fatalf("merge: %v", err)
			}
			tc.seed(t, issue.ID, prURL)

			w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
				"outcome":  "in_review",
				"evidence": "PR: " + prURL + "\n已合入。",
			})
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
			var resp CloseIssueResponse
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Status != "done" {
				t.Fatalf("response status = %q, want done", resp.Status)
			}
			if got := issueStatusDirect(t, issue.ID); got != "done" {
				t.Fatalf("status = %s, want done", got)
			}
			if !strings.Contains(strings.Join(resp.Warnings, "\n"), "跳过验收席") {
				t.Fatalf("warnings should say acceptance was skipped, got %v", resp.Warnings)
			}
			if got := issueMetaString(t, issue.ID, "review_skip"); !strings.Contains(got, tc.reason) {
				t.Fatalf("review_skip = %q, want reason containing %q", got, tc.reason)
			}
		})
	}
}

// Merged but nobody reviewed it: the ticket still goes to review.
func TestCloseInReviewKeepsReviewWhenOnlyMerged(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "review skip merged only", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	prURL := seedOpenPullForIssue(t, issue.ID, 167802)
	if _, err := testPool.Exec(context.Background(),
		`UPDATE github_pull_request SET state = 'merged', merged_at = now() WHERE html_url = $1`, prURL); err != nil {
		t.Fatalf("merge: %v", err)
	}
	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
		"outcome": "in_review", "evidence": "PR: " + prURL,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if got := issueStatusDirect(t, issue.ID); got != "in_review" {
		t.Fatalf("status = %s, want in_review", got)
	}
}
