package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestBotWakeTrustedChainHTTP(t *testing.T) {
	ctx := context.Background()
	oldBox := testHandler.BotWakeSecrets
	testHandler.BotWakeSecrets, _ = NewBotWakeSecretBox("chain-fixture")
	defer func() { testHandler.BotWakeSecrets = oldBox }()
	newBot := func(name string) (string, db.PersonalAccessToken) {
		t.Helper()
		raw, hash, prefix, _ := mintBotToken()
		b, err := testHandler.Queries.CreateBotAccessToken(ctx, db.CreateBotAccessTokenParams{UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), Name: name, TokenHash: hash, TokenPrefix: prefix})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM personal_access_token WHERE id=$1`, b.ID) })
		return raw, b
	}
	aToken, a := newBot("chain A")
	bToken, b := newBot("chain B")
	human := insertTestPATFor(t, testUserID)
	issue := createMentionFixtureIssue(t, ctx, "Trusted chain")
	// Use a different creator: status events must identify the current writer.
	other := dbfx.User(t, "Other creator", "wake-other@example.invalid")
	dbfx.Exec(t, `UPDATE issue SET creator_id=$2,visibility='workspace' WHERE id=$1`, issue, other)
	var deliveries []map[string]any
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		json.NewDecoder(r.Body).Decode(&v)
		deliveries = append(deliveries, v)
		w.WriteHeader(200)
	}))
	defer receiver.Close()
	t.Setenv("MULTICA_PLUGIN_DEV_ORIGINS", receiver.URL)
	worker := NewBotWakeWorker(testHandler)
	worker.client = func(*url.URL) *http.Client { return receiver.Client() }
	configure := func(bot db.PersonalAccessToken) {
		plain, _ := json.Marshal(botWakeSecrets{URL: receiver.URL, Key: "fixture"})
		sealed, _ := testHandler.BotWakeSecrets.Seal(plain)
		dbfx.Exec(t, `UPDATE bot_wake SET webhook_secret=$2,quiet_hours=jsonb_set(quiet_hours,'{enabled}','false'),hourly_limit=120 WHERE bot_id=$1`, bot.ID, sealed)
	}
	configure(a)
	configure(b)
	router := chi.NewRouter()
	router.Use(middleware.Auth(testHandler.Queries, nil, nil, nil))
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { next.ServeHTTP(w, withChatTestWorkspaceCtx(t, r)) })
	})
	router.Post("/issues/{id}/comments", testHandler.CreateComment)
	router.Patch("/issues/{id}", testHandler.UpdateIssue)
	router.Route("/api/workspaces/{id}", func(r chi.Router) {
		r.Get("/bots/mentions", testHandler.ListBotMentionDirectory)
		r.Get("/bots/{botId}/wake", testHandler.GetBotWake)
		r.Get("/bots/{botId}/wake/events", testHandler.ListBotWakeEvents)
		r.Get("/bots/{botId}/wake/pending", testHandler.ListBotWakeEvents)
		r.Post("/bots/{botId}/wake/events/{eventId}/woken", testHandler.RecordBotWakeReceipt)
		r.Post("/bots/{botId}/wake/events/{eventId}/result", testHandler.RecordBotWakeReceipt)
		r.Post("/bots/{botId}/wake/pending/ack", testHandler.AcknowledgeBotWakePending)
	})
	api := httptest.NewServer(router)
	defer api.Close()
	call := func(method, path, token string, body any) {
		t.Helper()
		data, _ := json.Marshal(body)
		r, _ := http.NewRequest(method, api.URL+path, bytes.NewReader(data))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Actor-Bot-ID", uuidToString(b.ID))
		r.Header.Set("X-Wake-Hop", "0")
		res, err := api.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		if res.StatusCode >= 300 {
			t.Fatalf("%s %d %s", path, res.StatusCode, out)
		}
		t.Logf("%s %s => %d", method, path, res.StatusCode)
	}
	comment := func(token string, target db.PersonalAccessToken) {
		call("POST", "/issues/"+issue+"/comments", token, map[string]any{"content": "[@target](mention://bot/" + uuidToString(target.ID) + ")", "hop": 0})
	}
	drain := func() {
		t.Helper()
		for i := 0; i < 30; i++ {
			worked, err := worker.ProcessNext(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !worked {
				return
			}
		}
		t.Fatal("worker did not drain")
	}
	comment(human, a)
	drain()
	if len(deliveries) != 1 || deliveries[0]["hop"] != float64(1) {
		t.Fatalf("first wake %v", deliveries)
	}
	author := deliveries[0]["author"].(map[string]any)
	if author["type"] != "member" || author["id"] != testUserID {
		t.Fatal("forged Bot header trusted")
	}
	comment(aToken, a)
	drain()
	if len(deliveries) != 1 {
		t.Fatal("self woke")
	}
	comment(aToken, b)
	drain()
	if len(deliveries) != 1 {
		t.Fatal("default bot none ignored")
	}
	dbfx.Exec(t, `UPDATE bot_wake SET wake_from=jsonb_set(wake_from,'{bot,mode}','"all"') WHERE bot_id IN ($1,$2)`, a.ID, b.ID)
	comment(aToken, b)
	drain()
	if len(deliveries) != 2 || deliveries[1]["hop"] != float64(2) {
		t.Fatalf("cross Bot %v", deliveries)
	}
	agent := createHandlerTestAgent(t, "ChainAgent", []byte("[]"))
	task := dbfx.Task(t, agent, testutil.Cols{"issue_id": issue, "via_bot_id": uuidToString(b.ID), "via_bot_name": "chain B", "originator_user_id": testUserID, "accountable_user_id": testUserID, "status": "running", "runtime_id": testRuntimeID})
	var taskHop int
	dbfx.QueryRow(t, `SELECT wake_hop FROM agent_task_queue WHERE id=$1`, task).Scan(&taskHop)
	if taskHop != 3 {
		t.Fatalf("agent hop=%d", taskHop)
	}
	token := "mat_" + task
	_, err := testHandler.Queries.CreateTaskToken(ctx, db.CreateTaskTokenParams{TokenHash: auth.HashToken(token), TaskID: parseUUID(task), AgentID: parseUUID(agent), WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(testUserID), ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM task_token WHERE task_id=$1`, task) })
	comment(token, a)
	drain()
	if len(deliveries) != 3 || deliveries[2]["hop"] != float64(4) {
		t.Fatalf("agent reply chain %v", deliveries)
	}
	// New worker instance reads durable cause and cap; no client hop is needed.
	worker = NewBotWakeWorker(testHandler)
	worker.client = func(*url.URL) *http.Client { return receiver.Client() }
	comment(aToken, b)
	drain()
	if len(deliveries) != 3 {
		t.Fatal("fifth wake escaped cap after restart")
	}
	var reason string
	dbfx.QueryRow(t, `SELECT reason FROM bot_wake_event WHERE bot_id=$1 ORDER BY sequence DESC LIMIT 1`, b.ID).Scan(&reason)
	if reason != "bot wake chain limit (4)" {
		t.Fatalf("cap reason %s", reason)
	}
	// Bot -> agent over the same cap is stored as cancelled and cannot dispatch.
	capped := dbfx.Task(t, agent, testutil.Cols{"issue_id": issue, "via_bot_id": uuidToString(a.ID), "originator_user_id": testUserID, "accountable_user_id": testUserID})
	var status string
	dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id=$1`, capped).Scan(&status)
	if status != "cancelled" {
		t.Fatalf("agent cap %s", status)
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='queued' WHERE id=$1`, capped)
	dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id=$1`, capped).Scan(&status)
	if status != "cancelled" {
		t.Fatal("requeue escaped cap")
	}
	call("PATCH", "/issues/"+issue, bToken, map[string]any{"status": "in_progress"})
	var atype, aid, via string
	dbfx.QueryRow(t, `SELECT author_type,author_id::text,via_bot_id::text FROM bot_wake_event WHERE issue_id=$1 AND event_type='started' ORDER BY sequence DESC LIMIT 1`, issue).Scan(&atype, &aid, &via)
	if atype != "bot" || aid != uuidToString(b.ID) || via != aid {
		t.Fatal("status actor was not actual Bot")
	}
	drain()
	if bin := os.Getenv("BOT_WAKE_TEST_CLI"); bin != "" {
		runCLI := func(token string, args ...string) bool {
			t.Helper()
			cmd := exec.Command(bin, args...)
			var cleanEnv []string
			for _, item := range os.Environ() {
				if !strings.HasPrefix(item, "MULTICA_") {
					cleanEnv = append(cleanEnv, item)
				}
			}
			cmd.Dir = t.TempDir()
			cmd.Env = append(cleanEnv, "MULTICA_SERVER_URL="+api.URL, "MULTICA_TOKEN="+token, "MULTICA_WORKSPACE_ID="+testWorkspaceID, "MULTICA_PROFILE_DIR="+t.TempDir())
			out, err := cmd.CombinedOutput()
			t.Logf("CLI %v exit_error=%v stdout/stderr=%s", args, err, out)
			return err == nil
		}
		aid := uuidToString(a.ID)
		eid := deliveries[0]["event_id"].(string)
		for _, args := range [][]string{{"bot", "mentions", "--issue", issue}, {"bot", "wake", "get", aid}, {"bot", "wake", "events", aid}, {"bot", "wake", "woken", aid, eid}, {"bot", "wake", "result", aid, eid, "--result-url", testHandler.cfg.PublicURL + "/api/issues/" + issue}, {"bot", "wake", "pending", aid}} {
			if !runCLI(aToken, args...) {
				t.Fatal("CLI integration failed")
			}
		}
		if runCLI(bToken, "bot", "wake", "woken", aid, eid) {
			t.Fatal("another Bot acknowledged event")
		}
		var pending string
		dbfx.QueryRow(t, `SELECT event_id::text FROM bot_wake_event WHERE bot_id=$1 AND status='pending' ORDER BY sequence DESC LIMIT 1`, a.ID).Scan(&pending)
		if !runCLI(aToken, "bot", "wake", "ack", aid, pending) {
			t.Fatal("CLI ack failed")
		}
	} else {
		t.Log("CLI subprocess verification not requested; set BOT_WAKE_TEST_CLI to a built binary")
	}
	t.Logf("deliveries=%d hops=1,2,4; agent hop=3; self/default-none/fifth wake suppressed; durable cap survives worker recreation", len(deliveries))
}
