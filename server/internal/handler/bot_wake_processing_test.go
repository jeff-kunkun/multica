package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestBotWakeProcessingIsolation(t *testing.T) {
	ctx := context.Background()
	f := newBotChainChatFixture(t)
	oldBox := testHandler.BotWakeSecrets
	testHandler.BotWakeSecrets, _ = NewBotWakeSecretBox("processing-fixture")
	defer func() { testHandler.BotWakeSecrets = oldBox }()
	var bodies []map[string]any
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, body)
		w.WriteHeader(200)
	}))
	defer receiver.Close()
	t.Setenv("MULTICA_PLUGIN_DEV_ORIGINS", receiver.URL)
	secret, _ := json.Marshal(botWakeSecrets{URL: receiver.URL, Key: "Bearer fixture"})
	sealed, _ := testHandler.BotWakeSecrets.Seal(secret)
	dbfx.Exec(t, `UPDATE bot_wake SET webhook_secret=$2,quiet_hours=jsonb_set(quiet_hours,'{enabled}','false'),hourly_limit=120 WHERE bot_id=$1`, f.bot.ID, sealed)
	issue := dbfx.Issue(t, "Poison isolation", testutil.Cols{"visibility": "workspace"})
	var poison int64
	if err := testPool.QueryRow(ctx, `INSERT INTO bot_wake_event(bot_id,workspace_id,event_type,issue_id,source_id,root_id,created_at) VALUES($1,$2,'task',$3,gen_random_uuid(),gen_random_uuid(),now()-interval '3 hours') RETURNING sequence`, f.bot.ID, testWorkspaceID, issue).Scan(&poison); err != nil {
		t.Fatal(err)
	}
	// A real PostgreSQL error aborts the admission transaction. Its chain/counter
	// writes must roll back, but its retry state must survive in a new transaction.
	dbfx.Exec(t, `CREATE FUNCTION test_poison_wake() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.sequence=`+strconv.FormatInt(poison, 10)+` AND NEW.status='sending' THEN RAISE EXCEPTION 'fixture poison'; END IF; RETURN NEW; END $$`)
	dbfx.Exec(t, `CREATE TRIGGER test_poison_wake BEFORE UPDATE ON bot_wake_event FOR EACH ROW EXECUTE FUNCTION test_poison_wake()`)
	t.Cleanup(func() {
		testPool.Exec(ctx, `DROP TRIGGER test_poison_wake ON bot_wake_event; DROP FUNCTION test_poison_wake()`)
	})
	healthy := dbfx.Issue(t, "Healthy later source", testutil.Cols{"visibility": "workspace"})
	dbfx.Exec(t, `INSERT INTO bot_wake_event(bot_id,workspace_id,event_type,issue_id,source_id,root_id,created_at) SELECT $1,$2,'task',$3,gen_random_uuid(),gen_random_uuid(),now()-interval '3 hours' FROM generate_series(1,3)`, f.bot.ID, testWorkspaceID, healthy)
	healthBefore, err := testHandler.Queries.GetBotWakeQueueHealth(ctx, f.bot.ID)
	if err != nil || healthBefore.HeadSequence != poison || healthBefore.StuckSeconds < 300 {
		t.Fatalf("queue health: %+v %v", healthBefore, err)
	}
	worker := NewBotWakeWorker(testHandler)
	worker.client = func(*url.URL) *http.Client { return receiver.Client() }
	process := func() {
		t.Helper()
		worked, err := worker.ProcessNext(ctx)
		if err != nil || !worked {
			t.Fatalf("worked=%v error=%v", worked, err)
		}
	}
	process()
	var attempts, processing int
	var delayed bool
	var status, reason string
	check := func() {
		t.Helper()
		if err := testPool.QueryRow(ctx, `SELECT status,reason,attempts,processing_attempts,next_attempt_at>now() FROM bot_wake_event WHERE sequence=$1`, poison).Scan(&status, &reason, &attempts, &processing, &delayed); err != nil {
			t.Fatal(err)
		}
	}
	check()
	if status != "queued" || processing != 1 || attempts != 0 || !delayed || reason == "" {
		t.Fatalf("first failure: %s %d %d %v %s", status, processing, attempts, delayed, reason)
	}
	process()
	if len(bodies) != 1 || len(bodies[0]["events"].([]any)) != 3 {
		t.Fatalf("healthy backlog not delivered in one POST: %+v", bodies)
	}
	for i := 0; i < 2; i++ {
		dbfx.Exec(t, `UPDATE bot_wake_event SET next_attempt_at=now() WHERE sequence=$1`, poison)
		process()
	}
	check()
	if status != "failed" || processing != 3 || attempts != 0 {
		t.Fatalf("retry cap: %s %d %d", status, processing, attempts)
	}
	var consumed int
	dbfx.QueryRow(t, `SELECT count(*) FROM bot_wake_chain c JOIN bot_wake_event e ON c.root_id=e.root_id WHERE e.sequence=$1 AND c.hops>0`, poison).Scan(&consumed)
	if consumed != 0 {
		t.Fatal("failed preparation consumed a hop")
	}
	health, err := testHandler.Queries.GetBotWake(ctx, f.bot.ID)
	if err != nil || !health.LastProcessedAt.Valid || health.LastError == "" {
		t.Fatalf("health missing: %+v %v", health, err)
	}
	t.Log("poison retries 3 times with backoff, failed; no network attempts/hops consumed; later 3 old events delivered in one POST")
}

func TestBotWakeAgentChatCompletion(t *testing.T) {
	ctx := context.Background()
	f := newBotChainChatFixture(t)
	dbfx.Exec(t, `INSERT INTO bot_wake_watch(bot_id,chat_session_id) VALUES($1,$2)`, f.bot.ID, f.session)
	task := dbfx.Task(t, f.agent, testutil.Cols{"chat_session_id": f.session, "status": "running", "runtime_id": testRuntimeID})
	insert := func(content string) string {
		return dbfx.Insert(t, "chat_message", testutil.Cols{"chat_session_id": f.session, "role": "assistant", "task_id": task, "content": content})
	}
	first := insert("completed agent reply")
	empty := insert("")
	count := func(id string) int {
		var n int
		dbfx.QueryRow(t, `SELECT count(*) FROM bot_wake_event WHERE bot_id=$1 AND source_id=$2 AND event_type='chat' AND author_type='agent' AND author_id=$3`, f.bot.ID, id, f.agent).Scan(&n)
		return n
	}
	if count(first) != 1 || count(empty) != 0 {
		t.Fatal("insert or empty reply capture incorrect")
	}
	dbfx.Exec(t, `UPDATE chat_message SET content='completed later' WHERE id=$1`, empty)
	if count(empty) != 1 {
		t.Fatal("assistant content completion did not emit chat")
	}
	dbfx.Exec(t, `UPDATE chat_message SET content='edited later' WHERE id=$1`, empty)
	if count(empty) != 1 {
		t.Fatal("assistant edit duplicated notification")
	}
	rows, err := testHandler.Queries.ListBotWakeEvents(ctx, db.ListBotWakeEventsParams{BotID: f.bot.ID, WorkspaceID: f.bot.WorkspaceID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("unexpected reply events: %d", len(rows))
	}
	t.Log("agent INSERT and delayed content UPDATE emit one chat each; empty reply and later edits do not emit duplicates")
}
