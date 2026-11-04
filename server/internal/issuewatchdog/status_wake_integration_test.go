package issuewatchdog

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// move changes the issue's status the way the product does (the trigger logs
// the status-change event) and dates both the issue and the event at.
func (b *reviewBench) move(id, status string, at time.Time) int64 {
	b.t.Helper()
	if _, err := b.pool.Exec(b.ctx, `UPDATE issue SET status=$2, updated_at=$3 WHERE id=$1::uuid`, id, status, at); err != nil {
		b.t.Fatal(err)
	}
	var eventID int64
	if err := b.pool.QueryRow(b.ctx, `
UPDATE office_event SET occurred_at=$2
WHERE id = (SELECT max(id) FROM office_event WHERE issue_id=$1::uuid AND event_type='issue.status_changed')
RETURNING id`, id, at).Scan(&eventID); err != nil {
		b.t.Fatal(err)
	}
	return eventID
}

func (b *reviewBench) reminders(id, eventType string) int {
	b.t.Helper()
	var n int
	if err := b.pool.QueryRow(b.ctx, `SELECT count(*) FROM office_issue_watchdog_attempt
WHERE issue_id=$1::uuid AND ($2 = '' OR event_type=$2)`, id, eventType).Scan(&n); err != nil {
		b.t.Fatal(err)
	}
	return n
}

func (b *reviewBench) claim(w *Watchdog, at time.Time) (string, []Reminder) {
	b.t.Helper()
	w.now = func() time.Time { return at }
	token := uuid.NewString()
	events, err := w.Claim(b.ctx, token)
	if err != nil {
		b.t.Fatal(err)
	}
	return token, events
}

func TestStatusWakeOncePerEntryWithLatestFacts(t *testing.T) {
	b := newReviewBench(t)
	w := b.watchdog(Config{AppURL: "https://office.example"})
	id := b.issue("Deliver proof", "in_progress", b.dev, nil)
	entered := b.now.Add(-time.Minute)
	entry := b.move(id, "in_review", entered)
	sha := "0123456789abcdef0123456789abcdef01234567"
	delivered := b.fx.Comment(t, id, "交付完成：PR https://github.com/x/y/pull/1 。\n\n token=abc123secret 提交 "+sha,
		testutil.Cols{"author_type": "agent", "author_id": b.dev, "created_at": entered.Add(5 * time.Second)})
	b.fx.Insert(t, "attachment", testutil.Cols{"workspace_id": b.workspaceID, "issue_id": id, "comment_id": delivered,
		"uploader_type": "agent", "uploader_id": b.dev, "filename": "result.html", "url": "https://files.example/r.html",
		"content_type": "text/html", "size_bytes": 10})
	// A later remark by someone else does not replace the assignee's delivery.
	b.fx.Comment(t, id, "【负责经理·扳手】我看一下", testutil.Cols{"created_at": entered.Add(10 * time.Second)})

	if err := w.WakeOnStatus(b.ctx, id); err != nil {
		t.Fatal(err)
	}
	for _, later := range []time.Duration{0, time.Minute, 2 * time.Minute} {
		b.run(w, b.now.Add(later))
	}
	if n := b.reminders(id, ""); n != 1 {
		t.Fatalf("event path plus three scans queued %d reminders, want 1", n)
	}

	token, events := b.claim(w, b.now)
	if len(events) != 1 {
		t.Fatalf("claimed %d, want 1", len(events))
	}
	got := events[0]
	var slug string
	if err := b.pool.QueryRow(b.ctx, `SELECT slug FROM workspace WHERE id=$1::uuid`, b.workspaceID).Scan(&slug); err != nil {
		t.Fatal(err)
	}
	if got.Kind != reminderKindStatus || got.Cause != "delivered" || got.Status != "in_review" ||
		got.StatusEntry != entry || got.Manager != "CTO·扳手" || !got.CheckedAt.Equal(b.now) ||
		got.IssueURL != "https://office.example/"+slug+"/issues/"+id {
		t.Fatalf("reminder = %+v", got)
	}
	c := got.Comment
	if c == nil || c.ID != delivered || strings.Contains(c.Summary, "abc123secret") ||
		!strings.Contains(c.Summary, "token=[已打码]") || !strings.Contains(c.Summary, sha) ||
		strings.Contains(c.Summary, "\n") || len(c.Links) != 1 || c.Links[0] != "https://github.com/x/y/pull/1" ||
		len(c.Attachments) != 1 || c.Attachments[0].Filename != "result.html" {
		t.Fatalf("delivery comment = %+v", c)
	}
	if err := w.Receipt(b.ctx, got.ID, token, "sent"); err != nil {
		t.Fatal(err)
	}
	// Same status entry: later scans, including past the 30-minute fallback,
	// never wake the owner again.
	for _, later := range []time.Duration{3 * time.Minute, 31 * time.Minute, 45 * time.Minute} {
		b.run(w, b.now.Add(later))
	}
	if n := b.reminders(id, ""); n != 1 {
		t.Fatalf("same status woke the owner %d times", n)
	}
	if _, more := b.claim(w, b.now.Add(46*time.Minute)); len(more) != 0 {
		t.Fatalf("resent: %+v", more)
	}
}

