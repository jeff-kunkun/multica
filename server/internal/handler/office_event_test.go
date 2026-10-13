package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/dbactor"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The office event stream (DENE-1963): real local sockets and a disposable
// Postgres; NOT an actual Grok endpoint.

type officeFixture struct {
	*humanNeededFixture
	worker *OfficeEventWorker
	api    *httptest.Server
}

func newOfficeFixture(t *testing.T) *officeFixture {
	t.Helper()
	if testHandler == nil {
		t.Skip("requires test database")
	}
	f := &officeFixture{humanNeededFixture: newHumanNeededFixture(t, true)}
	wake := NewBotWakeWorker(testHandler)
	wake.client = func(u *url.URL) *http.Client { return &http.Client{Timeout: 2 * time.Second} }
	f.worker = NewOfficeEventWorker(testHandler, wake)

	router := chi.NewRouter()
	router.Use(middleware.Auth(testHandler.Queries, nil, nil, nil))
	router.Route("/api/workspaces/{id}", func(r chi.Router) {
		r.Use(middleware.RequireWorkspaceMemberFromURL(testHandler.Queries, "id"))
		r.Post("/bots/{botId}/office-events/{eventId}/receipt", testHandler.OfficeEventReceipt)
		r.Get("/bots/{botId}/office-deliveries", testHandler.ListBotOfficeDeliveries)
		r.Post("/bots/{botId}/office-deliveries/retry", testHandler.RetryBotOfficeDeliveries)
		r.Get("/office-events", testHandler.ListOfficeEvents)
		r.Get("/office-events/{eventId}", testHandler.GetOfficeEvent)
	})
	f.api = httptest.NewServer(router)
	t.Cleanup(f.api.Close)
	// Seal whatever earlier tests left behind, and drop what that queued for
	// this Bot, so each test counts only its own pushes.
	f.seal(t)
	dbfx.Exec(t, `DELETE FROM office_event_delivery WHERE bot_id=$1`, uuidToString(f.bot.ID))
	return f
}

func (f *officeFixture) call(t *testing.T, method, path string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, f.api.URL+"/api/workspaces/"+testWorkspaceID+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+f.token)
	res, err := f.api.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	out := map[string]any{}
	_ = json.Unmarshal(data, &out)
	return res.StatusCode, out
}

