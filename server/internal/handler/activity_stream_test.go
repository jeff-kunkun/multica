package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// Real local sockets and disposable Postgres; NOT an actual Grok endpoint.

func setActivityScope(t *testing.T, f *humanNeededFixture, scope ...string) {
	t.Helper()
	if len(scope) == 0 {
		scope = []string{}
	}
	dbfx.Exec(t, `UPDATE member_bot_binding SET activity_scope=$2 WHERE id=$1`, f.binding, scope)
}

func routeActivity(t *testing.T, in service.ActivityInput) int {
	t.Helper()
	in.WorkspaceID = parseUUID(testWorkspaceID)
	n, err := service.RouteActivity(context.Background(), testHandler.Queries, in)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func activityRows(t *testing.T, bot string) int {
	t.Helper()
	return dbfx.Count(t, `SELECT count(*) FROM human_needed_event WHERE bot_id=$1 AND event_type='activity'`, bot)
}

func agentActor(t *testing.T) string {
	t.Helper()
	rt := dbfx.Runtime(t, "activity runtime")
	return dbfx.Agent(t, "Activity Agent", rt)
}

func TestActivityStreamIsOffUntilTheScopeIsSet(t *testing.T) {
	issue := dbfx.Issue(t, "Activity off", testutil.Cols{"visibility": "workspace"})
	f := newHumanNeededFixture(t, true)
	agent := agentActor(t)
	in := service.ActivityInput{
		IssueID: parseUUID(issue), Type: service.ActivityCommentCreated, DedupeKey: "act:comment:off",
		SourceID: "c1", ActorType: "agent", ActorID: parseUUID(agent), Text: "进展：第一步完成",
	}
	if n := routeActivity(t, in); n != 0 || activityRows(t, uuidToString(f.bot.ID)) != 0 {
		t.Fatalf("empty scope still wrote %d events", n)
	}
	// Scope on but the Bot never confirmed: nothing either.
	dbfx.Exec(t, `UPDATE member_bot_binding SET verified_at=NULL WHERE id=$1`, f.binding)
	setActivityScope(t, f, "owned")
	if n := routeActivity(t, in); n != 0 {
		t.Fatalf("unverified binding got %d events", n)
	}
}

func TestActivityStreamOwnedFollowedAndSkipRules(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	bot := uuidToString(f.bot.ID)
	agent := agentActor(t)
	owned := dbfx.Issue(t, "Mine", testutil.Cols{"visibility": "workspace"})
	other := dbfx.Issue(t, "Not mine", testutil.Cols{"visibility": "workspace", "creator_type": "agent", "creator_id": agent})
	input := func(issue, key string) service.ActivityInput {
		return service.ActivityInput{
			IssueID: parseUUID(issue), Type: service.ActivityCommentCreated, DedupeKey: key,
			SourceID: key, ActorType: "agent", ActorID: parseUUID(agent), Text: "我做完了这一步",
		}
	}

	setActivityScope(t, f, "owned")
	if n := routeActivity(t, input(owned, "act:comment:o1")); n != 1 {
		t.Fatalf("owned ticket: %d events", n)
	}
	if n := routeActivity(t, input(owned, "act:comment:o1")); n != 0 {
		t.Fatalf("a repeat was written again: %d", n)
	}
	if n := routeActivity(t, input(other, "act:comment:x1")); n != 0 {
		t.Fatalf("a ticket nobody here owns reached owned scope: %d", n)
	}

	// Following covers it only once the person follows it and the scope says so.
	dbfx.Exec(t, `INSERT INTO issue_subscriber (issue_id, user_type, user_id, reason) VALUES ($1,'member',$2,'manual')`, other, testUserID)
	if n := routeActivity(t, input(other, "act:comment:x2")); n != 0 {
		t.Fatalf("followed ticket reached owned-only scope: %d", n)
	}
	setActivityScope(t, f, "followed")
	if n := routeActivity(t, input(other, "act:comment:x3")); n != 1 {
		t.Fatalf("followed ticket: %d events", n)
	}
	dbfx.Exec(t, `UPDATE issue_subscriber SET unsubscribed_at=now() WHERE issue_id=$1`, other)
	if n := routeActivity(t, input(other, "act:comment:x4")); n != 0 {
		t.Fatalf("an unfollowed ticket still streamed: %d", n)
	}

	// Their own words and their own Bot's words are not news to them.
	setActivityScope(t, f, "owned", "followed")
	self := input(owned, "act:comment:self")
	self.ActorType, self.ActorID = "member", parseUUID(testUserID)
	if n := routeActivity(t, self); n != 0 {
		t.Fatalf("own comment was echoed: %d", n)
	}
	viaBot := input(owned, "act:comment:viabot")
	viaBot.ViaBotID = bot
	if n := routeActivity(t, viaBot); n != 0 {
		t.Fatalf("own Bot's comment was echoed back: %d", n)
	}

	// A comment already pushed as "you are needed" is not pushed again.
	comment := dbfx.Comment(t, owned, "要你拍板：是否发布？", testutil.Cols{"author_type": "agent", "author_id": agent})
	f.route(t, owned, "needed-for-comment", false)
	dbfx.Exec(t, `UPDATE human_needed_event SET source_comment_id=$2 WHERE bot_id=$1 AND event_type='human_needed'`, bot, comment)
	asked := input(owned, "act:comment:asked")
	asked.CommentID = parseUUID(comment)
	if n := routeActivity(t, asked); n != 0 {
		t.Fatalf("a comment already sent as human_needed was streamed too: %d", n)
	}
}

func TestActivityStreamChatIsTheOpenersOwn(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	agent := agentActor(t)
	chat := dbfx.ChatSession(t, agent)
	stranger := summonSecondMember(t)
	theirs := dbfx.ChatSession(t, agent, testutil.Cols{"creator_id": stranger})
	in := func(session, key string) service.ActivityInput {
		return service.ActivityInput{
			ChatSessionID: parseUUID(session), Type: service.ActivityChatMessage, DedupeKey: key,
			SourceID: key, ActorType: "agent", ActorID: parseUUID(agent), Text: "已经查完了",
		}
	}
	setActivityScope(t, f, "followed")
	if n := routeActivity(t, in(chat, "act:chat:1")); n != 0 {
		t.Fatalf("followed scope reached a chat: %d", n)
	}
	setActivityScope(t, f, "owned")
	if n := routeActivity(t, in(chat, "act:chat:2")); n != 1 {
		t.Fatalf("own chat: %d events", n)
	}
	if n := routeActivity(t, in(theirs, "act:chat:3")); n != 0 {
		t.Fatalf("someone else's chat reached this person: %d", n)
	}
}

func TestActivityPushCarriesTagsAndNoSecrets(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	agent := agentActor(t)
	issue := dbfx.Issue(t, "Push check", testutil.Cols{"visibility": "workspace"})
	setActivityScope(t, f, "owned")
	secrets := []string{"hunter2-super", "sk-abcdefghijklmnopqrstuvwx", "mul_AbCdEfGhIjKlMnOp", "ghp_abcdefghijklmnopqrstuvwxyz0123"}
	text := "线上部署失败，风险较高。password: " + secrets[0] + " key " + secrets[1] + " tok " + secrets[2] + " gh " + secrets[3]
	routeActivity(t, service.ActivityInput{
		IssueID: parseUUID(issue), Type: service.ActivityCommentCreated, DedupeKey: "act:comment:push",
		SourceID: "push", ActorType: "agent", ActorID: parseUUID(agent), Text: text,
	})
	if ok, err := f.worker.ProcessNext(context.Background()); err != nil || !ok {
		t.Fatalf("process: %v %v", ok, err)
	}
	if f.calls.Load() != 1 {
		t.Fatalf("webhook calls %d", f.calls.Load())
	}
	raw, _ := f.lastBody.Load().([]byte)
	for _, s := range secrets {
		if strings.Contains(string(raw), s) {
			t.Fatalf("a secret left in the body: %s\n%s", s, raw)
		}
	}
	var body struct {
		Events []struct {
			Type     string `json:"type"`
			Schema   string `json:"schema"`
			Activity struct {
				Importance string   `json:"importance"`
				Categories []string `json:"categories"`
				Excerpt    string   `json:"excerpt"`
			} `json:"activity"`
			HandledURL string `json:"handled_url"`
		} `json:"events"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Events) != 1 {
		t.Fatalf("body: %v %s", err, raw)
	}
	e := body.Events[0]
	if e.Type != "activity" || e.Schema != service.ActivitySchema || e.Activity.Importance != "high" || e.HandledURL == "" {
		t.Fatalf("event: %+v", e)
	}
	if !strings.Contains(strings.Join(e.Activity.Categories, ","), "risk") || !strings.Contains(e.Activity.Excerpt, service.MaskSecrets("线上部署失败")) {
		t.Fatalf("tags/excerpt: %+v", e.Activity)
	}
	var status string
	dbfx.QueryRow(t, `SELECT status FROM human_needed_event WHERE bot_id=$1 AND event_type='activity'`, uuidToString(f.bot.ID)).Scan(&status)
	if status != "sent" {
		t.Fatalf("a pushed activity should be done at 200, got %s", status)
	}
}

func TestActivityIsPushedAfterHumanNeededAndCheckedAtSendTime(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	agent := agentActor(t)
	issue := dbfx.Issue(t, "Order check", testutil.Cols{"visibility": "workspace"})
	setActivityScope(t, f, "owned")
	routeActivity(t, service.ActivityInput{
		IssueID: parseUUID(issue), Type: service.ActivityCommentCreated, DedupeKey: "act:comment:first",
		SourceID: "first", ActorType: "agent", ActorID: parseUUID(agent), Text: "先写入的普通进展",
	})
	res := f.route(t, issue, "needed-after", false)
	if ok, _ := f.worker.ProcessNext(context.Background()); !ok {
		t.Fatal("nothing processed")
	}
	if status, _ := f.status(t, uuidToString(res.Event.EventID)); status != "sent" {
		t.Fatalf("human_needed must go first, status %s", status)
	}

	// The person turns the stream off before the activity row goes out.
	setActivityScope(t, f)
	before := f.calls.Load()
	if ok, _ := f.worker.ProcessNext(context.Background()); !ok {
		t.Fatal("activity row not claimed")
	}
	if f.calls.Load() != before {
		t.Fatal("an activity row was pushed after the stream was switched off")
	}
	var status string
	dbfx.QueryRow(t, `SELECT status FROM human_needed_event WHERE bot_id=$1 AND event_type='activity'`, uuidToString(f.bot.ID)).Scan(&status)
	if status != "cancelled" {
		t.Fatalf("status %s", status)
	}
}

func TestActivityHandledReceiptAndCatchUpList(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	agent := agentActor(t)
	issue := dbfx.Issue(t, "Receipt check", testutil.Cols{"visibility": "workspace"})
	setActivityScope(t, f, "owned")
	routeActivity(t, service.ActivityInput{
		IssueID: parseUUID(issue), Type: service.ActivityStatusChanged, DedupeKey: "act:status:blocked",
		ActorType: "agent", ActorID: parseUUID(agent), FromStatus: "in_progress", ToStatus: "blocked",
	})
	var eventID string
	dbfx.QueryRow(t, `SELECT event_id::text FROM human_needed_event WHERE bot_id=$1 AND event_type='activity'`, uuidToString(f.bot.ID)).Scan(&eventID)

	router := chi.NewRouter()
	router.Use(middleware.Auth(testHandler.Queries, nil, nil, nil))
	router.Route("/api/workspaces/{id}", func(r chi.Router) {
		r.Use(middleware.RequireWorkspaceMemberFromURL(testHandler.Queries, "id"))
		r.Post("/bots/{botId}/wake/events/{eventId}/handled", testHandler.HandledActivity)
		r.Get("/bots/{botId}/activity", testHandler.ListActivityForBot)
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	call := func(method, path string, body any) (int, []byte) {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, srv.URL+"/api/workspaces/"+testWorkspaceID+"/bots/"+uuidToString(f.bot.ID)+path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+f.token)
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		return res.StatusCode, data
	}

	code, data := call("GET", "/activity?importance=high", nil)
	if code != 200 || !strings.Contains(string(data), eventID) || !strings.Contains(string(data), `"importance":"high"`) {
		t.Fatalf("list: %d %s", code, data)
	}
	if code, data = call("GET", "/activity?importance=critical", nil); code != 200 || strings.Contains(string(data), eventID) {
		t.Fatalf("a high event passed a critical floor: %d %s", code, data)
	}
	if code, data = call("POST", "/wake/events/"+eventID+"/handled", map[string]any{"reason": "已告诉他"}); code != 200 || !strings.Contains(string(data), `"handled"`) {
		t.Fatalf("handled: %d %s", code, data)
	}
	var status, reason string
	dbfx.QueryRow(t, `SELECT status, COALESCE(reason,'') FROM human_needed_event WHERE event_id=$1`, eventID).Scan(&status, &reason)
	if status != "handled" || reason != "已告诉他" {
		t.Fatalf("row: %s %q", status, reason)
	}
	// A human_needed event is not a stream event: the receipt refuses it.
	other := f.route(t, dbfx.Issue(t, "needed", testutil.Cols{"visibility": "workspace"}), "needed-handled", false)
	if code, _ = call("POST", "/wake/events/"+uuidToString(other.Event.EventID)+"/handled", map[string]any{}); code != 404 {
		t.Fatalf("handled on a human_needed event: %d", code)
	}
	// Handled rows leave the default list.
	if _, data = call("GET", "/activity", nil); strings.Contains(string(data), eventID) {
		t.Fatalf("handled event still listed: %s", data)
	}
}

func TestBotBindingActivityScopeRoundTrip(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	put := func(body map[string]any) (int, BotBindingResponse) {
		w := httptest.NewRecorder()
		testHandler.PutBotBinding(w, withURLParam(newRequest(http.MethodPut, "/api/workspaces/"+testWorkspaceID+"/me/bot-binding", body), "id", testWorkspaceID))
		var resp BotBindingResponse
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if w.Code != 200 {
			t.Logf("PUT %v -> %d %s", body, w.Code, w.Body.String())
		}
		return w.Code, resp
	}
	if code, resp := put(map[string]any{"bot_id": uuidToString(f.bot.ID), "activity_scope": []string{"followed", "owned", "owned"}}); code != 200 || strings.Join(resp.ActivityScope, ",") != "owned,followed" {
		t.Fatalf("set: %d %+v", code, resp)
	}
	// Leaving the field out leaves the scope alone.
	if code, resp := put(map[string]any{"mode": "mirror"}); code != 200 || len(resp.ActivityScope) != 2 {
		t.Fatalf("untouched: %d %+v", code, resp)
	}
	if code, _ := put(map[string]any{"activity_scope": []string{"everything"}}); code != 400 {
		t.Fatalf("unknown scope accepted: %d", code)
	}
	if code, resp := put(map[string]any{"activity_scope": []string{}}); code != 200 || len(resp.ActivityScope) != 0 {
		t.Fatalf("off: %d %+v", code, resp)
	}
	w := httptest.NewRecorder()
	testHandler.GetBotBinding(w, withURLParam(newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/me/bot-binding", nil), "id", testWorkspaceID))
	if !strings.Contains(w.Body.String(), `"activity_scope":[]`) {
		t.Fatalf("scope must always be an array: %s", w.Body.String())
	}
}

func TestHumanNeededPayloadIsTaggedCritical(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	issue := dbfx.Issue(t, "Critical tag", testutil.Cols{"visibility": "workspace"})
	f.route(t, issue, "critical-tag", false)
	if ok, _ := f.worker.ProcessNext(context.Background()); !ok {
		t.Fatal("nothing processed")
	}
	raw, _ := f.lastBody.Load().([]byte)
	if !strings.Contains(string(raw), `"importance":"critical"`) || !strings.Contains(string(raw), `"needs_human"`) {
		t.Fatalf("human_needed lost its top tag: %s", raw)
	}
}

func TestActivityPurgeKeepsOpenRows(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	agent := agentActor(t)
	issue := dbfx.Issue(t, "Purge check", testutil.Cols{"visibility": "workspace"})
	setActivityScope(t, f, "owned")
	for _, k := range []string{"old-open", "old-done"} {
		routeActivity(t, service.ActivityInput{
			IssueID: parseUUID(issue), Type: service.ActivityCommentCreated, DedupeKey: "act:comment:" + k,
			SourceID: k, ActorType: "agent", ActorID: parseUUID(agent), Text: "x",
		})
	}
	dbfx.Exec(t, `UPDATE human_needed_event SET created_at = now() - interval '20 days' WHERE bot_id=$1 AND event_type='activity'`, uuidToString(f.bot.ID))
	dbfx.Exec(t, `UPDATE human_needed_event SET status='handled' WHERE bot_id=$1 AND dedupe_key='act:comment:old-done'`, uuidToString(f.bot.ID))
	n, err := testHandler.Queries.PurgeActivityEvents(context.Background())
	if err != nil || n < 1 {
		t.Fatalf("purge: %d %v", n, err)
	}
	if left := activityRows(t, uuidToString(f.bot.ID)); left != 1 {
		t.Fatalf("expected the open row to survive, %d left", left)
	}
	_ = time.Now
}

// activityCaller serves the Bot's pull routes for a test and calls them with
// the Bot's own token.
func activityCaller(t *testing.T, f *humanNeededFixture) func(method, path string) (int, []byte) {
	t.Helper()
	router := chi.NewRouter()
	router.Use(middleware.Auth(testHandler.Queries, nil, nil, nil))
	router.Route("/api/workspaces/{id}", func(r chi.Router) {
		r.Use(middleware.RequireWorkspaceMemberFromURL(testHandler.Queries, "id"))
		r.Get("/bots/{botId}/activity", testHandler.ListActivityForBot)
		r.Get("/bots/{botId}/human-needed", testHandler.ListHumanNeededForBot)
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return func(method, path string) (int, []byte) {
		req, _ := http.NewRequest(method, srv.URL+"/api/workspaces/"+testWorkspaceID+"/bots/"+uuidToString(f.bot.ID)+path, nil)
		req.Header.Set("Authorization", "Bearer "+f.token)
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		return res.StatusCode, data
	}
}

// A row queued while the person could see the ticket must not be readable
// through the catch-up list once they cannot, and the same goes for a stream
// that no longer covers it.
func TestActivityCatchUpListFollowsTheCurrentAccess(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	agent := agentActor(t)
	call := activityCaller(t, f)
	// Someone else's ticket, followed by this person.
	issue := dbfx.Issue(t, "Followed then hidden", testutil.Cols{"visibility": "workspace", "creator_type": "member", "creator_id": summonSecondMember(t)})
	dbfx.Exec(t, `INSERT INTO issue_subscriber (issue_id, user_type, user_id, reason) VALUES ($1,'member',$2,'manual')`, issue, testUserID)
	setActivityScope(t, f, "followed")
	routeActivity(t, service.ActivityInput{
		IssueID: parseUUID(issue), Type: service.ActivityCommentCreated, DedupeKey: "act:comment:catchup",
		SourceID: "catchup", ActorType: "agent", ActorID: parseUUID(agent), Text: "REVIEW_PRIVATE_EXCERPT",
	})
	if code, data := call("GET", "/activity"); code != 200 || !strings.Contains(string(data), "REVIEW_PRIVATE_EXCERPT") {
		t.Fatalf("visible row missing: %d %s", code, data)
	}

	// The ticket goes private: the person no longer sees it, so the Bot must not.
	dbfx.Exec(t, `UPDATE issue SET visibility='private' WHERE id=$1`, issue)
	if code, data := call("GET", "/activity"); code != 200 || strings.Contains(string(data), "REVIEW_PRIVATE_EXCERPT") {
		t.Fatalf("a hidden ticket's text was served: %d %s", code, data)
	}
	dbfx.Exec(t, `UPDATE issue SET visibility='workspace' WHERE id=$1`, issue)

	// Narrowing the stream to owned, or unfollowing, takes it away too.
	setActivityScope(t, f, "owned")
	if _, data := call("GET", "/activity"); strings.Contains(string(data), "REVIEW_PRIVATE_EXCERPT") {
		t.Fatalf("a narrowed scope still served the row: %s", data)
	}
	setActivityScope(t, f, "followed")
	if _, data := call("GET", "/activity"); !strings.Contains(string(data), "REVIEW_PRIVATE_EXCERPT") {
		t.Fatalf("row should be back: %s", data)
	}
	dbfx.Exec(t, `UPDATE issue_subscriber SET unsubscribed_at=now() WHERE issue_id=$1`, issue)
	if _, data := call("GET", "/activity"); strings.Contains(string(data), "REVIEW_PRIVATE_EXCERPT") {
		t.Fatalf("an unfollowed ticket's row was served: %s", data)
	}
}

// Filtering must not strand the rows behind a run of hidden ones.
func TestActivityCatchUpListSkipsHiddenRunsWithoutEmptyPages(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	agent := agentActor(t)
	call := activityCaller(t, f)
	setActivityScope(t, f, "owned")
	visible := dbfx.Issue(t, "Visible", testutil.Cols{"visibility": "workspace"})
	routeActivity(t, service.ActivityInput{
		IssueID: parseUUID(visible), Type: service.ActivityCommentCreated, DedupeKey: "act:comment:old-visible",
		SourceID: "ov", ActorType: "agent", ActorID: parseUUID(agent), Text: "OLD_VISIBLE_ROW",
	})
	hidden := dbfx.Issue(t, "Hidden", testutil.Cols{"visibility": "workspace"})
	for i := 0; i < 5; i++ {
		routeActivity(t, service.ActivityInput{
			IssueID: parseUUID(hidden), Type: service.ActivityCommentCreated, DedupeKey: "act:comment:h" + string(rune('a'+i)),
			SourceID: "h", ActorType: "agent", ActorID: parseUUID(agent), Text: "HIDDEN_ROW",
		})
	}
	dbfx.Exec(t, `UPDATE issue SET visibility='private', creator_type='agent', creator_id=$2 WHERE id=$1`, hidden, agent)
	code, data := call("GET", "/activity?limit=2")
	if code != 200 || !strings.Contains(string(data), "OLD_VISIBLE_ROW") || strings.Contains(string(data), "HIDDEN_ROW") {
		t.Fatalf("limit=2 over five hidden rows: %d %s", code, data)
	}
}

// A queued row is judged again when it is about to go out.
func TestActivityPushIsReDecidedAtSendTime(t *testing.T) {
	agent := agentActor(t)
	cases := []struct {
		name   string
		change func(t *testing.T, f *humanNeededFixture, issue string)
	}{
		{"scope narrowed from followed to owned", func(t *testing.T, f *humanNeededFixture, _ string) { setActivityScope(t, f, "owned") }},
		{"unfollowed", func(t *testing.T, _ *humanNeededFixture, issue string) {
			dbfx.Exec(t, `UPDATE issue_subscriber SET unsubscribed_at=now() WHERE issue_id=$1`, issue)
		}},
		{"ticket became private", func(t *testing.T, _ *humanNeededFixture, issue string) {
			dbfx.Exec(t, `UPDATE issue SET visibility='private' WHERE id=$1`, issue)
		}},
		{"binding revoked", func(t *testing.T, f *humanNeededFixture, _ string) {
			dbfx.Exec(t, `UPDATE member_bot_binding SET revoked_at=now() WHERE id=$1`, f.binding)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newHumanNeededFixture(t, true)
			issue := dbfx.Issue(t, "Send time "+c.name, testutil.Cols{"visibility": "workspace", "creator_type": "member", "creator_id": summonSecondMember(t)})
			dbfx.Exec(t, `INSERT INTO issue_subscriber (issue_id, user_type, user_id, reason) VALUES ($1,'member',$2,'manual')`, issue, testUserID)
			setActivityScope(t, f, "followed")
			if n := routeActivity(t, service.ActivityInput{
				IssueID: parseUUID(issue), Type: service.ActivityCommentCreated, DedupeKey: "act:comment:sendtime",
				SourceID: "st", ActorType: "agent", ActorID: parseUUID(agent), Text: "进展",
			}); n != 1 {
				t.Fatalf("queued %d", n)
			}
			c.change(t, f, issue)
			before := f.calls.Load()
			f.worker.ProcessNext(context.Background())
			if f.calls.Load() != before {
				t.Fatal("a row no longer covered was pushed")
			}
		})
	}

	t.Run("chat handed to someone else", func(t *testing.T) {
		f := newHumanNeededFixture(t, true)
		chat := dbfx.ChatSession(t, agent)
		setActivityScope(t, f, "owned")
		if n := routeActivity(t, service.ActivityInput{
			ChatSessionID: parseUUID(chat), Type: service.ActivityChatMessage, DedupeKey: "act:chat:sendtime",
			SourceID: "cm", ActorType: "agent", ActorID: parseUUID(agent), Text: "查完了",
		}); n != 1 {
			t.Fatalf("queued %d", n)
		}
		stranger := summonSecondMember(t)
		dbfx.Exec(t, `UPDATE chat_session SET creator_id=$2 WHERE id=$1`, chat, stranger)
		before := f.calls.Load()
		f.worker.ProcessNext(context.Background())
		if f.calls.Load() != before {
			t.Fatal("a chat that changed hands was pushed")
		}
	})

	t.Run("still covered is pushed", func(t *testing.T) {
		f := newHumanNeededFixture(t, true)
		issue := dbfx.Issue(t, "Still covered", testutil.Cols{"visibility": "workspace"})
		setActivityScope(t, f, "owned")
		routeActivity(t, service.ActivityInput{
			IssueID: parseUUID(issue), Type: service.ActivityCommentCreated, DedupeKey: "act:comment:stillok",
			SourceID: "ok", ActorType: "agent", ActorID: parseUUID(agent), Text: "进展",
		})
		f.worker.ProcessNext(context.Background())
		if f.calls.Load() != 1 {
			t.Fatalf("calls %d", f.calls.Load())
		}
	})
}

// Nothing in a pushed or pulled body carries a credential, whichever field it
// rode in: question, summary, title, author name, option labels.
func TestWebhookBodiesAreMaskedWholeNotFieldByField(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	agent := agentActor(t)
	call := activityCaller(t, f)
	leaks := []string{"review only secret", "ReviewOnlySecret123", `"password":"ab"`, `\"password\":\"ab\"`}

	// human_needed: question, summary, option labels.
	issue := dbfx.Issue(t, `Deploy {"password":"review only secret"}`, testutil.Cols{"visibility": "workspace"})
	res := f.route(t, issue, "needed-secret", false)
	dbfx.Exec(t, `UPDATE human_needed_event SET question=$2, options=$3::jsonb WHERE event_id=$1`,
		uuidToString(res.Event.EventID),
		`发布吗？ {"password":"review only secret"} token=ReviewOnlySecret123`,
		`[{"id":"yes","label":"用 {\"password\":\"ab\"} 发布"}]`)
	if ok, _ := f.worker.ProcessNext(context.Background()); !ok {
		t.Fatal("nothing processed")
	}
	raw, _ := f.lastBody.Load().([]byte)
	for _, leak := range leaks {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("human_needed body leaked %q:\n%s", leak, raw)
		}
	}
	if !strings.Contains(string(raw), "ack_url") {
		t.Fatalf("masking broke the receipt links: %s", raw)
	}

	// activity push: excerpt and the ticket title.
	setActivityScope(t, f, "owned")
	routeActivity(t, service.ActivityInput{
		IssueID: parseUUID(issue), Type: service.ActivityCommentCreated, DedupeKey: "act:comment:secret-push",
		SourceID: "sp", ActorType: "agent", ActorID: parseUUID(agent), Text: `密码 {"password":"review only secret"} 已改 {"password":"ab"}`,
	})
	if ok, _ := f.worker.ProcessNext(context.Background()); !ok {
		t.Fatal("activity not processed")
	}
	raw, _ = f.lastBody.Load().([]byte)
	for _, leak := range leaks {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("activity body leaked %q:\n%s", leak, raw)
		}
	}

	// the two pull lists
	code, data := call("GET", "/activity?status=sent,queued")
	if code != 200 {
		t.Fatalf("activity list: %d", code)
	}
	for _, leak := range leaks {
		if strings.Contains(string(data), leak) {
			t.Fatalf("activity list leaked %q: %s", leak, data)
		}
	}
	code, data = call("GET", "/human-needed?status=sent,queued")
	if code != 200 {
		t.Fatalf("human-needed list: %d %s", code, data)
	}
	for _, leak := range leaks {
		if strings.Contains(string(data), leak) {
			t.Fatalf("human-needed list leaked %q: %s", leak, data)
		}
	}
}

