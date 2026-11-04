package issuewatchdog

import (
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
	"testing"
	"time"
)

// In review only the stall fallback rings, so each issue yields one row.
func TestOutboxClaimReceiptRetryAndFreshRead(t *testing.T) {
	b := newReviewBench(t)
	id := b.issue("Delivery proof", "in_review", b.dev, nil)
	w := b.watchdog(Config{})
	b.run(w, b.now)
	token := uuid.NewString()
	first, err := w.Claim(b.ctx, token)
	if err != nil || len(first) != 1 {
		t.Fatalf("claim: %+v %v", first, err)
	}
	again, err := w.Claim(b.ctx, token)
	if err != nil || len(again) != 1 || again[0].DeliveryCount != 1 {
		t.Fatalf("repeat claim: %+v %v", again, err)
	}
	other, err := w.Claim(b.ctx, uuid.NewString())
	if err != nil || len(other) != 0 {
		t.Fatalf("concurrent worker stole claim: %+v %v", other, err)
	}
	if err = w.Receipt(b.ctx, first[0].ID, token, "not_sent"); err != nil {
		t.Fatal(err)
	}
	if _, err = b.pool.Exec(b.ctx, `UPDATE office_issue_watchdog_attempt SET available_at=now()-interval '1 second' WHERE issue_id=$1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	token = uuid.NewString()
	retry, err := w.Claim(b.ctx, token)
	if err != nil || len(retry) != 1 || retry[0].DeliveryCount != 2 {
		t.Fatalf("retry: %+v %v", retry, err)
	}
	if err = w.Receipt(b.ctx, retry[0].ID, token, "uncertain"); err != nil {
		t.Fatal(err)
	}
	more, err := w.Claim(b.ctx, uuid.NewString())
	if err != nil || len(more) != 0 {
		t.Fatalf("uncertain replay: %+v %v", more, err)
	}
	id = b.issue("Resolved before send", "in_review", b.dev, nil)
	b.run(w, b.now)
	b.fx.Insert(t, "agent_task_queue", testutil.Cols{"issue_id": id, "agent_id": b.reviewer, "status": "running", "runtime_id": b.runtimeID, "started_at": b.now})
	more, err = w.Claim(b.ctx, uuid.NewString())
	if err != nil || len(more) != 0 {
		t.Fatalf("fresh live run ignored: %+v %v", more, err)
	}
	id = b.issue("Closed before send", "in_review", b.dev, nil)
	b.run(w, b.now)
	if _, err = b.pool.Exec(b.ctx, `UPDATE issue SET status='done',updated_at=$2 WHERE id=$1::uuid`, id, b.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	more, err = w.Claim(b.ctx, uuid.NewString())
	if err != nil || len(more) != 0 {
		t.Fatalf("closed issue delivered: %+v %v", more, err)
	}
}

func TestOfflineRuntimeGetsThirtyMinuteGraceWithoutRestartingRun(t *testing.T) {
	b := newReviewBench(t)
	id := b.issue("Offline run", "in_progress", b.dev, nil)
	b.fx.Insert(t, "agent_task_queue", testutil.Cols{"issue_id": id, "agent_id": b.dev, "status": "running", "runtime_id": b.runtimeID, "started_at": b.now.Add(-time.Hour)})
	seen := b.now.Add(-15 * time.Minute)
	if _, err := b.pool.Exec(b.ctx, `UPDATE agent_runtime SET status='offline',last_seen_at=$2,updated_at=$2 WHERE id=$1::uuid`, b.runtimeID, seen); err != nil {
		t.Fatal(err)
	}
	w := b.watchdog(Config{})
	b.run(w, b.now)
	if b.reminders(id, ownerStalledEvent) != 0 {
		t.Fatal("runtime reconnect grace was skipped")
	}
	b.run(w, b.now.Add(16*time.Minute))
	claimed, err := w.Claim(b.ctx, uuid.NewString())
	var events []Reminder
	for _, event := range claimed {
		if event.Kind == reminderKindStall {
			events = append(events, event)
		}
	}
	if err != nil || len(events) != 1 || events[0].Cause != "runtime_offline" {
		t.Fatalf("offline owner event=%+v %v", claimed, err)
	}
	var status string
	b.pool.QueryRow(b.ctx, `SELECT status FROM agent_task_queue WHERE issue_id=$1::uuid`, id).Scan(&status)
	if status != "running" {
		t.Fatalf("watchdog changed task status to %s", status)
	}
}