func (f *officeFixture) seal(t *testing.T) {
	t.Helper()
	for {
		n, err := f.worker.Seal(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if n < officeSealBatch {
			return
		}
	}
}

func (f *officeFixture) botPath() string { return "/bots/" + uuidToString(f.bot.ID) }

// asActor runs one write as a given actor, the way the server pool stamps it.
func asActor(t *testing.T, a dbactor.Actor, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	raw, _ := json.Marshal(a)
	if _, err := tx.Exec(ctx, `SELECT set_config('multica.actor', $1, true)`, string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

type officeRow struct {
	eventID, actorType, actorID, meaning, summary, businessKey string
	mustRelay, pendingApproval                                 bool
	seq                                                        *int64
}

func latestOfficeEvent(t *testing.T, where string, args ...any) officeRow {
	t.Helper()
	var r officeRow
	dbfx.QueryRow(t, `SELECT event_id::text, actor_type, COALESCE(actor_id::text,''), meaning, summary, business_key,
		must_relay, pending_approval, stream_seq FROM office_event WHERE `+where+` ORDER BY id DESC LIMIT 1`, args...).
		Scan(&r.eventID, &r.actorType, &r.actorID, &r.meaning, &r.summary, &r.businessKey, &r.mustRelay, &r.pendingApproval, &r.seq)
	return r
}

func deliveryStatus(t *testing.T, eventID, bot string) (status, reason, blocked string, attempts int) {
	t.Helper()
	dbfx.QueryRow(t, `SELECT status, reason, blocked_reason, attempts FROM office_event_delivery WHERE event_id=$1 AND bot_id=$2`, eventID, bot).
		Scan(&status, &reason, &blocked, &attempts)
	return
}

func ownedIssue(t *testing.T, title string) string {
	return dbfx.Insert(t, "issue", testutil.Cols{
		"workspace_id": testWorkspaceID, "title": title, "status": "in_progress", "priority": "none",
		"creator_type": "member", "creator_id": testUserID, "number": nextTestIssueNumber(t),
	})
}

func TestOfficeEventHandoffIsCapturedPushedAndNeedsARealReceipt(t *testing.T) {
	f := newOfficeFixture(t)
	peer := summonSecondMember(t)
	issue := ownedIssue(t, "Write the quarterly report")

	asActor(t, dbactor.Actor{Type: "member", ID: peer}, `UPDATE issue SET status='in_review', updated_at=now() WHERE id=$1`, issue)
	ev := latestOfficeEvent(t, `issue_id=$1 AND event_type='issue.status_changed'`, issue)
	if ev.actorType != "member" || ev.actorID != peer {
		t.Fatalf("actor = %s/%s, want member/%s", ev.actorType, ev.actorID, peer)
	}
	// The same fact carries the same key on the older activity lane.
	var updated time.Time
	dbfx.QueryRow(t, `SELECT updated_at FROM issue WHERE id=$1`, issue).Scan(&updated)
	if want := service.OfficeIssueStatusKey(issue, "in_progress", "in_review", updated); ev.businessKey != want {
		t.Fatalf("business key %q, activity lane would say %q", ev.businessKey, want)
	}

	f.seal(t)
	ev = latestOfficeEvent(t, `event_id=$1`, ev.eventID)
	if ev.seq == nil || ev.meaning != service.OfficeMeaningHandoff || !ev.mustRelay {
		t.Fatalf("sealed as %+v", ev)
	}
	status, reason, _, _ := deliveryStatus(t, ev.eventID, uuidToString(f.bot.ID))
	if status != "pending" || reason != "owner" {
		t.Fatalf("delivery %s/%s, want pending/owner", status, reason)
	}

	f.worker.DeliverDue(context.Background())
	if f.calls.Load() != 1 {
		t.Fatalf("webhook called %d times", f.calls.Load())
	}
	var pushed map[string]any
	_ = json.Unmarshal(f.lastBody.Load().([]byte), &pushed)
	if pushed["event_id"] != ev.eventID || pushed["type"] != "office_event" || pushed["must_relay"] != true {
		t.Fatalf("push body %v", pushed)
	}
	if d := pushed["delivery"].(map[string]any); d["redelivery"] != false || !strings.HasSuffix(d["receipt_url"].(string), "/office-events/"+ev.eventID+"/receipt") {
		t.Fatalf("delivery block %v", d)
	}
	// HTTP 200 is not a receipt.
	if status, _, _, _ := deliveryStatus(t, ev.eventID, uuidToString(f.bot.ID)); status != "sent" {
		t.Fatalf("after 200 the row is %s, want sent", status)
	}
	// No receipt in time: the same event goes again, marked as a repeat.
	dbfx.Exec(t, `UPDATE office_event_delivery SET next_attempt_at=now() WHERE event_id=$1`, ev.eventID)
	f.worker.DeliverDue(context.Background())
	_ = json.Unmarshal(f.lastBody.Load().([]byte), &pushed)
	if f.calls.Load() != 2 || pushed["event_id"] != ev.eventID || pushed["delivery"].(map[string]any)["redelivery"] != true {
		t.Fatalf("redelivery: calls=%d body=%v", f.calls.Load(), pushed)
	}

	receipt := f.botPath() + "/office-events/" + ev.eventID + "/receipt"
	if code, _ := f.call(t, "POST", receipt, map[string]any{"stage": "received"}); code != 200 {
		t.Fatalf("received receipt %d", code)
	}
	if code, out := f.call(t, "POST", receipt, map[string]any{"stage": "handled", "reason": "seen"}); code != http.StatusConflict {
		t.Fatalf("a must-relay result was closed as handled: %d %v", code, out)
	}
	if code, out := f.call(t, "POST", receipt, map[string]any{"stage": "relayed", "channel": "telegram", "message_ref": "tg:1"}); code != 200 || out["status"] != "relayed" {
		t.Fatalf("relayed receipt %d %v", code, out)
	}
	// Repeats change nothing.
	if code, out := f.call(t, "POST", receipt, map[string]any{"stage": "received"}); code != 200 || out["status"] != "relayed" {
		t.Fatalf("a late received receipt moved the row back: %d %v", code, out)
	}
	dbfx.Exec(t, `UPDATE office_event_delivery SET next_attempt_at=now() WHERE event_id=$1`, ev.eventID)
	f.worker.DeliverDue(context.Background())
	if f.calls.Load() != 2 {
		t.Fatal("a relayed result was sent again")
	}
}

func TestOfficeEventSkipsTheOwnersOwnActionButNotAnAgentsResult(t *testing.T) {
	f := newOfficeFixture(t)
	issue := ownedIssue(t, "Tidy the backlog")

	asActor(t, dbactor.Actor{Type: "member", ID: testUserID}, `UPDATE issue SET status='blocked' WHERE id=$1`, issue)
	own := latestOfficeEvent(t, `issue_id=$1`, issue)
	// An agent run the person started through their Bot: still news to them.
	agent := dbfx.Agent(t, "office agent", testRuntimeID)
	asActor(t, dbactor.Actor{Type: "agent", ID: agent, ViaBotID: uuidToString(f.bot.ID), OnBehalfOf: testUserID},
		`UPDATE issue SET status='in_review' WHERE id=$1`, issue)
	byAgent := latestOfficeEvent(t, `issue_id=$1`, issue)
	f.seal(t)

	if n := dbfx.Count(t, `SELECT count(*) FROM office_event_delivery WHERE event_id=$1`, own.eventID); n != 0 {
		t.Fatalf("the person's own change was queued to their Bot (%d)", n)
	}
	if status, reason, _, _ := deliveryStatus(t, byAgent.eventID, uuidToString(f.bot.ID)); status != "pending" || reason != "owner" {
		t.Fatalf("the agent's result was filtered out: %q/%q", status, reason)
	}
	var onBehalf string
	dbfx.QueryRow(t, `SELECT COALESCE(on_behalf_of_user_id::text,'') FROM office_event WHERE event_id=$1`, byAgent.eventID).Scan(&onBehalf)
	if onBehalf != testUserID {
		t.Fatalf("on_behalf_of = %q", onBehalf)
	}
}

func TestOfficeEventBlocksARevokedChannelAndRetryRecovers(t *testing.T) {
	f := newOfficeFixture(t)
	issue := ownedIssue(t, "Draft the notice")
	asActor(t, dbactor.Actor{Type: "member", ID: summonSecondMember(t)}, `UPDATE issue SET status='done' WHERE id=$1`, issue)
	ev := latestOfficeEvent(t, `issue_id=$1`, issue)
	f.seal(t)

	dbfx.Exec(t, `UPDATE member_bot_binding SET revoked_at=now() WHERE id=$1`, f.binding)
	f.worker.DeliverDue(context.Background())
	status, _, blocked, _ := deliveryStatus(t, ev.eventID, uuidToString(f.bot.ID))
	if status != "blocked" || blocked != officeBlockedChannel || f.calls.Load() != 0 {
		t.Fatalf("revoked channel: status=%s reason=%q calls=%d", status, blocked, f.calls.Load())
	}
	// Blocked shows on the Bot's own ledger, with why.
	code, out := f.call(t, "GET", f.botPath()+"/office-deliveries?status=blocked", nil)
	if code != 200 || len(out["deliveries"].([]any)) == 0 {
		t.Fatalf("ledger %d %v", code, out)
	}

	dbfx.Exec(t, `UPDATE member_bot_binding SET revoked_at=NULL WHERE id=$1`, f.binding)
	if code, out := f.call(t, "POST", f.botPath()+"/office-deliveries/retry", map[string]any{"event_id": ev.eventID}); code != 200 || out["requeued"].(float64) != 1 {
		t.Fatalf("retry %d %v", code, out)
	}
	f.worker.DeliverDue(context.Background())
	if status, _, _, _ := deliveryStatus(t, ev.eventID, uuidToString(f.bot.ID)); status != "sent" || f.calls.Load() != 1 {
		t.Fatalf("after retry: %s, calls=%d", status, f.calls.Load())
	}
}

func TestOfficeEventRoutingDoneOnReleaseWorkStillWaitsForApproval(t *testing.T) {
	f := newOfficeFixture(t)
	issue := ownedIssue(t, "正式发布 v2.0 到生产环境")
	asActor(t, dbactor.Actor{Type: "system", Source: "routing"}, `UPDATE issue SET status='done' WHERE id=$1`, issue)
	ev := latestOfficeEvent(t, `issue_id=$1`, issue)
	f.seal(t)
	ev = latestOfficeEvent(t, `event_id=$1`, ev.eventID)
	if !ev.pendingApproval || !ev.mustRelay || !strings.Contains(ev.summary, "正式发布仍待批准") {
		t.Fatalf("release auto-complete sealed as %+v", ev)
	}
	f.worker.DeliverDue(context.Background())
	var pushed map[string]any
	_ = json.Unmarshal(f.lastBody.Load().([]byte), &pushed)
	if pushed["pending_approval"] != true || pushed["bypass_semantic_dedupe"] != true || pushed["notice"] == nil {
		t.Fatalf("push %v", pushed)
	}

	// A reviewer agent's verdict closing it is not the release approval.
	reviewed := ownedIssue(t, "正式发布 v2.0.1")
	asActor(t, dbactor.Actor{Type: "agent", ID: dbfx.Agent(t, "reviewer", testRuntimeID)}, `UPDATE issue SET status='done' WHERE id=$1`, reviewed)
	f.seal(t)
	if ev := latestOfficeEvent(t, `issue_id=$1`, reviewed); !ev.pendingApproval || !ev.mustRelay || ev.meaning != service.OfficeMeaningPendingRel {
		t.Fatalf("an agent's close of release work sealed as %+v", ev)
	}

	// A person marking the same kind of ticket done is a plain completion.
	other := ownedIssue(t, "正式发布 v2.1")
	asActor(t, dbactor.Actor{Type: "member", ID: summonSecondMember(t)}, `UPDATE issue SET status='done' WHERE id=$1`, other)
	f.seal(t)
	if ev := latestOfficeEvent(t, `issue_id=$1`, other); ev.pendingApproval || ev.meaning != service.OfficeMeaningTaskDone {
		t.Fatalf("a person's done sealed as %+v", ev)
	}
}

func TestOfficeEventConfigChangesAreMaskedAndOnlyForAdmins(t *testing.T) {
	f := newOfficeFixture(t)
	setActivityScope(t, f.humanNeededFixture, "owner_visible_all")
	agent := dbfx.Agent(t, "config agent", testRuntimeID)
	peer := summonSecondMember(t)
	asActor(t, dbactor.Actor{Type: "member", ID: peer},
		`UPDATE agent SET custom_env='{"OPENAI_API_KEY":"sk-live-123"}', instructions='be brief' WHERE id=$1`, agent)
	ev := latestOfficeEvent(t, `event_type='config.agent.update' AND object_id=$1`, agent)
	var changes string
	dbfx.QueryRow(t, `SELECT changes::text FROM office_event WHERE event_id=$1`, ev.eventID).Scan(&changes)
	if strings.Contains(changes, "sk-live-123") || !strings.Contains(changes, "instructions") || !strings.Contains(changes, "be brief") {
		t.Fatalf("changes %s", changes)
	}
	f.seal(t)
	if status, reason, _, _ := deliveryStatus(t, ev.eventID, uuidToString(f.bot.ID)); status != "pending" || reason != "stream" {
		t.Fatalf("an admin streaming everything should get it: %q/%q", status, reason)
	}

	// Pull: the admin reads it; a plain member does not.
	code, out := f.call(t, "GET", "/office-events/"+ev.eventID, nil)
	if code != 200 || out["family"] != "config" {
		t.Fatalf("get %d %v", code, out)
	}
	setMemberRole(t, "member")
	if code, _ := f.call(t, "GET", "/office-events/"+ev.eventID, nil); code != 404 {
		t.Fatalf("a plain member read a config change: %d", code)
	}
	// And the queued push is refused at send time.
	f.worker.DeliverDue(context.Background())
	if _, _, blocked, _ := deliveryStatus(t, ev.eventID, uuidToString(f.bot.ID)); blocked != officeBlockedAccess {
		t.Fatalf("send-time check: %q", blocked)
	}
}

func TestOfficeEventPullCarriesTheFileCatalogue(t *testing.T) {
	f := newOfficeFixture(t)
	issue := ownedIssue(t, "Produce the chart")
	comment := dbfx.Comment(t, issue, "here it is")
	agent := dbfx.Agent(t, "artifact agent", testRuntimeID)
	sha := strings.Repeat("b", 64)
	att := dbfx.Insert(t, "attachment", testutil.Cols{"workspace_id": testWorkspaceID, "issue_id": issue, "comment_id": comment,
		"uploader_type": "agent", "uploader_id": agent, "filename": "chart.png", "url": "fixture/chart.png",
		"content_type": "image/png", "size_bytes": 42, "sha256": sha})
	f.seal(t)
	ev := latestOfficeEvent(t, `attachment_id=$1`, att)
	if ev.meaning != service.OfficeMeaningArtifact || !ev.mustRelay {
		t.Fatalf("artifact sealed as %+v", ev)
	}

	var seq int64
	dbfx.QueryRow(t, `SELECT stream_seq - 1 FROM office_event WHERE event_id=$1`, ev.eventID).Scan(&seq)
	code, out := f.call(t, "GET", "/office-events?issue_id="+issue+"&after="+strconv.FormatInt(seq, 10), nil)
	if code != 200 {
		t.Fatalf("pull %d %v", code, out)
	}
	events := out["events"].([]any)
	if len(events) == 0 {
		t.Fatalf("pull returned nothing: %v", out)
	}
	got := events[0].(map[string]any)
	files := got["artifacts"].([]any)
	if got["event_id"] != ev.eventID || len(files) != 1 {
		t.Fatalf("pull %v", got)
	}
	file := files[0].(map[string]any)
	if file["id"] != att || file["sha256"] != sha || file["status"] != "ready" ||
		!strings.HasSuffix(file["download_url"].(string), "/api/attachments/"+att+"/download?raw=1") {
		t.Fatalf("file %v", file)
	}
	// Push carries the same catalogue.
	f.worker.DeliverDue(context.Background())
	var pushed map[string]any
	_ = json.Unmarshal(f.lastBody.Load().([]byte), &pushed)
	if pf := pushed["artifacts"].([]any); len(pf) != 1 || pf[0].(map[string]any)["sha256"] != sha {
		t.Fatalf("push files %v", pushed["artifacts"])
	}
	// A file that is gone says so.
	dbfx.Exec(t, `DELETE FROM attachment WHERE id=$1`, att)
	_, out = f.call(t, "GET", "/office-events/"+ev.eventID, nil)
	if pf := out["artifacts"].([]any); len(pf) != 1 || pf[0].(map[string]any)["status"] != "deleted" {
		t.Fatalf("deleted file %v", out["artifacts"])
	}
}

func TestOfficeEventUnrelayedResultFallsBackToTheInboxOnce(t *testing.T) {
	f := newOfficeFixture(t)
	issue := ownedIssue(t, "Fallback me")
	asActor(t, dbactor.Actor{Type: "member", ID: summonSecondMember(t)}, `UPDATE issue SET status='in_review' WHERE id=$1`, issue)
	ev := latestOfficeEvent(t, `issue_id=$1`, issue)
	f.seal(t)
	f.worker.DeliverDue(context.Background())
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE details->>'office_event_id'=$1`, ev.eventID)
	})

	f.worker.Fallback(context.Background())
	count := func() int {
		return dbfx.Count(t, `SELECT count(*) FROM inbox_item WHERE recipient_id=$1 AND details->>'office_event_id'=$2`, testUserID, ev.eventID)
	}
	if count() != 0 {
		t.Fatal("fell back before the grace period")
	}
	dbfx.Exec(t, `UPDATE office_event_delivery SET created_at=now()-interval '1 hour' WHERE event_id=$1`, ev.eventID)
	f.worker.Fallback(context.Background())
	f.worker.Fallback(context.Background())
	if count() != 1 {
		t.Fatalf("fallback notices = %d, want 1", count())
	}
}

// The pool hook is what makes triggers see the right person, and never a
// stale one left by the previous request on the same connection.
func TestOfficeEventPoolStampsTheActorPerRequest(t *testing.T) {
	if testPool == nil {
		t.Skip("requires test database")
	}
	cfg := testPool.Config().Copy()
	cfg.MaxConns = 1
	cfg.PrepareConn = dbactor.PrepareConn
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	issue := ownedIssue(t, "Stamp me")
	peer := summonSecondMember(t)

	ctx := dbactor.With(context.Background(), dbactor.Actor{Type: "member", ID: peer})
	if _, err := pool.Exec(ctx, `UPDATE issue SET status='blocked' WHERE id=$1`, issue); err != nil {
		t.Fatal(err)
	}
	if ev := latestOfficeEvent(t, `issue_id=$1`, issue); ev.actorType != "member" || ev.actorID != peer {
		t.Fatalf("first write %+v", ev)
	}
	// Same connection, no actor on the context: a system write, not the peer.
	if _, err := pool.Exec(context.Background(), `UPDATE issue SET status='todo' WHERE id=$1`, issue); err != nil {
		t.Fatal(err)
	}
	if ev := latestOfficeEvent(t, `issue_id=$1`, issue); ev.actorType != "system" || ev.actorID != "" {
		t.Fatalf("second write blamed on %s/%s", ev.actorType, ev.actorID)
	}
}

// Review hold P1-1: an owner whose Bot is connected but not verified is a
// recorded gap (blocked row, inbox fallback, retry once verified), and a
// must-relay result nobody can be reached for says so on the event.
func TestOfficeEventUnverifiedOwnerBotIsARecordedGapNotALoss(t *testing.T) {
	f := newOfficeFixture(t)
	dbfx.Exec(t, `UPDATE member_bot_binding SET verified_at=NULL WHERE id=$1`, f.binding)
	issue := ownedIssue(t, "Ship the summary")
	agent := dbfx.Agent(t, "gap agent", testRuntimeID)
	asActor(t, dbactor.Actor{Type: "agent", ID: agent}, `UPDATE issue SET status='in_review' WHERE id=$1`, issue)
	ev := latestOfficeEvent(t, `issue_id=$1 AND event_type='issue.status_changed'`, issue)
	f.seal(t)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE details->>'office_event_id'=$1`, ev.eventID)
	})

	status, reason, blocked, _ := deliveryStatus(t, ev.eventID, uuidToString(f.bot.ID))
	if status != "blocked" || reason != "owner" || blocked != officeBlockedChannel {
		t.Fatalf("unverified owner Bot: %q/%q/%q, want a blocked owner row", status, reason, blocked)
	}
	f.worker.Fallback(context.Background())
	if n := dbfx.Count(t, `SELECT count(*) FROM inbox_item WHERE recipient_id=$1 AND details->>'office_event_id'=$2`, testUserID, ev.eventID); n != 1 {
		t.Fatalf("inbox fallback for the gap = %d, want 1", n)
	}

	dbfx.Exec(t, `UPDATE member_bot_binding SET verified_at=now() WHERE id=$1`, f.binding)
	if code, out := f.call(t, "POST", f.botPath()+"/office-deliveries/retry", map[string]any{"event_id": ev.eventID}); code != 200 || out["requeued"].(float64) != 1 {
		t.Fatalf("retry %d %v", code, out)
	}
	f.worker.DeliverDue(context.Background())
	if status, _, _, _ := deliveryStatus(t, ev.eventID, uuidToString(f.bot.ID)); status != "sent" || f.calls.Load() != 1 {
		t.Fatalf("after verifying: %s, calls=%d", status, f.calls.Load())
	}
}

