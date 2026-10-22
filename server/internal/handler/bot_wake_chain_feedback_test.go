package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type botChainChatFixture struct {
	agent, session, botToken, humanToken string
	bot                                  db.PersonalAccessToken
	router                               http.Handler
}

func newBotChainChatFixture(t *testing.T) botChainChatFixture {
	t.Helper()
	ctx := context.Background()
	f := botChainChatFixture{}
	f.agent, f.session, _, _ = setupDirectChatSession(t, ctx, "unanswered bot wakes")
	// Runtime-guard helpers derive their initial name from t.Name(). Free that
	// name so one test can own several independent Bot/chat fixtures.
	dbfx.Exec(t, `UPDATE agent SET name=name||'-'||id::text WHERE id=$1`, f.agent)
	raw, hash, prefix, err := mintBotToken()
	if err != nil {
		t.Fatal(err)
	}
	f.bot, err = testHandler.Queries.CreateBotAccessToken(ctx, db.CreateBotAccessTokenParams{UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), Name: "wake feedback bot", TokenHash: hash, TokenPrefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	f.botToken = raw
	f.humanToken = insertTestPATFor(t, testUserID)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM bot_wake_block WHERE bot_id=$1`, f.bot.ID)
		testPool.Exec(context.Background(), `DELETE FROM personal_access_token WHERE id=$1`, f.bot.ID)
	})
	router := chi.NewRouter()
	router.Use(middleware.Auth(testHandler.Queries, nil, nil, nil))
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { next.ServeHTTP(w, withChatTestWorkspaceCtx(t, r)) })
	})
	router.Post("/chats/{sessionId}/messages", testHandler.SendChatMessage)
	router.Post("/api/chat/sessions/{sessionId}/messages", testHandler.SendChatMessage)
	router.Post("/issues/{id}/comments", testHandler.CreateComment)
	f.router = router
	return f
}

func (f botChainChatFixture) send(t *testing.T, token, content, quote string, code int) SendChatMessageResponse {
	t.Helper()
	// Budget/refund tests deliberately request separate executions.
	return f.sendMode(t, token, content, quote, "queue", code)
}

func (f botChainChatFixture) sendMode(t *testing.T, token, content, quote, mode string, code int) SendChatMessageResponse {
	t.Helper()
	body := map[string]any{"content": content, "per_quote": quote}
	if mode != "" {
		body["mode"] = mode
	}
	r := newRequest("POST", "/chats/"+f.session+"/messages", body)
	r.Header.Del("X-User-ID")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	if w.Code != code {
		t.Fatalf("send: status=%d want=%d body=%s", w.Code, code, w.Body)
	}
	var out SendChatMessageResponse
	if code < 300 {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.TaskID == "" || out.MessageID == "" {
			t.Fatalf("send lost persisted IDs: %+v", out)
		}
	}
	return out
}

func assertBotChainHops(t *testing.T, f botChainChatFixture, want int) {
	t.Helper()
	var got int
	if err := testPool.QueryRow(context.Background(), `SELECT c.hops FROM bot_wake_chain c JOIN bot_wake_cause b ON b.root_id=c.root_id WHERE b.bot_id=$1`, f.bot.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("unanswered wake budget=%d want=%d", got, want)
	}
}

func requireBotChainAccepted(t *testing.T, out SendChatMessageResponse) {
	t.Helper()
	if (out.Mode != "start" && out.Mode != "queue") || out.Warning != "" || out.ReasonCode != "" {
		t.Fatalf("unexpected wake refusal: %+v", out)
	}
}

func TestBotWakeDefaultChatMergeConsumesOneWake(t *testing.T) {
	f := newBotChainChatFixture(t)
	first := f.sendMode(t, f.botToken, "first instruction", "", "", http.StatusCreated)
	requireBotChainAccepted(t, first)
	for i := 0; i < 3; i++ {
		out := f.sendMode(t, f.botToken, fmt.Sprintf("supplement %d", i), "", "", http.StatusCreated)
		if out.Mode != "merge" || out.TaskID != first.TaskID || out.Warning != "" || out.ReasonCode != "" {
			t.Fatalf("default Bot input did not merge: %+v", out)
		}
	}
	assertBotChainHops(t, f, 1)
	var runs, ownedInputs int
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE chat_session_id=$1 AND status='queued'`, f.session).Scan(&runs)
	dbfx.QueryRow(t, `SELECT count(*) FROM chat_message WHERE chat_session_id=$1 AND role='user' AND task_id=$2`, f.session, first.TaskID).Scan(&ownedInputs)
	if runs != 1 || ownedInputs != 4 {
		t.Fatalf("merged run/input ownership=%d/%d, want 1/4", runs, ownedInputs)
	}
	for i := 0; i < 3; i++ {
		requireBotChainAccepted(t, f.send(t, f.botToken, "explicit next execution", "", http.StatusCreated))
	}
	assertBotChainHops(t, f, 4)
	blocked := f.send(t, f.botToken, "beyond explicit execution budget", "", http.StatusCreated)
	if blocked.Mode != "blocked" || blocked.ReasonCode != "bot_wake_chain_limit" {
		t.Fatalf("explicit execution bypassed Bot budget: %+v", blocked)
	}
}

