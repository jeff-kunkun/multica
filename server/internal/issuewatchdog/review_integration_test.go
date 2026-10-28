package issuewatchdog

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// reviewBench is a workspace with a developer (the fixture's agent), a
// reviewer agent, a parent-owner agent, and helpers to build backdated issues.
type reviewBench struct {
	t           *testing.T
	ctx         context.Context
	pool        *pgxpool.Pool
	fx          *testutil.Fixture
	h           *handler.Handler
	workspaceID string
	dev         string
	reviewer    string
	parentOwner string
	runtimeID   string
	projectID   string
	properties  []byte
	now         time.Time
}

func newReviewBench(t *testing.T) *reviewBench {
	t.Helper()
	ctx := context.Background()
	pool := testutil.OpenTestDatabase(ctx, t)
	fx, workspaceID, agentID, _, seedIssueID := watchdogFixture(t, pool)
	b := &reviewBench{t: t, ctx: ctx, pool: pool, fx: fx, workspaceID: workspaceID, dev: agentID,
		now: time.Now().UTC().Truncate(time.Second)}
	b.h = handler.New(db.New(pool), pool, realtime.NewHub(), events.New(), nil, nil, nil, analytics.NoopClient{}, handler.Config{})
	if err := pool.QueryRow(ctx, `SELECT project_id::text, properties FROM issue WHERE id = $1::uuid`,
		seedIssueID).Scan(&b.projectID, &b.properties); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT runtime_id::text FROM agent WHERE id = $1::uuid`,
		agentID).Scan(&b.runtimeID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE issue SET status = 'backlog' WHERE id = $1::uuid`, seedIssueID); err != nil {
		t.Fatal(err)
	}
	b.reviewer = fx.Agent(t, "Reviewer "+seedIssueID[:8], b.runtimeID)
	b.parentOwner = fx.Agent(t, "Parent owner "+seedIssueID[:8], b.runtimeID)
	return b
}

func (b *reviewBench) watchdog(cfg Config) *Watchdog {
	b.t.Helper()
	base := Config{
		Enabled: true, WorkspaceID: b.workspaceID, WebhookURL: "http://127.0.0.1:1", WebhookKey: "unused",
		TickInterval: time.Minute, TodoStartDelay: 10 * time.Minute,
		IdleInterval: 30 * time.Minute, ManagerWait: 15 * time.Minute, MaxEmployeeRound: 2,
		HeartbeatFile: filepath.Join(b.t.TempDir(), "last-run"), SilentInterval: 20 * time.Minute,
		HoldWait: 10 * time.Minute, ReviewWait: 20 * time.Minute, NoRunWait: 15 * time.Minute, ChildWait: 30 * time.Minute,
	}
	base.HoldWake, base.ReviewWake, base.StuckWake, base.ChildWake = cfg.HoldWake, cfg.ReviewWake, cfg.StuckWake, cfg.ChildWake
	base.RoundStop, base.RoundLimit = cfg.RoundStop, cfg.RoundLimit
	if cfg.WebhookURL != "" {
		base.WebhookURL, base.WebhookKey = cfg.WebhookURL, cfg.WebhookKey
	}
	w := New(b.pool, base, b.h.CreateWatchdogNudgeComment)
	w.SetWake(b.h.CreateWatchdogWakeComment)
	w.SetNote(b.h.CreateWatchdogNoteComment)
	w.now = func() time.Time { return b.now }
	return w
}

// issue builds an issue created two hours ago whose status-change history is
// backdated with it, so only the comments and tasks a test adds count as
// recent activity.
func (b *reviewBench) issue(title, status, assignee string, cols testutil.Cols) string {
	b.t.Helper()
	base := testutil.Cols{
		"workspace_id": b.workspaceID, "project_id": b.projectID, "properties": json.RawMessage(b.properties),
		"status": status, "created_at": b.now.Add(-2 * time.Hour), "updated_at": b.now.Add(-2 * time.Hour),
		"first_executed_at": b.now.Add(-2 * time.Hour),
	}
	if assignee != "" {
		base["assignee_type"], base["assignee_id"] = "agent", assignee
	}
	for k, v := range cols {
		base[k] = v
	}
	id := b.fx.Issue(b.t, title, base)
	if _, err := b.pool.Exec(b.ctx, `UPDATE office_event SET occurred_at = $2 WHERE issue_id = $1::uuid`,
		id, b.now.Add(-2*time.Hour)); err != nil {
		b.t.Fatal(err)
	}
	return id
}