func TestOfficeEventMustRelayWithNobodyToReachRecordsTheGap(t *testing.T) {
	f := newOfficeFixture(t)
	agent := dbfx.Agent(t, "orphan agent", testRuntimeID)
	orphan := dbfx.Insert(t, "issue", testutil.Cols{
		"workspace_id": testWorkspaceID, "title": "Nobody's ticket", "status": "in_progress", "priority": "none",
		"creator_type": "agent", "creator_id": agent, "number": nextTestIssueNumber(t),
	})
	peer := summonSecondMember(t)
	peers := dbfx.Insert(t, "issue", testutil.Cols{
		"workspace_id": testWorkspaceID, "title": "Peer without a Bot", "status": "in_progress", "priority": "none",
		"creator_type": "member", "creator_id": peer, "number": nextTestIssueNumber(t),
	})
	asActor(t, dbactor.Actor{Type: "agent", ID: agent}, `UPDATE issue SET status='done' WHERE id = ANY($1::uuid[])`, []string{orphan, peers})
	f.seal(t)
	gap := func(issue string) string {
		var g string
		dbfx.QueryRow(t, `SELECT recipient_gap FROM office_event WHERE issue_id=$1 AND event_type='issue.status_changed'`, issue).Scan(&g)
		return g
	}
	if g := gap(orphan); g != officeGapNoOwner {
		t.Fatalf("ownerless gap = %q", g)
	}
	if g := gap(peers); g != officeGapNoBot {
		t.Fatalf("owner without a Bot gap = %q", g)
	}
	ev := latestOfficeEvent(t, `issue_id=$1 AND event_type='issue.status_changed'`, orphan)
	code, out := f.call(t, "GET", "/office-events/"+ev.eventID, nil)
	if out["recipient_gap"] != officeGapNoOwner {
		t.Fatalf("pull does not show the gap: %d %v", code, out)
	}
}

