package issuewatchdog

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// backdate moves an issue's creation and status history to ago, so it has
// been quiet that long.
func (b *reviewBench) backdate(issueID string, ago time.Duration) {
	b.t.Helper()
	at := b.now.Add(-ago)
	if _, err := b.pool.Exec(b.ctx, `
UPDATE issue SET created_at = $2, updated_at = $2, first_executed_at = $2 WHERE id = $1::uuid`,
		issueID, at); err != nil {
		b.t.Fatal(err)
	}
	if _, err := b.pool.Exec(b.ctx, `UPDATE office_event SET occurred_at = $2 WHERE issue_id = $1::uuid`,
		issueID, at); err != nil {
		b.t.Fatal(err)
	}
}

func (b *reviewBench) label(name string) string {
	b.t.Helper()
	return b.fx.Insert(b.t, "issue_label", testutil.Cols{
		"workspace_id": b.workspaceID, "name": name, "color": "#d97706", "resource_type": "issue",
	})
}

// setLabel attaches or detaches labelID the way the label handler does, with
// the issue_labels:changed event it publishes.
func (b *reviewBench) setLabel(w *Watchdog, issueID, labelID string, on bool) {
	b.t.Helper()
	sql := `DELETE FROM issue_to_label WHERE issue_id = $1::uuid AND label_id = $2::uuid`
	if on {
		sql = `INSERT INTO issue_to_label (issue_id, label_id) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`
	}
	if _, err := b.pool.Exec(b.ctx, sql, issueID, labelID); err != nil {
		b.t.Fatal(err)
	}
	if w != nil {
		w.HandleEvent(events.Event{
			Type: protocol.EventIssueLabelsChanged, WorkspaceID: b.workspaceID, ActorType: "member",
			Payload: map[string]any{"issue_id": issueID},
		})
	}
}

func eventsOf(all []webhookEvent, issueID, eventType string) []webhookEvent {
	var out []webhookEvent
	for _, e := range all {
		if e.IssueID == issueID && e.EventType == eventType {
			out = append(out, e)
		}
	}
	return out
}

// Kun's office timeouts (2026-10-28): an in_review issue quiet for 2h, and a
// started issue put back in todo quiet for 6h, alert the responsible manager,
// the only one who moves them on, once per stall; a live run counts as
// movement.
func TestQuietInReviewAndPausedTodoAlertTheManager(t *testing.T) {
	b := newReviewBench(t)
	const key = "office-timeouts-key"
	receiver, received, _ := newFakeReceiver(t, key, http.StatusAccepted)
	defer receiver.Close()
	w := b.watchdog(Config{
		InReviewWait: 2 * time.Hour, TodoStartDelay: 6 * time.Hour, KunLabel: "等 Kun 拍板",
		WebhookURL: receiver.URL, WebhookKey: key,
	})

	reviewID := b.issue("delivered, waiting on the manager", "in_review", b.dev, nil)
	b.comment(reviewID, b.dev, "已交付，请验收。", 119*time.Minute)
	liveID := b.issue("in review while a run is going", "in_review", b.dev, nil)
	b.backdate(liveID, 3*time.Hour)
	b.fx.Task(t, b.dev, testutil.Cols{
		"issue_id": liveID, "runtime_id": b.runtimeID, "status": "running",
		"created_at": b.now.Add(-3 * time.Hour), "started_at": b.now.Add(-3 * time.Hour),
	})
	pausedID := b.issue("started, then put back in todo", "todo", b.dev, nil)
	b.backdate(pausedID, 5*time.Hour+59*time.Minute)

	b.run(w, b.now)
	if got := received(); len(got) != 0 {
		t.Fatalf("before the timeouts the manager got %+v", got)
	}
	t.Log("in_review quiet 1h59m and todo paused 5h59m: no alert yet")

	b.run(w, b.now.Add(time.Minute))
	b.run(w, b.now.Add(2*time.Minute))
	review := eventsOf(received(), reviewID, inReviewOverdue)
	if len(review) != 1 || review[0].WakeTarget != "manager" || review[0].Manager != "CTO·扳手" ||
		!strings.Contains(review[0].Reason, "待审已 120 分钟") || !strings.Contains(review[0].Reason, "「等 Kun 拍板」") {
		t.Fatalf("in_review alerts = %+v, want one manager event after 2h", review)
	}
	paused := eventsOf(received(), pausedID, todoOverdue)
	if len(paused) != 1 || paused[0].WakeTarget != "manager" || !strings.Contains(paused[0].Reason, "放回了待办") {
		t.Fatalf("paused todo alerts = %+v, want one manager event after 6h", paused)
	}
	if got := eventsOf(received(), liveID, inReviewOverdue); len(got) != 0 {
		t.Fatalf("an in_review issue with a live run alerted the manager: %+v", got)
	}
	if n := b.allWakes(reviewID) + b.allWakes(pausedID); n != 0 {
		t.Fatalf("manager alerts left %d wake comments; the employee must not be woken", n)
	}
	t.Log("at 2h and 6h: one manager event each, no employee wake, nothing for the live run")

	// The ordinary manager wait follows: no signed reply in 15 minutes goes to COO.
	b.run(w, b.now.Add(17*time.Minute))
	if got := eventsOf(received(), reviewID, "manager_timeout"); len(got) != 1 || got[0].WakeTarget != "COO·Grok" {
		t.Fatalf("COO escalations for the quiet in_review issue = %+v, want one", got)
	}
	t.Log("15 minutes without a signed manager reply: one COO escalation")
}

