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
