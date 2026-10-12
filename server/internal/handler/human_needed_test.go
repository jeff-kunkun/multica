package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Real local sockets and disposable Postgres; NOT an actual Grok endpoint.
// humanNeededFixture is a person (the test user) with their own Bot, a local
// webhook receiver, and a verified binding.
type humanNeededFixture struct {
	bot      db.PersonalAccessToken
	token    string
	binding  string
	calls    atomic.Int32
	response atomic.Int32
	lastBody atomic.Value
	worker   *HumanNeededWorker
	api      *httptest.Server
}

func newHumanNeededFixture(t *testing.T, verified bool) *humanNeededFixture {
	t.Helper()
	ctx := context.Background()
	f := &humanNeededFixture{}
	f.response.Store(200)
	oldBox := testHandler.BotWakeSecrets
	testHandler.BotWakeSecrets, _ = NewBotWakeSecretBox("local-test-only")
	t.Cleanup(func() { testHandler.BotWakeSecrets = oldBox })
	raw, hash, prefix, err := mintBotToken()
	if err != nil {
		t.Fatal(err)
	}
	bot, err := testHandler.Queries.CreateBotAccessToken(ctx, db.CreateBotAccessTokenParams{UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), Name: "human needed fixture", TokenHash: hash, TokenPrefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(ctx, `DELETE FROM personal_access_token WHERE id=$1`, bot.ID) })
	f.bot, f.token = bot, raw

	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		f.lastBody.Store(body)
		w.WriteHeader(int(f.response.Load()))
	}))
	t.Cleanup(receiver.Close)
	t.Setenv("MULTICA_PLUGIN_DEV_ORIGINS", receiver.URL)

	router := chi.NewRouter()
	router.Use(middleware.Auth(testHandler.Queries, nil, nil, nil))
	router.Route("/api/workspaces/{id}", func(r chi.Router) {
		r.Use(middleware.RequireWorkspaceMemberFromURL(testHandler.Queries, "id"))
		r.Put("/bots/{botId}/wake", testHandler.PutBotWake)
		r.Post("/bots/{botId}/wake/events/{eventId}/woken", testHandler.RecordBotWakeReceipt)
		r.Post("/bots/{botId}/wake/events/{eventId}/relayed", testHandler.RelayHumanNeeded)
		r.Post("/bots/{botId}/wake/events/{eventId}/answered", testHandler.AnswerHumanNeeded)
	})
	f.api = httptest.NewServer(router)
	t.Cleanup(f.api.Close)
	code, data := f.call(t, "PUT", "/bots/"+uuidToString(bot.ID)+"/wake", map[string]any{
		"webhook_url": receiver.URL, "webhook_key": "Bearer local-fixture",
		"quiet_hours": map[string]any{"enabled": false, "start": "23:00", "end": "08:00", "timezone": "Asia/Shanghai"},
	})
	if code != 200 {
		t.Fatalf("webhook setup %d %s", code, data)
	}

	f.binding = dbfx.Insert(t, "member_bot_binding", testutil.Cols{"workspace_id": testWorkspaceID, "user_id": testUserID, "bot_id": uuidToString(bot.ID)})
	if verified {
		dbfx.Exec(t, `UPDATE member_bot_binding SET verified_at=now() WHERE id=$1`, f.binding)
	}
	f.worker = NewHumanNeededWorker(testHandler, func() *BotWakeWorker {
		w := NewBotWakeWorker(testHandler)
		w.client = func(u *url.URL) *http.Client { return &http.Client{Timeout: 2 * time.Second} }
		return w
	}())
	return f
}

func (f *humanNeededFixture) call(t *testing.T, method, path string, body any) (int, []byte) {
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
	return res.StatusCode, data
}