func TestBotWakeDirectChatBlockedVisibleAndReplyReset(t *testing.T) {
	f := newBotChainChatFixture(t)
	ctx := context.Background()
	oldBox := testHandler.BotWakeSecrets
	testHandler.BotWakeSecrets, _ = NewBotWakeSecretBox("feedback fixture")
	t.Cleanup(func() { testHandler.BotWakeSecrets = oldBox })
	var mu sync.Mutex
	var deliveries []map[string]any
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
			http.Error(w, "bad payload", 400)
			return
		}
		mu.Lock()
		deliveries = append(deliveries, v)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	t.Cleanup(receiver.Close)
	t.Setenv("MULTICA_PLUGIN_DEV_ORIGINS", receiver.URL)
	plain, _ := json.Marshal(botWakeSecrets{URL: receiver.URL, Key: "fixture"})
	sealed, err := testHandler.BotWakeSecrets.Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	// Refusals are recorded for audit but never pushed to the sender.
	dbfx.Exec(t, `UPDATE bot_wake SET webhook_secret=$2,events='{}',hourly_limit=0,wake_from='{"member":{"mode":"none","ids":[]},"agent":{"mode":"none","ids":[]},"bot":{"mode":"none","ids":[]}}' WHERE bot_id=$1`, f.bot.ID, sealed)
	var tasks []string
	for i := 1; i <= 6; i++ {
		out := f.send(t, f.botToken, fmt.Sprintf("wake %d", i), "", http.StatusCreated)
		tasks = append(tasks, out.TaskID)
		if i <= 4 {
			requireBotChainAccepted(t, out)
			continue
		}
		if out.Mode != "blocked" || out.ReasonCode != "bot_wake_chain_limit" || !strings.Contains(out.Warning, f.agent) || !strings.Contains(out.Warning, "4") || !strings.Contains(out.Warning, "reply") {
			t.Fatalf("wake %d hid refusal: %+v", i, out)
		}
		var status, reason string
		dbfx.QueryRow(t, `SELECT status,error FROM agent_task_queue WHERE id=$1`, out.TaskID).Scan(&status, &reason)
		if status != "cancelled" || reason != "bot wake chain limit (4)" {
			t.Fatalf("refusal row: %s %s", status, reason)
		}
		var owner string
		dbfx.QueryRow(t, `SELECT task_id::text FROM chat_message WHERE id=$1`, out.MessageID).Scan(&owner)
		if owner != out.TaskID {
			t.Fatal("blocked message lost its owning task")
		}
	}
	assertBotChainHops(t, f, 4)
	// A synthetic failure message is not evidence that the agent replied.
	dbfx.Exec(t, `INSERT INTO chat_message(chat_session_id,role,content,task_id,failure_reason) VALUES($1,'assistant','run failed before a reply',$2,'fixture failure')`, f.session, tasks[0])
	assertBotChainHops(t, f, 4)
	var blocks, alerts int
	dbfx.QueryRow(t, `SELECT count(*) FROM bot_wake_block WHERE bot_id=$1 AND chat_session_id=$2`, f.bot.ID, f.session).Scan(&blocks)
	dbfx.QueryRow(t, `SELECT count(*) FROM bot_wake_event WHERE bot_id=$1 AND event_type='wake_blocked'`, f.bot.ID).Scan(&alerts)
	if blocks != 2 || alerts != 2 {
		t.Fatalf("refusals must each leave an audit and alert: %d/%d", blocks, alerts)
	}
	worker := NewBotWakeWorker(testHandler)
	worker.coalesceSeconds = 0
	worker.client = func(*url.URL) *http.Client { return receiver.Client() }
	for n := 0; n < 30; n++ {
		worked, err := worker.ProcessNext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
		if n == 29 {
			t.Fatal("worker did not drain")
		}
	}
	mu.Lock()
	if len(deliveries) != 0 {
		t.Fatalf("refusal alerts pushed: %v", deliveries)
	}
	mu.Unlock()
	// User inputs were not replies. Actual assistant output resets this root.
	dbfx.Exec(t, `INSERT INTO chat_message(chat_session_id,role,content,task_id) VALUES($1,'assistant','I am here',$2)`, f.session, tasks[0])
	assertBotChainHops(t, f, 0)
	requireBotChainAccepted(t, f.send(t, f.botToken, "wake after reply", "", http.StatusCreated))
	assertBotChainHops(t, f, 1)
	// Rewriting an already-posted answer is not fresh progress.
	dbfx.Exec(t, `UPDATE chat_message SET content='edited reply' WHERE chat_session_id=$1 AND role='assistant'`, f.session)
	assertBotChainHops(t, f, 1)
}

