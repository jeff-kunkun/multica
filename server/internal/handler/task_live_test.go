package handler

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Fake secrets are assembled at run time so no literal token sits in the
// source for a secret scanner to flag.
var (
	liveFakeGitHubToken = "gh" + "p_" + strings.Repeat("Q7w8", 10)
	liveFakeAPIKey      = "s" + "k-" + strings.Repeat("Mn45", 8)
)

func reportTaskLive(t *testing.T, taskID string, body map[string]any) map[string]any {
	t.Helper()
	req := withURLParam(newOwnerDaemonRequest(http.MethodPost, "/api/daemon/tasks/"+taskID+"/live", body), "taskId", taskID)
	return testutil.Call(t, testHandler.ReportTaskLive, req).Want(http.StatusOK).Map()
}

func listTaskLive(t *testing.T) TaskLiveListResponse {
	t.Helper()
	req := withChatTestWorkspaceCtx(t, newRequest(http.MethodGet, "/api/task-live", nil))
	var resp TaskLiveListResponse
	testutil.Call(t, testHandler.ListTaskLive, req).Want(http.StatusOK).JSON(&resp)
	return resp
}

func findTaskLive(resp TaskLiveListResponse, taskID string) (protocol.TaskLivePayload, bool) {
	for _, live := range resp.Live {
		if live.TaskID == taskID {
			return live, true
		}
	}
	return protocol.TaskLivePayload{}, false
}

// A daemon of any version may call in, so the server redacts again on
// arrival: a fake token sent in the clear reaches neither the stored row, the
// pushed event nor the list (DENE-2667 PR 4).
func TestReportTaskLive_RedactsOnArrival(t *testing.T) {
	if testPool == nil {
		t.Skip("test database not available")
	}
	agentID, runtimeID, _ := createRuntimeGuardAgent(t, context.Background())
	issueID := dbfx.Issue(t, "live status redaction")
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID, "issue_id": issueID, "status": "running",
		"started_at": testutil.Raw("now() - interval '2 minutes'"),
	})

	var mu sync.Mutex
	var pushed []protocol.TaskLivePayload
	testHandler.Bus.Subscribe(protocol.EventTaskLiveUpdated, func(e events.Event) {
		if live, ok := e.Payload.(protocol.TaskLivePayload); ok && live.TaskID == taskID {
			mu.Lock()
			pushed = append(pushed, live)
			mu.Unlock()
		}
	})

	resp := reportTaskLive(t, taskID, map[string]any{
		"tool":            "Bash",
		"command":         "GITHUB_TOKEN=" + liveFakeGitHubToken + " gh release list",
		"last_output":     "export OPENAI_API_KEY=" + liveFakeAPIKey,
		"tools_in_flight": 1,
		"idle_seconds":    90,
	})
	if resp["status"] != "ok" {
		t.Fatalf("report on a running issue run = %v, want ok", resp)
	}

	var command, lastOutput string
	var idle float64
	dbfx.QueryRow(t, `
SELECT command, last_output, extract(epoch FROM reported_at - last_activity_at)
FROM task_live_status WHERE task_id = $1::uuid`, taskID).Scan(&command, &lastOutput, &idle)
	if !strings.Contains(command, "gh release list") || idle < 89 || idle > 91 {
		t.Fatalf("stored row = %q / %q, silence %.0fs; want the command and 90s", command, lastOutput, idle)
	}

	mu.Lock()
	got := append([]protocol.TaskLivePayload(nil), pushed...)
	mu.Unlock()
	if len(got) != 1 || got[0].IssueID != issueID || got[0].Tool != "Bash" {
		t.Fatalf("pushed events = %+v, want one for the issue", got)
	}

	live, ok := findTaskLive(listTaskLive(t), taskID)
	if !ok || live.ToolsInFlight != 1 || live.IdleSeconds < 90 {
		t.Fatalf("list entry = %+v (found %v)", live, ok)
	}

	for where, text := range map[string]string{
		"stored command": command, "stored output": lastOutput,
		"pushed command": got[0].Command, "pushed output": got[0].LastOutput,
		"listed command": live.Command, "listed output": live.LastOutput,
	} {
		for _, secret := range []string{liveFakeGitHubToken, liveFakeAPIKey} {
			if strings.Contains(text, secret) {
				t.Fatalf("%s leaked a fake secret: %q", where, text)
			}
		}
	}
}