// Review hold P1-2: "relayed" needs proof of where, files must belong to the
// event, and a late receipt never takes back what was confirmed.
func TestOfficeEventReceiptsNeedProofAndNeverGoBack(t *testing.T) {
	f := newOfficeFixture(t)
	issue := ownedIssue(t, "Deliver the deck")
	agent := dbfx.Agent(t, "deck agent", testRuntimeID)
	att := dbfx.Insert(t, "attachment", testutil.Cols{"workspace_id": testWorkspaceID, "issue_id": issue,
		"uploader_type": "agent", "uploader_id": agent, "filename": "deck.pdf", "url": "fixture/deck.pdf",
		"content_type": "application/pdf", "size_bytes": 7, "sha256": strings.Repeat("c", 64)})
	other := ownedIssue(t, "Unrelated")
	stranger := dbfx.Insert(t, "attachment", testutil.Cols{"workspace_id": testWorkspaceID, "issue_id": other,
		"uploader_type": "member", "uploader_id": testUserID, "filename": "x.png", "url": "fixture/x.png",
		"content_type": "image/png", "size_bytes": 1})
	f.seal(t)
	ev := latestOfficeEvent(t, `attachment_id=$1`, att)
	receipt := f.botPath() + "/office-events/" + ev.eventID + "/receipt"

	for name, body := range map[string]map[string]any{
		"relayed without anything": {"stage": "relayed"},
		"relayed without ref":      {"stage": "relayed", "channel": "telegram"},
		"relay_failed without why": {"stage": "relay_failed"},
		"file of another event": {"stage": "received", "artifacts": []map[string]any{
			{"id": stranger, "status": "sent", "ref": "tg:9"}}},
		"sent file without ref": {"stage": "received", "artifacts": []map[string]any{{"id": att, "status": "sent"}}},
		"unknown file status":   {"stage": "received", "artifacts": []map[string]any{{"id": att, "status": "done"}}},
	} {
		if code, out := f.call(t, "POST", receipt, body); code != http.StatusBadRequest {
			t.Fatalf("%s: %d %v, want 400", name, code, out)
		}
	}
	if status, _, _, _ := deliveryStatus(t, ev.eventID, uuidToString(f.bot.ID)); status != "pending" {
		t.Fatalf("a rejected receipt moved the row to %s", status)
	}

	code, out := f.call(t, "POST", receipt, map[string]any{"stage": "relayed", "channel": "telegram", "message_ref": "tg:1",
		"artifacts": []map[string]any{{"id": att, "status": "sent", "ref": "tg:2"}}})
	if code != 200 || out["status"] != "relayed" {
		t.Fatalf("relayed with proof %d %v", code, out)
	}
	// Late, contradicting receipts change nothing already confirmed.
	code, out = f.call(t, "POST", receipt, map[string]any{"stage": "relay_failed", "error": "late",
		"artifacts": []map[string]any{{"id": att, "status": "failed", "error": "late"}}})
	if code != 200 || out["status"] != "relayed" || out["relay_error"] != "" {
		t.Fatalf("late relay_failed moved a relayed row: %d %v", code, out)
	}
	if file := out["artifacts"].(map[string]any)[att].(map[string]any); file["status"] != "sent" || file["ref"] != "tg:2" {
		t.Fatalf("late file receipt took back a sent file: %v", file)
	}
}