func TestBotWakeDirectChatCompletionAndRefund(t *testing.T) {
	t.Run("completed running task resets", func(t *testing.T) {
		f := newBotChainChatFixture(t)
		first := f.send(t, f.botToken, "first", "", 201)
		for i := 0; i < 3; i++ {
			requireBotChainAccepted(t, f.send(t, f.botToken, "followup", "", 201))
		}
		markTaskRunning(t, context.Background(), first.TaskID)
		dbfx.Exec(t, `UPDATE agent_task_queue SET status='completed',completed_at=now() WHERE id=$1`, first.TaskID)
		assertBotChainHops(t, f, 0)
		requireBotChainAccepted(t, f.send(t, f.botToken, "after completion", "", 201))
	})
	for _, status := range []string{"failed", "cancelled", "completed"} {
		t.Run("unstarted "+status+" refunds once", func(t *testing.T) {
			f := newBotChainChatFixture(t)
			out := f.send(t, f.botToken, "never started", "", 201)
			assertBotChainHops(t, f, 1)
			dbfx.Exec(t, `UPDATE agent_task_queue SET status=$2,completed_at=now() WHERE id=$1`, out.TaskID, status)
			assertBotChainHops(t, f, 0)
			requireBotChainAccepted(t, f.send(t, f.botToken, "new budget", "", 201))
			dbfx.Exec(t, `UPDATE agent_task_queue SET status=$2 WHERE id=$1`, out.TaskID, status)
			assertBotChainHops(t, f, 1)
		})
	}
	t.Run("stale refund leaves new generation intact", func(t *testing.T) {
		f := newBotChainChatFixture(t)
		old := f.send(t, f.botToken, "old pending wake", "", 201)
		dbfx.Exec(t, `INSERT INTO chat_message(chat_session_id,role,content,task_id) VALUES($1,'assistant','progress',$2)`, f.session, old.TaskID)
		requireBotChainAccepted(t, f.send(t, f.botToken, "new generation", "", 201))
		dbfx.Exec(t, `UPDATE agent_task_queue SET status='cancelled' WHERE id=$1`, old.TaskID)
		assertBotChainHops(t, f, 1)
	})
}

