package handler

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestBotWakeReviewMentionAddedToAssistant(t *testing.T) {
	f := newBotChainChatFixture(t)
	dbfx.Exec(t, `INSERT INTO bot_wake_watch(bot_id,chat_session_id) VALUES($1,$2)`, f.bot.ID, f.session)
	task := dbfx.Task(t, f.agent, testutil.Cols{"chat_session_id": f.session, "status": "running", "runtime_id": testRuntimeID})
	msg := dbfx.Insert(t, "chat_message", testutil.Cols{"chat_session_id": f.session, "role": "assistant", "task_id": task, "content": "completed reply"})
	var chats, mentions int
	dbfx.QueryRow(t, `SELECT count(*) FROM bot_wake_event WHERE bot_id=$1 AND source_id=$2 AND event_type='chat'`, f.bot.ID, msg).Scan(&chats)
	if chats != 1 {
		t.Fatalf("fixture chat count=%d", chats)
	}
	dbfx.Exec(t, `UPDATE chat_message SET content=$2 WHERE id=$1`, msg, "completed reply [@Bot](mention://bot/"+uuidToString(f.bot.ID)+")")
	dbfx.QueryRow(t, `SELECT count(*) FROM bot_wake_event WHERE bot_id=$1 AND source_id=$2 AND event_type='mention'`, f.bot.ID, msg).Scan(&mentions)
	if mentions != 1 {
		t.Fatalf("assistant edit added explicit mention: chat events=%d, mention events=%d; want one mention", chats, mentions)
	}
	for _, body := range []string{"edited reply", "edited reply [@Bot](mention://bot/" + uuidToString(f.bot.ID) + ")", "again [@Bot](mention://bot/" + uuidToString(f.bot.ID) + ")"} {
		dbfx.Exec(t, `UPDATE chat_message SET content=$2 WHERE id=$1`, msg, body)
	}
	dbfx.QueryRow(t, `SELECT count(*) FILTER (WHERE event_type='chat'),count(*) FILTER (WHERE event_type='mention') FROM bot_wake_event WHERE bot_id=$1 AND source_id=$2`, f.bot.ID, msg).Scan(&chats, &mentions)
	if chats != 1 || mentions != 1 {
		t.Fatalf("ordinary/repeated edits: chat=%d mention=%d", chats, mentions)
	}

}

func TestBotWakeReviewAttachmentOnlyCompletion(t *testing.T) {
	f := newBotChainChatFixture(t)
	dbfx.Exec(t, `INSERT INTO bot_wake_watch(bot_id,chat_session_id) VALUES($1,$2)`, f.bot.ID, f.session)
	task := seedRunningChatTask(t, f.agent, f.session)
	att := seedAgentChatAttachment(t, f.agent, f.session, task)
	second := seedAgentChatAttachment(t, f.agent, f.session, task)
	if _, err := testHandler.TaskService.CompleteTask(context.Background(), parseUUID(task), []byte(`{"output":""}`), "", "", "", false, "", ""); err != nil {
		t.Fatal(err)
	}
	msg, body, ok := assistantMessageForTask(t, task)
	if !ok || body != "" {
		t.Fatalf("expected file-only outcome, exists=%v body=%q", ok, body)
	}
	bound := attachmentMessageID(t, att)
	if bound == nil || *bound != msg {
		t.Fatal("fixture attachment not bound")
	}
	if bound := attachmentMessageID(t, second); bound == nil || *bound != msg {
		t.Fatal("second attachment not bound")
	}
	dbfx.Exec(t, `UPDATE attachment SET chat_message_id=chat_message_id WHERE id=$1`, att)
	var chats int
	dbfx.QueryRow(t, `SELECT count(*) FROM bot_wake_event WHERE bot_id=$1 AND source_id=$2 AND event_type='chat'`, f.bot.ID, msg).Scan(&chats)
	if chats != 1 {
		t.Fatalf("real attachment-only completion: assistant message created, attachment bound, chat events=%d; want one", chats)
	}
}

func TestBotWakeReviewNonReplyAttachments(t *testing.T) {
	f := newBotChainChatFixture(t)
	dbfx.Exec(t, `INSERT INTO bot_wake_watch(bot_id,chat_session_id) VALUES($1,$2)`, f.bot.ID, f.session)
	for _, kind := range []string{"failure", "no_response"} {
		task := seedRunningChatTask(t, f.agent, f.session)
		cols := testutil.Cols{"chat_session_id": f.session, "role": "assistant", "task_id": task, "content": ""}
		if kind == "failure" {
			cols["failure_reason"] = "failed"
		} else {
			cols["message_kind"] = "no_response"
		}
		msg := dbfx.Insert(t, "chat_message", cols)
		att := seedAgentChatAttachment(t, f.agent, f.session, task)
		dbfx.Exec(t, `UPDATE attachment SET chat_message_id=$2 WHERE id=$1`, att, msg)
		var count int
		dbfx.QueryRow(t, `SELECT count(*) FROM bot_wake_event WHERE bot_id=$1 AND source_id=$2`, f.bot.ID, msg).Scan(&count)
		if count != 0 {
			t.Fatalf("%s emitted %d events", kind, count)
		}
	}
}
