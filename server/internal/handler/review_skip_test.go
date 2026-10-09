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
		{"acceptance seat verdict", func(t *testing.T, issueID, _ string) {
			setMemberReviewer(t, issueID)
			seedComment(t, issueID, time.Now(), "审过了\nverdict: pass", nil)
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

// Merged but not reviewed in a way that counts: the ticket still goes to
// review. Covers the DENE-1678 review counterexamples — a pass by someone who
// is not the acceptance seat, and an approval of another PR on the ticket.
func TestCloseInReviewKeepsReviewWithoutQualifyingReview(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	for _, tc := range []struct {
		name string
		seed func(t *testing.T, issueID, prURL string)
	}{
		{"only merged", func(t *testing.T, _, _ string) {}},
		{"pass by a non-seat", func(t *testing.T, issueID, _ string) {
			seedComment(t, issueID, time.Now().Add(-time.Minute), "verdict: pass", nil)
		}},
		{"another PR approved", func(t *testing.T, issueID, _ string) {
			old := seedOpenPullForIssue(t, issueID, 167899)
			if _, err := testPool.Exec(context.Background(),
				`UPDATE github_pull_request SET state = 'merged', merged_at = now(), approved_by = 'octo', approved_at = now() WHERE html_url = $1`, old); err != nil {
				t.Fatalf("approve old PR: %v", err)
			}
		}},
		{"seat pass older than the PR", func(t *testing.T, issueID, prURL string) {
			setMemberReviewer(t, issueID)
			seedComment(t, issueID, time.Now().Add(-2*time.Hour), "verdict: pass", nil)
			if _, err := testPool.Exec(context.Background(),
				`UPDATE github_pull_request SET pr_created_at = now() - interval '1 hour' WHERE html_url = $1`, prURL); err != nil {
				t.Fatalf("date PR: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issue := createIssueHTTP(t, "review skip negative "+tc.name, "in_progress")
			agentID := handlerTestAgentID(t)
			taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
			prURL := seedOpenPullForIssue(t, issue.ID, 167802)
			if _, err := testPool.Exec(context.Background(),
				`UPDATE github_pull_request SET state = 'merged', merged_at = now() WHERE html_url = $1`, prURL); err != nil {
				t.Fatalf("merge: %v", err)
			}
			tc.seed(t, issue.ID, prURL)
			w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
				"outcome": "in_review", "evidence": "PR: " + prURL,
			})
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
			if got := issueStatusDirect(t, issue.ID); got != "in_review" {
				t.Fatalf("status = %s, want in_review", got)
			}
			if got := issueMetaString(t, issue.ID, "review_skip"); got != "" {
				t.Fatalf("review_skip = %q, want none", got)
			}
		})
	}
}

// setMemberReviewer makes the test user the issue's acceptance seat.
func setMemberReviewer(t *testing.T, issueID string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(),
		`UPDATE issue SET reviewer_type = 'member', reviewer_id = $2 WHERE id = $1`, issueID, testUserID); err != nil {
		t.Fatalf("set reviewer: %v", err)
	}
}