func TestBotWakeDirectChatHumanAndVerifiedQuote(t *testing.T) {
	f := newBotChainChatFixture(t)
	for i := 0; i < 4; i++ {
		requireBotChainAccepted(t, f.send(t, f.botToken, "bot wake", "", 201))
	}
	quote := "Kun says: get the code now"
	for i := 0; i < 6; i++ {
		out := f.send(t, f.humanToken, quote, "", 201)
		requireBotChainAccepted(t, out)
		var root *string
		dbfx.QueryRow(t, `SELECT wake_root_id::text FROM agent_task_queue WHERE id=$1`, out.TaskID).Scan(&root)
		if root != nil {
			t.Fatalf("human request inherited automatic cap: %s", *root)
		}
	}
	assertBotChainHops(t, f, 4)
	f.send(t, f.botToken, "pretend Kun said this", "invented human instruction", 400)
	f.send(t, f.botToken, "quoting my own Bot message", "bot wake", 400)
	otherUser := dbfx.User(t, "Another quote author", "bot-wake-quote-other@example.invalid")
	dbfx.Exec(t, `INSERT INTO chat_message(chat_session_id,role,content,sender_user_id) VALUES($1,'user','only another human said this',$2)`, f.session, otherUser)
	f.send(t, f.botToken, "quoting another account", "only another human said this", 400)
	out := f.send(t, f.botToken, "relaying the original human instruction", quote, 201)
	requireBotChainAccepted(t, out)
	var root *string
	dbfx.QueryRow(t, `SELECT wake_root_id::text FROM agent_task_queue WHERE id=$1`, out.TaskID).Scan(&root)
	if root != nil {
		t.Fatal("verified original human quote was capped")
	}
	assertBotChainHops(t, f, 4)
	other := f
	other.session = createHandlerTestChatSession(t, f.agent)
	other.send(t, f.botToken, "quote from a different chat", quote, 400)
}

func TestBotWakeDirectChatWorkspaceLimit(t *testing.T) {
	var settings []byte
	dbfx.QueryRow(t, `SELECT settings FROM workspace WHERE id=$1`, testWorkspaceID).Scan(&settings)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `UPDATE workspace SET settings=$2 WHERE id=$1`, testWorkspaceID, settings)
	})
	dbfx.Exec(t, `UPDATE workspace SET settings=jsonb_set(COALESCE(settings,'{}'),'{bot_wake_chain_limit}','2') WHERE id=$1`, testWorkspaceID)
	f := newBotChainChatFixture(t)
	for i := 0; i < 2; i++ {
		requireBotChainAccepted(t, f.send(t, f.botToken, "wake", "", 201))
	}
	out := f.send(t, f.botToken, "third wake", "", 201)
	if out.Mode != "blocked" || !strings.Contains(out.Warning, "2") {
		t.Fatalf("workspace cap ignored: %+v", out)
	}
	assertBotChainHops(t, f, 2)
}

func TestBotWakeIssueCommentReportsRefusedTarget(t *testing.T) {
	f := newBotChainChatFixture(t)
	for i := 0; i < 4; i++ {
		requireBotChainAccepted(t, f.send(t, f.botToken, "unanswered wake", "", 201))
	}
	issue := createMentionFixtureIssue(t, context.Background(), "wake limit issue mention")
	r := newRequest("POST", "/issues/"+issue+"/comments", map[string]any{"content": "[@target](mention://agent/" + f.agent + ")"})
	r.Header.Del("X-User-ID")
	r.Header.Set("Authorization", "Bearer "+f.botToken)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("comment response=%d %s", w.Code, w.Body)
	}
	var out CommentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.TriggerOutcomes) != 1 || out.TriggerOutcomes[0].TargetID != f.agent || out.TriggerOutcomes[0].Status != "blocked" || out.TriggerOutcomes[0].ReasonCode != ReasonBotWakeChainLimit {
		t.Fatalf("comment hid refused target: %s", w.Body)
	}
	var blocks int
	dbfx.QueryRow(t, `SELECT count(*) FROM bot_wake_block WHERE issue_id=$1 AND bot_id=$2`, issue, f.bot.ID).Scan(&blocks)
	if blocks != 1 {
		t.Fatalf("issue comment refusal audit=%d want=1", blocks)
	}
}

