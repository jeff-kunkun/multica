package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// insertUrgentChatTask seeds one chat task with a pickup deadline already in
// the past (or not, for the control rows), in the given status.
func insertUrgentChatTask(t *testing.T, agentID, runtimeID, sessionID, status string, deadlineAgo time.Duration) string {
	t.Helper()
	var id string
	started := "NULL"
	if status == "running" {
		started = "now()"
	}
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (agent_id, runtime_id, chat_session_id, status, priority, dispatched_at, started_at, pickup_deadline_at, max_attempts)
		VALUES ($1, $2, $3, $4, 4, CASE WHEN $4 = 'queued' THEN NULL ELSE now() END, `+started+`, now() - make_interval(secs => $5), 3)
		RETURNING id
	`, agentID, runtimeID, sessionID, status, deadlineAgo.Seconds()).Scan(&id); err != nil {
		t.Fatalf("insert %s urgent task: %v", status, err)
	}
	return id
}

// The 30 s pickup deadline (DENE-1844): a queued or dispatched urgent turn
// whose deadline has passed fails with pickup_timeout and leaves a system
// note in the chat; a running turn and a turn still inside its deadline are
// untouched, and a task without a deadline is never swept.
func TestPickupDeadlineSweepFailsOnlyOverdueUnstartedTurns(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}
	ctx := context.Background()
	issueID, agentID, _ := setupSweeperTestFixture(t, "dispatched")
	t.Cleanup(func() { cleanupSweeperFixture(t, issueID, agentID) })

	var runtimeID string
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("load agent runtime: %v", err)
	}
	sessionOverdue := newPoisonTestChatSession(t, agentID, runtimeID, "urgent overdue")
	sessionDispatched := newPoisonTestChatSession(t, agentID, runtimeID, "urgent dispatched overdue")
	sessionWaiting := newPoisonTestChatSession(t, agentID, runtimeID, "urgent waiting overdue")
	sessionRunning := newPoisonTestChatSession(t, agentID, runtimeID, "urgent running")
	sessionFresh := newPoisonTestChatSession(t, agentID, runtimeID, "urgent fresh")
	for _, s := range []string{sessionOverdue, sessionDispatched, sessionWaiting, sessionRunning, sessionFresh} {
		s := s
		t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM chat_message WHERE chat_session_id = $1`, s) })
	}
	overdue := insertUrgentChatTask(t, agentID, runtimeID, sessionOverdue, "queued", 5*time.Second)
	dispatchedOverdue := insertUrgentChatTask(t, agentID, runtimeID, sessionDispatched, "dispatched", 5*time.Second)
	waitingOverdue := insertUrgentChatTask(t, agentID, runtimeID, sessionWaiting, "waiting_local_directory", 5*time.Second)
	running := insertUrgentChatTask(t, agentID, runtimeID, sessionRunning, "running", 5*time.Second)
	fresh := insertUrgentChatTask(t, agentID, runtimeID, sessionFresh, "queued", -25*time.Second)

	taskSvc := service.NewTaskService(db.New(testPool), testPool, nil, events.New())
	failed, err := taskSvc.FailTasksPastPickupDeadline(ctx, pickupDeadlineFailBatchSize)
	if err != nil {
		t.Fatalf("FailTasksPastPickupDeadline: %v", err)
	}
	got := map[[16]byte]db.AgentTaskQueue{}
	for _, f := range failed {
		got[f.ID.Bytes] = f
	}
	for _, want := range []string{overdue, dispatchedOverdue, waitingOverdue} {
		f, ok := got[parseUUIDBytes(want)]
		if !ok {
			t.Fatalf("overdue task %s was not failed", want)
		}
		if f.FailureReason.String != "pickup_timeout" {
			t.Fatalf("failure_reason = %q, want pickup_timeout", f.FailureReason.String)
		}
	}
	for _, keep := range []string{running, fresh} {
		if _, ok := got[parseUUIDBytes(keep)]; ok {
			t.Fatalf("task %s must not be swept (running or inside its deadline)", keep)
		}
	}

	var status string
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, running).Scan(&status); err != nil || status != "running" {
		t.Fatalf("running task status = %q (%v), want running", status, err)
	}

	var role, content, reason string
	if err := testPool.QueryRow(ctx, `
		SELECT role, content, COALESCE(failure_reason, '') FROM chat_message
		WHERE chat_session_id = $1 AND task_id = $2
	`, sessionOverdue, overdue).Scan(&role, &content, &reason); err != nil {
		t.Fatalf("expected one chat note for the overdue turn: %v", err)
	}
	if role != "assistant" || reason != "pickup_timeout" || !strings.Contains(content, "加急") {
		t.Fatalf("chat note = role %q reason %q content %q", role, reason, content)
	}

	// Idempotent: a second sweep finds nothing.
	again, err := taskSvc.FailTasksPastPickupDeadline(ctx, pickupDeadlineFailBatchSize)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second sweep failed %d tasks, want 0", len(again))
	}
}