func TestStatusWakeDropsStaleAndWakesAgainOnReentry(t *testing.T) {
	b := newReviewBench(t)
	w := b.watchdog(Config{})
	id := b.issue("Blocked then resumed", "in_progress", b.dev, nil)
	b.move(id, "blocked", b.now.Add(-3*time.Minute))
	if err := w.WakeOnStatus(b.ctx, id); err != nil {
		t.Fatal(err)
	}
	b.move(id, "in_progress", b.now.Add(-2*time.Minute))
	if _, events := b.claim(w, b.now); len(events) != 0 {
		t.Fatalf("stale reminder delivered: %+v", events)
	}
	var result string
	if err := b.pool.QueryRow(b.ctx, `SELECT delivery_result FROM office_issue_watchdog_attempt WHERE issue_id=$1::uuid`, id).Scan(&result); err != nil || result != "resolved_before_delivery" {
		t.Fatalf("stale reminder result=%q err=%v", result, err)
	}

	second := b.move(id, "blocked", b.now.Add(-time.Minute))
	if err := w.WakeOnStatus(b.ctx, id); err != nil {
		t.Fatal(err)
	}
	token, events := b.claim(w, b.now)
	if len(events) != 1 || events[0].Status != "blocked" || events[0].Cause != "blocked" || events[0].StatusEntry != second {
		t.Fatalf("re-entry reminder = %+v", events)
	}
	// The worker's own last read found it moved on: recorded, never replayed.
	if err := w.Receipt(b.ctx, events[0].ID, token, "stale"); err != nil {
		t.Fatal(err)
	}
	if _, more := b.claim(w, b.now); len(more) != 0 {
		t.Fatalf("stale receipt replayed: %+v", more)
	}

	done := b.issue("Closed right after delivery", "in_progress", b.dev, nil)
	b.move(done, "in_review", b.now.Add(-time.Minute))
	if err := w.WakeOnStatus(b.ctx, done); err != nil {
		t.Fatal(err)
	}
	b.move(done, "done", b.now.Add(-30*time.Second))
	if _, events := b.claim(w, b.now); len(events) != 0 {
		t.Fatalf("closed issue delivered: %+v", events)
	}
}