// A run that is not running, or has no issue (a chat run), has no live view: the report
// is accepted and dropped, which tells the daemon to stop sending.
func TestReportTaskLive_IgnoresRunsWithoutLiveView(t *testing.T) {
	if testPool == nil {
		t.Skip("test database not available")
	}
	agentID, runtimeID, _ := createRuntimeGuardAgent(t, context.Background())
	issueID := dbfx.Issue(t, "live status finished run")
	for name, cols := range map[string]testutil.Cols{
		"finished": {"runtime_id": runtimeID, "issue_id": issueID, "status": "completed",
			"started_at": testutil.Raw("now() - interval '1 minute'"), "completed_at": testutil.Raw("now()")},
		"chat run": {"runtime_id": runtimeID, "chat_session_id": createHandlerTestChatSession(t, agentID),
			"status": "running", "started_at": testutil.Raw("now()")},
	} {
		t.Run(name, func(t *testing.T) {
			taskID := dbfx.Task(t, agentID, cols)
			if resp := reportTaskLive(t, taskID, map[string]any{"command": "make test"}); resp["status"] != "ignored" {
				t.Fatalf("report = %v, want ignored", resp)
			}
			if n := dbfx.Count(t, `SELECT count(*) FROM task_live_status WHERE task_id = $1::uuid`, taskID); n != 0 {
				t.Fatalf("stored %d rows for a run without a live view", n)
			}
		})
	}
}

// The list serves only running runs on issues the caller can see: a run on
// another member's private issue, and a run that has since finished, are
// left out even though their rows remain.
func TestListTaskLive_HidesInvisibleAndFinishedRuns(t *testing.T) {
	if testPool == nil {
		t.Skip("test database not available")
	}
	hiddenIssueID, hiddenTaskID := hiddenIssueTaskFixture(t)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', completed_at = NULL WHERE id = $1::uuid`, hiddenTaskID)

	agentID, runtimeID, _ := createRuntimeGuardAgent(t, context.Background())
	issueID := dbfx.Issue(t, "live status visible run")
	visibleTaskID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID, "issue_id": issueID, "status": "running", "started_at": testutil.Raw("now()"),
	})
	finishedTaskID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID, "issue_id": issueID, "status": "running", "started_at": testutil.Raw("now()"),
	})
	insertLive := func(taskID, issueID, agentID string) {
		t.Helper()
		dbfx.Exec(t, `
INSERT INTO task_live_status (task_id, workspace_id, issue_id, agent_id, command, last_activity_at)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'make test', now())`, taskID, testWorkspaceID, issueID, agentID)
	}
	var hiddenAgentID string
	dbfx.QueryRow(t, `SELECT agent_id::text FROM agent_task_queue WHERE id = $1::uuid`, hiddenTaskID).Scan(&hiddenAgentID)
	insertLive(hiddenTaskID, hiddenIssueID, hiddenAgentID)
	insertLive(visibleTaskID, issueID, agentID)
	insertLive(finishedTaskID, issueID, agentID)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1::uuid`, finishedTaskID)

	resp := listTaskLive(t)
	if _, ok := findTaskLive(resp, visibleTaskID); !ok {
		t.Fatalf("visible running run missing from %+v", resp.Live)
	}
	if _, ok := findTaskLive(resp, hiddenTaskID); ok {
		t.Fatal("run on another member's private issue was listed")
	}
	if _, ok := findTaskLive(resp, finishedTaskID); ok {
		t.Fatal("finished run was listed as live")
	}
}
