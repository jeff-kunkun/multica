package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Real local sockets and disposable Postgres; NOT an actual Grok endpoint.
func TestBotWakeLocalHTTP(t *testing.T) {
	ctx := context.Background()
	oldBox := testHandler.BotWakeSecrets
	testHandler.BotWakeSecrets, _ = NewBotWakeSecretBox("local-test-only")
	defer func() { testHandler.BotWakeSecrets = oldBox }()
	raw, hash, prefix, err := mintBotToken()
	if err != nil {
		t.Fatal(err)
	}
	bot, err := testHandler.Queries.CreateBotAccessToken(ctx, db.CreateBotAccessTokenParams{UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), Name: "wake acceptance", TokenHash: hash, TokenPrefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	defer testPool.Exec(ctx, `DELETE FROM personal_access_token WHERE id=$1`, bot.ID)
	id := uuidToString(bot.ID)
	issueID := dbfx.Issue(t, "Wake receipt test", testutil.Cols{"visibility": "workspace"})
	base := "/api/workspaces/" + testWorkspaceID + "/bots/" + id + "/wake"
	router := chi.NewRouter()
	router.Use(middleware.Auth(testHandler.Queries, nil, nil, nil))
	router.Route("/api/workspaces/{id}", func(r chi.Router) {
		r.Use(middleware.RequireWorkspaceMemberFromURL(testHandler.Queries, "id"))
		r.Get("/bots/mentions", testHandler.ListBotMentionDirectory)
		r.Get("/bots/{botId}/wake", testHandler.GetBotWake)
		r.Put("/bots/{botId}/wake", testHandler.PutBotWake)
		r.Get("/bots/{botId}/wake/events", testHandler.ListBotWakeEvents)
		r.Get("/bots/{botId}/wake/pending", testHandler.ListBotWakeEvents)
	})
	api := httptest.NewServer(router)
	defer api.Close()
	call := func(method, path, token string, body any) (int, []byte) {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, api.URL+path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := api.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		return res.StatusCode, data
	}
	code, data := call("GET", base, raw, nil)
	if code != 200 || !bytes.Contains(data, []byte(`"hourly_limit":6`)) {
		t.Fatalf("defaults %d %s", code, data)
	}
	t.Logf("GET defaults: %d %s", code, data)
	var calls atomic.Int32
	var response atomic.Int32
	response.Store(200)
	var slow atomic.Bool
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer local-fixture" {
			t.Error("missing authorization")
		}
		var payload map[string]any
		if json.NewDecoder(r.Body).Decode(&payload) != nil {
			t.Error("invalid payload")
		}
		if r.Header.Get("X-Multica-Event-ID") == "" || payload["summary"] == nil {
			t.Error("missing metadata")
		}
		if _, ok := payload["content"]; ok {
			t.Error("body leaked")
		}
		if slow.Load() {
			time.Sleep(150 * time.Millisecond)
		}
		w.WriteHeader(int(response.Load()))
	}))
	defer receiver.Close()
	t.Setenv("MULTICA_PLUGIN_DEV_ORIGINS", receiver.URL)
	worker := NewBotWakeWorker(testHandler)
	worker.client = func(u *url.URL) *http.Client { return &http.Client{Timeout: 50 * time.Millisecond} }
	quietOff := map[string]any{"enabled": false, "start": "23:00", "end": "08:00", "timezone": "Asia/Shanghai"}
	put := func(body any) {
		t.Helper()
		code, data := call("PUT", base, raw, body)
		if code != 200 {
			t.Fatalf("PUT %d %s", code, data)
		}
		if bytes.Contains(data, []byte("local-fixture")) || bytes.Contains(data, []byte(receiver.URL)) {
			t.Fatal("secret echoed")
		}
	}
	put(map[string]any{"webhook_url": receiver.URL, "webhook_key": "Bearer local-fixture", "quiet_hours": quietOff})
	stored, err := testHandler.Queries.GetBotWake(ctx, bot.ID)
	if err != nil || bytes.Contains(stored.WebhookSecret, []byte("local-fixture")) {
		t.Fatal("secret not encrypted")
	}
	t.Log("PUT credentials: 200; encrypted at rest; URL/key absent from response")
	comment := func() string {
		t.Helper()
		return dbfx.Insert(t, "comment", testutil.Cols{"workspace_id": testWorkspaceID, "issue_id": issueID, "author_type": "member", "author_id": testUserID, "content": "[@Bot](mention://bot/" + id + ") private body"})
	}
	process := func() {
		t.Helper()
		worked, err := worker.ProcessNext(ctx)
		if err != nil || !worked {
			t.Fatalf("worker %v %v", worked, err)
		}
	}
	state := func(source string) string {
		t.Helper()
		var status string
		if err := testPool.QueryRow(ctx, `SELECT status FROM bot_wake_event WHERE bot_id=$1 AND source_id=$2 ORDER BY sequence DESC LIMIT 1`, bot.ID, source).Scan(&status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	first := comment()
	process()
	if state(first) != "received" || calls.Load() != 1 {
		t.Fatal("not received")
	}
	t.Log("comment mention -> local HTTP 200 -> received; receiver calls=1")
	dbfx.Exec(t, `UPDATE comment SET content=content||' edited' WHERE id=$1`, first)
	worked, err := worker.ProcessNext(ctx)
	if err != nil || worked || calls.Load() != 1 {
		t.Fatal("duplicate mention resent")
	}
	t.Log("same source mention edited: no second receipt/send")
	put(map[string]any{"hourly_limit": 1})
	limited := comment()
	process()
	if state(limited) != "received" || calls.Load() != 2 {
		t.Fatal("mention must bypass hourly limit")
	}
	t.Log("hourly limit 1: mentions bypass quota")
	put(map[string]any{"hourly_limit": 120, "quiet_hours": map[string]any{"enabled": true, "start": "00:00", "end": "00:00", "timezone": "Asia/Shanghai"}})
	quiet := comment()
	process()
	if state(quiet) != "pending" {
		t.Fatal("quiet failed")
	}
	t.Log("quiet hours: pending, no send")
	dbfx.Exec(t, `UPDATE bot_wake_event SET acknowledged_at=now() WHERE bot_id=$1 AND source_id=$2`, bot.ID, quiet)
	put(map[string]any{"quiet_hours": quietOff, "events": []string{}})
	disabled := comment()
	process()
	if state(disabled) != "pending" {
		t.Fatal("disabled failed")
	}
	t.Log("disabled event: pending, no send")
	put(map[string]any{"events": []string{"mention", "started", "done", "blocked", "needs_kun", "task", "chat"}})
	response.Store(404)
	bad := comment()
	process()
	if state(bad) != "failed" {
		t.Fatal("404 failed")
	}
	t.Log("wrong HTTP path: failed HTTP 404")
	response.Store(503)
	retry := comment()
	for n := 0; n < 3; n++ {
		process()
		if n < 2 {
			dbfx.Exec(t, `UPDATE bot_wake_event SET next_attempt_at=now() WHERE bot_id=$1 AND source_id=$2`, bot.ID, retry)
		}
	}
	if state(retry) != "failed" {
		t.Fatal("retry not bounded")
	}
	t.Log("HTTP 503: exactly 3 attempts then failed; test advances backoff clock")
	response.Store(200)
	slow.Store(true)
	uncertain := comment()
	process()
	if state(uncertain) != "uncertain" {
		t.Fatal("timeout must be uncertain")
	}
	slow.Store(false)
	worked, err = worker.ProcessNext(ctx)
	if worked || err != nil {
		t.Fatal("uncertain retried")
	}
	t.Log("timeout after send: uncertain, no retry")
	tx, _ := testPool.Begin(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO comment(issue_id,workspace_id,author_type,author_id,content) VALUES($1,$2,'member',$3,$4)`, issueID, testWorkspaceID, testUserID, "[@Bot](mention://bot/"+id+")")
	if err != nil {
		t.Fatal(err)
	}
	tx.Rollback(ctx)
	worked, err = worker.ProcessNext(ctx)
	if worked || err != nil {
		t.Fatal("rolled back event persisted")
	}
	t.Log("source transaction rollback: no queued event")
	for _, status := range []string{"in_progress", "done", "blocked"} {
		dbfx.Exec(t, `UPDATE issue SET status=$2 WHERE id=$1`, issueID, status)
		process()
		if state(issueID) != "received" {
			t.Fatalf("lifecycle %s", status)
		}
	}
	summonID := dbfx.Insert(t, "issue_summon", testutil.Cols{"workspace_id": testWorkspaceID, "issue_id": issueID, "recipient_id": testUserID, "caller_type": "member", "caller_id": testUserID, "source": "test", "reason": "decision"})
	process()
	if state(summonID) != "received" {
		t.Fatal("summon failed")
	}
	t.Log("started/done/blocked: delivered with unknown attribution; needs_kun received")
	dbfx.Exec(t, `UPDATE issue SET assignee_type='member',assignee_id=$2 WHERE id=$1`, issueID, testUserID)
	process()
	if state(issueID) != "received" {
		t.Fatal("task assignment missing")
	}
	t.Log("watched issue assignment: delivered with unknown attribution")

	agentID := createHandlerTestAgent(t, "WakeChatAgent", []byte("[]"))
	chatID := createHandlerTestChatSession(t, agentID)
	message := dbfx.Insert(t, "chat_message", testutil.Cols{"chat_session_id": chatID, "role": "user", "sender_user_id": testUserID, "content": "[@Bot](mention://bot/" + id + ")"})
	process()
	if state(message) != "received" {
		t.Fatal("chat mention failed")
	}
	follow := dbfx.Insert(t, "chat_message", testutil.Cols{"chat_session_id": chatID, "role": "assistant", "content": "reply"})
	process()
	if state(follow) != "received" {
		t.Fatal("chat watch failed")
	}
	t.Log("chat mention and assistant reply without a task actor received")
	code, data = call("GET", base+"/events?limit=2", raw, nil)
	if code != 200 || !bytes.Contains(data, []byte(`"next_cursor":"`)) {
		t.Fatalf("pagination %d %s", code, data)
	}
	t.Logf("GET events page: %d %s", code, data)
	code, data = call("GET", base+"/pending?limit=100", raw, nil)
	if code != 200 || !bytes.Contains(data, []byte(`"uncertain"`)) {
		t.Fatal("pending missing")
	}
	t.Log("pending contains disabled/quiet/limited/failed/uncertain receipts")
	raw2, hash2, prefix2, _ := mintBotToken()
	other, err := testHandler.Queries.CreateBotAccessToken(ctx, db.CreateBotAccessTokenParams{UserID: bot.UserID, WorkspaceID: bot.WorkspaceID, Name: "other", TokenHash: hash2, TokenPrefix: prefix2})
	if err != nil {
		t.Fatal(err)
	}
	defer testPool.Exec(ctx, `DELETE FROM personal_access_token WHERE id=$1`, other.ID)
	code, _ = call("GET", base, raw2, nil)
	if code != 403 {
		t.Fatalf("cross bot=%d", code)
	}
	code, _ = call("PUT", base, raw2, map[string]any{"hourly_limit": 120})
	if code != 403 {
		t.Fatal("cross bot write")
	}
	t.Log("other Bot with same owner: GET/PUT 403")
	memberID := dbfx.User(t, "Wake member", "wake-member@example.invalid")
	dbfx.Insert(t, "member", testutil.Cols{"workspace_id": testWorkspaceID, "user_id": memberID, "role": "member"})
	memberKey := insertTestPATFor(t, memberID)
	code, _ = call("GET", base, memberKey, nil)
	if code != 403 {
		t.Fatal("member settings access")
	}
	code, _ = call("GET", base+"/events", memberKey, nil)
	if code != 403 {
		t.Fatal("member events access")
	}
	code, _ = call("PUT", base, raw, map[string]any{"hourly_limit": 121})
	if code != 400 {
		t.Fatal("invalid limit accepted")
	}
	code, _ = call("PUT", base, raw, map[string]any{"webhook_url": "http://127.0.0.1:1/private", "webhook_key": "fixture"})
	if code != 400 {
		t.Fatal("private endpoint accepted")
	}
	t.Log("ordinary member settings/events:403; invalid rules/private endpoint:400")

	human := insertTestPATFor(t, testUserID)
	code, _ = call("GET", base, human, nil)
	if code != 200 {
		t.Fatal("human owner denied")
	}
	outsider := dbfx.Insert(t, "user", testutil.Cols{"name": "outsider", "email": "wake-outsider@example.invalid"})
	dbfx.Exec(t, `UPDATE chat_session SET creator_id=$2,visibility='private' WHERE id=$1`, chatID, outsider)
	hidden := dbfx.Insert(t, "chat_message", testutil.Cols{"chat_session_id": chatID, "role": "user", "sender_user_id": testUserID, "content": "hidden"})
	process()
	if state(hidden) != "pending" {
		t.Fatal("private chat sent")
	}
	code, data = call("GET", base+"/events?limit=100", raw, nil)
	if code != 200 || bytes.Contains(data, []byte(chatID)) {
		t.Fatal("private chat leaked")
	}
	t.Log("revoked chat access: no send; prior receipts hidden")
	code, data = call("GET", base+"/pending?limit=1", raw, nil)
	var filtered struct {
		Events []any  `json:"events"`
		Next   string `json:"next_cursor"`
	}
	if json.Unmarshal(data, &filtered) != nil || code != 200 || len(filtered.Events) != 0 || filtered.Next == "" {
		t.Fatalf("filtered empty page lost cursor: %d %s", code, data)
	}
	code, data = call("GET", base+"/pending?limit=100&before="+filtered.Next, raw, nil)
	if code != 200 || !bytes.Contains(data, []byte(`"event_id"`)) {
		t.Fatalf("filtered pagination lost older events: %s", data)
	}
	t.Log("permission-filtered empty page keeps next_cursor; next page returns authorized pending events")
	code, data = call("GET", "/api/workspaces/"+testWorkspaceID+"/bots/mentions?issue_id="+issueID, human, nil)
	if code != 200 || !bytes.Contains(data, []byte(id)) || bytes.Contains(data, []byte("token_")) {
		t.Fatal("directory failed")
	}
	t.Log("mention directory: identities only, no token metadata")
	crash := comment()
	dbfx.Exec(t, `UPDATE bot_wake_event SET status='sending',sent_at=now()-interval '3 minutes' WHERE bot_id=$1 AND source_id=$2`, bot.ID, crash)
	if err := testHandler.Queries.RecoverBotWakeUncertain(ctx); err != nil {
		t.Fatal(err)
	}
	if state(crash) != "uncertain" {
		t.Fatal("crash recovery failed")
	}
	t.Log("worker interruption: sending -> uncertain")
}

func TestBotWakeAttachmentSHA256(t *testing.T) {
	t.Setenv("LOCAL_UPLOAD_DIR", t.TempDir())
	t.Setenv("LOCAL_UPLOAD_BASE_URL", "")
	old := testHandler.Storage
	testHandler.Storage = storage.NewLocalStorageFromEnv()
	defer func() { testHandler.Storage = old }()
	data := []byte("verified attachment\n")
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	for _, good := range []bool{true, false} {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		part, _ := mw.CreateFormFile("file", "sample.txt")
		part.Write(data)
		expected := digest
		if !good {
			expected = strings.Repeat("0", 64)
		}
		mw.WriteField("sha256", expected)
		mw.Close()
		req := httptest.NewRequest("POST", "/api/upload-file", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("X-User-ID", testUserID)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		rec := httptest.NewRecorder()
		testHandler.UploadFile(rec, req)
		if !good {
			if rec.Code != 400 || !strings.Contains(rec.Body.String(), "sha256 mismatch") {
				t.Fatalf("bad sample %d %s", rec.Code, rec.Body)
			}
			t.Logf("bad upload: %d %s", rec.Code, rec.Body)
			continue
		}
		if rec.Code != 200 {
			t.Fatalf("good sample %d %s", rec.Code, rec.Body)
		}
		var att AttachmentResponse
		json.Unmarshal(rec.Body.Bytes(), &att)
		if att.SHA256 != digest {
			t.Fatal("server digest missing")
		}
		stored, err := testHandler.Queries.GetAttachmentByIDOnly(context.Background(), parseUUID(att.ID))
		if err != nil || stored.Sha256.String != digest {
			t.Fatal("digest not saved")
		}
		t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM attachment WHERE id=$1`, att.ID) })
		dreq, dw := newDownloadRequest(t, att.ID, testWorkspaceID)
		testHandler.DownloadAttachment(dw, dreq)
		if dw.Code != 200 || !bytes.Equal(dw.Body.Bytes(), data) {
			t.Fatal("download failed")
		}
		t.Logf("good upload/download: 200 sha256=%s bytes=%d", digest, len(data))
		dbfx.Exec(t, `UPDATE attachment SET sha256=NULL WHERE id=$1`, att.ID)
		mreq, metaW := newDownloadRequest(t, att.ID, testWorkspaceID)
		testHandler.GetAttachmentByID(metaW, mreq)
		if metaW.Code != 200 || !strings.Contains(metaW.Body.String(), digest) {
			t.Fatalf("legacy backfill %d %s", metaW.Code, metaW.Body)
		}
		t.Log("legacy metadata GET: checksum backfilled from authorized storage")

	}
}

func TestBotWakeOwnerFollowsHTTP(t *testing.T) {
	ctx := context.Background()
	oldBox := testHandler.BotWakeSecrets
	testHandler.BotWakeSecrets, _ = NewBotWakeSecretBox("local-test-only")
	defer func() { testHandler.BotWakeSecrets = oldBox }()
	raw, hash, prefix, err := mintBotToken()
	if err != nil {
		t.Fatal(err)
	}
	bot, err := testHandler.Queries.CreateBotAccessToken(ctx, db.CreateBotAccessTokenParams{UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), Name: "wake acceptance", TokenHash: hash, TokenPrefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	defer testPool.Exec(ctx, `DELETE FROM personal_access_token WHERE id=$1`, bot.ID)
	id := uuidToString(bot.ID)
	issueID := dbfx.Issue(t, "Wake receipt test", testutil.Cols{"visibility": "workspace"})
	base := "/api/workspaces/" + testWorkspaceID + "/bots/" + id + "/wake"
	router := chi.NewRouter()
	router.Use(middleware.Auth(testHandler.Queries, nil, nil, nil))
	router.Route("/api/workspaces/{id}", func(r chi.Router) {
		r.Use(middleware.RequireWorkspaceMemberFromURL(testHandler.Queries, "id"))
		r.Get("/bots/mentions", testHandler.ListBotMentionDirectory)
		r.Get("/bots/{botId}/wake", testHandler.GetBotWake)
		r.Put("/bots/{botId}/wake", testHandler.PutBotWake)
		r.Get("/bots/{botId}/wake/events", testHandler.ListBotWakeEvents)
		r.Get("/bots/{botId}/wake/pending", testHandler.ListBotWakeEvents)
	})
	api := httptest.NewServer(router)
	defer api.Close()
	call := func(method, path, token string, body any) (int, []byte) {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, api.URL+path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := api.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		return res.StatusCode, data
	}
	code, data := call("GET", base, raw, nil)
	if code != 200 || !bytes.Contains(data, []byte(`"hourly_limit":6`)) {
		t.Fatalf("defaults %d %s", code, data)
	}
	t.Logf("GET defaults: %d %s", code, data)
	var calls atomic.Int32
	var response atomic.Int32
	response.Store(200)
	var slow atomic.Bool
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer local-fixture" {
			t.Error("missing authorization")
		}
		var payload map[string]any
		if json.NewDecoder(r.Body).Decode(&payload) != nil {
			t.Error("invalid payload")
		}
		if r.Header.Get("X-Multica-Event-ID") == "" || payload["summary"] == nil {
			t.Error("missing metadata")
		}
		if _, ok := payload["content"]; ok {
			t.Error("body leaked")
		}
		if slow.Load() {
			time.Sleep(150 * time.Millisecond)
		}
		w.WriteHeader(int(response.Load()))
	}))
	defer receiver.Close()
	t.Setenv("MULTICA_PLUGIN_DEV_ORIGINS", receiver.URL)
	worker := NewBotWakeWorker(testHandler)
	worker.client = func(u *url.URL) *http.Client { return &http.Client{Timeout: 50 * time.Millisecond} }
	quietOff := map[string]any{"enabled": false, "start": "23:00", "end": "08:00", "timezone": "Asia/Shanghai"}
	put := func(body any) {
		t.Helper()
		code, data := call("PUT", base, raw, body)
		if code != 200 {
			t.Fatalf("PUT %d %s", code, data)
		}
		if bytes.Contains(data, []byte("local-fixture")) || bytes.Contains(data, []byte(receiver.URL)) {
			t.Fatal("secret echoed")
		}
	}
	put(map[string]any{"webhook_url": receiver.URL, "webhook_key": "Bearer local-fixture", "quiet_hours": quietOff})
	if !bytes.Contains(data, []byte(`"inherit_owner_follows":false`)) {
		t.Fatal("default inheritance not false")
	}
	dbfx.Exec(t, `INSERT INTO issue_subscriber(issue_id,user_type,user_id,reason) VALUES($1,'member',$2,'manual') ON CONFLICT DO NOTHING`, issueID, testUserID)
	change := func(status string) {
		t.Helper()
		dbfx.Exec(t, `WITH actor AS MATERIALIZED (SELECT set_config('multica.actor_type','member',true),set_config('multica.actor_id',$3,true)) UPDATE issue SET status=$2 FROM actor WHERE issue.id=$1`, issueID, status, testUserID)
	}
	count := func() int {
		var n int
		dbfx.QueryRow(t, `SELECT count(*) FROM bot_wake_event WHERE bot_id=$1`, bot.ID).Scan(&n)
		return n
	}
	drain := func() {
		t.Helper()
		for i := 0; i < 20; i++ {
			ok, err := worker.ProcessNext(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				return
			}
		}
		t.Fatal("did not drain")
	}
	change("in_progress")
	if count() != 0 {
		t.Fatal("default inherited")
	}
	put(map[string]any{"inherit_owner_follows": true})
	code, data = call("GET", base, raw, nil)
	if code != 200 || !bytes.Contains(data, []byte(`"inherit_owner_follows":true`)) {
		t.Fatalf("saved %d %s", code, data)
	}
	change("done")
	drain()
	if count() != 1 || calls.Load() != 1 {
		t.Fatalf("enabled subscription not delivered: events=%d sent=%d", count(), calls.Load())
	}
	// Inheritance does not make a persistent explicit Bot watch.
	var watches int
	dbfx.QueryRow(t, `SELECT count(*) FROM bot_wake_watch WHERE bot_id=$1`, bot.ID).Scan(&watches)
	if watches != 0 {
		t.Fatal("inheritance promoted to permanent watch")
	}
	change("blocked")
	put(map[string]any{"inherit_owner_follows": false})
	drain()
	var reason string
	dbfx.QueryRow(t, `SELECT reason FROM bot_wake_event WHERE bot_id=$1 ORDER BY sequence DESC LIMIT 1`, bot.ID).Scan(&reason)
	if reason != "owner inheritance disabled" || calls.Load() != 1 {
		t.Fatalf("disable queued: %s", reason)
	}
	change("in_progress")
	if count() != 2 {
		t.Fatal("disabled creates new inherited event")
	}
	put(map[string]any{"inherit_owner_follows": true})
	change("done")
	dbfx.Exec(t, `DELETE FROM issue_subscriber WHERE issue_id=$1 AND user_id=$2`, issueID, testUserID)
	drain()
	dbfx.QueryRow(t, `SELECT reason FROM bot_wake_event WHERE bot_id=$1 ORDER BY sequence DESC LIMIT 1`, bot.ID).Scan(&reason)
	if reason != "owner no longer follows source" {
		t.Fatal(reason)
	}
	dbfx.Exec(t, `UPDATE issue SET assignee_type='member',assignee_id=$2 WHERE id=$1`, issueID, testUserID)
	drain() // Unattributed assignments now deliver with an explicit unknown actor.
	change("blocked")
	drain()
	if calls.Load() != 3 {
		t.Fatalf("owner assignment missing: %d", calls.Load())
	}
	// Current private-source permission still wins over subscription inheritance.
	stranger := dbfx.User(t, "private creator", "wake-private@example.invalid")
	dbfx.Exec(t, `UPDATE issue SET visibility='private',creator_type='member',creator_id=$2,assignee_id=NULL,assignee_type=NULL WHERE id=$1`, issueID, stranger)
	dbfx.Exec(t, `INSERT INTO issue_subscriber(issue_id,user_type,user_id,reason) VALUES($1,'member',$2,'manual')`, issueID, testUserID)
	change("done")
	drain()
	dbfx.QueryRow(t, `SELECT reason FROM bot_wake_event WHERE bot_id=$1 ORDER BY sequence DESC LIMIT 1`, bot.ID).Scan(&reason)
	if reason != "access unavailable" || calls.Load() != 3 {
		t.Fatalf("inheritance permission: %s sent=%d", reason, calls.Load())
	}
	if bin := os.Getenv("BOT_WAKE_TEST_CLI"); bin != "" {
		cmd := exec.Command(bin, "bot", "wake", "set", id, "--config-stdin")
		for _, e := range os.Environ() {
			if !strings.HasPrefix(e, "MULTICA_") {
				cmd.Env = append(cmd.Env, e)
			}
		}
		cmd.Env = append(cmd.Env, "MULTICA_SERVER_URL="+api.URL, "MULTICA_TOKEN="+raw, "MULTICA_WORKSPACE_ID="+testWorkspaceID, "MULTICA_PROFILE_DIR="+t.TempDir())
		cmd.Dir = t.TempDir()
		cmd.Stdin = strings.NewReader(`{"inherit_owner_follows":false}`)
		output, err := cmd.CombinedOutput()
		t.Logf("CLI set inheritance: %s error=%v", output, err)
		if err != nil || !bytes.Contains(output, []byte(`"inherit_owner_follows": false`)) {
			t.Fatal("CLI inheritance failed")
		}
	}
	t.Log("inheritance default false; enabled subscription/assignment delivered; disabling/unfollowing queued events holds pending; private source denied")
}