func TestWebhookMasksEscapedQuotePasswordsAndOptionIDs(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	agent := agentActor(t)
	const secretID = "sk-ReviewOnlySyntheticKey1234567890"
	field, _ := json.Marshal(map[string]string{"password": "ab\"cd\\ef\nTAILSUFFIX9"})

	// A legacy row whose option id holds a credential: nothing leaves, receipts stay.
	issue := dbfx.Issue(t, "Deploy", testutil.Cols{"visibility": "workspace"})
	res := f.route(t, issue, "needed-optid", false)
	dbfx.Exec(t, `UPDATE human_needed_event SET question=$2, options=$3::jsonb WHERE event_id=$1`,
		uuidToString(res.Event.EventID), "发布吗？ "+string(field),
		`[{"id":"`+secretID+`","label":"yes"},{"id":"b","label":"no"}]`)
	if ok, _ := f.worker.ProcessNext(context.Background()); !ok {
		t.Fatal("nothing processed")
	}
	raw, _ := f.lastBody.Load().([]byte)
	for _, leak := range []string{"ReviewOnlySynthetic", "TAILSUFFIX9"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("human_needed body leaked %q:\n%s", leak, raw)
		}
	}
	if !strings.Contains(string(raw), "ack_url") || !strings.Contains(string(raw), `"id":"b"`) {
		t.Fatalf("masking broke receipts or ordinary options: %s", raw)
	}

	// An activity push of a comment that quotes the password field.
	setActivityScope(t, f, "owned")
	routeActivity(t, service.ActivityInput{
		IssueID: parseUUID(issue), Type: service.ActivityCommentCreated, DedupeKey: "act:comment:escaped-quote",
		SourceID: "eq", ActorType: "agent", ActorID: parseUUID(agent), Text: "已改 " + string(field),
	})
	if ok, _ := f.worker.ProcessNext(context.Background()); !ok {
		t.Fatal("activity not processed")
	}
	raw, _ = f.lastBody.Load().([]byte)
	if strings.Contains(string(raw), "TAILSUFFIX9") {
		t.Fatalf("activity body leaked the password tail:\n%s", raw)
	}
}

func TestAskRefusesCredentialsInOptionIDs(t *testing.T) {
	bad := AskHumanRequest{Question: "发布吗？", Options: []service.HumanNeededOption{{ID: "sk-ReviewOnlySyntheticKey1234567890", Label: "是"}}}
	if _, _, _, problem := bad.validate(); problem == "" {
		t.Fatal("an option id holding a key was accepted")
	}
	ok := AskHumanRequest{Question: "发布吗？", Options: []service.HumanNeededOption{{ID: "a", Label: "是"}, {ID: "no-go", Label: "否"}}}
	if _, opts, _, problem := ok.validate(); problem != "" || len(opts) != 2 || opts[1].ID != "no-go" {
		t.Fatalf("ordinary options broke: %q %v", problem, opts)
	}
}
