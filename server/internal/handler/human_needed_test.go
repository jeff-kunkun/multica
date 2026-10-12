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
	// Issues default to private; the asker is a plain member who must see it.
	dbfx.Exec(t, `UPDATE issue SET visibility='workspace' WHERE id=$1`, issue.ID)
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

// agentRequest is a request the way a running agent sends it: the person's
// credentials plus the agent and task headers the CLI adds.
func agentRequest(method, path string, body any, agentID, taskID string) *http.Request {
	req := newRequest(method, path, body)
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	return req
}

// openRuns counts the runs an agent has waiting or going on a ticket.
func openRuns(t *testing.T, issueID, agentID string) int {
	t.Helper()
	return summonCount(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id=$1 AND agent_id=$2 AND status IN ('queued','dispatched','running')`, issueID, agentID)
}

// deliver pushes the next queued event to the Bot and records the Bot's two
// receipts, as a live Bot would.
func (f *humanNeededFixture) deliver(t *testing.T, eventID string) {
	t.Helper()
	if worked, err := f.worker.ProcessNext(context.Background()); err != nil || !worked {
		t.Fatalf("push %v %v", worked, err)
	}
	for _, step := range []struct {
		path string
		body map[string]any
	}{
		{"woken", map[string]any{}},
		{"relayed", map[string]any{"channel": "telegram", "message_ref": "m1"}},
	} {
		if code, data := f.call(t, "POST", "/bots/"+uuidToString(f.bot.ID)+"/wake/events/"+eventID+"/"+step.path, step.body); code != 200 {
			t.Fatalf("%s %d %s", step.path, code, data)
		}
	}
}

func decodeAskHuman(t *testing.T, w *httptest.ResponseRecorder) AskHumanResponse {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("ask-human %d: %s", w.Code, w.Body.String())
	}
	var resp AskHumanResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode ask-human: %v", err)
	}
	return resp
}

// A running agent asks a person who has a Bot, with choices. The question is a
// card; the Bot pushes it, the person answers there, and the Bot's `answered`
// fills the card, lifts the block and queues exactly one run for the agent —
// however many times the receipt is repeated.
func TestAgentAsksBoundPersonCardAnsweredByBotWakesAgentOnce(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "agent asks a bound person", "in_progress")
	agentID := handlerTestAgentID(t)
	setIssueAssigneeDirect(t, issue.ID, "agent", agentID)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	f := newHumanNeededFixture(t, true)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, issue.ID)
		testPool.Exec(context.Background(), `DELETE FROM issue_summon WHERE issue_id = $1`, issue.ID)
		testPool.Exec(context.Background(), `DELETE FROM agent_ask WHERE issue_id = $1`, issue.ID)
	})

	w := httptest.NewRecorder()
	testHandler.AskHumanIssue(w, withURLParam(agentRequest("POST", "/api/issues/"+issue.ID+"/ask-human", map[string]any{
		"to": testUserID, "question": "Ship it?",
		"options": []map[string]string{{"id": "yes", "label": "Yes"}, {"id": "no", "label": "No"}},
	}, agentID, taskID), "id", issue.ID))
	resp := decodeAskHuman(t, w)
	if !resp.ViaBot || !resp.Blocked || resp.AskID == "" {
		t.Fatalf("ask: %+v", resp)
	}
	var eventID, eventAsk string
	if err := testPool.QueryRow(context.Background(), `SELECT event_id::text, ask_id::text FROM human_needed_event WHERE issue_id=$1 AND event_type='human_needed'`, issue.ID).Scan(&eventID, &eventAsk); err != nil || eventAsk != resp.AskID {
		t.Fatalf("event %q ask %q (want %q): %v", eventID, eventAsk, resp.AskID, err)
	}
	f.deliver(t, eventID)
	// The pushed payload carries the card the Bot fills in.
	var payload struct {
		HumanNeeded struct {
			Ask struct {
				ID        string `json:"id"`
				Questions []struct {
					Options []struct {
						ID string `json:"id"`
					} `json:"options"`
				} `json:"questions"`
			} `json:"ask"`
		} `json:"human_needed"`
	}
	body, _ := f.lastBody.Load().([]byte)
	if err := json.Unmarshal(body, &payload); err != nil || payload.HumanNeeded.Ask.ID != resp.AskID || len(payload.HumanNeeded.Ask.Questions) != 1 {
		t.Fatalf("payload does not carry the card: %v %s", err, body)
	}

	// The agent finished its turn after asking; the answer must start the next one.
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='completed' WHERE id=$1`, taskID)
	if got := openRuns(t, issue.ID, agentID); got != 0 {
		t.Fatalf("runs before the answer = %d", got)
	}
	answer := func() (int, []byte) {
		return f.call(t, "POST", "/bots/"+uuidToString(f.bot.ID)+"/wake/events/"+eventID+"/answered", map[string]any{"answers": map[string]string{"0": "yes"}})
	}
	if code, data := answer(); code != 200 {
		t.Fatalf("answered %d %s", code, data)
	}
	var askStatus, askAnswers, eventStatus string
	dbfx.QueryRow(t, `SELECT status, answers::text FROM agent_ask WHERE id=$1`, resp.AskID).Scan(&askStatus, &askAnswers)
	dbfx.QueryRow(t, `SELECT status FROM human_needed_event WHERE event_id=$1`, eventID).Scan(&eventStatus)
	if askStatus != "answered" || !strings.Contains(askAnswers, "yes") || eventStatus != "answered" {
		t.Fatalf("card %s %s, event %s", askStatus, askAnswers, eventStatus)
	}
	if got := issueStatus(t, issue.ID); got != "in_progress" {
		t.Fatalf("the answer did not lift the block: %s", got)
	}
	if got := openRuns(t, issue.ID, agentID); got != 1 {
		t.Fatalf("runs after the answer = %d, want exactly 1", got)
	}
	var viaBot bool
	dbfx.QueryRow(t, `SELECT via_bot_id IS NOT NULL FROM comment WHERE issue_id=$1 AND content LIKE '%Ship it?%' ORDER BY created_at DESC LIMIT 1`, issue.ID).Scan(&viaBot)
	if !viaBot {
		t.Fatal("the answer comment does not show the Bot carried it")
	}
	// A repeated receipt neither errors nor starts a second run.
	if code, data := answer(); code != 200 {
		t.Fatalf("repeated answered %d %s", code, data)
	}
	if got := openRuns(t, issue.ID, agentID); got != 1 {
		t.Fatalf("runs after the repeat = %d, want still 1", got)
	}
}