func (b *reviewBench) comment(issueID, authorID, content string, ago time.Duration) string {
	b.t.Helper()
	return b.fx.Comment(b.t, issueID, content, testutil.Cols{
		"author_type": "agent", "author_id": authorID, "created_at": b.now.Add(-ago),
	})
}

func (b *reviewBench) mention(agentID string) string {
	return "[@Agent](mention://agent/" + agentID + ")"
}

// wakes counts watchdog comments on issueID that @mention agentID.
func (b *reviewBench) wakes(issueID, agentID string) int {
	b.t.Helper()
	var n int
	if err := b.pool.QueryRow(b.ctx, `
SELECT count(*) FROM comment
WHERE issue_id = $1::uuid AND author_type = 'system' AND content LIKE '%office-issue-watchdog%'
  AND content LIKE '%mention://agent/' || $2 || '%'`, issueID, agentID).Scan(&n); err != nil {
		b.t.Fatal(err)
	}
	return n
}

func (b *reviewBench) allWakes(issueID string) int {
	b.t.Helper()
	var n int
	if err := b.pool.QueryRow(b.ctx, `
SELECT count(*) FROM comment
WHERE issue_id = $1::uuid AND author_type = 'system' AND content LIKE '%office-issue-watchdog%'`,
		issueID).Scan(&n); err != nil {
		b.t.Fatal(err)
	}
	return n
}

func (b *reviewBench) queuedFor(issueID, agentID string) int {
	b.t.Helper()
	var n int
	if err := b.pool.QueryRow(b.ctx, `
SELECT count(*) FROM agent_task_queue
WHERE issue_id = $1::uuid AND agent_id = $2::uuid AND status IN ('queued', 'dispatched', 'deferred', 'running')`,
		issueID, agentID).Scan(&n); err != nil {
		b.t.Fatal(err)
	}
	return n
}

func (b *reviewBench) commentEvent(w *Watchdog, issueID, commentID, authorID, content string) {
	w.HandleEvent(events.Event{
		Type: protocol.EventCommentCreated, WorkspaceID: b.workspaceID, ActorType: "agent", ActorID: authorID,
		Payload: map[string]any{"comment": map[string]any{
			"id": commentID, "issue_id": issueID, "author_type": "agent", "author_id": authorID,
			"content": content, "type": "comment",
		}},
	})
}

func (b *reviewBench) run(w *Watchdog, at time.Time) {
	b.t.Helper()
	if err := w.Run(b.ctx, at); err != nil {
		b.t.Fatalf("scan: %v", err)
	}
}