// Review hold P1-3: an answer lost after the request went out is not resent
// at once; the next push asks the Bot to reconcile from its inbox.
func TestOfficeEventUncertainPushWaitsAndThenAsksToReconcile(t *testing.T) {
	f := newOfficeFixture(t)
	issue := ownedIssue(t, "Lost answer")
	asActor(t, dbactor.Actor{Type: "member", ID: summonSecondMember(t)}, `UPDATE issue SET status='in_review' WHERE id=$1`, issue)
	ev := latestOfficeEvent(t, `issue_id=$1`, issue)
	f.seal(t)

	f.response.Store(500)
	f.worker.DeliverDue(context.Background())
	var uncertain bool
	var next time.Time
	dbfx.QueryRow(t, `SELECT uncertain_at IS NOT NULL, next_attempt_at FROM office_event_delivery WHERE event_id=$1`, ev.eventID).Scan(&uncertain, &next)
	if status, _, _, _ := deliveryStatus(t, ev.eventID, uuidToString(f.bot.ID)); status != "sent" || !uncertain {
		t.Fatalf("after a lost answer: %s uncertain=%v, want sent + uncertain", status, uncertain)
	}
	if time.Until(next) < time.Minute {
		t.Fatalf("an uncertain push is retried at once (next in %s)", time.Until(next))
	}
	f.worker.DeliverDue(context.Background())
	if f.calls.Load() != 1 {
		t.Fatalf("resent blindly: %d calls", f.calls.Load())
	}

	f.response.Store(200)
	dbfx.Exec(t, `UPDATE office_event_delivery SET next_attempt_at=now() WHERE event_id=$1`, ev.eventID)
	f.worker.DeliverDue(context.Background())
	var pushed map[string]any
	_ = json.Unmarshal(f.lastBody.Load().([]byte), &pushed)
	d := pushed["delivery"].(map[string]any)
	if pushed["event_id"] != ev.eventID || d["reconcile"] != true || d["redelivery"] != true {
		t.Fatalf("follow-up push %v", d)
	}
}

// Review hold P1-4: files that land straight on a ticket (most agent
// deliverables), files bound later, and new versions under the same name are
// each one event; a finished ticket carries its files.
func TestOfficeEventTicketFilesLateBindingAndNewVersions(t *testing.T) {
	f := newOfficeFixture(t)
	issue := ownedIssue(t, "Release notes")
	agent := dbfx.Agent(t, "notes agent", testRuntimeID)
	file := func(cols testutil.Cols) string {
		base := testutil.Cols{"workspace_id": testWorkspaceID, "uploader_type": "agent", "uploader_id": agent,
			"filename": "notes.html", "url": "fixture/notes.html", "content_type": "text/html", "size_bytes": 5,
			"sha256": strings.Repeat("d", 64)}
		for k, v := range cols {
			base[k] = v
		}
		return dbfx.Insert(t, "attachment", base)
	}
	events := func(att string) int {
		return dbfx.Count(t, `SELECT count(*) FROM office_event WHERE attachment_id=$1 AND event_type='artifact.attached'`, att)
	}

	direct := file(testutil.Cols{"issue_id": issue})
	if events(direct) != 1 {
		t.Fatalf("a file uploaded straight to the ticket made %d events", events(direct))
	}
	late := file(testutil.Cols{})
	if events(late) != 0 {
		t.Fatal("a file with no place yet made an event")
	}
	dbfx.Exec(t, `UPDATE attachment SET issue_id=$1 WHERE id=$2`, issue, late)
	if events(late) != 1 {
		t.Fatalf("binding a file to the ticket later made %d events", events(late))
	}
	comment := dbfx.Comment(t, issue, "attached")
	dbfx.Exec(t, `UPDATE attachment SET comment_id=$1 WHERE id=$2`, comment, late)
	if events(late) != 1 {
		t.Fatalf("moving an announced file into a comment announced it again (%d)", events(late))
	}
	again := file(testutil.Cols{"issue_id": issue})
	if events(again) != 1 {
		t.Fatal("a new version under the same name made no event")
	}
	f.seal(t)
	ev := latestOfficeEvent(t, `attachment_id=$1`, direct)
	if ev.meaning != service.OfficeMeaningArtifact || !ev.mustRelay {
		t.Fatalf("ticket file sealed as %+v", ev)
	}

	asActor(t, dbactor.Actor{Type: "agent", ID: agent}, `UPDATE issue SET status='done' WHERE id=$1`, issue)
	f.seal(t)
	done := latestOfficeEvent(t, `issue_id=$1 AND event_type='issue.status_changed'`, issue)
	_, out := f.call(t, "GET", "/office-events/"+done.eventID, nil)
	got := map[string]bool{}
	for _, a := range out["artifacts"].([]any) {
		got[a.(map[string]any)["id"].(string)] = true
	}
	if !got[direct] || !got[late] || !got[again] {
		t.Fatalf("finished ticket catalogue %v misses its files", out["artifacts"])
	}
}

