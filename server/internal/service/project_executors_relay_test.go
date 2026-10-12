package service

import (
	"context"
	"testing"

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