// A card someone else's Bot may not fill: the answerer must be the person asked.
func TestBotCannotAnswerSomeoneElsesCard(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "card for the peer", "in_progress")
	peer := summonSecondMember(t)
	f := newHumanNeededFixture(t, true)
	card := dbfx.Insert(t, "agent_ask", testutil.Cols{
		"workspace_id": testWorkspaceID, "issue_id": issue.ID, "asker_type": "member", "asker_id": testUserID,
		"title": "For the peer", "mode": "needs_you", "recipient_id": peer,
		"questions": `[{"text":"Ok?","options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}]`,
	})
	ev := f.route(t, issue.ID, "k-foreign-card", false)
	dbfx.Exec(t, `UPDATE human_needed_event SET ask_id=$1 WHERE event_id=$2`, card, uuidToString(ev.Event.EventID))
	code, _ := f.call(t, "POST", "/bots/"+uuidToString(f.bot.ID)+"/wake/events/"+uuidToString(ev.Event.EventID)+"/answered", map[string]any{"answers": map[string]string{"0": "a"}})
	if code != http.StatusForbidden {
		t.Fatalf("a Bot answered a card asked of someone else: %d", code)
	}
	var status string
	dbfx.QueryRow(t, `SELECT status FROM agent_ask WHERE id=$1`, card).Scan(&status)
	if status != "open" {
		t.Fatalf("card status %s", status)
	}
}