func TestHoldWithoutMentionWakesTheDeveloper(t *testing.T) {
	b := newReviewBench(t)
	const hold = "审查结论：需要修改，暂不通过，先请修正下面这项后再交 CTO·扳手签字。"

	// in_review keeps the ordinary idle wake off the two event-driven issues.
	atOnceID := b.issue("hold posted just now", "in_review", b.dev, nil)
	silentID := b.issue("hold 11m ago, nothing ran", "in_progress", b.dev, nil)
	b.comment(silentID, b.reviewer, hold, 11*time.Minute)
	orphanParentID := b.issue("parent of an unassigned issue", "in_review", b.parentOwner, nil)
	orphanID := b.issue("hold on an issue with no developer", "in_progress", "",
		testutil.Cols{"parent_issue_id": orphanParentID})
	b.comment(orphanID, b.reviewer, hold, 11*time.Minute)
	ranID := b.issue("hold 11m ago, the developer ran since", "in_progress", b.dev, nil)
	b.comment(ranID, b.reviewer, hold, 11*time.Minute)
	b.fx.Task(t, b.dev, testutil.Cols{
		"issue_id": ranID, "runtime_id": b.runtimeID, "status": "completed",
		"created_at": b.now.Add(-8 * time.Minute), "started_at": b.now.Add(-8 * time.Minute),
		"completed_at": b.now.Add(-6 * time.Minute),
	})
	mentionedID := b.issue("hold that names the developer", "in_review", b.dev, nil)

	// Off by default: the same scan with no switch leaves every issue alone.
	off := b.watchdog(Config{})
	b.run(off, b.now)
	for _, id := range []string{silentID, orphanID, ranID} {
		if n := b.allWakes(id); n != 0 {
			t.Fatalf("with HoldWake off, issue %s got %d watchdog comments", id, n)
		}
	}
	t.Log("switch off: no hold wakes")

	w := b.watchdog(Config{HoldWake: true})

	commentID := b.comment(atOnceID, b.reviewer, hold, 0)
	b.commentEvent(w, atOnceID, commentID, b.reviewer, hold)
	b.commentEvent(w, atOnceID, commentID, b.reviewer, hold)
	if n := b.wakes(atOnceID, b.dev); n != 1 {
		t.Fatalf("hold without a mention: developer wakes = %d, want 1", n)
	}
	if b.queuedFor(atOnceID, b.dev) == 0 {
		t.Fatal("the at-once wake did not queue a developer run")
	}
	t.Log("hold posted without @developer: developer woken once, at once, run queued")

	named := hold + "\n\n" + b.mention(b.dev) + " 请修改。"
	namedID := b.comment(mentionedID, b.reviewer, named, 0)
	b.commentEvent(w, mentionedID, namedID, b.reviewer, named)
	if n := b.allWakes(mentionedID); n != 0 {
		t.Fatalf("hold that already names the developer got %d watchdog comments", n)
	}
	t.Log("hold that names the developer: no extra wake")

	b.run(w, b.now)
	b.run(w, b.now.Add(time.Minute))
	if n := b.wakes(silentID, b.dev); n != 1 {
		t.Fatalf("hold 11m ago with no run: developer wakes = %d, want 1", n)
	}
	t.Log("hold, 11m with no run: developer woken once by the scan, repeat scan silent")

	if n := b.wakes(orphanID, b.parentOwner); n != 1 || b.allWakes(orphanID) != 1 {
		t.Fatalf("hold on an issue with no developer: parent-owner wakes = %d of %d", n, b.allWakes(orphanID))
	}
	t.Log("no developer on the issue: the parent's owner was woken instead")

	if n := b.allWakes(ranID); n != 0 {
		t.Fatalf("a run started after the hold, yet the issue got %d watchdog comments", n)
	}
	t.Log("a run after the hold: no wake")
}

func TestReviewRequestWithoutVerdictWakesReviewerOnce(t *testing.T) {
	b := newReviewBench(t)
	request := "修好了，已推新提交。" + b.mention(b.reviewer) + " 请复审。"

	overdueID := b.issue("re-review requested 25m ago", "in_progress", b.dev, nil)
	b.comment(overdueID, b.dev, request, 25*time.Minute)
	answeredID := b.issue("re-review answered within 10m", "in_progress", b.dev, nil)
	b.comment(answeredID, b.dev, request, 25*time.Minute)
	b.comment(answeredID, b.reviewer, "复审结论：代码复审通过，之前的 P2 已修复。", 16*time.Minute)

	w := b.watchdog(Config{ReviewWake: true})
	b.run(w, b.now)
	b.run(w, b.now.Add(time.Minute))
	b.run(w, b.now.Add(25*time.Minute))

	if n := b.wakes(overdueID, b.reviewer); n != 1 {
		t.Fatalf("re-review with no verdict after 25m: reviewer wakes = %d, want exactly 1", n)
	}
	if b.queuedFor(overdueID, b.reviewer) == 0 {
		t.Fatal("the reviewer wake did not queue a reviewer run")
	}
	t.Log("re-review, 25m without a verdict: reviewer @mentioned once, run queued; later scans silent")

	// By +25m the answered issue is 41m idle, so the ordinary idle wake may
	// nudge its developer; the reviewer must never be woken.
	if n := b.wakes(answeredID, b.reviewer); n != 0 {
		t.Fatalf("re-review answered within 10m: reviewer wakes = %d, want 0", n)
	}
	t.Log("re-review answered within 10m: reviewer not woken")
}

