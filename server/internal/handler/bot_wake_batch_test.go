package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	if calls.Load() != 1 || len(events) != 3 {
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
	if received != 3 || attempts != 1 {
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
	t.Log("30s window: no early push; mention + blocked + needs_kun: 1 HTTP POST, 3 events, 1 attempt; content/context/attachment complete; reply POST 201 under original mention; shared thread feed contains reply")

	// Retry membership survives worker recreation; a later source event is not
	// appended to a previously attempted batch.
	response.Store(503)
	for i := 0; i < 2; i++ {
		dbfx.Exec(t, `INSERT INTO bot_wake_event(bot_id,workspace_id,event_type,issue_id,source_id,author_type,author_id,root_id,created_at) VALUES($1,$2,'done',$3,gen_random_uuid(),'member',$4,gen_random_uuid(),now()-interval '31 seconds')`, bot.ID, testWorkspaceID, issueID, testUserID)
	}
	if worked, err := worker.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("retry first: %v %v", worked, err)
	}
	retryID := body["delivery_id"]
	dbfx.Exec(t, `UPDATE bot_wake_event SET next_attempt_at=now() WHERE bot_id=$1 AND status='queued'`, bot.ID)
	dbfx.Exec(t, `INSERT INTO bot_wake_event(bot_id,workspace_id,event_type,issue_id,source_id,author_type,author_id,root_id) VALUES($1,$2,'done',$3,gen_random_uuid(),'member',$4,gen_random_uuid())`, bot.ID, testWorkspaceID, issueID, testUserID)
	response.Store(200)
	worker = NewBotWakeWorker(testHandler)
	worker.client = func(*url.URL) *http.Client { return receiver.Client() }
	if worked, err := worker.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("retry second: %v %v", worked, err)
	}
	if body["delivery_id"] != retryID || len(body["events"].([]any)) != 2 {
		t.Fatal("retry batch changed")
	}
	var pendingNew int
	testPool.QueryRow(ctx, `SELECT count(*) FROM bot_wake_event WHERE bot_id=$1 AND status='queued' AND delivery_id IS NULL`, bot.ID).Scan(&pendingNew)
	if pendingNew != 1 {
		t.Fatal("new event joined retry")
	}
	t.Log("503 retry after worker recreation: same 2 events, stable delivery_id; newer event stays queued")
	// A disabled event in the same source window cannot hitchhike in the push.
	dbfx.Exec(t, `UPDATE bot_wake_event SET created_at=now()-interval '31 seconds' WHERE bot_id=$1 AND status='queued'`, bot.ID)
	dbfx.Exec(t, `INSERT INTO bot_wake_event(bot_id,workspace_id,event_type,issue_id,source_id,author_type,author_id,root_id,created_at) VALUES($1,$2,'started',$3,gen_random_uuid(),'member',$4,gen_random_uuid(),now()-interval '30 seconds')`, bot.ID, testWorkspaceID, issueID, testUserID)
	if worked, err := worker.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("filtered batch: %v %v", worked, err)
	}
	if len(body["events"].([]any)) != 1 {
		t.Fatal("disabled started event entered push")
	}
	var disabled int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM bot_wake_event WHERE bot_id=$1 AND event_type='started' AND status='pending' AND reason='event disabled'`, bot.ID).Scan(&disabled); err != nil || disabled != 1 {
		t.Fatalf("disabled=%d %v", disabled, err)
	}
	t.Log("mixed source batch: done delivered; disabled started kept pending")

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