// DENE-1884 review counterexample: a daemon that claimed an urgent turn and
// then parked it in waiting_local_directory (another run holds the project
// directory) must not shelter it from the pickup deadline. The sweep fails it
// with pickup_timeout, clears the wait reason, and leaves one chat notice.
func TestPickupDeadlineSweepFailsExpiredWaitingLocalDirectoryTurn(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}
	ctx := context.Background()
	issueID, agentID, _ := setupSweeperTestFixture(t, "dispatched")
	t.Cleanup(func() { cleanupSweeperFixture(t, issueID, agentID) })
	var runtimeID string
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("load agent runtime: %v", err)
	}
	session := newPoisonTestChatSession(t, agentID, runtimeID, "urgent expired while waiting for directory")
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM chat_message WHERE chat_session_id = $1`, session)
	})
	taskID := insertUrgentChatTask(t, agentID, runtimeID, session, "waiting_local_directory", time.Minute)
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET wait_reason = '/srv/project (holder: abc123)' WHERE id = $1`, taskID); err != nil {
		t.Fatalf("set wait_reason: %v", err)
	}

	taskSvc := service.NewTaskService(db.New(testPool), testPool, nil, events.New())
	failed, err := taskSvc.FailTasksPastPickupDeadline(ctx, pickupDeadlineFailBatchSize)
	if err != nil {
		t.Fatalf("FailTasksPastPickupDeadline: %v", err)
	}
	if len(failed) != 1 || failed[0].ID.Bytes != parseUUIDBytes(taskID) {
		t.Fatalf("swept %d tasks, want exactly the expired waiting turn", len(failed))
	}
	var status, reason string
	var waitReason *string
	if err := testPool.QueryRow(ctx, `SELECT status, COALESCE(failure_reason, ''), wait_reason FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status, &reason, &waitReason); err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if status != "failed" || reason != "pickup_timeout" || waitReason != nil {
		t.Fatalf("task = status %q reason %q wait_reason %v, want failed/pickup_timeout/NULL", status, reason, waitReason)
	}
	var notices int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM chat_message WHERE chat_session_id = $1 AND task_id = $2 AND failure_reason = 'pickup_timeout'`, session, taskID).Scan(&notices); err != nil || notices != 1 {
		t.Fatalf("chat notices = %d (%v), want exactly one", notices, err)
	}
}

// A task dispatched to the agent's configured standby runtime must be
// claimable by that runtime (the claim fence used to require a.runtime_id =
// atq.runtime_id), and an unrelated runtime still cannot claim it.
func TestStandbyRuntimeMayClaimUrgentTask(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}
	ctx := context.Background()
	// A dedicated agent: the shared sweeper agent accumulates queued tasks
	// from other tests, and a claim on it would dispatch one of those.
	var primaryID string
	if err := testPool.QueryRow(ctx, `
		SELECT a.runtime_id FROM agent a
		JOIN member m ON m.workspace_id = a.workspace_id
		JOIN "user" u ON u.id = m.user_id
		WHERE u.email = $1 LIMIT 1
	`, integrationTestEmail).Scan(&primaryID); err != nil {
		t.Fatalf("find primary runtime: %v", err)
	}
	var agentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (workspace_id, name, description, runtime_mode, runtime_config, runtime_id, visibility, max_concurrent_tasks, owner_id)
		VALUES ($1, 'dene1844-standby-claim-agent', '', 'local', '{}'::jsonb, $2, 'workspace', 1, $3)
		RETURNING id::text
	`, parseUUID(testWorkspaceID), parseUUID(primaryID), parseUUID(testUserID)).Scan(&agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE agent_id = $1`, agentID)
		testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID)
	})
	newRuntime := func(name string) string {
		var id string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, last_seen_at)
			SELECT workspace_id, $2, 'local', provider, 'online', '', '{}'::jsonb, owner_id, now() FROM agent_runtime WHERE id = $1
			RETURNING id::text
		`, primaryID, name).Scan(&id); err != nil {
			t.Fatalf("seed runtime %s: %v", name, err)
		}
		t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, id) })
		return id
	}
	standby := newRuntime("dene1844-standby")
	stranger := newRuntime("dene1844-stranger")
	if _, err := testPool.Exec(ctx, `UPDATE agent SET fallback_runtime_id = $2 WHERE id = $1`, agentID, standby); err != nil {
		t.Fatalf("set fallback runtime: %v", err)
	}

	session := newPoisonTestChatSession(t, agentID, standby, "urgent on standby")
	taskID := insertUrgentChatTask(t, agentID, standby, session, "queued", -25*time.Second)

	q := db.New(testPool)
	claim := func(runtime string) (db.AgentTaskQueue, error) {
		return q.ClaimAgentTask(ctx, db.ClaimAgentTaskParams{
			AgentID:          pgtype.UUID{Bytes: parseUUIDBytes(agentID), Valid: true},
			RuntimeID:        pgtype.UUID{Bytes: parseUUIDBytes(runtime), Valid: true},
			PrepareLeaseSecs: 60,
			RuntimeStaleSecs: service.RuntimeClaimFreshnessSeconds,
		})
	}
	if _, err := claim(stranger); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stranger runtime claim err = %v, want no rows", err)
	}
	if _, err := claim(primaryID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("primary runtime must not claim a task addressed to the standby: err = %v", err)
	}
	got, err := claim(standby)
	if err != nil {
		t.Fatalf("standby runtime claim: %v", err)
	}
	if got.ID.Bytes != parseUUIDBytes(taskID) || got.Status != "dispatched" {
		t.Fatalf("claimed %x status %q, want %s dispatched", got.ID.Bytes, got.Status, taskID)
	}
}
