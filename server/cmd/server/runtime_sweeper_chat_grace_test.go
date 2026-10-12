package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestOfflineRuntimeChatTaskFailsAfterChatGrace locks the DENE-1839 split: a
// chat turn on a runtime that has been offline longer than the chat grace
// fails with runtime_offline and leaves a failure message in the chat, while
// an issue run on the same runtime keeps waiting through the long grace. The
// failed chat turn is then NOT given a deferred retry pinned to that machine.
func TestOfflineRuntimeChatTaskFailsAfterChatGrace(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}

	ctx := context.Background()
	issueID, agentID, issueTaskID := setupSweeperTestFixture(t, "running")
	t.Cleanup(func() { cleanupSweeperFixture(t, issueID, agentID) })

	var runtimeID string
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("load agent runtime: %v", err)
	}
	chatSessionID := newPoisonTestChatSession(t, agentID, runtimeID, "runtime offline chat grace")
	var chatTaskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, chat_session_id, status, priority, dispatched_at, started_at, max_attempts)
		VALUES ($1, $2, $3, 'running', 0, now() - interval '5 minutes', now() - interval '5 minutes', 3)
		RETURNING id
	`, agentID, runtimeID, chatSessionID).Scan(&chatTaskID); err != nil {
		t.Fatalf("insert chat task: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM chat_message WHERE chat_session_id = $1`, chatSessionID)
	})

	// Offline for five minutes: past the chat grace, well inside the issue grace.
	setAgentRuntimeOffline(t, agentID, 5*time.Minute)
	queries := db.New(testPool)
	taskSvc := service.NewTaskService(queries, testPool, nil, events.New())

	failed, err := taskSvc.FailTasksForOfflineRuntimes(ctx, db.FailTasksForOfflineRuntimesParams{
		ReconnectGraceSecs:     defaultRuntimeReconnectGrace.Seconds(),
		ChatReconnectGraceSecs: defaultChatRuntimeReconnectGrace.Seconds(),
		MaxPerTick:             offlineTaskFailBatchSize,
	})
	if err != nil {
		t.Fatalf("FailTasksForOfflineRuntimes: %v", err)
	}
	var chatFailed *db.AgentTaskQueue
	for i := range failed {
		switch failed[i].ID.Bytes {
		case parseUUIDBytes(issueTaskID):
			t.Fatal("issue task was failed inside the issue reconnect grace")
		case parseUUIDBytes(chatTaskID):
			chatFailed = &failed[i]
		}
	}
	if chatFailed == nil {
		t.Fatal("chat task was not failed after the chat reconnect grace elapsed")
	}
	if chatFailed.FailureReason.String != "runtime_offline" {
		t.Fatalf("failure reason = %q, want runtime_offline", chatFailed.FailureReason.String)
	}

	var issueStatus string
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, issueTaskID).Scan(&issueStatus); err != nil {
		t.Fatalf("load issue task: %v", err)
	}
	if issueStatus != "running" {
		t.Fatalf("issue task status = %q, want running (long grace untouched)", issueStatus)
	}

	var role, content, failureReason string
	if err := testPool.QueryRow(ctx, `
		SELECT role, content, COALESCE(failure_reason, '') FROM chat_message
		WHERE chat_session_id = $1 AND task_id = $2
	`, chatSessionID, chatTaskID).Scan(&role, &content, &failureReason); err != nil {
		t.Fatalf("expected one chat message for the failed turn: %v", err)
	}
	if role != "assistant" || failureReason != "runtime_offline" {
		t.Fatalf("chat message role=%q failure_reason=%q, want assistant/runtime_offline", role, failureReason)
	}
	var runtimeName string
	if err := testPool.QueryRow(ctx, `SELECT COALESCE(NULLIF(custom_name, ''), name) FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&runtimeName); err != nil {
		t.Fatalf("load runtime name: %v", err)
	}
	if !strings.Contains(content, runtimeName) || !strings.Contains(content, "最后在线") {
		t.Fatalf("chat notice %q should name the machine %q and its last heartbeat", content, runtimeName)
	}

	// Post-failure handling must not queue a retry that waits on this machine.
	taskSvc.HandleFailedTasks(ctx, []db.AgentTaskQueue{*chatFailed})
	var retries int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE retry_of_task_id = $1`, chatTaskID).Scan(&retries); err != nil {
		t.Fatalf("count retries: %v", err)
	}
	if retries != 0 {
		t.Fatalf("chat runtime_offline failure spawned %d retry task(s), want none", retries)
	}
}

// TestOfflineRuntimeChatTaskWaitsInsideChatGrace: a runtime that just dropped
// still gets its chat grace, so a brief reconnect does not lose the turn.
func TestOfflineRuntimeChatTaskWaitsInsideChatGrace(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}

	ctx := context.Background()
	issueID, agentID, _ := setupSweeperTestFixture(t, "running")
	t.Cleanup(func() { cleanupSweeperFixture(t, issueID, agentID) })
	var runtimeID string
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("load agent runtime: %v", err)
	}
	chatSessionID := newPoisonTestChatSession(t, agentID, runtimeID, "runtime offline inside chat grace")
	var chatTaskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, chat_session_id, status, priority, dispatched_at, started_at)
		VALUES ($1, $2, $3, 'running', 0, now() - interval '1 minute', now() - interval '1 minute')
		RETURNING id
	`, agentID, runtimeID, chatSessionID).Scan(&chatTaskID); err != nil {
		t.Fatalf("insert chat task: %v", err)
	}
	setAgentRuntimeOffline(t, agentID, 1*time.Minute)

	failed, err := db.New(testPool).FailTasksForOfflineRuntimes(ctx, db.FailTasksForOfflineRuntimesParams{
		ReconnectGraceSecs:     defaultRuntimeReconnectGrace.Seconds(),
		ChatReconnectGraceSecs: defaultChatRuntimeReconnectGrace.Seconds(),
		MaxPerTick:             offlineTaskFailBatchSize,
	})
	if err != nil {
		t.Fatalf("FailTasksForOfflineRuntimes: %v", err)
	}
	for _, task := range failed {
		if task.ID.Bytes == parseUUIDBytes(chatTaskID) {
			t.Fatal("chat task was failed inside the chat reconnect grace")
		}
	}
}

func TestChatRuntimeReconnectGraceStaysAboveHeartbeatWindow(t *testing.T) {
	if defaultChatRuntimeReconnectGrace < minimumRuntimeReconnectGrace {
		t.Fatalf("chat grace %s is below the heartbeat freshness window %s", defaultChatRuntimeReconnectGrace, minimumRuntimeReconnectGrace)
	}
	if defaultChatRuntimeReconnectGrace >= defaultRuntimeReconnectGrace {
		t.Fatalf("chat grace %s should be shorter than the issue grace %s", defaultChatRuntimeReconnectGrace, defaultRuntimeReconnectGrace)
	}
}