func TestBotWakeDirectChatCLIPreservesBlockedReceipt(t *testing.T) {
	bin := os.Getenv("BOT_WAKE_TEST_CLI")
	if bin == "" {
		t.Skip("set BOT_WAKE_TEST_CLI to exercise the actual CLI binary")
	}
	f := newBotChainChatFixture(t)
	first := f.send(t, f.botToken, "first", "", 201)
	for i := 0; i < 3; i++ {
		requireBotChainAccepted(t, f.send(t, f.botToken, "unanswered", "", 201))
	}
	api := httptest.NewServer(f.router)
	t.Cleanup(api.Close)
	callCLI := func(wantBlocked bool) SendChatMessageResponse {
		t.Helper()
		cmd := exec.Command(bin, "chat", "send", "--session", f.session, "CLI followup", "--mode", "queue", "--output", "json")
		var cleanEnv []string
		for _, item := range os.Environ() {
			if !strings.HasPrefix(item, "MULTICA_") {
				cleanEnv = append(cleanEnv, item)
			}
		}
		cmd.Dir = t.TempDir()
		cmd.Env = append(cleanEnv, "MULTICA_SERVER_URL="+api.URL, "MULTICA_TOKEN="+f.botToken, "MULTICA_WORKSPACE_ID="+testWorkspaceID, "MULTICA_PROFILE_DIR="+t.TempDir())
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		var out SendChatMessageResponse
		if decodeErr := json.Unmarshal(stdout.Bytes(), &out); decodeErr != nil {
			t.Fatalf("CLI receipt is not JSON: %v stdout=%s stderr=%s", decodeErr, &stdout, &stderr)
		}
		if out.MessageID == "" || out.TaskID == "" {
			t.Fatalf("CLI lost saved receipt: %s", &stdout)
		}
		if wantBlocked {
			if err == nil || out.Mode != "blocked" || out.ReasonCode != "bot_wake_chain_limit" || !strings.Contains(stderr.String(), "not woken") || !strings.Contains(stderr.String(), "do not resend") {
				t.Fatalf("CLI hid refusal err=%v stdout=%s stderr=%s", err, &stdout, &stderr)
			}
		} else {
			if err != nil {
				t.Fatalf("CLI send after reply failed: %v %s", err, &stderr)
			}
			requireBotChainAccepted(t, out)
		}
		return out
	}
	callCLI(true)
	callCLI(true)
	var messages int
	dbfx.QueryRow(t, `SELECT count(*) FROM chat_message WHERE chat_session_id=$1 AND role='user'`, f.session).Scan(&messages)
	if messages != 6 {
		t.Fatalf("CLI duplicate send or lost receipt: messages=%d want=6", messages)
	}
	dbfx.Exec(t, `INSERT INTO chat_message(chat_session_id,role,content,task_id) VALUES($1,'assistant','ready',$2)`, f.session, first.TaskID)
	callCLI(false)
}

func TestBotWakeImplicitIssueCommentReportsRefusedAssignee(t *testing.T) {
	f := newBotChainChatFixture(t)
	for i := 0; i < 4; i++ {
		requireBotChainAccepted(t, f.send(t, f.botToken, "unanswered wake", "", 201))
	}
	issue := createMentionFixtureIssue(t, context.Background(), "implicit assignee wake limit")
	dbfx.Exec(t, `UPDATE issue SET assignee_type='agent',assignee_id=$2,status='in_progress' WHERE id=$1`, issue, f.agent)
	r := newRequest("POST", "/issues/"+issue+"/comments", map[string]any{"content": "please continue"})
	r.Header.Del("X-User-ID")
	r.Header.Set("Authorization", "Bearer "+f.botToken)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("implicit comment response=%d %s", w.Code, w.Body)
	}
	var out CommentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.TriggerOutcomes) != 1 || out.TriggerOutcomes[0].TargetID != f.agent || out.TriggerOutcomes[0].Status != "blocked" || out.TriggerOutcomes[0].ReasonCode != ReasonBotWakeChainLimit {
		t.Fatalf("implicit comment hid refused assignee: %s", w.Body)
	}
	var blocks int
	dbfx.QueryRow(t, `SELECT count(*) FROM bot_wake_block WHERE issue_id=$1 AND bot_id=$2`, issue, f.bot.ID).Scan(&blocks)
	if blocks != 1 {
		t.Fatalf("implicit comment refusal audit=%d want=1", blocks)
	}
}
