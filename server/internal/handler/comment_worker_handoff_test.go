package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func claimWorkerReplyRun(t *testing.T, runtimeID string) *AgentTaskResponse {
	t.Helper()
	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil, testWorkspaceID, "worker-reply-handoff")
	req = withURLParam(req, "runtimeId", runtimeID)
	req.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilityCoalescedCommentsV1)
	var response struct {
		Task *AgentTaskResponse `json:"task"`
	}
	testutil.Call(t, testHandler.ClaimTaskByRuntime, req).Want(http.StatusOK).JSON(&response)
	return response.Task
}

func completeWorkerReplyRun(t *testing.T, taskID string) {
	t.Helper()
	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+taskID+"/complete", map[string]any{"output": "Processed the inputs delivered to this run"}, testWorkspaceID, "worker-reply-handoff")
	req = withURLParam(req, "taskId", taskID)
	testutil.Call(t, testHandler.CompleteTask, req).Want(http.StatusOK)
}

// endWorkerReplyRun ends the worker's run without completion side effects, so
// the leader can claim the same issue under the one-run-per-issue rule.
func endWorkerReplyRun(t *testing.T, taskID string) {
	t.Helper()
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, taskID)
}

// A worker progress comment wakes the leader; the worker's later replies must
// coalesce into that queued leader run. One run per issue keeps the leader
// queued until the worker's run ends, so no leader run is claimed mid-thread.
func TestWorkerReplyDelivery(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit=%t", explicit), func(t *testing.T) {
			ctx := context.Background()
			leaderRuntimeID := dbfx.Runtime(t, "Worker handoff leader runtime")
			leaderID := dbfx.Agent(t, "Worker handoff leader", leaderRuntimeID, testutil.Cols{"max_concurrent_tasks": 3})
			workerRuntimeID := dbfx.Runtime(t, "Worker handoff worker runtime")
			workerID := dbfx.Agent(t, "Worker handoff worker", workerRuntimeID)
			squadID := dbfx.Squad(t, "Worker handoff squad", leaderID)
			dbfx.SquadMember(t, squadID, "agent", workerID)
			issueID := dbfx.Issue(t, "Worker results must reach the coordinator", testutil.Cols{
				"status": "in_progress", "assignee_type": "squad", "assignee_id": squadID,
			})
			sourceID := dbfx.Task(t, leaderID, testutil.Cols{
				"runtime_id": leaderRuntimeID, "issue_id": issueID, "status": "completed",
				"is_leader_task": true, "squad_id": squadID,
				"originator_user_id": testUserID, "accountable_user_id": testUserID,
			})
			rootID := dbfx.Comment(t, issueID, fmt.Sprintf("[@Worker](mention://agent/%s) verify the implementation", workerID), testutil.Cols{
				"author_type": "agent", "author_id": leaderID, "source_task_id": sourceID,
			})
			workerTaskID := dbfx.Task(t, workerID, testutil.Cols{
				"runtime_id": workerRuntimeID, "issue_id": issueID, "status": "running",
				"trigger_comment_id": rootID, "squad_id": squadID, "delegated_from_task_id": sourceID,
				"originator_user_id": testUserID, "accountable_user_id": testUserID,
			})
			post := func(content string) CommentResponse {
				req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{
					"content": content, "parent_id": rootID,
				}), "id", issueID)
				req.Header.Set("X-Agent-ID", workerID)
				req.Header.Set("X-Task-ID", workerTaskID)
				var response CommentResponse
				testutil.Call(t, testHandler.CreateComment, req).Want(http.StatusCreated).JSON(&response)
				if response.AuthorType != "agent" || response.SourceTaskID == nil || *response.SourceTaskID != workerTaskID {
					t.Fatal("worker reply lost its authenticated source task")
				}
				return response
			}
			progress := post("Verification started; final results will follow in this thread")
			var leaderTaskID string
			dbfx.QueryRow(t, `SELECT id FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'`, issueID, leaderID).Scan(&leaderTaskID)
			if other := claimWorkerReplyRun(t, leaderRuntimeID); other != nil {
				t.Fatal("leader must not run while the worker's run on the same issue is active")
			}
			content := "Verification complete: found a correctness issue; please coordinate the repair"
			if explicit {
				content = fmt.Sprintf("[@Squad](mention://squad/%s) %s", squadID, content)
			}
			details := post("Evidence: the final reply follows the progress note")
			result := post(content)
			endWorkerReplyRun(t, workerTaskID)
			first := claimWorkerReplyRun(t, leaderRuntimeID)
			if first == nil || first.ID != leaderTaskID || !slices.Contains(first.DeliveredCommentIDs, progress.ID) || !slices.Contains(first.DeliveredCommentIDs, result.ID) || !slices.Contains(first.DeliveredCommentIDs, details.ID) || first.TriggerCommentContent != content {
				t.Fatal("queued leader must coalesce and receive the worker result")
			}
			if _, err := testHandler.TaskService.StartTask(ctx, parseUUID(leaderTaskID)); err != nil {
				t.Fatal(err)
			}
			if other := claimWorkerReplyRun(t, leaderRuntimeID); other != nil {
				t.Fatal("same issue/leader must remain serialized")
			}
			completeWorkerReplyRun(t, leaderTaskID)
			if next := claimWorkerReplyRun(t, leaderRuntimeID); next != nil {
				t.Fatal("already delivered result must not create another leader run")
			}
		})
	}
}

// If completion commits while registration waits for the task row lock, the
// caller must see a miss and enqueue fresh, not attach an obligation to a run
// whose completion snapshot can no longer include it.
func TestWorkerReplyRegistrationLosesCompletionRace(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runtimeID := dbfx.Runtime(t, "Worker registration race")
	agentID := dbfx.Agent(t, "Worker registration race", runtimeID)
	issueID := dbfx.Issue(t, "Worker registration race")
	rootID := dbfx.Comment(t, issueID, "Initial input")
	replyID := dbfx.Comment(t, issueID, "Worker result", testutil.Cols{"parent_id": rootID})
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID, "issue_id": issueID, "status": "running", "trigger_comment_id": rootID,
	})
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(context.Background()); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Error(err)
		}
	}()
	if _, err := tx.Exec(ctx, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, taskID); err != nil {
		t.Fatal(err)
	}
	conn, err := testPool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	pid := conn.Conn().PgConn().PID()
	done := make(chan struct{})
	var registrationErr error
	go func() {
		_, registrationErr = db.New(conn).RegisterPlannedCommentForActiveTask(ctx, db.RegisterPlannedCommentForActiveTaskParams{
			IssueID: parseUUID(issueID), AgentID: parseUUID(agentID), CommentID: parseUUID(replyID),
		})
		close(done)
	}()
	// Stop the registration query before returning its connection to the pool,
	// including assertion failures while it is waiting for the task row lock.
	defer func() { cancel(); <-done }()
	for {
		var blocked bool
		if err := testPool.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1)) > 0`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("registration did not block on the completing row")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	<-done
	if !errors.Is(registrationErr, pgx.ErrNoRows) {
		t.Fatalf("registration after completion = %v, want no rows", registrationErr)
	}
	task, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(taskID))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(task.CoalescedCommentIds, parseUUID(replyID)) {
		t.Fatal("completed run acquired an undeliverable obligation")
	}
}