func (f *humanNeededFixture) route(t *testing.T, issueID, key string, urgent bool) service.HumanNeededResult {
	t.Helper()
	inbox := dbfx.Insert(t, "inbox_item", testutil.Cols{
		"workspace_id": testWorkspaceID, "recipient_type": "member", "recipient_id": testUserID,
		"type": service.InboxTypeNeedsYou, "severity": "action_required", "issue_id": issueID,
		"title": "Human needed fixture", "read": true,
	})
	res, err := service.RouteHumanNeeded(context.Background(), testHandler.Queries, service.HumanNeededInput{
		WorkspaceID: parseUUID(testWorkspaceID),
		Target:      parseUUID(testUserID),
		Kind:        "ask_human",
		DedupeKey:   key,
		IssueID:     parseUUID(issueID),
		InboxItemID: parseUUID(inbox),
		Question:    "Ship it?",
		Options:     []service.HumanNeededOption{{ID: "yes", Label: "Yes"}, {ID: "no", Label: "No"}},
		Urgent:      urgent,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (f *humanNeededFixture) status(t *testing.T, eventID string) (string, int) {
	t.Helper()
	var status string
	var attempts int
	dbfx.QueryRow(t, `SELECT status, attempts FROM human_needed_event WHERE event_id=$1`, eventID).Scan(&status, &attempts)
	return status, attempts
}

func TestHumanNeededRoutesOnlyThroughAVerifiedBinding(t *testing.T) {
	issue := dbfx.Issue(t, "Human needed routing", testutil.Cols{"visibility": "workspace"})

	// No binding at all: the call is not routed, so producers keep today's behavior.
	res, err := service.RouteHumanNeeded(context.Background(), testHandler.Queries, service.HumanNeededInput{
		WorkspaceID: parseUUID(testWorkspaceID), Target: parseUUID(testUserID), Kind: "mention", DedupeKey: "k-none", IssueID: parseUUID(issue),
	})
	if err != nil || res.Routed || res.Silent() {
		t.Fatalf("unbound person was routed: %+v %v", res, err)
	}

	// Bound but the Bot never answered the handshake: still not routed.
	f := newHumanNeededFixture(t, false)
	if res := f.route(t, issue, "k-unverified", false); res.Routed {
		t.Fatal("unverified binding was routed")
	}

	// Verified: routed, and a repeat of the same source is a duplicate, not a second push.
	dbfx.Exec(t, `UPDATE member_bot_binding SET verified_at=now() WHERE id=$1`, f.binding)
	first := f.route(t, issue, "k-verified", true)
	if !first.Routed || first.Duplicate || !first.Silent() || first.Event.Urgency != "urgent" {
		t.Fatalf("verified binding not routed: %+v", first)
	}
	again := f.route(t, issue, "k-verified", true)
	if !again.Routed || !again.Duplicate {
		t.Fatalf("repeat was not a duplicate: %+v", again)
	}

	// Mirror mode keeps the in-app alert.
	dbfx.Exec(t, `UPDATE member_bot_binding SET mode='mirror' WHERE id=$1`, f.binding)
	if res := f.route(t, issue, "k-mirror", false); !res.Routed || res.Silent() {
		t.Fatalf("mirror mode must not be silent: %+v", res)
	}

	// A revoked binding falls back to the old path.
	dbfx.Exec(t, `UPDATE member_bot_binding SET revoked_at=now() WHERE id=$1`, f.binding)
	if res := f.route(t, issue, "k-revoked", false); res.Routed {
		t.Fatal("revoked binding was routed")
	}
}

func TestHumanNeededRetriesWithBackoffUntilTheBotAnswers(t *testing.T) {
	ctx := context.Background()
	issue := dbfx.Issue(t, "Human needed retry", testutil.Cols{"visibility": "workspace"})
	f := newHumanNeededFixture(t, true)
	f.response.Store(500)
	routed := f.route(t, issue, "k-retry", false)
	eventID := uuidToString(routed.Event.EventID)

	process := func() {
		t.Helper()
		worked, err := f.worker.ProcessNext(ctx)
		if err != nil || !worked {
			t.Fatalf("worker %v %v", worked, err)
		}
	}
	for want := 1; want <= 2; want++ {
		process()
		status, attempts := f.status(t, eventID)
		if status != "queued" || attempts != want {
			t.Fatalf("after failure %d: %s attempts=%d", want, status, attempts)
		}
		// Backoff holds it back until the delay is up.
		if worked, _ := f.worker.ProcessNext(ctx); worked {
			t.Fatal("retried before the backoff elapsed")
		}
		dbfx.Exec(t, `UPDATE human_needed_event SET next_attempt_at=now() WHERE event_id=$1`, eventID)
	}

	// The third failed push puts the call back in front of the person in the app.
	process()
	var unread bool
	dbfx.QueryRow(t, `SELECT NOT read FROM inbox_item WHERE id=$1`, uuidToString(routed.Event.InboxItemID)).Scan(&unread)
	if !unread {
		t.Fatal("third failure did not put the call back in the app")
	}
	dbfx.Exec(t, `UPDATE human_needed_event SET next_attempt_at=now() WHERE event_id=$1`, eventID)

	f.response.Store(200)
	process()
	status, _ := f.status(t, eventID)
	if status != "sent" {
		t.Fatalf("not delivered after the Bot recovered: %s", status)
	}
	var payload struct {
		Type        string `json:"type"`
		HumanNeeded struct {
			Kind    string `json:"kind"`
			Urgency string `json:"urgency"`
		} `json:"human_needed"`
	}
	body, _ := f.lastBody.Load().([]byte)
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("payload: %v %s", err, body)
	}
	if payload.Type != "human_needed" || payload.HumanNeeded.Urgency != "normal" {
		t.Fatalf("unexpected payload: %s", body)
	}
}

func TestHumanNeededEscalatesWhenTheBotDoesNotRelay(t *testing.T) {
	ctx := context.Background()
	issue := dbfx.Issue(t, "Human needed escalation", testutil.Cols{"visibility": "workspace"})
	f := newHumanNeededFixture(t, true)
	routed := f.route(t, issue, "k-escalate", false)
	eventID := uuidToString(routed.Event.EventID)

	if worked, err := f.worker.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("worker %v %v", worked, err)
	}
	if code, data := f.call(t, "POST", "/bots/"+uuidToString(f.bot.ID)+"/wake/events/"+eventID+"/woken", map[string]any{}); code != 200 {
		t.Fatalf("woken %d %s", code, data)
	}

	// Four minutes after woken: not yet.
	dbfx.Exec(t, `UPDATE human_needed_event SET woken_at=now() - interval '4 minutes' WHERE event_id=$1`, eventID)
	f.worker.Sweep(ctx)
	var escalated bool
	dbfx.QueryRow(t, `SELECT escalated_at IS NOT NULL FROM human_needed_event WHERE event_id=$1`, eventID).Scan(&escalated)
	if escalated {
		t.Fatal("escalated before the five minute window")
	}

	// Past five minutes: one escalation event, one follow-up, never a second.
	dbfx.Exec(t, `UPDATE human_needed_event SET woken_at=now() - interval '6 minutes' WHERE event_id=$1`, eventID)
	f.worker.Sweep(ctx)
	f.worker.Sweep(ctx)
	var followUps int
	dbfx.QueryRow(t, `SELECT count(*) FROM human_needed_event WHERE parent_event_id=$1 AND event_type='human_needed.escalation'`, eventID).Scan(&followUps)
	if followUps != 1 {
		t.Fatalf("escalation events = %d, want 1", followUps)
	}
	var unread bool
	dbfx.QueryRow(t, `SELECT NOT read FROM inbox_item WHERE id=$1`, uuidToString(routed.Event.InboxItemID)).Scan(&unread)
	if !unread {
		t.Fatal("in-app fallback did not make the item unread")
	}

	// The Bot finally relays and the person answers through it.
	if code, data := f.call(t, "POST", "/bots/"+uuidToString(f.bot.ID)+"/wake/events/"+eventID+"/relayed", map[string]any{"channel": "telegram", "message_ref": "m1"}); code != 200 {
		t.Fatalf("relayed %d %s", code, data)
	}
	if code, data := f.call(t, "POST", "/bots/"+uuidToString(f.bot.ID)+"/wake/events/"+eventID+"/answered", map[string]any{"answer": "yes"}); code != 400 {
		t.Fatalf("a ticket question answered without a comment_id must be refused: %d %s", code, data)
	}
}

func TestHumanNeededBackoffGrowsAndCaps(t *testing.T) {
	prev := time.Duration(0)
	for n := 1; n <= 10; n++ {
		d := service.HumanNeededBackoff(n)
		if d < prev {
			t.Fatalf("backoff shrank at %d: %s < %s", n, d, prev)
		}
		prev = d
	}
	if service.HumanNeededBackoff(100) != 30*time.Minute {
		t.Fatal("backoff must cap at 30 minutes")
	}
}

func askHumanHTTP(t *testing.T, asker, issueID, to string) (int, AskHumanResponse) {
	t.Helper()
	w := httptest.NewRecorder()
	req := withURLParam(newRequestAs(asker, "POST", "/api/issues/"+issueID+"/ask-human", map[string]any{
		"to": to, "question": "Ship it?", "options": []map[string]string{{"id": "yes", "label": "Yes"}, {"id": "no", "label": "No"}},
	}), "id", issueID)
	testHandler.AskHumanIssue(w, req)
	var resp AskHumanResponse
	if w.Code != http.StatusOK {
		t.Logf("ask-human %d: %s", w.Code, w.Body.String())
	}
	if w.Code == http.StatusOK {
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode ask-human: %v", err)
		}
	}
	return w.Code, resp
}

