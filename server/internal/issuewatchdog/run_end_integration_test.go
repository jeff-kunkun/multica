package issuewatchdog

import (
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"testing"
	"time"
)

func TestEndedRunGetsGraceAndNeverAutomaticRelay(t *testing.T) {
	b := newReviewBench(t)
	id := b.issue("Quota failed", "in_progress", b.dev, nil)
	task := b.fx.Insert(t, "agent_task_queue", testutil.Cols{"issue_id": id, "agent_id": b.dev, "status": "failed",
		"created_at": b.now.Add(-time.Hour), "completed_at": b.now.Add(-5 * time.Minute), "failure_reason": "agent_error.provider_quota_limit"})
	w := b.watchdog(Config{StopRouting: true})
	w.HandleEvent(events.Event{Type: protocol.EventTaskFailed, WorkspaceID: b.workspaceID, Payload: map[string]any{"issue_id": id, "task_id": task}})
	b.run(w, b.now)
	var n int
	if err := b.pool.QueryRow(b.ctx, `SELECT count(*) FROM office_issue_watchdog_attempt WHERE issue_id=$1::uuid`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("run end skipped 30-minute grace")
	}
	b.run(w, b.now.Add(26*time.Minute))
	var cause string
	if err := b.pool.QueryRow(b.ctx, `SELECT payload->>'cause' FROM office_issue_watchdog_attempt WHERE issue_id=$1::uuid`, id).Scan(&cause); err != nil {
		t.Fatal(err)
	}
	if cause != "provider_quota_limit" {
		t.Fatal(cause)
	}
	if err := b.pool.QueryRow(b.ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id=$1::uuid`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("watchdog created a relay run")
	}
}
