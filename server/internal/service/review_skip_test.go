package service

import (
	"testing"
	"time"
)

func TestDecideReviewSkip(t *testing.T) {
	t0 := time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC)
	prA := ReviewSkipPR{URL: "https://github.com/o/r/pull/1", State: "merged", OpenedAt: t0}
	prB := ReviewSkipPR{URL: "https://github.com/o/r/pull/2", State: "merged", OpenedAt: t0.Add(2 * time.Hour)}
	approvedA := prA
	approvedA.ApprovedBy, approvedA.ApprovedAt = "octo", t0.Add(time.Minute)
	approvedB := prB
	approvedB.ApprovedBy, approvedB.ApprovedAt = "octo", t0.Add(3*time.Hour)
	at := func(h time.Duration) time.Time { return t0.Add(h) }
	seatPass := ReviewSkipVerdict{AuthorID: "seat", Seat: true, Content: "看过了\nverdict: pass", At: at(time.Hour)}
	seatPassLate := ReviewSkipVerdict{AuthorID: "seat", Seat: true, Content: "verdict: pass", At: at(4 * time.Hour)}
	seatHold := ReviewSkipVerdict{AuthorID: "seat", Seat: true, Content: "verdict: hold", At: at(time.Hour)}
	otherPass := ReviewSkipVerdict{AuthorID: "other", Content: "verdict: pass", At: at(4 * time.Hour)}
	selfPass := ReviewSkipVerdict{AuthorID: "exec", Seat: true, Content: "verdict: pass", At: at(4 * time.Hour)}
	executors := map[string]bool{"exec": true}

	for _, tc := range []struct {
		name     string
		prs      []ReviewSkipPR
		verdicts []ReviewSkipVerdict
		want     string
		wantURL  string
	}{
		{"no PR", nil, []ReviewSkipVerdict{seatPass}, "", ""},
		{"open PR", []ReviewSkipPR{{State: "open"}}, []ReviewSkipVerdict{seatPass}, "", ""},
		{"merged and an open PR", []ReviewSkipPR{prA, {State: "open"}}, []ReviewSkipVerdict{seatPass}, "", ""},
		{"closed unmerged", []ReviewSkipPR{{State: "closed"}}, []ReviewSkipVerdict{seatPass}, "", ""},
		{"merged, not reviewed", []ReviewSkipPR{prA}, nil, "", ""},
		{"merged, seat passed", []ReviewSkipPR{prA}, []ReviewSkipVerdict{seatPass}, ReviewSkipPlatformVerdict, prA.URL},
		{"merged, a non-seat passed", []ReviewSkipPR{prA}, []ReviewSkipVerdict{otherPass}, "", ""},
		{"merged, the executor passed as seat", []ReviewSkipPR{prA}, []ReviewSkipVerdict{selfPass}, "", ""},
		{"non-seat pass newer than seat hold", []ReviewSkipPR{prA}, []ReviewSkipVerdict{otherPass, seatHold}, "", ""},
		{"seat hold is newest", []ReviewSkipPR{prA}, []ReviewSkipVerdict{seatHold, seatPass}, "", ""},
		{"merged and approved", []ReviewSkipPR{approvedA}, nil, ReviewSkipGitHubApprove, prA.URL},
		{"approved but seat hold", []ReviewSkipPR{approvedA}, []ReviewSkipVerdict{seatHold}, "", ""},
		{"merged plus closed duplicate", []ReviewSkipPR{{State: "closed"}, prA}, []ReviewSkipVerdict{seatPass}, ReviewSkipPlatformVerdict, prA.URL},
		// DENE-1678 review F3: A reviewed and merged, B only merged.
		{"A approved, B only merged", []ReviewSkipPR{approvedA, prB}, nil, "", ""},
		{"old pass, new PR after it", []ReviewSkipPR{prA, prB}, []ReviewSkipVerdict{seatPass}, "", ""},
		{"pass after the new PR covers both", []ReviewSkipPR{prA, prB}, []ReviewSkipVerdict{seatPassLate}, ReviewSkipPlatformVerdict, prB.URL},
		{"each PR approved", []ReviewSkipPR{approvedA, approvedB}, nil, ReviewSkipGitHubApprove, prB.URL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			skip, ok := DecideReviewSkip(tc.prs, tc.verdicts, executors)
			if got := map[bool]string{true: skip.Kind}[ok]; got != tc.want {
				t.Fatalf("kind = %q (ok=%v), want %q", skip.Kind, ok, tc.want)
			}
			if ok && skip.PRURL != tc.wantURL {
				t.Fatalf("pr url = %q, want %q", skip.PRURL, tc.wantURL)
			}
		})
	}
}

func TestReviewSkipReason(t *testing.T) {
	r := ReviewSkip{Kind: ReviewSkipGitHubApprove, By: "octo", PRURL: "u"}
	if got := r.Reason(); got != "PR u 已合入，octo 在 GitHub 上批准过" {
		t.Fatalf("reason = %q", got)
	}
	r = ReviewSkip{Kind: ReviewSkipPlatformVerdict, By: "布尔玛", PRURL: "u"}
	if got := r.Reason(); got != "PR u 已合入，验收席 布尔玛 在票上给过审查通过" {
		t.Fatalf("reason = %q", got)
	}
}