// Second review hold P1-1: a claim a crash left in "sending", and a resend
// after a 200 whose receipt never came, may both already be in the Bot's
// inbox: each goes out again marked reconcile.
func TestOfficeEventCrashAndLostReceiptBothResendAsReconcile(t *testing.T) {
	f := newOfficeFixture(t)
	push := func() map[string]any {
		t.Helper()
		var pushed map[string]any
		_ = json.Unmarshal(f.lastBody.Load().([]byte), &pushed)
		return pushed["delivery"].(map[string]any)
	}

	// Server died between claiming the row and recording the answer.
	crashed := ownedIssue(t, "Crash mid-push")
	asActor(t, dbactor.Actor{Type: "member", ID: summonSecondMember(t)}, `UPDATE issue SET status='in_review' WHERE id=$1`, crashed)
	ev := latestOfficeEvent(t, `issue_id=$1`, crashed)
	f.seal(t)
	dbfx.Exec(t, `UPDATE office_event_delivery SET status='sending', updated_at=now()-interval '5 minutes' WHERE event_id=$1`, ev.eventID)
	f.worker.Sweep(context.Background())
	var uncertain, sent bool
	dbfx.QueryRow(t, `SELECT uncertain_at IS NOT NULL, sent_at IS NOT NULL FROM office_event_delivery WHERE event_id=$1`, ev.eventID).Scan(&uncertain, &sent)
	if status, _, _, _ := deliveryStatus(t, ev.eventID, uuidToString(f.bot.ID)); status != "sent" || !uncertain || !sent {
		t.Fatalf("after a crash: %s uncertain=%v sent=%v, want sent + uncertain", status, uncertain, sent)
	}
	f.worker.DeliverDue(context.Background())
	if d := push(); d["reconcile"] != true || d["redelivery"] != true {
		t.Fatalf("push after a crash %v", d)
	}

	// 200 came back, the Bot's receipt did not.
	lost := ownedIssue(t, "Receipt lost")
	asActor(t, dbactor.Actor{Type: "member", ID: summonSecondMember(t)}, `UPDATE issue SET status='in_review' WHERE id=$1`, lost)
	ev = latestOfficeEvent(t, `issue_id=$1`, lost)
	f.seal(t)
	f.worker.DeliverDue(context.Background())
	if d := push(); d["reconcile"] != false {
		t.Fatalf("first push already says reconcile: %v", d)
	}
	dbfx.Exec(t, `UPDATE office_event_delivery SET next_attempt_at=now() WHERE event_id=$1`, ev.eventID)
	f.worker.DeliverDue(context.Background())
	if d := push(); d["reconcile"] != true || d["redelivery"] != true {
		t.Fatalf("resend after a 200 %v", d)
	}
}

// Second review hold P1-2: file receipts merge atomically; a sent file is
// never taken back, whatever order receipts land in, and one receipt cannot
// name the same file twice.
func TestOfficeEventFileReceiptsMergeAtomically(t *testing.T) {
	f := newOfficeFixture(t)
	issue := ownedIssue(t, "Two files")
	agent := dbfx.Agent(t, "files agent", testRuntimeID)
	att := dbfx.Insert(t, "attachment", testutil.Cols{"workspace_id": testWorkspaceID, "issue_id": issue,
		"uploader_type": "agent", "uploader_id": agent, "filename": "a.pdf", "url": "fixture/a.pdf",
		"content_type": "application/pdf", "size_bytes": 3, "sha256": strings.Repeat("e", 64)})
	f.seal(t)
	ev := latestOfficeEvent(t, `attachment_id=$1`, att)
	receipt := f.botPath() + "/office-events/" + ev.eventID + "/receipt"

	if code, out := f.call(t, "POST", receipt, map[string]any{"stage": "received", "artifacts": []map[string]any{
		{"id": att, "status": "failed", "error": "x"}, {"id": att, "status": "sent", "ref": "tg:1"}}}); code != http.StatusBadRequest {
		t.Fatalf("duplicate file id: %d %v, want 400", code, out)
	}
	// Two receipts race: one says sent, a stale one says failed. Whatever
	// the order, sent wins.
	bodies := []map[string]any{
		{"stage": "received", "artifacts": []map[string]any{{"id": att, "status": "sent", "ref": "tg:2"}}},
		{"stage": "received", "artifacts": []map[string]any{{"id": att, "status": "failed", "error": "late"}}},
	}
	for round := 0; round < 5; round++ {
		dbfx.Exec(t, `UPDATE office_event_delivery SET artifacts='{}' WHERE event_id=$1`, ev.eventID)
		done := make(chan int, len(bodies))
		for _, b := range bodies {
			go func(b map[string]any) {
				code, _ := f.call(t, "POST", receipt, b)
				done <- code
			}(b)
		}
		for range bodies {
			if code := <-done; code != 200 {
				t.Fatalf("receipt %d", code)
			}
		}
		var status string
		dbfx.QueryRow(t, `SELECT artifacts->$2->>'status' FROM office_event_delivery WHERE event_id=$1`, ev.eventID, att).Scan(&status)
		if status != "sent" {
			t.Fatalf("round %d: file %s, want sent", round, status)
		}
	}
}

