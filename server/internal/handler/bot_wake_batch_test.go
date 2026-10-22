package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestBotWakeBatchContentAndReply(t *testing.T) {
	ctx := context.Background()
	oldBox := testHandler.BotWakeSecrets
	testHandler.BotWakeSecrets, _ = NewBotWakeSecretBox("batch-local-only")
	defer func() { testHandler.BotWakeSecrets = oldBox }()
	raw, hash, prefix, err := mintBotToken()
	if err != nil {
		t.Fatal(err)
	}
	bot, err := testHandler.Queries.CreateBotAccessToken(ctx, db.CreateBotAccessTokenParams{UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), Name: "Grok fixture", TokenHash: hash, TokenPrefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	defer testPool.Exec(ctx, `DELETE FROM personal_access_token WHERE id=$1`, bot.ID)
	defaults, err := testHandler.Queries.GetBotWake(ctx, bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range defaults.Events {
		if event == "started" {
			t.Fatal("started subscribed by default")
		}
	}
	var calls atomic.Int32
	var body map[string]any
	var response atomic.Int32
	response.Store(200)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.Header.Get("X-Multica-Event-ID") != body["event_id"] {
			t.Error("header identity mismatch")
		}
		w.WriteHeader(int(response.Load()))
	}))
	defer receiver.Close()
	t.Setenv("MULTICA_PLUGIN_DEV_ORIGINS", receiver.URL)
	secret, _ := json.Marshal(botWakeSecrets{URL: receiver.URL, Key: "Bearer fixture"})
	sealed, _ := testHandler.BotWakeSecrets.Seal(secret)
	dbfx.Exec(t, `UPDATE bot_wake SET webhook_secret=$2, quiet_hours=jsonb_set(quiet_hours,'{enabled}','false'),hourly_limit=120 WHERE bot_id=$1`, bot.ID, sealed)
	issueID := dbfx.Issue(t, "Batch mention fixture", testutil.Cols{"visibility": "workspace"})
	rootID := dbfx.Insert(t, "comment", testutil.Cols{"workspace_id": testWorkspaceID, "issue_id": issueID, "author_type": "member", "author_id": testUserID, "content": strings.Repeat("原", 220)})
	for i := 0; i < 7; i++ {
		dbfx.Insert(t, "comment", testutil.Cols{"workspace_id": testWorkspaceID, "issue_id": issueID, "author_type": "member", "author_id": testUserID, "content": "Earlier reply", "parent_id": rootID})
	}
	content := "[@Grok](mention://bot/" + uuidToString(bot.ID) + ") 原文，请处理附件"
	sourceID := dbfx.Insert(t, "comment", testutil.Cols{"workspace_id": testWorkspaceID, "issue_id": issueID, "author_type": "member", "author_id": testUserID, "content": content, "parent_id": rootID})
	attachmentID := dbfx.Insert(t, "attachment", testutil.Cols{"workspace_id": testWorkspaceID, "issue_id": issueID, "comment_id": sourceID, "uploader_type": "member", "uploader_id": testUserID, "filename": "report.txt", "url": "fixture/report.txt", "content_type": "text/plain", "size_bytes": 7, "sha256": strings.Repeat("a", 64)})
	var root string
	if err := testPool.QueryRow(ctx, `SELECT root_id::text FROM bot_wake_event WHERE bot_id=$1 AND source_id=$2`, bot.ID, sourceID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	// Lifecycle events in the same source window are recorded but never pushed.
	for _, kind := range []string{"blocked", "needs_kun"} {
		dbfx.Exec(t, `INSERT INTO bot_wake_event(bot_id,workspace_id,event_type,issue_id,source_id,author_type,author_id,root_id) VALUES($1,$2,$3,$4,gen_random_uuid(),'member',$5,$6)`, bot.ID, testWorkspaceID, kind, issueID, testUserID, root)
	}
	worker := NewBotWakeWorker(testHandler)
	worker.client = func(*url.URL) *http.Client { return receiver.Client() }
	if worked, err := worker.ProcessNext(ctx); err != nil || worked || calls.Load() != 0 {
		t.Fatalf("window did not wait: %v %v", worked, err)
	}
	// Move only the source group's timestamps: exercise the real production
	// window without a 30-second sleep or changing the worker's configuration.
	dbfx.Exec(t, `UPDATE bot_wake_event SET created_at=created_at-interval '31 seconds' WHERE bot_id=$1`, bot.ID)
	if worked, err := worker.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("worker: %v %v", worked, err)
	}
	events := body["events"].([]any)
	if calls.Load() != 1 || len(events) != 1 {
		t.Fatalf("calls=%d events=%d", calls.Load(), len(events))
	}
	mention := events[0].(map[string]any)
	if mention["content"] != content || mention["comment_id"] != sourceID || mention["reply_to_comment_id"] != sourceID {
		t.Fatalf("mention: %+v", mention)
	}
	issue := mention["issue"].(map[string]any)
	if issue["title"] != "Batch mention fixture" || issue["identifier"] == "" {
		t.Fatal(issue)
	}
	author := mention["author"].(map[string]any)
	if author["name"] == nil {
		t.Fatal("author name missing")
	}
	thread := mention["thread"].(map[string]any)
	rootComment := thread["root"].(map[string]any)
	if rootComment["id"] != rootID || rootComment["content_truncated"] != true || len(thread["replies"].([]any)) != 5 {
		t.Fatalf("thread: %+v", thread)
	}
	files := mention["attachments"].([]any)
	if len(files) != 1 {
		t.Fatal(files)
	}
	file := files[0].(map[string]any)
	if file["id"] != attachmentID || file["filename"] != "report.txt" || file["sha256"] != strings.Repeat("a", 64) || file["size_bytes"] != float64(7) || !strings.HasSuffix(file["url"].(string), "/download") {
		t.Fatal(file)
	}
	var received, attempts int
	testPool.QueryRow(ctx, `SELECT count(*) FROM bot_wake_event WHERE bot_id=$1 AND status='received'`, bot.ID).Scan(&received)
	testPool.QueryRow(ctx, `SELECT count(*) FROM bot_wake_attempt WHERE bot_id=$1`, bot.ID).Scan(&attempts)
	var disabled int
	testPool.QueryRow(ctx, `SELECT count(*) FROM bot_wake_event WHERE bot_id=$1 AND status='pending' AND reason='event disabled'`, bot.ID).Scan(&disabled)
	if received != 1 || attempts != 0 || disabled != 2 {
		t.Fatalf("received=%d attempts=%d", received, attempts)
	}
	if worked, err := worker.ProcessNext(ctx); err != nil || worked || calls.Load() != 1 {
		t.Fatalf("duplicate push: %v %v", worked, err)
	}

	// The recipient replies using only the webhook's URL/body and its normal
	// credential. There are no GETs for an issue or comment before this POST.
	router := chi.NewRouter()
	router.Use(middleware.Auth(testHandler.Queries, nil, nil, nil))
	router.Post("/api/issues/{id}/comments", testHandler.CreateComment)
	router.Get("/api/issues/{id}/comments", testHandler.ListComments)
	router.Get("/api/workspaces/{id}/bots/{botId}/wake/events", testHandler.ListBotWakeEvents)
	router.Post("/api/workspaces/{id}/bots/{botId}/wake/pending/ack", testHandler.AcknowledgeBotWakePending)
	router.Post("/api/workspaces/{id}/bots/{botId}/wake/events/{eventId}/result", testHandler.RecordBotWakeReceipt)
	api := httptest.NewServer(router)
	defer api.Close()
	reply := mention["reply"].(map[string]any)
	replyBody := reply["body"].(map[string]any)
	replyBody["content"] = "🤖 Grok fixture\n收到，在处理"
	b, _ := json.Marshal(replyBody)
	replyURL := reply["url"].(string)
	parsed, _ := url.Parse(replyURL)
	req, _ := http.NewRequest("POST", api.URL+parsed.Path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+raw)
	req.Header.Set("X-Workspace-ID", mention["workspace_id"].(string))
	res, err := api.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var saved map[string]any
	json.NewDecoder(res.Body).Decode(&saved)
	if res.StatusCode != 201 || saved["parent_id"] != sourceID {
		t.Fatalf("reply=%d %+v", res.StatusCode, saved)
	}
	// The same comment feed consumed by Web/Desktop keeps that exact parent.
	req, _ = http.NewRequest("GET", api.URL+parsed.Path+"?thread="+rootID+"&tail=5", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	req.Header.Set("X-Workspace-ID", mention["workspace_id"].(string))
	res, err = api.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var comments []map[string]any
	json.NewDecoder(res.Body).Decode(&comments)
	found := false
	for _, c := range comments {
		if c["id"] == saved["id"] && c["parent_id"] == sourceID {
			found = true
		}
	}
	if !found {
		t.Fatalf("reply missing from thread feed: %+v", comments)
	}
	req, _ = http.NewRequest("GET", api.URL+"/api/workspaces/"+testWorkspaceID+"/bots/"+uuidToString(bot.ID)+"/wake/events?include_content=true", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	res, err = api.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var listing map[string]any
	if err := json.NewDecoder(res.Body).Decode(&listing); err != nil {
		t.Fatal(err)
	}
	foundPayload := false
	for _, item := range listing["events"].([]any) {
		row := item.(map[string]any)
		if row["type"] == "mention" {
			p := row["payload"].(map[string]any)
			if p["comment_id"] == sourceID && p["reply_to_comment_id"] == sourceID && row["delivery_id"] != "" {
				foundPayload = true
			}
		}
	}
	if res.StatusCode != 200 || !foundPayload {
		t.Fatalf("content listing: %d %+v", res.StatusCode, listing)
	}
	// Every member retains the reliability contract's own receipt action.
	// Confirm the full delivery with no GET, then record only one member result.
	for i, item := range events {
		event := item.(map[string]any)
		action := "ack_url"
		input := event["ack_body"]
		if i == 0 {
			action = "result_url"
			input = map[string]any{"result_url": event["url"]}
		}
		receiptURL, _ := url.Parse(event[action].(string))
		data, _ := json.Marshal(input)
		req, _ := http.NewRequest("POST", api.URL+receiptURL.Path, bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+raw)
		res, err := api.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("member receipt %d=%d", i, res.StatusCode)
		}
	}
	var results, woken int
	dbfx.QueryRow(t, `SELECT count(*) FILTER(WHERE result_at IS NOT NULL),count(*) FILTER(WHERE woken_at IS NOT NULL) FROM bot_wake_event WHERE bot_id=$1 AND delivery_id=$2`, bot.ID, body["delivery_id"]).Scan(&results, &woken)
	if results != 1 || woken != 0 {
		t.Fatalf("delivery receipt: results=%d woken=%d", results, woken)
	}

	t.Log("30s window: no early push; mention pushed alone, blocked + needs_kun kept pending; content/context/attachment complete; reply POST 201 under original mention; shared thread feed contains reply")

	// Chat containers use the same window, preserving every source message.
	agentID := createHandlerTestAgent(t, "BatchChatAgent", []byte("[]"))
	chatID := createHandlerTestChatSession(t, agentID)
	messageIDs := []string{}
	for i := 0; i < 3; i++ {
		text := "chat follow-up"
		if i == 0 {
			text = "[@Bot](mention://bot/" + uuidToString(bot.ID) + ") chat source"
		}
		messageIDs = append(messageIDs, dbfx.Insert(t, "chat_message", testutil.Cols{"chat_session_id": chatID, "role": "user", "sender_user_id": testUserID, "content": text}))
	}
	dbfx.Exec(t, `UPDATE bot_wake_event SET created_at=created_at-interval '31 seconds' WHERE bot_id=$1 AND chat_session_id=$2`, bot.ID, chatID)
	beforeCalls := calls.Load()
	if worked, err := worker.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("chat batch: %v %v", worked, err)
	}
	if calls.Load() != beforeCalls+1 || len(body["events"].([]any)) != 1 {
		t.Fatal("chat follow-ups pushed")
	}
	if event := body["events"].([]any)[0].(map[string]any); event["message_id"] != messageIDs[0] || event["reply_to_message_id"] != messageIDs[0] || event["content"] == nil {
		t.Fatalf("chat source lost: %+v", event)
	}
	t.Log("3 messages in the same chat: 1 HTTP POST carrying only the mention; same-chat reply location included")
	// Description mentions can acknowledge with a top-level comment, and
	// deleted source bodies must never reappear through enriched event reads.
	var mentionEvent db.BotWakeEvent
	rows, err := testHandler.Queries.ListBotWakeEvents(ctx, db.ListBotWakeEventsParams{BotID: bot.ID, WorkspaceID: bot.WorkspaceID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if uuidToString(row.SourceID) == sourceID {
			mentionEvent = row
		}
	}
	descriptionEvent := mentionEvent
	descriptionEvent.SourceID = descriptionEvent.IssueID
	fields, err := testHandler.botWakePayloadFields(ctx, descriptionEvent)
	if err != nil {
		t.Fatal(err)
	}
	if fields["reply"] == nil || fields["reply_to_comment_id"] != nil {
		t.Fatal("description reply target incorrect")
	}
	dbfx.Exec(t, `UPDATE comment SET deleted_at=now() WHERE id=$1`, sourceID)
	fields, err = testHandler.botWakePayloadFields(ctx, mentionEvent)
	if err != nil {
		t.Fatal(err)
	}
	if fields["content"] != "" || fields["reply"] != nil || len(fields["attachments"].([]map[string]any)) != 0 {
		t.Fatal("deleted source leaked")
	}

}

