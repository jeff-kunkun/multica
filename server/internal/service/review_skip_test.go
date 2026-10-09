package service

import (
	"testing"
	"time"
)

func TestDecideReviewSkip(t *testing.T) {
	at := time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC)
	merged := ReviewSkipPR{URL: "https://github.com/o/r/pull/1", State: "merged"}
	approved := merged
	approved.ApprovedBy, approved.ApprovedAt = "octo", at
	pass := ReviewSkipVerdict{AuthorID: "reviewer", Content: "看过了\nverdict: pass", At: at}
	hold := ReviewSkipVerdict{AuthorID: "reviewer", Content: "verdict: hold", At: at}
	selfPass := ReviewSkipVerdict{AuthorID: "exec", Content: "verdict: pass", At: at}
	executors := map[string]bool{"exec": true}

	for _, tc := range []struct {
		name     string
		prs      []ReviewSkipPR
		verdicts []ReviewSkipVerdict
		want     string
	}{
		{"no PR", nil, []ReviewSkipVerdict{pass}, ""},
		{"open PR", []ReviewSkipPR{{State: "open"}}, []ReviewSkipVerdict{pass}, ""},
		{"merged and an open PR", []ReviewSkipPR{merged, {State: "open"}}, []ReviewSkipVerdict{pass}, ""},
		{"closed unmerged", []ReviewSkipPR{{State: "closed"}}, []ReviewSkipVerdict{pass}, ""},
		{"merged, not reviewed", []ReviewSkipPR{merged}, nil, ""},
		{"merged, only the executor passed", []ReviewSkipPR{merged}, []ReviewSkipVerdict{selfPass}, ""},
		{"merged, reviewer passed", []ReviewSkipPR{merged}, []ReviewSkipVerdict{pass}, ReviewSkipPlatformVerdict},
		{"merged, executor pass newer than reviewer pass", []ReviewSkipPR{merged}, []ReviewSkipVerdict{selfPass, pass}, ReviewSkipPlatformVerdict},
		{"merged, reviewer hold is newest", []ReviewSkipPR{merged}, []ReviewSkipVerdict{hold, pass}, ""},
		{"merged and approved", []ReviewSkipPR{approved}, nil, ReviewSkipGitHubApprove},
		{"approved but reviewer hold", []ReviewSkipPR{approved}, []ReviewSkipVerdict{hold}, ""},
		{"merged plus closed duplicate", []ReviewSkipPR{{State: "closed"}, merged}, []ReviewSkipVerdict{pass}, ReviewSkipPlatformVerdict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			skip, ok := DecideReviewSkip(tc.prs, tc.verdicts, executors)
			if got := map[bool]string{true: skip.Kind}[ok]; got != tc.want {
				t.Fatalf("kind = %q (ok=%v), want %q", skip.Kind, ok, tc.want)
			}
			if ok && skip.PRURL != merged.URL {
				t.Fatalf("pr url = %q, want the merged PR", skip.PRURL)
			}
		})
	}
}

func TestReviewSkipReason(t *testing.T) {
	r := ReviewSkip{Kind: ReviewSkipGitHubApprove, By: "octo", PRURL: "u"}
	if got := r.Reason(); got != "PR u 已合入，octo 在 GitHub 上批准过" {
		t.Fatalf("reason = %q", got)
	}
	r = ReviewSkip{Kind: ReviewSkipPlatformVerdict, PRURL: "u"}
	if got := r.Reason(); got != "PR u 已合入，有人 在票上给过审查通过" {
		t.Fatalf("reason = %q", got)
	}
}