// Second review hold P1-3: an owner whose Bot was disconnected when the
// result came is a gap of their own (another owner's Bot does not hide it),
// gets one in-app reminder, and gets the result on their Bot once they
// reconnect, also when they connect a different Bot.
func TestOfficeEventDisconnectedOwnerIsRemindedAndCaughtUpOnReconnect(t *testing.T) {
	f := newOfficeFixture(t)
	dbfx.Exec(t, `UPDATE office_event_gap SET resolved_at=now() WHERE resolved_at IS NULL`)
	dbfx.Exec(t, `UPDATE member_bot_binding SET revoked_at=now() WHERE id=$1`, f.binding)
	peer := summonSecondMember(t)
	agent := dbfx.Agent(t, "reconnect agent", testRuntimeID)
	issue := dbfx.Insert(t, "issue", testutil.Cols{
		"workspace_id": testWorkspaceID, "title": "Hand it over", "status": "in_progress", "priority": "none",
		"creator_type": "member", "creator_id": testUserID, "assignee_type": "member", "assignee_id": peer,
		"number": nextTestIssueNumber(t),
	})
	asActor(t, dbactor.Actor{Type: "agent", ID: agent}, `UPDATE issue SET status='in_review' WHERE id=$1`, issue)
	ev := latestOfficeEvent(t, `issue_id=$1 AND event_type='issue.status_changed'`, issue)
	f.seal(t)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE details->>'office_event_id'=$1`, ev.eventID)
	})

	gaps := func() map[string]bool {
		out := map[string]bool{}
		rows, err := testPool.Query(context.Background(), `SELECT user_id::text, resolved_at IS NOT NULL FROM office_event_gap WHERE event_id=$1`, ev.eventID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var u string
			var resolved bool
			_ = rows.Scan(&u, &resolved)
			out[u] = resolved
		}
		return out
	}
	if g := gaps(); len(g) != 2 || g[testUserID] || g[peer] {
		t.Fatalf("gaps %v, want both owners open", g)
	}
	var gap string
	dbfx.QueryRow(t, `SELECT recipient_gap FROM office_event WHERE event_id=$1`, ev.eventID).Scan(&gap)
	if gap != officeGapNoBot {
		t.Fatalf("recipient_gap %q", gap)
	}

	inbox := func(user string) int {
		return dbfx.Count(t, `SELECT count(*) FROM inbox_item WHERE recipient_id=$1 AND details->>'office_event_id'=$2`, user, ev.eventID)
	}
	f.worker.Fallback(context.Background())
	f.worker.Fallback(context.Background())
	if inbox(testUserID) != 1 || inbox(peer) != 0 {
		t.Fatalf("reminders: owner with a past Bot %d (want 1), never-connected peer %d (want 0)", inbox(testUserID), inbox(peer))
	}

	f.worker.Restore(context.Background())
	if g := gaps(); g[testUserID] {
		t.Fatal("restored while still disconnected")
	}
	dbfx.Exec(t, `UPDATE member_bot_binding SET revoked_at=NULL WHERE id=$1`, f.binding)
	f.worker.Restore(context.Background())
	if g := gaps(); !g[testUserID] || g[peer] {
		t.Fatalf("after reconnect gaps %v, want owner resolved, peer open", g)
	}
	f.worker.DeliverDue(context.Background())
	status, reason, _, _ := deliveryStatus(t, ev.eventID, uuidToString(f.bot.ID))
	if status != "sent" || reason != "owner" {
		t.Fatalf("caught-up delivery %s/%s", status, reason)
	}

	// A row blocked because the connection went away moves to a new Bot.
	later := ownedIssue(t, "After the switch")
	asActor(t, dbactor.Actor{Type: "agent", ID: agent}, `UPDATE issue SET status='in_review' WHERE id=$1`, later)
	ev2 := latestOfficeEvent(t, `issue_id=$1 AND event_type='issue.status_changed'`, later)
	f.seal(t)
	dbfx.Exec(t, `UPDATE member_bot_binding SET revoked_at=now() WHERE id=$1`, f.binding)
	f.worker.DeliverDue(context.Background())
	if status, _, blocked, _ := deliveryStatus(t, ev2.eventID, uuidToString(f.bot.ID)); status != "blocked" || blocked != officeBlockedChannel {
		t.Fatalf("after disconnect %s %q", status, blocked)
	}
	_, hash, prefix, err := mintBotToken()
	if err != nil {
		t.Fatal(err)
	}
	bot2, err := testHandler.Queries.CreateBotAccessToken(context.Background(), db.CreateBotAccessTokenParams{UserID: parseUUID(testUserID),
		WorkspaceID: parseUUID(testWorkspaceID), Name: "second bot", TokenHash: hash, TokenPrefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM personal_access_token WHERE id=$1`, bot2.ID)
	})
	b2 := dbfx.Insert(t, "member_bot_binding", testutil.Cols{"workspace_id": testWorkspaceID, "user_id": testUserID, "bot_id": uuidToString(bot2.ID)})
	dbfx.Exec(t, `UPDATE member_bot_binding SET verified_at=now() WHERE id=$1`, b2)
	f.worker.Restore(context.Background())
	if status, reason, _, _ := deliveryStatus(t, ev2.eventID, uuidToString(bot2.ID)); status != "pending" || reason != "owner" {
		t.Fatalf("new Bot delivery %q/%q", status, reason)
	}
	var moved string
	var fallback bool
	dbfx.QueryRow(t, `SELECT blocked_reason, fallback_at IS NOT NULL FROM office_event_delivery WHERE event_id=$1 AND bot_id=$2`, ev2.eventID, uuidToString(f.bot.ID)).Scan(&moved, &fallback)
	if moved != officeMovedToNewBot || !fallback {
		t.Fatalf("old row %q fallback=%v", moved, fallback)
	}
}