func TestStuckInProgressWakesWhoeverItWaitsOn(t *testing.T) {
	b := newReviewBench(t)
	const managerKey = "stuck-wake-key"
	receiver, received, _ := newFakeReceiver(t, managerKey, http.StatusAccepted)
	defer receiver.Close()
	request := "PR 已推。" + b.mention(b.reviewer) + " 请审查。"

	authorID := b.issue("stuck on the author", "in_progress", b.dev, nil)
	b.comment(authorID, b.dev, "在改测试", 16*time.Minute)
	reviewerID := b.issue("stuck on the reviewer", "in_progress", b.dev, nil)
	b.comment(reviewerID, b.dev, request, 16*time.Minute)
	signoffID := b.issue("stuck waiting for sign-off", "in_progress", b.dev, nil)
	b.comment(signoffID, b.dev, request, 40*time.Minute)
	b.comment(signoffID, b.reviewer, "复审通过 PR #14，可以交 CTO·扳手签字。", 16*time.Minute)
	runningID := b.issue("a run is live", "in_progress", b.dev, nil)
	b.comment(runningID, b.dev, "在改测试", 16*time.Minute)
	b.fx.Task(t, b.dev, testutil.Cols{
		"issue_id": runningID, "runtime_id": b.runtimeID, "status": "running",
		"dispatched_at": b.now.Add(-20 * time.Minute), "started_at": b.now.Add(-20 * time.Minute),
	})

	w := b.watchdog(Config{StuckWake: true, WebhookURL: receiver.URL, WebhookKey: managerKey})
	b.run(w, b.now)
	b.run(w, b.now.Add(time.Minute))

	if n := b.wakes(authorID, b.dev); n != 1 || b.allWakes(authorID) != 1 {
		t.Fatalf("stuck on the author: developer wakes = %d of %d, want 1", n, b.allWakes(authorID))
	}
	t.Log("in_progress, 16m with no run, author's turn: the author was woken once (before the 30m idle wake)")

	if n := b.wakes(reviewerID, b.reviewer); n != 1 || b.allWakes(reviewerID) != 1 {
		t.Fatalf("stuck on the reviewer: reviewer wakes = %d of %d, want only the reviewer once", n, b.allWakes(reviewerID))
	}
	t.Log("in_progress, 16m with no run, waiting on review: the reviewer was woken, not the author")

	var signoff []webhookEvent
	for _, e := range received() {
		if e.IssueID == signoffID {
			signoff = append(signoff, e)
		}
	}
	if len(signoff) != 1 || signoff[0].EventType != "awaiting_signoff" || signoff[0].WakeTarget != "manager" ||
		signoff[0].Manager != "CTO·扳手" || b.allWakes(signoffID) != 0 {
		t.Fatalf("waiting for sign-off: events=%+v comments=%d, want one manager reminder and no comment",
			signoff, b.allWakes(signoffID))
	}
	t.Log("passed and waiting for sign-off: one reminder to the responsible manager, no wake comment")

	if n := b.allWakes(runningID); n != 0 {
		t.Fatalf("a live run got %d watchdog comments", n)
	}
	t.Log("a live run: no wake")
}

