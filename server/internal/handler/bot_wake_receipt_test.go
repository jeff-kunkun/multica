package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestBotWakeReceiptsAndStableScan(t *testing.T) {
	ctx := context.Background()
	raw, hash, prefix, err := mintBotToken()
	if err != nil {
		t.Fatal(err)
	}
	bot, err := testHandler.Queries.CreateBotAccessToken(ctx, db.CreateBotAccessTokenParams{UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), Name: "receipt fixture", TokenHash: hash, TokenPrefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	defer testPool.Exec(ctx, `DELETE FROM personal_access_token WHERE id=$1`, bot.ID)
	issue := dbfx.Issue(t, "Receipt source", testutil.Cols{"visibility": "workspace"})
	base := "/api/workspaces/" + testWorkspaceID + "/bots/" + uuidToString(bot.ID) + "/wake"
	router := chi.NewRouter()
	router.Use(middleware.Auth(testHandler.Queries, nil, nil, nil))
	router.Route("/api/workspaces/{id}", func(r chi.Router) {
		r.Use(middleware.RequireWorkspaceMemberFromURL(testHandler.Queries, "id"))
		r.Get("/bots/{botId}/wake/pending", testHandler.ListBotWakeEvents)
		r.Post("/bots/{botId}/wake/pending/ack", testHandler.AcknowledgeBotWakePending)
		r.Post("/bots/{botId}/wake/events/{eventId}/woken", testHandler.RecordBotWakeReceipt)
		r.Post("/bots/{botId}/wake/events/{eventId}/result", testHandler.RecordBotWakeReceipt)
	})
	api := httptest.NewServer(router)
	defer api.Close()
	call := func(method, path string, body any) (int, []byte) {
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
		t.Logf("%s %s => %d %s", method, path, res.StatusCode, data)
		return res.StatusCode, data
	}
	insert := func(status string) (string, int64) {
		t.Helper()
		var id string
		var seq int64
		err := testPool.QueryRow(ctx, `INSERT INTO bot_wake_event(bot_id,workspace_id,event_type,issue_id,source_id,author_type,author_id,status) VALUES($1,$2,'chat',$3,$3,'member',$4,$5) RETURNING event_id::text,sequence`, bot.ID, testWorkspaceID, issue, testUserID, status).Scan(&id, &seq)
		if err != nil {
			t.Fatal(err)
		}
		return id, seq
	}
	old, oldSeq := insert("queued")
	newID, _ := insert("pending")
	code, page := call("GET", base+"/pending?limit=1", nil)
	if code != 200 || !bytes.Contains(page, []byte(newID)) {
		t.Fatal("missing pending")
	}
	if code, _ = call("POST", base+"/pending/ack", map[string]any{"event_ids": []string{newID, old}}); code != 409 {
		t.Fatal("batch must fail atomically")
	}
	_, page = call("GET", base+"/pending", nil)
	if !bytes.Contains(page, []byte(newID)) {
		t.Fatal("partial acknowledgement")
	}
	for i := 0; i < 2; i++ {
		if code, _ = call("POST", base+"/pending/ack", map[string]any{"event_ids": []string{newID}}); code != 200 {
			t.Fatal("idempotent ack")
		}
	}
	dbfx.Exec(t, `UPDATE bot_wake_event SET status='failed' WHERE sequence=$1`, oldSeq)
	// A new consumer/process needs no volatile cursor state: repeat the stable
	// unacknowledged scan after exhaustion, including old rows that failed late.
	_, page = call("GET", base+"/pending", nil)
	if !bytes.Contains(page, []byte(old)) || bytes.Contains(page, []byte(newID)) {
		t.Fatal("late failure lost")
	}
	resultPath := base + "/events/" + old + "/result"
	if code, _ = call("POST", resultPath, map[string]any{"result_url": "https://unrelated.invalid/private"}); code != 400 {
		t.Fatal("unrelated result accepted")
	}
	expected := testHandler.cfg.PublicURL + "/api/issues/" + issue
	code, first := call("POST", resultPath, map[string]any{"result_url": expected})
	if code != 200 {
		t.Fatal("result rejected")
	}
	code, second := call("POST", resultPath, map[string]any{"result_url": expected})
	if code != 200 || !bytes.Equal(first, second) {
		t.Fatal("result not idempotent")
	}
	if code, _ = call("POST", base+"/events/"+old+"/woken", map[string]any{}); code != 200 {
		t.Fatal("late woken rejected")
	}
	var state string
	var result, woken bool
	dbfx.QueryRow(t, `SELECT status,result_at IS NOT NULL,woken_at IS NOT NULL FROM bot_wake_event WHERE sequence=$1`, oldSeq).Scan(&state, &result, &woken)
	if state != "failed" || !result || !woken {
		t.Fatal("receipt changed transport or regressed result")
	}
	// Late commit below a newer sequence is discovered by the next full scan.
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var delayed string
	err = tx.QueryRow(ctx, `INSERT INTO bot_wake_event(bot_id,workspace_id,event_type,issue_id,source_id,status) VALUES($1,$2,'chat',$3,$3,'pending') RETURNING event_id::text`, bot.ID, testWorkspaceID, issue).Scan(&delayed)
	if err != nil {
		t.Fatal(err)
	}
	newer, _ := insert("pending")
	_, page = call("GET", base+"/pending?limit=1", nil)
	if !bytes.Contains(page, []byte(newer)) {
		t.Fatal("newer missing")
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	_, page = call("GET", base+"/pending", nil)
	if !bytes.Contains(page, []byte(delayed)) {
		t.Fatal("late commit lost")
	}
	t.Log("stable unacknowledged scan covers late failure and out-of-order commit; receipts remain separate and idempotent")
}

func TestBotWakeMentionGrammar(t *testing.T) {
	mention := "[@Bot](mention://bot/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa)"
	samples := []string{mention, "`" + mention + "`", "``" + mention + "``", "> " + mention, "    " + mention, "\t" + mention, "```go\n" + mention + "\n```\n" + mention, "~~~\n" + mention + "\n~~~", "before `unclosed " + mention}
	for _, s := range samples {
		var got string
		dbfx.QueryRow(t, `SELECT bot_wake_mention_text($1)`, s).Scan(&got)
		if want := util.MentionText(s); got != want {
			t.Fatalf("parser mismatch %q: SQL %q Go %q", s, got, want)
		}
		t.Logf("mention grammar %q -> %q", s, got)
	}
}

func TestBotWakeEncryptionReplica(t *testing.T) {
	first, _ := NewBotWakeSecretBox("test-stable-key")
	second, _ := NewBotWakeSecretBox("test-stable-key")
	wrong, _ := NewBotWakeSecretBox("test-rotated-key")
	sealed, err := first.Seal([]byte("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := second.Open(sealed)
	if err != nil || string(plain) != "fixture" {
		t.Fatal("replica could not decrypt")
	}
	if _, err = wrong.Open(sealed); err == nil {
		t.Fatal("wrong key accepted")
	}
	t.Log("independent instances with stable key decrypt; changed key fails closed")
}
