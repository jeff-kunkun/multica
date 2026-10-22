package handler

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

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