func TestStatusWakeQuietForWaitingKunOldAndCreatedEntries(t *testing.T) {
	b := newReviewBench(t)
	w := b.watchdog(Config{})
	raw, err := json.Marshal(map[string]string{"block_kind": "decision"})
	if err != nil {
		t.Fatal(err)
	}
	waiting := b.issue("Needs Kun", "in_progress", b.dev, testutil.Cols{"metadata": raw})
	b.move(waiting, "blocked", b.now.Add(-time.Minute))
	old := b.issue("Delivered long ago", "in_progress", b.dev, nil)
	b.move(old, "in_review", b.now.Add(-11*time.Minute))
	created := b.issue("Created in review", "in_review", b.dev, testutil.Cols{"updated_at": b.now.Add(-time.Minute)})
	working := b.issue("Back to work", "in_review", b.dev, nil)
	b.move(working, "in_progress", b.now.Add(-time.Minute))
	for _, id := range []string{waiting, old, created, working} {
		if err := w.WakeOnStatus(b.ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	b.run(w, b.now)
	for name, id := range map[string]string{"waiting Kun": waiting, "old entry": old, "created in status": created, "in_progress": working} {
		if n := b.reminders(id, ownerStatusEvent); n != 0 {
			t.Fatalf("%s queued %d status reminders", name, n)
		}
	}
}

func TestStallFallbackOnlyAfterStatusWakeProvablyUndelivered(t *testing.T) {
	b := newReviewBench(t)
	w := b.watchdog(Config{})
	id := b.issue("Undelivered wake", "in_progress", b.dev, nil)
	entry := b.move(id, "in_review", b.now.Add(-time.Minute))
	if err := w.WakeOnStatus(b.ctx, id); err != nil {
		t.Fatal(err)
	}
	token, events := b.claim(w, b.now)
	if len(events) != 1 {
		t.Fatalf("claimed %d", len(events))
	}
	if err := w.Receipt(b.ctx, events[0].ID, token, "not_sent"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.pool.Exec(b.ctx, `UPDATE office_issue_watchdog_attempt SET available_at=now()-interval '1 second' WHERE issue_id=$1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	token, events = b.claim(w, b.now)
	if len(events) != 1 || events[0].DeliveryCount != 2 {
		t.Fatalf("retry = %+v", events)
	}
	if err := w.Receipt(b.ctx, events[0].ID, token, "not_sent"); err != nil {
		t.Fatal(err)
	}

	b.run(w, b.now.Add(31*time.Minute))
	var stalls int
	if err := b.pool.QueryRow(b.ctx, `SELECT count(*) FROM office_issue_watchdog_attempt
WHERE issue_id=$1::uuid AND event_type='owner_stalled' AND payload->>'status_entry'=$2`, id, fmt.Sprint(entry)).Scan(&stalls); err != nil {
		t.Fatal(err)
	}
	if stalls != 1 {
		t.Fatalf("fallback stall reminders = %d, want 1", stalls)
	}
	// New progress in the same status entry does not wake the owner again.
	b.fx.Comment(t, id, "补充说明", testutil.Cols{"created_at": b.now.Add(35 * time.Minute)})
	b.run(w, b.now.Add(70*time.Minute))
	if n := b.reminders(id, ownerStalledEvent); n != 1 {
		t.Fatalf("same entry stalled twice: %d", n)
	}
}

func TestStatusWakeUncertainDeliveryBlocksFallback(t *testing.T) {
	b := newReviewBench(t)
	w := b.watchdog(Config{})
	id := b.issue("Maybe delivered", "in_progress", b.dev, nil)
	b.move(id, "blocked", b.now.Add(-time.Minute))
	if err := w.WakeOnStatus(b.ctx, id); err != nil {
		t.Fatal(err)
	}
	token, events := b.claim(w, b.now)
	if len(events) != 1 {
		t.Fatalf("claimed %d", len(events))
	}
	if err := w.Receipt(b.ctx, events[0].ID, token, "uncertain"); err != nil {
		t.Fatal(err)
	}
	b.run(w, b.now.Add(31*time.Minute))
	if n := b.reminders(id, ""); n != 1 {
		t.Fatalf("uncertain delivery followed by %d reminders", n-1)
	}
}
