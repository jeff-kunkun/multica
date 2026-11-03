package issuewatchdog

import (
	"encoding/json"
	"github.com/multica-ai/multica/server/internal/testutil"
	"testing"
	"time"
)

func TestThirtyMinutesAndQuietWaitingOrClosedStates(t *testing.T) {
	b := newReviewBench(t)
	w := b.watchdog(Config{})
	for _, tc := range []struct {
		title, status string
		ago           time.Duration
		want          int
	}{
		{"working", "in_progress", 29 * time.Minute, 0}, {"stalled", "in_progress", 31 * time.Minute, 1},
		{"review", "in_review", 31 * time.Minute, 1}, {"waiting external", "blocked", 31 * time.Minute, 1},
		{"待 Kun｜确认", "blocked", time.Hour, 0}, {"paused", "todo", time.Hour, 0},
		{"later", "backlog", time.Hour, 0}, {"closed", "done", time.Hour, 0}, {"cancelled", "cancelled", time.Hour, 0},
	} {
		t.Run(tc.title, func(t *testing.T) {
			id := b.issue(tc.title, tc.status, b.dev, testutil.Cols{"updated_at": b.now.Add(-tc.ago), "created_at": b.now.Add(-tc.ago)})
			if _, err := b.pool.Exec(b.ctx, `UPDATE office_event SET occurred_at=$2 WHERE issue_id=$1::uuid`, id, b.now.Add(-tc.ago)); err != nil {
				t.Fatal(err)
			}
			b.run(w, b.now)
			var n int
			if err := b.pool.QueryRow(b.ctx, `SELECT count(*) FROM office_issue_watchdog_attempt WHERE issue_id=$1::uuid`, id).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != tc.want {
				t.Fatalf("got %d reminders, want %d", n, tc.want)
			}
		})
	}
}

func TestWaitingKunMetadataContractsStayQuiet(t *testing.T) {
	b := newReviewBench(t)
	w := b.watchdog(Config{})
	for _, metadata := range []map[string]string{
		{"close.block_kind": "permission"},
		{"close.block_kind": "decision"},
		{"block_kind": "permission"},
		{"waiting_for_kun": "true"},
	} {
		raw, err := json.Marshal(metadata)
		if err != nil {
			t.Fatal(err)
		}
		id := b.issue("Needs human action", "blocked", b.dev, testutil.Cols{"metadata": raw})
		b.run(w, b.now)
		var n int
		if err := b.pool.QueryRow(b.ctx, `SELECT count(*) FROM office_issue_watchdog_attempt WHERE issue_id=$1::uuid`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("metadata %v produced %d reminders", metadata, n)
		}
	}
}