func TestBotWakeBatchIsolationAndLock(t *testing.T) {
	ctx := context.Background()
	_, hash, prefix, err := mintBotToken()
	if err != nil {
		t.Fatal(err)
	}
	bot, err := testHandler.Queries.CreateBotAccessToken(ctx, db.CreateBotAccessTokenParams{UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), Name: "batch isolation", TokenHash: hash, TokenPrefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	defer testPool.Exec(ctx, `DELETE FROM personal_access_token WHERE id=$1`, bot.ID)
	firstIssue := dbfx.Issue(t, "first source", testutil.Cols{"visibility": "workspace"})
	secondIssue := dbfx.Issue(t, "second source", testutil.Cols{"visibility": "workspace"})
	insert := func(issue string, seconds int) {
		dbfx.Exec(t, `INSERT INTO bot_wake_event(bot_id,workspace_id,event_type,issue_id,source_id,author_type,author_id,root_id,created_at) VALUES($1,$2,'done',$3,gen_random_uuid(),'member',$4,gen_random_uuid(),now()-$5*interval '1 second')`, bot.ID, testWorkspaceID, issue, testUserID, seconds)
	}
	insert(firstIssue, 60)
	insert(firstIssue, 45)
	insert(secondIssue, 55)
	insert(firstIssue, 20)
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	q := testHandler.Queries.WithTx(tx)
	first, err := q.NextBotWakeEvent(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	members, err := q.ListBotWakeBatch(ctx, db.ListBotWakeBatchParams{BotID: first.BotID, WorkspaceID: first.WorkspaceID, IssueID: first.IssueID, ChatSessionID: first.ChatSessionID, WindowStart: first.CreatedAt, FirstSequence: first.Sequence, WindowSeconds: 30})
	if err != nil || len(members) != 2 {
		t.Fatalf("members=%d %v", len(members), err)
	}
	for _, e := range members {
		if uuidToString(e.IssueID) != firstIssue {
			t.Fatal("another source joined")
		}
	}
	// A later mention prioritizes the source, without skipping its earlier
	// ordinary events and accidentally splitting the fixed source window.
	dbfx.Exec(t, `INSERT INTO bot_wake_event(bot_id,workspace_id,event_type,issue_id,source_id,author_type,author_id,root_id,created_at) VALUES($1,$2,'mention',$3,gen_random_uuid(),'member',$4,gen_random_uuid(),now()-interval '40 seconds')`, bot.ID, testWorkspaceID, firstIssue, testUserID)
	prioritized, err := q.NextBotWakeEvent(ctx, 30)
	if err != nil || prioritized.Sequence != first.Sequence {
		t.Fatalf("mention split earlier source events: %+v %v", prioritized, err)
	}
	// A second transaction skips the already locked Bot, even though this Bot
	// has several ready events. It cannot seize a member and split the push.
	other, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Rollback(ctx)
	if _, err := testHandler.Queries.WithTx(other).NextBotWakeEvent(ctx, 30); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second worker stole locked bot: %v", err)
	}
	t.Log("fixed window excludes later event and different issue; second worker skips locked Bot")
}

func TestBotWakeDefaultSubscriptionUpgrade(t *testing.T) {
	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	down, err := os.ReadFile("../../migrations/668_bot_wake_batches.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, err := os.ReadFile("../../migrations/668_bot_wake_batches.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	q := testHandler.Queries.WithTx(tx)
	create := func(name string) db.PersonalAccessToken {
		t.Helper()
		_, hash, prefix, err := mintBotToken()
		if err != nil {
			t.Fatal(err)
		}
		b, err := q.CreateBotAccessToken(ctx, db.CreateBotAccessTokenParams{UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), Name: name, TokenHash: hash, TokenPrefix: prefix})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	defaultBot := create("old default")
	customBot := create("custom started")
	if _, err := tx.Exec(ctx, `UPDATE bot_wake SET events=ARRAY['started'] WHERE bot_id=$1`, customBot.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, string(up)); err != nil {
		t.Fatal(err)
	}
	newBot := create("new default")
	for _, b := range []db.PersonalAccessToken{defaultBot, newBot} {
		rules, err := q.GetBotWake(ctx, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range rules.Events {
			if event == "started" {
				t.Fatalf("default still contains started: %s", b.Name)
			}
		}
	}
	rules, err := q.GetBotWake(ctx, customBot.ID)
	if err != nil || len(rules.Events) != 1 || rules.Events[0] != "started" {
		t.Fatalf("custom selection lost: %+v %v", rules.Events, err)
	}
	// Schema downgrade remains executable and preserves saved selections.
	if _, err := tx.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	rules, err = q.GetBotWake(ctx, defaultBot.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range rules.Events {
		if event == "started" {
			t.Fatal("downgrade re-enabled saved started")
		}
	}
	t.Log("real migration down/up: old and new defaults exclude started; explicit started-only preserved; downgrade works without re-enabling saved subscriptions")
}