// The existing question card (`multica ask`, POST /api/asks) for a person with
// a Bot goes through the Bot too: an event carrying the card, the in-app row
// kept but read, and the Bot's answer fills the card and resumes the asker.
func TestExistingAskCardForBoundPersonGoesToBotAndAnswerResumesAsker(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "needs_you card for a bound person", "in_progress")
	agentID := handlerTestAgentID(t)
	setIssueAssigneeDirect(t, issue.ID, "agent", agentID)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	f := newHumanNeededFixture(t, true)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, issue.ID)
		testPool.Exec(context.Background(), `DELETE FROM agent_ask WHERE issue_id = $1`, issue.ID)
	})

	w := httptest.NewRecorder()
	testHandler.CreateAsk(w, agentRequest("POST", "/api/asks", map[string]any{
		"title": "Which colour?", "issue_id": issue.ID, "mode": "needs_you",
		"questions": []AskQuestion{{Text: "Colour", Options: []AskOption{{ID: "red", Label: "Red"}, {ID: "blue", Label: "Blue"}}}},
	}, agentID, taskID))
	if w.Code != http.StatusCreated {
		t.Fatalf("create ask %d %s", w.Code, w.Body.String())
	}
	var card AskResponse
	if err := json.NewDecoder(w.Body).Decode(&card); err != nil {
		t.Fatal(err)
	}
	var eventID, askID string
	if err := testPool.QueryRow(context.Background(), `SELECT event_id::text, ask_id::text FROM human_needed_event WHERE issue_id=$1 AND event_type='human_needed'`, issue.ID).Scan(&eventID, &askID); err != nil || askID != card.ID {
		t.Fatalf("event %q ask %q (want %q): %v", eventID, askID, card.ID, err)
	}
	if got := summonCount(t, `SELECT count(*) FROM inbox_item WHERE issue_id=$1 AND recipient_id=$2 AND type='needs_you' AND NOT read`, issue.ID, testUserID); got != 0 {
		t.Fatalf("the Bot path left %d unread in-app rows", got)
	}
	f.deliver(t, eventID)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='completed' WHERE id=$1`, taskID)
	if code, data := f.call(t, "POST", "/bots/"+uuidToString(f.bot.ID)+"/wake/events/"+eventID+"/answered", map[string]any{"answers": map[string]string{"0": "blue"}}); code != 200 {
		t.Fatalf("answered %d %s", code, data)
	}
	var status, answers string
	dbfx.QueryRow(t, `SELECT status, answers::text FROM agent_ask WHERE id=$1`, card.ID).Scan(&status, &answers)
	if status != "answered" || !strings.Contains(answers, "blue") {
		t.Fatalf("card %s %s", status, answers)
	}
	if got := openRuns(t, issue.ID, agentID); got != 1 {
		t.Fatalf("the asker was not resumed exactly once: %d runs", got)
	}
}

func chatAskRequest(t *testing.T, sessionID string, body any) *http.Request {
	t.Helper()
	req := newRequest("POST", "/api/chat/sessions/"+sessionID+"/ask-human", body)
	member, err := testHandler.Queries.GetMemberByUserAndWorkspace(context.Background(), db.GetMemberByUserAndWorkspaceParams{
		UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("load member: %v", err)
	}
	req = req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, member))
	return withURLParam(req, "sessionId", sessionID)
}

func chatMessages(t *testing.T, sessionID, role string) []string {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT content FROM chat_message WHERE chat_session_id=$1 AND role=$2 ORDER BY created_at`, sessionID, role)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