// Third review hold: a result is owed until it is delivered or no longer
// visible, however long the person stayed disconnected. A month-old gap and a
// month-old blocked row both catch up on reconnect (also to a new Bot); one
// the person can no longer see is closed without sending.
func TestOfficeEventCatchUpHasNoAgeLimitButStillChecksAccess(t *testing.T) {
	f := newOfficeFixture(t)
	dbfx.Exec(t, `UPDATE office_event_gap SET resolved_at=now() WHERE resolved_at IS NULL`)
	agent := dbfx.Agent(t, "old results agent", testRuntimeID)
	result := func(title string) (string, string) {
		issue := ownedIssue(t, title)
		asActor(t, dbactor.Actor{Type: "agent", ID: agent}, `UPDATE issue SET status='in_review' WHERE id=$1`, issue)
		return issue, latestOfficeEvent(t, `issue_id=$1 AND event_type='issue.status_changed'`, issue).eventID
	}

	// Blocked while connected, a month ago.
	_, blockedEv := result("Blocked long ago")
	f.seal(t)
	dbfx.Exec(t, `UPDATE member_bot_binding SET revoked_at=now() WHERE id=$1`, f.binding)
	f.worker.DeliverDue(context.Background())
	// Produced while disconnected, a month ago; and one whose ticket is gone.
	_, gapEv := result("Missed long ago")
	goneIssue, goneEv := result("Ticket deleted since")
	f.seal(t)
	month := `now() - interval '30 days'`
	dbfx.Exec(t, `UPDATE office_event_delivery SET created_at=`+month+` WHERE event_id=$1`, blockedEv)
	dbfx.Exec(t, `UPDATE office_event_gap SET created_at=`+month+` WHERE event_id = ANY($1::uuid[])`, []string{gapEv, goneEv})
	dbfx.Exec(t, `DELETE FROM issue WHERE id=$1`, goneIssue)

	_, hash, prefix, err := mintBotToken()
	if err != nil {
		t.Fatal(err)
	}
	bot2, err := testHandler.Queries.CreateBotAccessToken(context.Background(), db.CreateBotAccessTokenParams{UserID: parseUUID(testUserID),
		WorkspaceID: parseUUID(testWorkspaceID), Name: "bot after a month", TokenHash: hash, TokenPrefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM personal_access_token WHERE id=$1`, bot2.ID)
	})
	b2 := dbfx.Insert(t, "member_bot_binding", testutil.Cols{"workspace_id": testWorkspaceID, "user_id": testUserID, "bot_id": uuidToString(bot2.ID)})
	dbfx.Exec(t, `UPDATE member_bot_binding SET verified_at=now() WHERE id=$1`, b2)
	f.worker.Restore(context.Background())

	newBot := uuidToString(bot2.ID)
	for name, ev := range map[string]string{"month-old blocked row": blockedEv, "month-old gap": gapEv} {
		if status, reason, _, _ := deliveryStatus(t, ev, newBot); status != "pending" || reason != "owner" {
			t.Fatalf("%s on the new Bot: %q/%q, want a pending owner delivery", name, status, reason)
		}
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM office_event_delivery WHERE event_id=$1`, goneEv); n != 0 {
		t.Fatalf("result the person can no longer see was queued %d times", n)
	}
	var resolved bool
	dbfx.QueryRow(t, `SELECT resolved_at IS NOT NULL FROM office_event_gap WHERE event_id=$1`, goneEv).Scan(&resolved)
	if !resolved {
		t.Fatal("invisible gap left open: it would be rescanned forever")
	}
}

// An agent's sub-ticket belongs to whoever owns the ticket above it (DENE-1963
// drill DENE-1993): a must-relay result there reaches the parent's owner, a
// ticket with its own owner keeps it, a private sub-ticket the parent owner
// cannot open stays a gap, and a broken or looping chain ends in a gap.
func TestOfficeEventAgentSubTicketBelongsToTheParentOwner(t *testing.T) {
	f := newOfficeFixture(t)
	agent := dbfx.Agent(t, "split agent", testRuntimeID)
	child := func(title, parent, visibility string) string {
		cols := testutil.Cols{
			"workspace_id": testWorkspaceID, "title": title, "status": "in_progress", "priority": "none",
			"creator_type": "agent", "creator_id": agent, "number": nextTestIssueNumber(t), "visibility": visibility,
		}
		if parent != "" {
			cols["parent_issue_id"] = parent
		}
		return dbfx.Insert(t, "issue", cols)
	}
	parent := ownedIssue(t, "Ship the release")
	middle := child("Split the release", parent, "private")
	grandchild := child("Build the installer", middle, "private")

	peer := summonSecondMember(t)
	peerParent := dbfx.Insert(t, "issue", testutil.Cols{
		"workspace_id": testWorkspaceID, "title": "Peer's plan", "status": "in_progress", "priority": "none",
		"creator_type": "member", "creator_id": peer, "number": nextTestIssueNumber(t),
	})
	open := child("Open part", peerParent, "workspace")
	hidden := child("Hidden part", peerParent, "private")
	ownOwner := dbfx.Insert(t, "issue", testutil.Cols{
		"workspace_id": testWorkspaceID, "title": "Peer's own child", "status": "in_progress", "priority": "none",
		"creator_type": "member", "creator_id": peer, "number": nextTestIssueNumber(t), "parent_issue_id": parent,
	})
	// The nearest owner cannot open the sub-ticket; a higher owner who can
	// must not take it over.
	peerUnderOwner := dbfx.Insert(t, "issue", testutil.Cols{
		"workspace_id": testWorkspaceID, "title": "Peer's slice", "status": "in_progress", "priority": "none",
		"creator_type": "member", "creator_id": peer, "number": nextTestIssueNumber(t), "parent_issue_id": parent,
	})
	skipped := child("Skip attempt", peerUnderOwner, "private")
	// Two owners on the nearest ticket, one can open the sub-ticket.
	shared := dbfx.Insert(t, "issue", testutil.Cols{
		"workspace_id": testWorkspaceID, "title": "Shared plan", "status": "in_progress", "priority": "none",
		"creator_type": "member", "creator_id": peer, "assignee_type": "member", "assignee_id": testUserID,
		"number": nextTestIssueNumber(t),
	})
	partial := child("Partly visible", shared, "private")
	loopA := child("Loop A", "", "workspace")
	loopB := child("Loop B", loopA, "workspace")
	dbfx.Exec(t, `UPDATE issue SET parent_issue_id=$1 WHERE id=$2`, loopB, loopA)

	asActor(t, dbactor.Actor{Type: "agent", ID: agent}, `UPDATE issue SET status='done' WHERE id = ANY($1::uuid[])`,
		[]string{grandchild, open, hidden, ownOwner, loopA, skipped, partial})
	f.seal(t)

	done := func(issue string) (officeRow, string) {
		ev := latestOfficeEvent(t, `issue_id=$1 AND event_type='issue.status_changed'`, issue)
		var gap string
		dbfx.QueryRow(t, `SELECT recipient_gap FROM office_event WHERE event_id=$1`, ev.eventID).Scan(&gap)
		return ev, gap
	}
	bot := uuidToString(f.bot.ID)

	ev, gap := done(grandchild)
	if !ev.mustRelay || gap != "" {
		t.Fatalf("grandchild: must_relay=%v gap=%q", ev.mustRelay, gap)
	}
	if status, reason, _, _ := deliveryStatus(t, ev.eventID, bot); status != "pending" || reason != "owner" {
		t.Fatalf("the parent's owner was not reached two levels up: %q/%q", status, reason)
	}
	var mustRelay bool
	dbfx.QueryRow(t, `SELECT must_relay FROM office_event_delivery WHERE event_id=$1 AND bot_id=$2`, ev.eventID, bot).Scan(&mustRelay)
	if !mustRelay {
		t.Fatal("the owner delivery does not carry must_relay")
	}

	if _, gap := done(open); gap != officeGapNoBot {
		t.Fatalf("visible child of a Bot-less owner: gap=%q", gap)
	}
	ev, _ = done(open)
	if n := dbfx.Count(t, `SELECT count(*) FROM office_event_gap WHERE event_id=$1 AND user_id=$2`, ev.eventID, peer); n != 1 {
		t.Fatalf("the parent owner without a Bot has %d gap rows", n)
	}
	if _, gap := done(hidden); gap != officeGapNoOwner {
		t.Fatalf("a private child reached a parent owner who cannot open it: gap=%q", gap)
	}
	ev, gap = done(ownOwner)
	if gap != officeGapNoBot {
		t.Fatalf("a ticket with its own owner: gap=%q", gap)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM office_event_delivery WHERE event_id=$1 AND bot_id=$2 AND reason='owner'`, ev.eventID, bot); n != 0 {
		t.Fatal("the parent's owner took over a ticket that has its own owner")
	}
	ev, gap = done(skipped)
	if gap != officeGapNoOwner {
		t.Fatalf("a hidden nearest owner let a grandparent owner take over: gap=%q", gap)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM office_event_delivery WHERE event_id=$1 AND reason='owner'`, ev.eventID); n != 0 {
		t.Fatal("the grandparent's owner got a sub-ticket whose nearest owner cannot open it")
	}
	ev, gap = done(partial)
	if gap != "" {
		t.Fatalf("partly visible nearest owners: gap=%q", gap)
	}
	if status, reason, _, _ := deliveryStatus(t, ev.eventID, bot); status != "pending" || reason != "owner" {
		t.Fatalf("the owner who can open it was not reached: %q/%q", status, reason)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM office_event_gap WHERE event_id=$1 AND user_id=$2`, ev.eventID, peer); n != 0 {
		t.Fatal("the owner who cannot open it was counted as a recipient")
	}
	if _, gap := done(loopA); gap != officeGapNoOwner {
		t.Fatalf("looping chain: gap=%q", gap)
	}
}
