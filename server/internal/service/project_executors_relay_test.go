package service

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// putIssueInAllowlistedProject moves the quota world's ticket into a project
// whose executor allowlist (DENE-1959) names only these seats.
func putIssueInAllowlistedProject(t *testing.T, w quotaWorld, seats ...string) {
	t.Helper()
	ctx := context.Background()
	var projectID string
	if err := w.pool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title, executor_agent_ids) VALUES ($1, 'allowlisted', $2::uuid[]) RETURNING id`,
		w.workspaceID, seats).Scan(&projectID); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := w.pool.Exec(ctx, `UPDATE issue SET project_id = $2 WHERE id = $1`, w.issueID, projectID); err != nil {
		t.Fatalf("move issue: %v", err)
	}
}

// 额度接力 inside an allowlisted project skips the free same-tier seat that is
// not listed and steps down to the listed one.
func TestQuotaRelayStaysInsideProjectExecutors(t *testing.T) {
	w := seedQuotaWorld(t, string(taskfailure.ReasonAgentProviderQuotaLimit), "Weekly usage limit reached", true)
	putIssueInAllowlistedProject(t, w, w.failedID, w.mediumID)
	if _, err := w.service().RelayQuotaFailure(context.Background(), w.task(t)); err != nil {
		t.Fatalf("relay: %v", err)
	}
	assertAssignee(t, w, w.mediumID)
}

// With no other listed seat the relay hands the ticket to nobody, even
// though unlisted seats are free.
func TestQuotaRelayDeclinesWhenNoListedSeatIsLeft(t *testing.T) {
	w := seedQuotaWorld(t, string(taskfailure.ReasonAgentProviderQuotaLimit), "Weekly usage limit reached", true)
	putIssueInAllowlistedProject(t, w, w.failedID)
	if _, err := w.service().RelayQuotaFailure(context.Background(), w.task(t)); err != nil {
		t.Fatalf("relay: %v", err)
	}
	assertAssignee(t, w, w.failedID)
}

// The safety relay is a relay like any other: an unlisted seat of another
// house is not a way out of the project's allowlist.
func TestSafetyRelayStaysInsideProjectExecutors(t *testing.T) {
	w := seedSafetyWorld(t, safetyRefusalText)
	putIssueInAllowlistedProject(t, w, w.failedID, w.mediumID)
	ctx := context.Background()
	// The same-tier GPT seat would be the pick (see the unlisted test above);
	// the listed DeepSeek seat one tier down is the only way out.
	if !w.service().relaySafetyRefusal(ctx, w.task(t), safetyRefusalText) {
		t.Fatal("the listed seat of another house was not used")
	}
	assertAssignee(t, w, w.mediumID)
}

// A designated reviewer that comes back on does not take the review back
// from its cover once the project's allowlist no longer names it; while the
// list still names it, the review goes back as before.
func TestReclaimDesignatedReviewRespectsProjectExecutors(t *testing.T) {
	for name, delisted := range map[string]bool{"still listed": false, "delisted": true} {
		t.Run(name, func(t *testing.T) {
			w := seedQuotaWorld(t, string(taskfailure.ReasonAgentProviderQuotaLimit), "Weekly usage limit reached", true)
			ctx := context.Background()
			if delisted {
				putIssueInAllowlistedProject(t, w, w.sameID)
			} else {
				putIssueInAllowlistedProject(t, w, w.failedID, w.sameID)
			}
			if _, err := w.pool.Exec(ctx, `
				UPDATE issue SET status = 'in_review', reviewer_type = 'agent', reviewer_id = $2::uuid,
					metadata = jsonb_build_object('reviewer_relay', jsonb_build_object(
						'original_id', $3::text, 'replacement_id', $4::text, 'designated', true,
						'covered_at', to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')))
				WHERE id = $1`, w.issueID, w.sameID, w.failedID, w.sameID); err != nil {
				t.Fatalf("seed relay: %v", err)
			}
			failedUUID := util.MustParseUUID(w.failedID)
			if err := w.service().ReclaimDesignatedReviews(ctx, failedUUID); err != nil {
				t.Fatalf("reclaim: %v", err)
			}
			var reviewer string
			if err := w.pool.QueryRow(ctx, `SELECT reviewer_id::text FROM issue WHERE id = $1`, w.issueID).Scan(&reviewer); err != nil {
				t.Fatal(err)
			}
			want := w.failedID
			if delisted {
				want = w.sameID
			}
			if reviewer != want {
				t.Fatalf("reviewer = %s, want %s", reviewer, want)
			}
		})
	}
}