func TestTwoFailedRoundsStopTheLoopForKun(t *testing.T) {
	b := newReviewBench(t)
	const managerKey = "round-stop-key"
	receiver, received, _ := newFakeReceiver(t, managerKey, http.StatusAccepted)
	defer receiver.Close()
	const firstHold = "审查结论：hold，暂不交 CTO·扳手签字；请修完以下问题后再审。"
	const secondHold = "复审 PR #14 当前提交 `6e603072`：hold，暂不交 CTO·扳手签字。"
	request := "已修复，提交 `abc1234`。" + b.mention(b.reviewer) + " 请复审。"
	twoRounds := func(title string) (string, string) {
		id := b.issue(title, "in_progress", b.dev, nil)
		b.comment(id, b.reviewer, firstHold, 40*time.Minute)
		b.comment(id, b.dev, request, 30*time.Minute)
		return id, b.comment(id, b.reviewer, secondHold, 0)
	}
	roundLimitEvents := func(issueID string) int {
		n := 0
		for _, e := range received() {
			if e.IssueID == issueID && e.EventType == "review_round_limit" {
				n++
			}
		}
		return n
	}

	offID, offHold := twoRounds("two failed rounds, stop switched off")
	off := b.watchdog(Config{HoldWake: true, WebhookURL: receiver.URL, WebhookKey: managerKey})
	b.commentEvent(off, offID, offHold, b.reviewer, secondHold)
	if n := b.wakes(offID, b.dev); n != 1 || roundLimitEvents(offID) != 0 {
		t.Fatalf("RoundStop off: developer wakes = %d, round-limit events = %d; want the ordinary hold wake only",
			n, roundLimitEvents(offID))
	}
	t.Log("switch off: the second hold wakes the developer as usual, no stop")

	issueID, holdID := twoRounds("two failed review rounds")
	w := b.watchdog(Config{HoldWake: true, ReviewWake: true, StuckWake: true, RoundStop: true, RoundLimit: 2,
		WebhookURL: receiver.URL, WebhookKey: managerKey})
	b.commentEvent(w, issueID, holdID, b.reviewer, secondHold)
	b.commentEvent(w, issueID, holdID, b.reviewer, secondHold)
	var note string
	if err := b.pool.QueryRow(b.ctx, `
SELECT content FROM comment
WHERE issue_id = $1::uuid AND author_type = 'system' AND content LIKE '%office-issue-watchdog%'`,
		issueID).Scan(&note); err != nil {
		t.Fatalf("expected exactly one watchdog note: %v", err)
	}
	if !strings.Contains(note, "连续 2 轮不通过") || !strings.Contains(note, "A. 再改一轮") || strings.Contains(note, "mention://") {
		t.Fatalf("note does not lay out options without a mention:\n%s", note)
	}
	if n := roundLimitEvents(issueID); n != 1 {
		t.Fatalf("round-limit manager events = %d, want 1", n)
	}
	if n := b.queuedFor(issueID, b.dev) + b.queuedFor(issueID, b.reviewer); n != 0 {
		t.Fatalf("the stop started %d runs", n)
	}
	t.Log("second failed round: one options note with no mention, one manager event, no run started")

	for _, at := range []time.Duration{11 * time.Minute, 16 * time.Minute, 45 * time.Minute} {
		b.run(w, b.now.Add(at))
	}
	if n := b.allWakes(issueID); n != 1 || roundLimitEvents(issueID) != 1 {
		t.Fatalf("after the stop, scans added comments (%d total) or events (%d)", n, roundLimitEvents(issueID))
	}
	t.Log("scans at +11m, +16m and +45m: hold, stuck and idle wakes all stay quiet")

	// Kun picks "one more round": the developer fixes and asks again, and the
	// review rules are back.
	b.comment(issueID, b.dev, request, -50*time.Minute)
	b.run(w, b.now.Add(75*time.Minute))
	if n := b.wakes(issueID, b.reviewer); n != 1 {
		t.Fatalf("after a new review request, reviewer wakes = %d, want 1", n)
	}
	t.Log("a new review request lifts the stop: the overdue reviewer is woken again")
}

