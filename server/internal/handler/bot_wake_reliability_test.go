package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestBotWakeReliableDelivery(t *testing.T) {
	ctx := context.Background()
	oldBox, oldURL := testHandler.BotWakeSecrets, testHandler.cfg.PublicURL
	testHandler.BotWakeSecrets, _ = NewBotWakeSecretBox("reliability-fixture")
	defer func() { testHandler.BotWakeSecrets, testHandler.cfg.PublicURL = oldBox, oldURL }()
	raw, hash, prefix, err := mintBotToken()
	if err != nil {
		t.Fatal(err)
	}
	bot, err := testHandler.Queries.CreateBotAccessToken(ctx, db.CreateBotAccessTokenParams{UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), Name: "reliable receiver", TokenHash: hash, TokenPrefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	defer testPool.Exec(ctx, `DELETE FROM personal_access_token WHERE id=$1`, bot.ID)
	issue := dbfx.Issue(t, "Reliable event source", testutil.Cols{"visibility": "workspace"})
	dbfx.Exec(t, `INSERT INTO bot_wake_watch(bot_id,issue_id) VALUES($1,$2)`, bot.ID, issue)
	base := "/api/workspaces/" + testWorkspaceID + "/bots/" + uuidToString(bot.ID) + "/wake"
	router := chi.NewRouter()
	router.Use(middleware.Auth(testHandler.Queries, nil, nil, nil))
	router.Route("/api/workspaces/{id}", func(r chi.Router) {
		r.Use(middleware.RequireWorkspaceMemberFromURL(testHandler.Queries, "id"))
		r.Get("/bots/{botId}/wake", testHandler.GetBotWake)
		r.Put("/bots/{botId}/wake", testHandler.PutBotWake)
		r.Get("/bots/{botId}/wake/pending", testHandler.ListBotWakeEvents)
		r.Post("/bots/{botId}/wake/pending/ack", testHandler.AcknowledgeBotWakePending)
		r.Post("/bots/{botId}/wake/events/{eventId}", testHandler.RecordBotWakeReceipt)
		r.Post("/bots/{botId}/wake/events/{eventId}/woken", testHandler.RecordBotWakeReceipt)
		r.Post("/bots/{botId}/wake/events/{eventId}/result", testHandler.RecordBotWakeReceipt)
	})
	api := httptest.NewServer(router)
	defer api.Close()
	testHandler.cfg.PublicURL = api.URL
	request := func(method, path string, body any) []byte {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, api.URL+path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+raw)
		res, err := api.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatalf("%s %s => %d %s", method, path, res.StatusCode, data)
		}
		return data
	}
	received := make(chan map[string]any, 32)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		received <- p
		w.WriteHeader(200)
	}))
	defer receiver.Close()
	t.Setenv("MULTICA_PLUGIN_DEV_ORIGINS", receiver.URL)
	worker := NewBotWakeWorker(testHandler)
	worker.client = func(*url.URL) *http.Client { return receiver.Client() }
	request("PUT", base, map[string]any{"webhook_url": receiver.URL, "webhook_key": "Bearer fixture", "hourly_limit": 120, "quiet_hours": map[string]any{"enabled": false, "start": "00:00", "end": "00:00", "timezone": "UTC"}})
	process := func() {
		t.Helper()
		ok, err := worker.ProcessNext(ctx)
		if err != nil || !ok {
			t.Fatalf("worker=%v err=%v", ok, err)
		}
	}
	take := func(typ string) map[string]any {
		t.Helper()
		start := time.Now()
		process()
		select {
		case p := <-received:
			if p["type"] != typ {
				t.Fatalf("type=%v wanted=%s", p["type"], typ)
			}
			if time.Since(start) >= 5*time.Second {
				t.Fatal("delivery exceeded 5 seconds")
			}
			return p
		default:
			t.Fatal("event not delivered")
			return nil
		}
	}
	// Exercise the actual database capture paths without transaction actor metadata.
	dbfx.Exec(t, `UPDATE issue SET assignee_type='member',assignee_id=$2 WHERE id=$1`, issue, testUserID)
	task := take("task")
	author := task["author"].(map[string]any)
	if author["id"] != "" || author["type"] != "" || author["note"] != "source attribution unavailable" {
		t.Fatalf("unknown actor fabricated: %v", author)
	}
	dbfx.Exec(t, `UPDATE issue SET status='done' WHERE id=$1`, issue)
	take("done")
	dbfx.Insert(t, "issue_summon", testutil.Cols{"workspace_id": testWorkspaceID, "issue_id": issue, "recipient_id": testUserID, "caller_type": "member", "caller_id": testUserID, "source": "test", "reason": "decision"})
	take("needs_kun")
	mention := func() {
		dbfx.Insert(t, "comment", testutil.Cols{"issue_id": issue, "workspace_id": testWorkspaceID, "author_type": "member", "author_id": testUserID, "content": "[@Bot](mention://bot/" + uuidToString(bot.ID) + ")"})
	}
	mention()
	push := take("mention")
	// Both CLI ack and the webhook use the same batch endpoint and body.
	ackURL := push["ack_url"].(string)
	if ackURL != api.URL+base+"/pending/ack" {
		t.Fatalf("ack endpoint=%s", ackURL)
	}
	request("POST", base+"/pending/ack", push["ack_body"])
	request("POST", base+"/pending/ack", push["ack_body"])
	wokenURL := push["woken_url"].(string)
	request("POST", wokenURL[len(api.URL):], map[string]any{})
	request("POST", push["result_url"].(string)[len(api.URL):], map[string]any{"result_url": push["url"]})
	// Compatibility for receivers holding older payloads with the bare URL.
	request("POST", base+"/events/"+push["event_id"].(string), map[string]any{})
	var confirmed bool
	dbfx.QueryRow(t, `SELECT woken_at IS NOT NULL AND result_at IS NOT NULL AND status='received' FROM bot_wake_event WHERE event_id=$1 AND bot_id=$2`, push["event_id"], bot.ID).Scan(&confirmed)
	if !confirmed {
		t.Fatal("execution not confirmed")
	}
	t.Log("task/done/needs_kun/mention delivered within 5s; unknown actor noted; payload ack/woken/result and old receipt URL return 200")

	// A source run supplies an exact actor, including durable event-list metadata.
	agentID := createHandlerTestAgent(t, "RecoveredWakeActor", []byte("[]"))
	runID := dbfx.Task(t, agentID, testutil.Cols{"issue_id": issue, "status": "running", "runtime_id": testRuntimeID})
	var recoveredID string
	err = testPool.QueryRow(ctx, `INSERT INTO bot_wake_event(bot_id,workspace_id,event_type,issue_id,source_id,source_task_id,root_id) VALUES($1,$2,'task',$3,gen_random_uuid(),$4,gen_random_uuid()) RETURNING event_id::text`, bot.ID, testWorkspaceID, issue, runID).Scan(&recoveredID)
	if err != nil {
		t.Fatal(err)
	}
	recovered := take("task")["author"].(map[string]any)
	if recovered["type"] != "agent" || recovered["id"] != agentID {
		t.Fatalf("actor recovery: %v", recovered)
	}
	var storedActor string
	dbfx.QueryRow(t, `SELECT author_id::text FROM bot_wake_event WHERE bot_id=$1 AND event_id=$2`, bot.ID, recoveredID).Scan(&storedActor)
	if storedActor != agentID {
		t.Fatal("recovered actor not persisted")
	}

	// Make the first of three ordinary events fill the new quota.
	dbfx.Exec(t, `DELETE FROM bot_wake_attempt WHERE bot_id=$1`, bot.ID)
	request("PUT", base, map[string]any{"hourly_limit": 1})
	enqueue := func(typ string) string {
		t.Helper()
		var id string
		err := testPool.QueryRow(ctx, `INSERT INTO bot_wake_event(bot_id,workspace_id,event_type,issue_id,source_id,author_type,author_id,root_id) VALUES($1,$2,$3,$4,gen_random_uuid(),'member',$5,gen_random_uuid()) RETURNING event_id::text`, bot.ID, testWorkspaceID, typ, issue, testUserID).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	enqueue("task")
	take("task")
	second, third := enqueue("done"), enqueue("needs_kun")
	process()
	process()
	var settings BotWakeResponse
	if err := json.Unmarshal(request("GET", base, nil), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.HourlyLimit != 1 || settings.LimitedCount != 2 {
		t.Fatalf("effective rules: %+v", settings)
	}
	mention()
	take("mention")
	count, err := testHandler.Queries.CountRecentBotWakeAttempts(ctx, bot.ID)
	if err != nil || count != 1 {
		t.Fatalf("mention consumed quota: count=%d err=%v", count, err)
	}
	// Advance the rolling window, without waiting one hour or manually requeuing.
	dbfx.Exec(t, `UPDATE bot_wake_attempt SET attempted_at=now()-interval '61 minutes' WHERE bot_id=$1`, bot.ID)
	dbfx.Exec(t, `UPDATE bot_wake_event SET next_attempt_at=now() WHERE bot_id=$1 AND status='pending'`, bot.ID)
	if p := take("done"); p["event_id"] != second {
		t.Fatal("deferred order changed")
	}
	dbfx.Exec(t, `UPDATE bot_wake_attempt SET attempted_at=now()-interval '61 minutes' WHERE bot_id=$1`, bot.ID)
	if p := take("needs_kun"); p["event_id"] != third {
		t.Fatal("deferred order changed")
	}
	var page struct {
		Events []any `json:"events"`
	}
	json.Unmarshal(request("GET", base+"/pending", nil), &page)
	if len(page.Events) != 0 {
		t.Fatalf("pending did not clear: %v", page.Events)
	}
	t.Log("limit=1 holds exactly two; mention still delivers without consuming quota; both deferred events deliver FIFO after window expiry; pending clears")
	// Saving a rule change rechecks held events immediately.
	request("PUT", base, map[string]any{"quiet_hours": map[string]any{"enabled": true, "start": "00:00", "end": "00:00", "timezone": "UTC"}})
	enqueue("task")
	process()
	request("PUT", base, map[string]any{"hourly_limit": 120, "quiet_hours": map[string]any{"enabled": false, "start": "00:00", "end": "00:00", "timezone": "UTC"}})
	take("task")
	t.Log("ending quiet hours and raising the saved limit immediately releases deferred events")
}
