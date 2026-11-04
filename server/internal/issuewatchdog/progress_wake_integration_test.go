package issuewatchdog

import (
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// moveBy is move with the status change attributed to actorType.
func (b *reviewBench) moveBy(id, status, actorType string, at time.Time) int64 {
	b.t.Helper()
	eventID := b.move(id, status, at)
	if _, err := b.pool.Exec(b.ctx, `UPDATE office_event SET actor_type=$2 WHERE id=$1`, eventID, actorType); err != nil {
		b.t.Fatal(err)
	}
	return eventID
}

func TestStatusWakeOnEmployeeDoneButNotOnManagerMoves(t *testing.T) {
	b := newReviewBench(t)
	w := b.watchdog(Config{})
	done := b.issue("Finished straight to done", "in_progress", b.dev, nil)
	b.moveBy(done, "done", "agent", b.now.Add(-time.Minute))
	held := b.issue("Manager parks it", "in_progress", b.dev, nil)
	b.moveBy(held, "blocked", "member", b.now.Add(-time.Minute))
	closed := b.issue("Manager accepts", "in_review", b.dev, nil)
	b.moveBy(closed, "done", "member", b.now.Add(-time.Minute))

	b.run(w, b.now)
	if n := b.reminders(done, ownerStatusEvent); n != 1 {
		t.Fatalf("employee done queued %d status reminders, want 1", n)
	}
	if n := b.reminders(held, "") + b.reminders(closed, ""); n != 0 {
		t.Fatalf("manager moves queued %d reminders, want 0", n)
	}
	_, events := b.claim(w, b.now)
	if len(events) != 1 || events[0].Cause != "done" || events[0].Status != "done" {
		t.Fatalf("claimed %+v, want one done reminder", events)
	}
}

func TestProgressWakeOnCommentThenOncePerInterval(t *testing.T) {
	b := newReviewBench(t)
	w := b.watchdog(Config{})
	// Claims compare available_at with the database clock: keep the story in the past.
	b.now = b.now.Add(-time.Hour)
	id := b.issue("Long job", "in_progress", b.dev, nil)
	task := b.fx.Insert(t, "agent_task_queue", testutil.Cols{"issue_id": id, "agent_id": b.dev, "status": "running",
		"runtime_id": b.runtimeID, "started_at": b.now.Add(-5 * time.Minute)})
	for i, text := range []string{"读代码", "改好了 progress_wake.go", "跑测试 token={{SECRET_9ixuy0a3}}", ""} {
		b.fx.Insert(t, "task_message", testutil.Cols{"task_id": task, "seq": i + 1, "type": "text", "content": text,
			"created_at": b.now.Add(-4 * time.Minute)})
	}
	b.fx.Insert(t, "task_message", testutil.Cols{"task_id": task, "seq": 9, "type": "thinking", "content": "private",
		"created_at": b.now.Add(-time.Minute)})

	// A run that wrote a step a minute ago wakes nobody.
	b.run(w, b.now)
	if n := b.reminders(id, ownerProgressEvent); n != 0 {
		t.Fatalf("busy run queued %d progress reminders", n)
	}

	// An employee comment wakes the owner once, after the settle delay.
	b.fx.Comment(t, id, "中途回报：改完一半", testutil.Cols{"author_type": "agent", "author_id": b.dev, "created_at": b.now.Add(time.Minute)})
	at := b.now.Add(2 * time.Minute)
	b.run(w, at)
	if n := b.reminders(id, ownerProgressEvent); n != 1 {
		t.Fatalf("comment queued %d progress reminders, want 1", n)
	}
	var settle time.Duration
	if err := b.pool.QueryRow(b.ctx, `SELECT EXTRACT(EPOCH FROM available_at - attempted_at)::int * interval '1 second'
FROM office_issue_watchdog_attempt WHERE issue_id=$1::uuid AND event_type=$2`, id, ownerProgressEvent).Scan(&settle); err != nil || settle != progressWakeSettle {
		t.Fatalf("progress reminder settles for %v (%v), want %v", settle, err, progressWakeSettle)
	}
	token, events := b.claim(w, at.Add(progressWakeSettle))
	if len(events) != 1 {
		t.Fatalf("claimed %d, want 1", len(events))
	}
	got := events[0]
	if err := w.Receipt(b.ctx, got.ID, token, "sent"); err != nil {
		t.Fatal(err)
	}
	// Receipt stamps the database clock; put it back on the story's timeline.
	if _, err := b.pool.Exec(b.ctx, `UPDATE office_issue_watchdog_attempt SET available_at=$2, delivered_at=$2 WHERE id=$1`,
		got.ID, at.Add(progressWakeSettle)); err != nil {
		t.Fatal(err)
	}
	if got.Kind != reminderKindProgress || got.Cause != progressCauseComment || got.Comment == nil ||
		got.Comment.Summary != "中途回报：改完一半" || got.RunMinutes < 7 {
		t.Fatalf("progress reminder = %+v", got)
	}
	if len(got.Steps) != 3 || got.Steps[0] != "读代码" || got.Steps[2] != "跑测试 token="+secretMark {
		t.Fatalf("steps = %q", got.Steps)
	}

	// A comment inside the interval stays quiet until it ends, then is
	// reported even though it is older than one interval by then.
	b.fx.Comment(t, id, "又一步", testutil.Cols{"author_type": "agent", "author_id": b.dev, "created_at": at.Add(time.Minute)})
	b.run(w, at.Add(9*time.Minute))
	if n := b.reminders(id, ownerProgressEvent); n != 1 {
		t.Fatalf("second comment inside the interval queued a reminder (%d)", n)
	}
	late := at.Add(progressWakeInterval + 5*time.Minute)
	if _, err := b.pool.Exec(b.ctx, `UPDATE agent_task_queue SET status='completed', completed_at=$2 WHERE id=$1::uuid`, task, at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	b.run(w, late)
	_, events = b.claim(w, late.Add(progressWakeSettle))
	if len(events) != 1 || events[0].Cause != progressCauseComment || events[0].Comment.Summary != "又一步" {
		t.Fatalf("after the interval claimed %+v, want the comment written during it", events)
	}
}

func TestProgressWakeDoesNotStackWhileUndelivered(t *testing.T) {
	b := newReviewBench(t)
	w := b.watchdog(Config{})
	b.now = b.now.Add(-2 * time.Hour)
	id := b.issue("Relay down", "in_progress", b.dev, nil)
	b.fx.Insert(t, "agent_task_queue", testutil.Cols{"issue_id": id, "agent_id": b.dev, "status": "running",
		"runtime_id": b.runtimeID, "started_at": b.now.Add(-20 * time.Minute)})
	for _, minutes := range []int{0, 16, 32, 48} {
		b.run(w, b.now.Add(time.Duration(minutes)*time.Minute))
	}
	if n := b.reminders(id, ownerProgressEvent); n != 1 {
		t.Fatalf("undelivered run stacked %d progress reminders, want 1", n)
	}
	_, events := b.claim(w, b.now.Add(50*time.Minute))
	if len(events) != 1 || events[0].Cause != progressCauseQuiet || events[0].RunMinutes < 70 {
		t.Fatalf("claimed %+v, want one current quiet reminder", events)
	}
}

func TestProgressWakeAfterTenQuietMinutesOncePerStretch(t *testing.T) {
	b := newReviewBench(t)
	w := b.watchdog(Config{})
	b.now = b.now.Add(-2 * time.Hour)
	id := b.issue("Silent job", "todo", b.dev, nil)
	b.moveBy(id, "in_progress", "agent", b.now.Add(-9*time.Minute))
	task := b.fx.Insert(t, "agent_task_queue", testutil.Cols{"issue_id": id, "agent_id": b.dev, "status": "running",
		"runtime_id": b.runtimeID, "started_at": b.now.Add(-8 * time.Minute)})
	step := func(seq int, at time.Time) {
		b.fx.Insert(t, "task_message", testutil.Cols{"task_id": task, "seq": seq, "type": "tool_use", "content": "", "created_at": at})
	}
	quiet := func() []time.Time {
		t.Helper()
		var out []time.Time
		rows, err := b.pool.Query(b.ctx, `SELECT (payload->>'quiet_since')::timestamptz FROM office_issue_watchdog_attempt
WHERE issue_id=$1::uuid AND event_type=$2 AND payload->>'cause'=$3 ORDER BY id`, id, ownerProgressEvent, progressCauseQuiet)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var at time.Time
			if err := rows.Scan(&at); err != nil {
				t.Fatal(err)
			}
			out = append(out, at)
		}
		return out
	}

	// Nine quiet minutes ring nothing; a run step restarts the clock.
	b.run(w, b.now)
	step(1, b.now)
	b.run(w, b.now.Add(9*time.Minute))
	if got := quiet(); len(got) != 0 {
		t.Fatalf("quiet reminders before ten quiet minutes: %v", got)
	}

	// Ten minutes after the last step the owner is woken, with no settle delay.
	at := b.now.Add(progressWakeInterval)
	b.run(w, at)
	if got := quiet(); len(got) != 1 || !got[0].Equal(b.now) {
		t.Fatalf("quiet reminders = %v, want one since the last step %v", got, b.now)
	}
	token, events := b.claim(w, at)
	if len(events) != 1 || events[0].Cause != progressCauseQuiet || !strings.Contains(events[0].Reason, "10 分钟") {
		t.Fatalf("claimed %+v, want the quiet reminder", events)
	}
	if err := w.Receipt(b.ctx, events[0].ID, token, "sent"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.pool.Exec(b.ctx, `UPDATE office_issue_watchdog_attempt SET available_at=$2, delivered_at=$2 WHERE id=$1`,
		events[0].ID, at); err != nil {
		t.Fatal(err)
	}

	// The same quiet stretch never rings again.
	b.run(w, at.Add(25*time.Minute))
	if got := quiet(); len(got) != 1 {
		t.Fatalf("one quiet stretch rang %d times", len(got))
	}

	// Anyone's comment restarts the clock; a member's comment rings nothing itself.
	commented := at.Add(26 * time.Minute)
	b.fx.Comment(t, id, "扫帚：进度怎样", testutil.Cols{"created_at": commented})
	b.run(w, commented.Add(9*time.Minute))
	if got := b.reminders(id, ownerProgressEvent); got != 1 {
		t.Fatalf("member comment and nine quiet minutes queued %d reminders, want the first only", got)
	}
	b.run(w, commented.Add(progressWakeInterval))
	if got := quiet(); len(got) != 2 || !got[1].Equal(commented) {
		t.Fatalf("quiet reminders = %v, want a second one since the comment", got)
	}

	// Activity between queueing and claiming drops the bell instead of ringing late.
	step(2, commented.Add(progressWakeInterval+5*time.Second))
	if _, events := b.claim(w, commented.Add(progressWakeInterval+10*time.Second)); len(events) != 0 {
		t.Fatalf("claimed %+v after new activity, want none", events)
	}
	var result string
	if err := b.pool.QueryRow(b.ctx, `SELECT delivery_result FROM office_issue_watchdog_attempt
WHERE issue_id=$1::uuid AND event_type=$2 ORDER BY id DESC LIMIT 1`, id, ownerProgressEvent).Scan(&result); err != nil || result != "resolved_before_delivery" {
		t.Fatalf("stale quiet reminder ended %q (%v)", result, err)
	}
}

func TestProgressWakeCarriesTheCommentThatTriggeredIt(t *testing.T) {
	b := newReviewBench(t)
	w := b.watchdog(Config{})
	b.now = b.now.Add(-time.Hour)
	id := b.issue("Reviewer weighs in", "in_progress", b.dev, nil)
	b.fx.Comment(t, id, "旧回报", testutil.Cols{"author_type": "agent", "author_id": b.dev, "created_at": b.now.Add(-time.Hour)})
	b.fx.Comment(t, id, "审查：两处要改", testutil.Cols{"author_type": "agent", "author_id": b.reviewer, "created_at": b.now.Add(-time.Minute)})
	b.run(w, b.now)
	_, events := b.claim(w, b.now.Add(progressWakeSettle))
	if len(events) != 1 || events[0].Comment == nil || events[0].Comment.Summary != "审查：两处要改" {
		t.Fatalf("claimed %+v, want the reviewer's new comment", events)
	}
}

func TestProgressWakeYieldsToDeliveryAndKun(t *testing.T) {
	b := newReviewBench(t)
	w := b.watchdog(Config{})
	b.now = b.now.Add(-time.Hour)
	id := b.issue("Comment then deliver", "in_progress", b.dev, nil)
	b.fx.Comment(t, id, "交付：PR 已开", testutil.Cols{"author_type": "agent", "author_id": b.dev, "created_at": b.now.Add(-20 * time.Second)})
	b.run(w, b.now)
	if n := b.reminders(id, ownerProgressEvent); n != 1 {
		t.Fatalf("comment queued %d progress reminders", n)
	}
	b.moveBy(id, "in_review", "agent", b.now.Add(5*time.Second))
	b.run(w, b.now.Add(10*time.Second))
	_, events := b.claim(w, b.now.Add(time.Minute))
	if len(events) != 1 || events[0].Kind != reminderKindStatus || events[0].Cause != "delivered" {
		t.Fatalf("claimed %+v, want only the delivery", events)
	}

	parked := b.issue("待 Kun｜选方案", "in_progress", b.dev, nil)
	b.fx.Comment(t, parked, "方案写好了", testutil.Cols{"author_type": "agent", "author_id": b.dev, "created_at": b.now.Add(-time.Minute)})
	b.run(w, b.now)
	if n := b.reminders(parked, ""); n != 0 {
		t.Fatalf("issue waiting on Kun queued %d reminders", n)
	}
}

func TestOverdueQuietIssueRingsTheStallAlone(t *testing.T) {
	b := newReviewBench(t)
	w := b.watchdog(Config{})
	b.now = b.now.Add(-time.Hour)
	id := b.issue("Old quiet issue", "in_progress", b.dev, nil)
	b.run(w, b.now)
	_, events := b.claim(w, b.now)
	if len(events) != 1 || events[0].Kind != reminderKindStall || events[0].IssueID != id {
		t.Fatalf("overdue quiet issue claimed %+v, want the stall reminder alone", events)
	}
	// A stuck live run never stalls, so it still gets its quiet bell.
	stuck := b.issue("Stuck run", "in_progress", b.dev, nil)
	b.fx.Insert(t, "agent_task_queue", testutil.Cols{"issue_id": stuck, "agent_id": b.dev, "status": "running",
		"runtime_id": b.runtimeID, "started_at": b.now.Add(-time.Hour)})
	b.run(w, b.now)
	if n, q := b.reminders(stuck, ownerStalledEvent), b.reminders(stuck, ownerProgressEvent); n != 0 || q != 1 {
		t.Fatalf("stuck run queued %d stall and %d progress reminders, want 0 and 1", n, q)
	}
}
