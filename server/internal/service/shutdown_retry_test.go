package service

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// A run the daemon reports as interrupted by its own shutdown is retried within
// the task's existing attempt budget and keeps its resumable session. Both the
// "cancelled" platform interrupt (DENE-813) and runtime_recovery qualify.
func TestShutdownRetryUsesExistingBudgetAndSessionPolicy(t *testing.T) {
	agent := db.Agent{AutoRetryEnabled: true}
	for _, tc := range []struct {
		reason       string
		attempt, max int32
		want         bool
	}{
		{"runtime_recovery", 1, 2, true},
		{"runtime_recovery", 2, 2, false},
		{"runtime_recovery", 1, 1, false},
		{"cancelled", 1, 2, true},
		{"cancelled", 2, 2, false},
		{"cancelled", 1, 1, false},
	} {
		task := db.AgentTaskQueue{Attempt: tc.attempt, MaxAttempts: tc.max, IssueID: pgtype.UUID{Valid: true}}
		if got := retryEligible(tc.reason, task, agent); got != tc.want {
			t.Errorf("retryEligible(%s, %d/%d) = %v, want %v", tc.reason, tc.attempt, tc.max, got, tc.want)
		}
		if ceiling := retryAttemptCeiling(tc.reason, tc.max); ceiling != tc.max {
			t.Errorf("%s widened budget to %d", tc.reason, ceiling)
		}
	}
	for _, reason := range []string{"runtime_recovery", "cancelled"} {
		if resumeUnsafeFailureReason(reason) {
			t.Fatalf("%s must preserve the resumable session", reason)
		}
	}
}