// A manager whose "负责经理" option the old signature table did not list
// (基建管家·扫帚) ends the wait with a signed reply instead of always being
// escalated.
func TestUnlistedManagerSignatureEndsTheWait(t *testing.T) {
	b := newReviewBench(t)
	const key = "broom-key"
	receiver, received, _ := newFakeReceiver(t, key, http.StatusAccepted)
	defer receiver.Close()
	var propertyID, optionID string
	if err := b.pool.QueryRow(b.ctx, `
SELECT id::text, config->'options'->0->>'id' FROM issue_property
WHERE workspace_id = $1::uuid AND name = '负责经理'`, b.workspaceID).Scan(&propertyID, &optionID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.pool.Exec(b.ctx, `
UPDATE issue_property SET config = jsonb_set(config, '{options,0,name}', '"基建管家·扫帚"')
WHERE id = $1::uuid`, propertyID); err != nil {
		t.Fatal(err)
	}
	w := b.watchdog(Config{InReviewWait: 2 * time.Hour, WebhookURL: receiver.URL, WebhookKey: key})
	reviewID := b.issue("broom's issue", "in_review", b.dev, nil)
	b.run(w, b.now)
	if got := eventsOf(received(), reviewID, inReviewOverdue); len(got) != 1 || got[0].Manager != "基建管家·扫帚" {
		t.Fatalf("in_review alert = %+v, want one for 基建管家·扫帚", got)
	}
	memberID := testUserIDForWorkspace(t, b.pool, b.workspaceID)
	b.fx.Insert(t, "comment", testutil.Cols{
		"issue_id": reviewID, "workspace_id": b.workspaceID, "author_type": "member", "author_id": memberID,
		"type": "comment", "content": "【负责经理·扫帚】验收通过，我来关单。", "created_at": b.now.Add(5 * time.Minute),
	})
	b.run(w, b.now.Add(16*time.Minute))
	if got := eventsOf(received(), reviewID, "manager_timeout"); len(got) != 0 {
		t.Fatalf("a signed 扫帚 reply was escalated to COO: %+v", got)
	}
	t.Log("【负责经理·扫帚】 reply within 15 minutes: no COO escalation")
}

// The 「等 Kun 拍板」 label: putting it on pushes the issue to the office
// group once; while it is on, nobody is woken on the issue; taking it off
// gives the issue a fresh idle clock, and putting it on again pushes again.
func TestKunDecisionLabelPushesToOfficeGroupAndQuietsWakes(t *testing.T) {
	b := newReviewBench(t)
	const key = "kun-label-key"
	receiver, received, _ := newFakeReceiver(t, key, http.StatusAccepted)
	defer receiver.Close()
	w := b.watchdog(Config{
		InReviewWait: 2 * time.Hour, KunLabel: "等 Kun 拍板", KunGroup: "Multica 办公室",
		HoldWake: true, WebhookURL: receiver.URL, WebhookKey: key,
	})
	labelID := b.label("等 kun 拍板") // matched case-insensitively
	issueID := b.issue("needs Kun to say 执行", "in_progress", b.dev, nil)
	b.backdate(issueID, 3*time.Hour)
	heldID := b.issue("held while waiting on Kun", "in_review", b.dev, nil)
	b.setLabel(nil, heldID, labelID, true)
	start := b.now

	b.setLabel(w, issueID, labelID, true)
	b.setLabel(w, issueID, labelID, true) // a repeated event is not a second push
	b.run(w, start)
	b.run(w, start.Add(time.Hour))
	pushes := eventsOf(received(), issueID, KunDecisionEventType)
	if len(pushes) != 1 {
		t.Fatalf("Kun-decision pushes = %+v, want exactly one", pushes)
	}
	if p := pushes[0]; p.WakeTarget != "office_group" || p.Group != "Multica 办公室" || p.Status != "in_progress" ||
		p.Manager != "CTO·扳手" || !strings.Contains(p.Reason, "「等 Kun 拍板」") {
		t.Fatalf("Kun-decision push = %+v", p)
	}
	var waitUntil *time.Time
	if err := b.pool.QueryRow(b.ctx, `
SELECT manager_wait_until FROM office_issue_watchdog_attempt
WHERE issue_id = $1::uuid AND event_type = $2`, issueID, KunDecisionEventType).Scan(&waitUntil); err != nil {
		t.Fatal(err)
	}
	if waitUntil != nil {
		t.Fatal("the push to Kun started a manager wait, which would escalate to COO")
	}
	hold := "审查结论：需要修改，暂不通过。"
	holdID := b.comment(heldID, b.reviewer, hold, 0)
	b.commentEvent(w, heldID, holdID, b.reviewer, hold)
	b.run(w, start.Add(2*time.Hour+time.Minute))
	if n := b.allWakes(issueID) + b.allWakes(heldID); n != 0 {
		t.Fatalf("issues waiting on Kun got %d wake comments after hours idle and a hold", n)
	}
	if got := eventsOf(received(), heldID, inReviewOverdue); len(got) != 0 {
		t.Fatalf("an in_review issue waiting on Kun alerted the manager: %+v", got)
	}
	t.Log("label on: one office_group push to Multica 办公室, no manager wait, no idle, hold, or in_review wake")

	// Kun answers at +3h by taking the label off; the bench's idle interval
	// is 30 minutes.
	b.now = start.Add(3 * time.Hour)
	b.setLabel(w, issueID, labelID, false)
	b.run(w, b.now.Add(time.Minute))
	if n := b.allWakes(issueID); n != 0 {
		t.Fatalf("taking the label off woke someone at once (%d wakes); Kun's answer gets a fresh clock", n)
	}
	b.run(w, b.now.Add(31*time.Minute))
	if n := b.wakes(issueID, b.dev); n != 1 {
		t.Fatalf("30 minutes after the label came off, developer wakes = %d, want the ordinary idle wake", n)
	}
	t.Log("label off: quiet for the idle interval, then the ordinary idle wake")

	// Put back on with no label event (as at issue creation): the scan pushes it.
	b.setLabel(nil, issueID, labelID, true)
	b.run(w, b.now.Add(32*time.Minute))
	if got := eventsOf(received(), issueID, KunDecisionEventType); len(got) != 2 {
		t.Fatalf("label put on again: pushes = %d, want 2", len(got))
	}
	t.Log("label on again, seen only by the scan: a second push")
}

// The Kun-decision push goes to the issue's 「回哪个群」 (DENE-2743): the
// issue's reply_group, a 随上级单 sub-issue's nearest ancestor with one (past
// a parent with none), else the project table; an issue with no project goes to 杂项, and one whose group is
// 不发群 (or whose project has none) to the office group.
func TestKunDecisionPushFollowsReplyGroup(t *testing.T) {
	b := newReviewBench(t)
	const key = "kun-reply-group-key"
	receiver, received, _ := newFakeReceiver(t, key, http.StatusAccepted)
	defer receiver.Close()
	w := b.watchdog(Config{KunLabel: "等 Kun 拍板", KunGroup: "Multica 办公室",
		WebhookURL: receiver.URL, WebhookKey: key})
	labelID := b.label("等 Kun 拍板")
	project := func(title string) string {
		return b.fx.Project(t, title, testutil.Cols{"workspace_id": b.workspaceID})
	}
	tarot, relay := project("tarot"), project("game-relay")
	replyGroup := func(group string) json.RawMessage {
		raw, err := json.Marshal(map[string]string{"reply_group": group})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	own := b.issue("own reply group wins over the project", "in_progress", b.dev,
		testutil.Cols{"project_id": tarot, "metadata": replyGroup(" 游戏 ")})
	byProject := b.issue("no reply group, project table", "in_progress", b.dev,
		testutil.Cols{"project_id": tarot})
	unprojected := b.issue("no reply group, no project", "in_progress", b.dev,
		testutil.Cols{"project_id": nil})
	none := b.issue("不发群 still reaches Kun", "in_progress", b.dev,
		testutil.Cols{"project_id": tarot, "metadata": replyGroup("不发群")})
	unlisted := b.issue("project with no group", "in_progress", b.dev, nil)
	grandparent := b.issue("grandparent names the group", "in_progress", b.dev,
		testutil.Cols{"project_id": tarot, "metadata": replyGroup("Agent mono")})
	parent := b.issue("parent follows its parent", "in_progress", b.dev,
		testutil.Cols{"project_id": tarot, "parent_issue_id": grandparent, "metadata": replyGroup("随上级单")})
	child := b.issue("child follows the chain", "in_progress", b.dev,
		testutil.Cols{"parent_issue_id": parent, "metadata": replyGroup("随上级单")})
	blankParent := b.issue("parent with no reply group", "in_progress", b.dev,
		testutil.Cols{"project_id": relay})
	orphanChild := b.issue("child of a parent with none", "in_progress", b.dev,
		testutil.Cols{"project_id": tarot, "parent_issue_id": blankParent, "metadata": replyGroup("随上级单")})
	topFollower := b.issue("随上级单 with no parent", "in_progress", b.dev,
		testutil.Cols{"project_id": relay, "metadata": replyGroup("随上级单")})
	namedGrandparent := b.issue("grandparent names the group over a blank parent", "in_progress", b.dev,
		testutil.Cols{"project_id": tarot, "metadata": replyGroup("自媒体")})
	blankMiddle := b.issue("parent with no reply group under a named grandparent", "in_progress", b.dev,
		testutil.Cols{"project_id": relay, "parent_issue_id": namedGrandparent})
	skipChild := b.issue("child skips the blank parent", "in_progress", b.dev,
		testutil.Cols{"project_id": tarot, "parent_issue_id": blankMiddle, "metadata": replyGroup("随上级单")})

	want := map[string]struct{ group, route string }{
		own:         {"游戏", "按单上的「回哪个群」"},
		byProject:   {"塔罗站", "按项目对照表"},
		unprojected: {"杂项", "按项目对照表"},
		none:        {"Multica 办公室", "单上的「回哪个群」是「不发群」"},
		unlisted:    {"Multica 办公室", "项目对照表是「不发群」"},
		child:       {"Agent mono", "按上级单的「回哪个群」"},
		orphanChild: {"game relay", "按项目对照表"},
		topFollower: {"game relay", "按项目对照表"},
		skipChild:   {"自媒体", "按上级单的「回哪个群」"},
	}
	for id := range want {
		b.setLabel(w, id, labelID, true)
	}
	for id, wantPush := range want {
		pushes := eventsOf(received(), id, KunDecisionEventType)
		if len(pushes) != 1 {
			t.Fatalf("issue %s: pushes = %+v, want one", id, pushes)
		}
		p := pushes[0]
		if p.Group != wantPush.group || p.WakeTarget != "office_group" ||
			!strings.Contains(p.Reason, wantPush.route) || !strings.Contains(p.Reason, "「"+wantPush.group+"」群") {
			t.Fatalf("issue %s: push group / reason = %q / %q, want %q / %q", id, p.Group, p.Reason, wantPush.group, wantPush.route)
		}
	}
	t.Log("reply_group, 随上级单 up two levels and past a blank parent, project table, 杂项, and 不发群 → office group")
}

// A sub-issue stalled under a parent waiting on Kun does not wake the parent's
// owner; the manager hears about it instead.
func TestStalledChildUnderLabelledParentGoesToTheManager(t *testing.T) {
	b := newReviewBench(t)
	const key = "kun-parent-key"
	receiver, received, _ := newFakeReceiver(t, key, http.StatusAccepted)
	defer receiver.Close()
	w := b.watchdog(Config{ChildWake: true, KunLabel: "等 Kun 拍板", KunGroup: "Multica 办公室",
		WebhookURL: receiver.URL, WebhookKey: key})
	labelID := b.label("等 Kun 拍板")
	parentID := b.issue("parent waiting on Kun", "in_progress", b.parentOwner, nil)
	b.setLabel(nil, parentID, labelID, true)
	childID := b.issue("unassigned child", "todo", "", testutil.Cols{"parent_issue_id": parentID})

	b.run(w, b.now)
	if n := b.allWakes(parentID); n != 0 {
		t.Fatalf("a parent waiting on Kun got %d wakes from its stalled child", n)
	}
	got := eventsOf(received(), childID, "child_unattended")
	if len(got) != 1 || got[0].WakeTarget != "manager" || !strings.Contains(got[0].Reason, "父单正等 Kun 拍板") {
		t.Fatalf("stalled child under a labelled parent: events = %+v, want one manager event", got)
	}
	t.Log("child stalled under a labelled parent: no wake on the parent, one manager event")
}