// Codex review of 9624439: a stalled sub-issue must not wake anyone on a
// parent that is stopped for Kun.
func TestStalledChildDoesNotWakeAStoppedParent(t *testing.T) {
	b := newReviewBench(t)
	const managerKey = "stopped-parent-key"
	receiver, received, _ := newFakeReceiver(t, managerKey, http.StatusAccepted)
	defer receiver.Close()
	request := "已修复。" + b.mention(b.reviewer) + " 请复审。"
	stoppedParent := func(title string) (string, string) {
		parentID := b.issue(title, "in_review", b.dev, nil)
		b.comment(parentID, b.reviewer, "审查结论：hold，暂不交 CTO·扳手签字。", 40*time.Minute)
		b.comment(parentID, b.dev, request, 35*time.Minute)
		b.comment(parentID, b.reviewer, "复审结论：hold，仍有一项需要补齐。", 32*time.Minute)
		childID := b.issue(title+" child, unassigned", "todo", "", testutil.Cols{"parent_issue_id": parentID})
		return parentID, childID
	}
	childEvents := func(childID string) []webhookEvent {
		var out []webhookEvent
		for _, e := range received() {
			if e.IssueID == childID && e.EventType == "child_unattended" {
				out = append(out, e)
			}
		}
		return out
	}

	offParent, _ := stoppedParent("round stop off")
	b.run(b.watchdog(Config{ChildWake: true, WebhookURL: receiver.URL, WebhookKey: managerKey}), b.now)
	if n := b.wakes(offParent, b.dev); n != 1 {
		t.Fatalf("RoundStop off: parent-owner wakes on the parent = %d, want 1", n)
	}
	t.Log("switch off: the stalled child wakes the parent's owner on the parent, as before")

	parentID, childID := stoppedParent("round stop on")
	w := b.watchdog(Config{ChildWake: true, RoundStop: true, RoundLimit: 2, WebhookURL: receiver.URL, WebhookKey: managerKey})
	b.run(w, b.now)
	b.run(w, b.now.Add(time.Minute))
	if n := b.wakes(parentID, b.dev); n != 0 {
		t.Fatalf("a parent stopped for Kun got %d wakes from its stalled child", n)
	}
	if n := b.queuedFor(parentID, b.dev); n != 0 {
		t.Fatalf("a parent stopped for Kun got %d runs started", n)
	}
	if events := childEvents(childID); len(events) != 1 || events[0].WakeTarget != "manager" ||
		!strings.Contains(events[0].Reason, "等 Kun 拍板") {
		t.Fatalf("stalled child under a stopped parent: events = %+v, want one manager event", events)
	}
	t.Log("switch on: no wake and no run on the stopped parent; the manager gets one child_unattended event instead")
}

func TestStalledSubIssueWakesParentOwnerOnParent(t *testing.T) {
	b := newReviewBench(t)
	// One wake per parent per scan: once the owner has a run queued on the
	// parent, the watchdog waits instead of interrupting it, so the second
	// case gets its own parent.
	parentID := b.issue("parent with stalled children", "in_review", b.parentOwner, nil)
	otherParentID := b.issue("parent with an idle owner", "in_review", b.parentOwner, nil)
	child := func(parentID, title, status, assignee string) (string, string) {
		id := b.issue(title, status, assignee, testutil.Cols{"parent_issue_id": parentID})
		var identifier string
		if err := b.pool.QueryRow(b.ctx, `
SELECT w.issue_prefix || '-' || i.number::text FROM issue i JOIN workspace w ON w.id = i.workspace_id
WHERE i.id = $1::uuid`, id).Scan(&identifier); err != nil {
			t.Fatal(err)
		}
		return id, identifier
	}
	_, unowned := child(parentID, "child with no assignee", "todo", "")
	activeID, active := child(parentID, "child whose owner is moving", "in_progress", b.dev)
	b.comment(activeID, b.dev, "测试跑完一半", 5*time.Minute)
	_, idleBoth := child(otherParentID, "child idle, parent owner idle", "in_progress", b.dev)

	w := b.watchdog(Config{ChildWake: true})
	b.run(w, b.now)
	b.run(w, b.now.Add(time.Minute))

	mentionsChild := func(parentID, identifier string) int {
		t.Helper()
		var n int
		if err := b.pool.QueryRow(b.ctx, `
SELECT count(*) FROM comment
WHERE issue_id = $1::uuid AND author_type = 'system' AND content LIKE '%office-issue-watchdog%'
  AND content LIKE '%mention://agent/' || $2 || '%' AND content LIKE '%' || $3 || '「%'`,
			parentID, b.parentOwner, identifier).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := mentionsChild(parentID, unowned); n != 1 {
		t.Fatalf("unassigned stalled child: parent comments naming it = %d, want 1", n)
	}
	t.Logf("child %s with no assignee: parent owner woken once, on the parent", unowned)
	if n := mentionsChild(otherParentID, idleBoth); n != 1 {
		t.Fatalf("stalled child with an idle parent owner: parent comments naming it = %d, want 1", n)
	}
	t.Logf("child %s stalled while the parent owner is idle: parent owner woken once", idleBoth)
	if n := mentionsChild(parentID, active); n != 0 {
		t.Fatalf("child whose owner commented 5m ago produced %d parent comments", n)
	}
	t.Logf("child %s whose owner is moving: no parent wake", active)
	if b.queuedFor(parentID, b.parentOwner) == 0 {
		t.Fatal("the parent-owner wake did not queue a run on the parent")
	}
}
