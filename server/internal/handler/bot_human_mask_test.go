package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// What a Bot is sent about its person: the scope setting office events read,
// the human-needed tags, and credential masking on every body. Real local
// sockets and disposable Postgres; NOT an actual Grok endpoint.

// humanNeededCaller serves the Bot's human-needed pull list for a test and
// calls it with the Bot's own token.
// setActivityScope sets which office events the fixture's Bot is told about.
func setActivityScope(t *testing.T, f *humanNeededFixture, scope ...string) {
	t.Helper()
	if len(scope) == 0 {
		scope = []string{}
	}
	dbfx.Exec(t, `UPDATE member_bot_binding SET activity_scope=$2 WHERE id=$1`, f.binding, scope)
}

func humanNeededCaller(t *testing.T, f *humanNeededFixture) func(method, path string) (int, []byte) {
	t.Helper()
	router := chi.NewRouter()
	router.Use(middleware.Auth(testHandler.Queries, nil, nil, nil))
	router.Route("/api/workspaces/{id}", func(r chi.Router) {
		r.Use(middleware.RequireWorkspaceMemberFromURL(testHandler.Queries, "id"))
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

// A row the retired activity stream queued before the upgrade is cancelled at
// send time, never pushed.
func TestRetiredActivityRowsAreCancelledNotSent(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	issue := dbfx.Issue(t, "Retired stream", testutil.Cols{"visibility": "workspace"})
	res := f.route(t, issue, "retired-activity", false)
	dbfx.Exec(t, `UPDATE human_needed_event SET event_type='activity' WHERE event_id=$1`, uuidToString(res.Event.EventID))
	before := f.calls.Load()
	if ok, _ := f.worker.ProcessNext(context.Background()); !ok {
		t.Fatal("nothing processed")
	}
	if f.calls.Load() != before {
		t.Fatal("a retired activity row reached the Bot")
	}
	var status string
	dbfx.QueryRow(t, `SELECT status FROM human_needed_event WHERE event_id=$1`, uuidToString(res.Event.EventID)).Scan(&status)
	if status != "cancelled" {
		t.Fatalf("status = %q, want cancelled", status)
	}
}

// Nothing in a pushed or pulled human-needed body carries a credential,
// whichever field it rode in: question, summary, title, option labels.
func TestWebhookBodiesAreMaskedWholeNotFieldByField(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	call := humanNeededCaller(t, f)
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

	code, data := call("GET", "/human-needed?status=sent,queued")
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