func issueStatus(t *testing.T, issueID string) string {
	t.Helper()
	var s string
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id=$1`, issueID).Scan(&s)
	return s
}

// A colleague without a Bot is asked exactly the old way: inbox row they see,
// a visible @, nothing in the Bot outbox. The ticket waits on them.
func TestAskHumanForUnboundPersonKeepsTheOldPath(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "ask human unbound", "in_progress")
	peer := summonSecondMember(t)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, issue.ID)
		testPool.Exec(context.Background(), `DELETE FROM issue_summon WHERE issue_id = $1`, issue.ID)
	})

	code, resp := askHumanHTTP(t, testUserID, issue.ID, peer)
	if code != http.StatusOK || resp.ViaBot || !resp.Blocked {
		t.Fatalf("ask: %d %+v", code, resp)
	}
	if got := summonCount(t, `SELECT count(*) FROM human_needed_event WHERE issue_id=$1`, issue.ID); got != 0 {
		t.Fatalf("unbound person produced %d Bot events", got)
	}
	if got := summonCount(t, `SELECT count(*) FROM inbox_item WHERE issue_id=$1 AND recipient_id=$2 AND NOT read`, issue.ID, peer); got != 1 {
		t.Fatalf("unread inbox rows = %d, want 1", got)
	}
	if issueStatus(t, issue.ID) != "blocked" {
		t.Fatal("ticket did not wait on the person")
	}
	// Asking again is not a second notification.
	if _, again := askHumanHTTP(t, testUserID, issue.ID, peer); !again.Duplicate {
		t.Fatalf("repeat ask was not a duplicate: %+v", again)
	}
}

// A person with a verified Bot is asked through the Bot: one pending event,
// the in-app row kept but read, and their answer clears the block.
func TestAskHumanForBoundPersonGoesToTheBotAndAnswerUnblocks(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "ask human bound", "in_progress")
	asker := summonSecondMember(t)
	f := newHumanNeededFixture(t, true)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, issue.ID)
		testPool.Exec(context.Background(), `DELETE FROM issue_summon WHERE issue_id = $1`, issue.ID)
	})

	code, resp := askHumanHTTP(t, asker, issue.ID, testUserID)
	if code != http.StatusOK || !resp.ViaBot || !resp.Blocked {
		t.Fatalf("ask: %d %+v", code, resp)
	}
	var eventID, status string
	dbfx.QueryRow(t, `SELECT event_id::text, status FROM human_needed_event WHERE issue_id=$1 AND target_user_id=$2 AND event_type='human_needed'`, issue.ID, testUserID).Scan(&eventID, &status)
	if eventID == "" || status != "queued" {
		t.Fatalf("event %q status %q", eventID, status)
	}
	if got := summonCount(t, `SELECT count(*) FROM inbox_item WHERE issue_id=$1 AND recipient_id=$2 AND NOT read`, issue.ID, testUserID); got != 0 {
		t.Fatalf("the Bot path left %d unread in-app rows", got)
	}

	// The person answers (through the Bot, which comments as them).
	postMemberComment(t, testUserID, issue.ID, "Yes, ship it.")
	if got := issueStatus(t, issue.ID); got != "in_progress" {
		t.Fatalf("answer did not clear the block: %s", got)
	}
	dbfx.QueryRow(t, `SELECT status FROM human_needed_event WHERE event_id=$1`, eventID).Scan(&status)
	if status == "queued" || status == "sent" || status == "woken" || status == "relayed" {
		t.Fatalf("event still open after the answer: %s", status)
	}
	_ = f
}