// Chat round trip with a Bot: the question lands in the chat, the Bot's answer
// is written back as the person's message, and the asking agent gets one run.
// Without a Bot (or with a broken one) the question is still in the chat, so
// the person sees it where they are.
func TestChatAskHumanRoundTripAndFallback(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID, sessionID, _, _ := setupDirectChatSession(t, ctx, "ask-human chat")
	ask := map[string]any{"question": "Which domain?", "options": []map[string]string{{"id": "a", "label": "a.example.com"}, {"id": "b", "label": "b.example.com"}}}
	run := func() AskHumanResponse {
		w := httptest.NewRecorder()
		ask["urgent"] = false
		testHandler.AskHumanChat(w, chatAskRequest(t, sessionID, ask))
		return decodeAskHuman(t, w)
	}
	// Unbound: no Bot event, the question is a visible chat message.
	resp := run()
	if resp.ViaBot || resp.EventID != "" {
		t.Fatalf("unbound chat ask: %+v", resp)
	}
	if msgs := chatMessages(t, sessionID, "assistant"); len(msgs) != 1 || !strings.Contains(msgs[0], "Which domain?") || !strings.Contains(msgs[0], "a.example.com") {
		t.Fatalf("question not in the chat: %q", msgs)
	}

	// Bound and verified: routed to the Bot as well as written to the chat.
	f := newHumanNeededFixture(t, true)
	resp = run()
	if !resp.ViaBot || resp.EventID == "" {
		t.Fatalf("bound chat ask: %+v", resp)
	}
	if msgs := chatMessages(t, sessionID, "assistant"); len(msgs) != 2 {
		t.Fatalf("chat has %d question messages, want 2", len(msgs))
	}
	f.deliver(t, resp.EventID)
	var payload struct {
		HumanNeeded struct {
			ChatSessionID string `json:"chat_session_id"`
		} `json:"human_needed"`
	}
	body, _ := f.lastBody.Load().([]byte)
	_ = json.Unmarshal(body, &payload)

	if code, data := f.call(t, "POST", "/bots/"+uuidToString(f.bot.ID)+"/wake/events/"+resp.EventID+"/answered", map[string]any{}); code != 400 {
		t.Fatalf("a chat answer without text must be refused: %d %s", code, data)
	}
	answerBody := map[string]any{"answer": "b.example.com"}
	if code, data := f.call(t, "POST", "/bots/"+uuidToString(f.bot.ID)+"/wake/events/"+resp.EventID+"/answered", answerBody); code != 200 {
		t.Fatalf("answered %d %s", code, data)
	}
	if msgs := chatMessages(t, sessionID, "user"); len(msgs) != 1 || !strings.Contains(msgs[0], "b.example.com") {
		t.Fatalf("the answer is not in the chat as the person's message: %q", msgs)
	}
	if got := summonCount(t, `SELECT count(*) FROM agent_task_queue WHERE chat_session_id=$1 AND agent_id=$2 AND status IN ('queued','dispatched','running')`, sessionID, agentID); got != 1 {
		t.Fatalf("chat runs after the answer = %d, want 1", got)
	}
	// A repeated receipt writes nothing more.
	if code, data := f.call(t, "POST", "/bots/"+uuidToString(f.bot.ID)+"/wake/events/"+resp.EventID+"/answered", answerBody); code != 200 {
		t.Fatalf("repeated answered %d %s", code, data)
	}
	if msgs := chatMessages(t, sessionID, "user"); len(msgs) != 1 {
		t.Fatalf("the repeat wrote the answer twice: %q", msgs)
	}

	// Broken channel: the Bot is stopped, so the next question is not routed
	// but is still in the chat.
	dbfx.Exec(t, `UPDATE personal_access_token SET revoked=true WHERE id=$1`, uuidToString(f.bot.ID))
	resp = run()
	if resp.ViaBot || resp.EventID != "" {
		t.Fatalf("a stopped Bot was still routed: %+v", resp)
	}
	if msgs := chatMessages(t, sessionID, "assistant"); len(msgs) != 3 {
		t.Fatalf("chat has %d question messages, want 3", len(msgs))
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
