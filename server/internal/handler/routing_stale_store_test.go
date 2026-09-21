package handler

import (
	"context"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// The stale-review row's four store methods, against a real database.
//
// The routing package's own tests drive the decision logic through a fake
// store, so the one thing they cannot check is the half that is written in
// SQL: which tickets the sweep picks up, whose remarks it reads, and whether
// the completion write is really conditional on the status. Each of those is a
// guard, and a guard that only exists in a fake is not a guard.
func TestStaleReviewStoreQueries(t *testing.T) {
	ctx := context.Background()
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	store := testHandler.RoutingStore()
	long := time.Now().Add(-48 * time.Hour)

	stale := fx.Issue(t, "stalled in review", testutil.Cols{
		"status":           "in_review",
		"last_activity_at": long,
	})
	fresh := fx.Issue(t, "reviewed an hour ago", testutil.Cols{
		"status":           "in_review",
		"last_activity_at": time.Now().Add(-time.Hour),
	})
	working := fx.Issue(t, "still being worked on", testutil.Cols{
		"status":           "in_progress",
		"last_activity_at": long,
	})
	running := fx.Issue(t, "quiet but a run is active", testutil.Cols{
		"status":           "in_review",
		"last_activity_at": long,
	})
	runtimeID := fx.Runtime(t, "stale-sweep-runtime")
	agentID := fx.Agent(t, "stale-sweep-agent", runtimeID)
	fx.Task(t, agentID, testutil.Cols{
		"issue_id":   running,
		"status":     "running",
		"runtime_id": runtimeID,
	})

	t.Run("picks up only quiet in-review tickets with no run", func(t *testing.T) {
		ids, err := store.StaleReviews(ctx, testWorkspaceID, time.Now().Add(-24*time.Hour), 50)
		if err != nil {
			t.Fatalf("stale reviews: %v", err)
		}
		seen := map[string]bool{}
		for _, id := range ids {
			seen[id] = true
		}
		if !seen[stale] {
			t.Errorf("the stalled ticket was not picked up")
		}
		for label, id := range map[string]string{
			"a ticket touched an hour ago": fresh,
			"a ticket being worked on":     working,
			"a ticket with an active run":  running,
		} {
			if seen[id] {
				t.Errorf("%s was picked up by the sweep", label)
			}
		}
	})

	t.Run("reads only the reviewer's own remarks", func(t *testing.T) {
		reviewer := routing.ReviewerRef{Kind: routing.ReviewerMember, ID: testUserID}
		fx.Comment(t, stale, "从验收席的角度：通过")
		fx.Comment(t, stale, "执行席的交付说明", testutil.Cols{"author_type": "agent", "author_id": agentID})

		remarks, err := store.ReviewRemarks(ctx, testWorkspaceID, stale, reviewer)
		if err != nil {
			t.Fatalf("review remarks: %v", err)
		}
		if len(remarks) != 1 || remarks[0] != "从验收席的角度：通过" {
			t.Fatalf("remarks = %v, want only the reviewer's own", remarks)
		}

		// A slot holding "needs no acceptance pass" has no author at all, so
		// it can never produce the remark that unlocks a completion.
		none, err := store.ReviewRemarks(ctx, testWorkspaceID, stale,
			routing.ReviewerRef{Kind: routing.ReviewerNoReview})
		if err != nil {
			t.Fatalf("review remarks for none: %v", err)
		}
		if len(none) != 0 {
			t.Fatalf("a no-review slot produced remarks: %v", none)
		}
	})

	t.Run("the completion write is conditional on the status", func(t *testing.T) {
		written, err := store.CompleteFromReview(ctx, testWorkspaceID, stale)
		if err != nil {
			t.Fatalf("complete: %v", err)
		}
		if !written {
			t.Fatalf("the write lost against an in_review ticket")
		}
		var status string
		fx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, stale).Scan(&status)
		if status != "done" {
			t.Fatalf("status = %q, want done", status)
		}

		// Second pass: the ticket already left in_review, so the guard must
		// hold and the caller must be told it wrote nothing.
		written, err = store.CompleteFromReview(ctx, testWorkspaceID, stale)
		if err != nil {
			t.Fatalf("second complete: %v", err)
		}
		if written {
			t.Fatalf("a ticket outside in_review was moved anyway")
		}

		// And a ticket that was never in review is never eligible.
		written, err = store.CompleteFromReview(ctx, testWorkspaceID, working)
		if err != nil {
			t.Fatalf("complete in_progress: %v", err)
		}
		if written {
			t.Fatalf("an in_progress ticket was moved to done")
		}
	})

	t.Run("lists the workspace only while routing is on", func(t *testing.T) {
		ids, err := store.EnabledWorkspaces(ctx)
		if err != nil {
			t.Fatalf("enabled workspaces: %v", err)
		}
		for _, id := range ids {
			if id == testWorkspaceID {
				t.Fatalf("a workspace with routing off was listed for the sweep")
			}
		}

		fx.Exec(t, `UPDATE workspace SET settings = jsonb_set(COALESCE(settings, '{}'::jsonb),
			'{routing}', '{"enabled": true, "model": "m"}'::jsonb, true) WHERE id = $1`, testWorkspaceID)
		t.Cleanup(func() {
			fx.Exec(t, `UPDATE workspace SET settings = settings - 'routing' WHERE id = $1`, testWorkspaceID)
		})

		ids, err = store.EnabledWorkspaces(ctx)
		if err != nil {
			t.Fatalf("enabled workspaces after enabling: %v", err)
		}
		found := false
		for _, id := range ids {
			if id == testWorkspaceID {
				found = true
			}
		}
		if !found {
			t.Fatalf("an enabled workspace was not listed for the sweep")
		}
	})
}
