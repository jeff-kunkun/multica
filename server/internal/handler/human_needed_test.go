package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

// The Bot channel goes bad: after three failed pushes the person gets one
// notice and the call is back in the app; stopping the Bot afterwards must
// still move every other open call, without a second notice.
func TestBrokenChannelNoticesOnceAndFallsBackEverything(t *testing.T) {
	ctx := context.Background()
	issue := dbfx.Issue(t, "Broken channel", testutil.Cols{"visibility": "workspace"})
	f := newHumanNeededFixture(t, true)
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM inbox_item WHERE recipient_id=$1 AND title='你的 Bot 暂时收不到消息'`, testUserID)
	})
	notices := func() int {
		return summonCount(t, `SELECT count(*) FROM inbox_item WHERE recipient_id=$1 AND details->>'human_needed'='channel_broken'`, testUserID)
	}
	before := notices()

	f.response.Store(500)
	first := f.route(t, issue, "k-broken-1", false)
	second := f.route(t, issue, "k-broken-2", false)
	firstID := uuidToString(first.Event.EventID)
	for attempt := 1; attempt <= 3; attempt++ {
		dbfx.Exec(t, `UPDATE human_needed_event SET next_attempt_at=now() WHERE event_id=$1`, firstID)
		dbfx.Exec(t, `UPDATE human_needed_event SET next_attempt_at=now() + interval '1 hour' WHERE event_id=$1`, uuidToString(second.Event.EventID))
		if worked, err := f.worker.ProcessNext(ctx); err != nil || !worked {
			t.Fatalf("attempt %d: %v %v", attempt, worked, err)
		}
	}
	if got := notices() - before; got != 1 {
		t.Fatalf("notices after three failures = %d, want 1", got)
	}
	var unread bool
	dbfx.QueryRow(t, `SELECT NOT read FROM inbox_item WHERE id=$1`, uuidToString(first.Event.InboxItemID)).Scan(&unread)
	if !unread {
		t.Fatal("the failed call was not put back in the app")
	}

	// Now the Bot is stopped. The still-waiting second call must fall back,
	// and the already-flagged channel must not notice a second time.
	dbfx.Exec(t, `UPDATE personal_access_token SET revoked=true WHERE id=$1`, uuidToString(f.bot.ID))
	f.worker.Sweep(ctx)
	f.worker.Sweep(ctx)
	if got := notices() - before; got != 1 {
		t.Fatalf("notices after stopping the Bot = %d, want still 1", got)
	}
	var status string
	dbfx.QueryRow(t, `SELECT status FROM human_needed_event WHERE event_id=$1`, uuidToString(second.Event.EventID)).Scan(&status)
	if status != "cancelled" {
		t.Fatalf("the waiting call was left on a dead Bot: %s", status)
	}
	dbfx.QueryRow(t, `SELECT NOT read FROM inbox_item WHERE id=$1`, uuidToString(second.Event.InboxItemID)).Scan(&unread)
	if !unread {
		t.Fatal("the waiting call was not put back in the app")
	}
}

// A call whose in-app row was never made still reaches the person: the
// fallback creates the row instead of returning early.
func TestFallbackCreatesTheInAppRowWhenThereWasNone(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID, sessionID, _, _ := setupDirectChatSession(t, ctx, "fallback row")
	_ = agentID
	f := newHumanNeededFixture(t, true)
	res, err := service.RouteHumanNeeded(ctx, testHandler.Queries, service.HumanNeededInput{
		WorkspaceID: parseUUID(testWorkspaceID), Target: parseUUID(testUserID), Kind: "chat_ask",
		DedupeKey: "chat-fallback", ChatSessionID: parseUUID(sessionID), Question: "Which domain?",
	})
	if err != nil || !res.Routed || res.Event.InboxItemID.Valid {
		t.Fatalf("route: %+v %v", res, err)
	}
	service.FallbackHumanNeededInApp(ctx, testHandler.Queries, testHandler.Bus, res.Event)
	var inbox string
	dbfx.QueryRow(t, `SELECT COALESCE(inbox_item_id::text,'') FROM human_needed_event WHERE event_id=$1`, uuidToString(res.Event.EventID)).Scan(&inbox)
	if inbox == "" {
		t.Fatal("the fallback left no in-app row")
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM inbox_item WHERE id=$1`, inbox) })
	var unread bool
	var body string
	dbfx.QueryRow(t, `SELECT NOT read, COALESCE(body,'') FROM inbox_item WHERE id=$1`, inbox).Scan(&unread, &body)
	if !unread || !strings.Contains(body, "Which domain?") {
		t.Fatalf("fallback row unread=%v body=%q", unread, body)
	}
	_ = f
}
