package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// A reply that finishes while the replace is being created must not carry the
// voided instructions into the new run: the stop of the running task is part
// of the same transaction, so the late completion is a no-op.
func TestChatReplaceLateCompletionDoesNotRebindVoidedInput(t *testing.T) {
	f := newChatSteerFixture(t, "claude", true)
	var supplement SendChatMessageResponse
	sendChatWithMode(t, f.sessionID, "OLD: also publish the old plan", "").Want(http.StatusCreated).JSON(&supplement)

	oldBus := testHandler.TaskService.Bus
	bus := events.New()
	testHandler.TaskService.Bus = bus
	defer func() { testHandler.TaskService.Bus = oldBus }()
	fired := false
	bus.Subscribe(protocol.EventTaskQueued, func(events.Event) {
		if fired {
			return
		}
		fired = true
		// The daemon reports the old run complete just as the new task appears.
		if _, err := testHandler.TaskService.CompleteTask(context.Background(), parseUUID(f.headID), []byte(`{}`), "", "", "", false, "", ""); err != nil {
			t.Errorf("late completion: %v", err)
		}
	})

	var sent SendChatMessageResponse
	sendChatWithMode(t, f.sessionID, "NEW: drop every old instruction", "replace").Want(http.StatusCreated).JSON(&sent)
	if !fired {
		t.Fatal("the queued event never fired, so the late completion was not exercised")
	}

	var owner, status string
	var superseded bool
	dbfx.QueryRow(t, `SELECT task_id::text, superseded_at IS NOT NULL FROM chat_message WHERE id=$1`, supplement.MessageID).Scan(&owner, &superseded)
	dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id=$1`, f.headID).Scan(&status)
	if owner == sent.TaskID {
		t.Fatal("voided instruction was rebound into the new run's input")
	}
	if !superseded {
		t.Fatal("voided instruction lost its superseded mark")
	}
	if status != "cancelled" {
		t.Fatalf("old run status=%s, want cancelled", status)
	}
}

// A Bot whose wake budget is spent is refused: nothing the chat already had
// may change, the refused task itself stays visible as cancelled.
func TestChatReplaceRefusedByBotBudgetLeavesTheChatUntouched(t *testing.T) {
	f := newBotChainChatFixture(t)
	first := f.send(t, f.botToken, "original old instruction", "", http.StatusCreated)
	markTaskRunning(t, context.Background(), first.TaskID)
	dbfx.Exec(t, `UPDATE bot_wake_chain SET hops=4 WHERE root_id=(SELECT root_id FROM bot_wake_cause WHERE bot_id=$1)`, f.bot.ID)
	queued := f.send(t, f.humanToken, "old queued followup", "", http.StatusCreated)

	sent := f.sendMode(t, f.botToken, "replace with new intent", "", "replace", http.StatusCreated)
	if sent.Mode != "blocked" || sent.ReasonCode != "bot_wake_chain_limit" {
		t.Fatalf("refused replace answered %+v", sent)
	}
	if sent.ChatReplaceOutcome != nil {
		t.Fatalf("refused replace still reported an outcome: %+v", sent.ChatReplaceOutcome)
	}

	var oldStatus, queuedStatus, newStatus string
	var firstSuperseded, queuedSuperseded bool
	dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id=$1`, first.TaskID).Scan(&oldStatus)
	dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id=$1`, queued.TaskID).Scan(&queuedStatus)
	dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id=$1`, sent.TaskID).Scan(&newStatus)
	dbfx.QueryRow(t, `SELECT superseded_at IS NOT NULL FROM chat_message WHERE id=$1`, first.MessageID).Scan(&firstSuperseded)
	dbfx.QueryRow(t, `SELECT superseded_at IS NOT NULL FROM chat_message WHERE id=$1`, queued.MessageID).Scan(&queuedSuperseded)
	if oldStatus != "running" || queuedStatus != "queued" || newStatus != "cancelled" {
		t.Fatalf("task states old=%s queued=%s new=%s, want running/queued/cancelled", oldStatus, queuedStatus, newStatus)
	}
	if firstSuperseded || queuedSuperseded {
		t.Fatal("refused replace marked old messages superseded")
	}
}

// A Bot with budget left may replace like anyone else.
func TestChatReplaceByBotWithBudgetVoidsTheQueue(t *testing.T) {
	f := newBotChainChatFixture(t)
	first := f.send(t, f.botToken, "original old instruction", "", http.StatusCreated)
	markTaskRunning(t, context.Background(), first.TaskID)
	queued := f.send(t, f.humanToken, "old queued followup", "", http.StatusCreated)

	sent := f.sendMode(t, f.botToken, "replace with new intent", "", "replace", http.StatusCreated)
	if sent.ChatReplaceOutcome == nil || sent.Mode == "blocked" {
		t.Fatalf("bot replace was not applied: %+v", sent)
	}
	var oldStatus, queuedStatus, newStatus string
	var firstSuperseded, queuedSuperseded bool
	dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id=$1`, first.TaskID).Scan(&oldStatus)
	dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id=$1`, queued.TaskID).Scan(&queuedStatus)
	dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id=$1`, sent.TaskID).Scan(&newStatus)
	dbfx.QueryRow(t, `SELECT superseded_at IS NOT NULL FROM chat_message WHERE id=$1`, first.MessageID).Scan(&firstSuperseded)
	dbfx.QueryRow(t, `SELECT superseded_at IS NOT NULL FROM chat_message WHERE id=$1`, queued.MessageID).Scan(&queuedSuperseded)
	if oldStatus != "cancelled" || queuedStatus != "cancelled" || newStatus != "queued" {
		t.Fatalf("task states old=%s queued=%s new=%s, want cancelled/cancelled/queued", oldStatus, queuedStatus, newStatus)
	}
	if !firstSuperseded || !queuedSuperseded {
		t.Fatalf("old messages superseded first=%v queued=%v, want both", firstSuperseded, queuedSuperseded)
	}
}
