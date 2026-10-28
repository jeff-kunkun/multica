package handler

import (
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// interruptFixture is a running codex turn on its own issue, woken by a
// comment that mentions its agent. interruptible marks the run as one whose
// daemon offered steer-interrupt-v1.
func interruptFixture(t *testing.T, interruptible bool) supplementFixture {
	t.Helper()
	f := newSupplementFixture(t, "codex", "running", true)
	if interruptible {
		dbfx.Exec(t, `UPDATE task_supplement_capability SET interrupt_supported = true WHERE task_id = $1`, f.taskID)
	}
	dbfx.Exec(t, `UPDATE comment SET content = $2 WHERE id = $1`, f.triggerID,
		agentMention("Supplement", f.agentID)+" run sleep 600")
	dbfx.Exec(t, `UPDATE agent_task_queue SET delivered_comment_ids = ARRAY[trigger_comment_id] WHERE id = $1`, f.taskID)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id = $1`, f.issueID)
	return f
}

// postInterruptComment posts a reply to the fixture's trigger comment, as the
// test member or, with agentTask set, as the agent running that task.
func postInterruptComment(t *testing.T, f supplementFixture, body map[string]any, agentID, agentTask string) *testutil.Response {
	t.Helper()
	body["parent_id"] = f.triggerID
	req := newRequest(http.MethodPost, "/api/issues/"+f.issueID+"/comments", body)
	if agentID != "" {
		req.Header.Set("X-Actor-Source", "task_token")
		req.Header.Set("X-Agent-ID", agentID)
		req.Header.Set("X-Task-ID", agentTask)
	}
	return testutil.Call(t, testHandler.CreateComment, withURLParam(req, "id", f.issueID))
}

func TestCreateCommentInterruptCarriesFilesToTheRunningTurn(t *testing.T) {
	f := interruptFixture(t, true)
	attachmentID := dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id": testWorkspaceID, "issue_id": f.issueID, "uploader_type": "member", "uploader_id": testUserID,
		"filename": "plan.txt", "url": "https://files.test/plan.txt", "content_type": "text/plain", "size_bytes": 12,
	})

	var created CommentResponse
	postInterruptComment(t, f, map[string]any{
		"content": "改成写 hello", "mode": "interrupt", "attachment_ids": []string{attachmentID},
	}, "", "").Want(http.StatusCreated).JSON(&created)
	if len(created.Supplements) != 1 || created.Supplements[0].TaskID != f.taskID || created.Supplements[0].DeliveryMode != "interrupt" {
		t.Fatalf("receipts = %+v, want one interrupt into the running turn", created.Supplements)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, f.issueID); n != 1 {
		t.Fatalf("interrupt created %d runs, want the running turn only", n)
	}

	claim := daemonClaimSupplement(t, f.taskID)
	if claim["comment_id"] != created.ID || claim["delivery_mode"] != "interrupt" {
		t.Fatalf("claim = %+v", claim)
	}
	files, _ := claim["attachments"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["id"] != attachmentID || files[0].(map[string]any)["filename"] != "plan.txt" {
		t.Fatalf("claim attachments = %+v", claim["attachments"])
	}

	daemonAckSupplement(t, f.taskID, created.ID, true).Want(http.StatusOK)
	receipts := testHandler.listCommentSupplements(t.Context(), parseUUID(testWorkspaceID), []pgtype.UUID{parseUUID(created.ID)})[created.ID]
	if len(receipts) != 1 || receipts[0].Status != "delivered" || receipts[0].DeliveredAfterMs == nil || *receipts[0].DeliveredAfterMs < 0 {
		t.Fatalf("delivered receipt = %+v", receipts)
	}
}

func TestCreateCommentInterruptFallsBackToSupplementWhenTheRunCannotStop(t *testing.T) {
	f := interruptFixture(t, false)
	var created CommentResponse
	postInterruptComment(t, f, map[string]any{"content": "改成写 hello", "mode": "interrupt"}, "", "").
		Want(http.StatusCreated).JSON(&created)
	if len(created.Supplements) != 1 || created.Supplements[0].DeliveryMode != "" {
		t.Fatalf("receipts = %+v, want one supplement", created.Supplements)
	}
	if claim := daemonClaimSupplement(t, f.taskID); claim["comment_id"] != created.ID || claim["delivery_mode"] != nil {
		t.Fatalf("claim = %+v, want a plain supplement", claim)
	}
}

func TestCreateCommentInterruptFromAManagerAgent(t *testing.T) {
	f := interruptFixture(t, true)
	// The manager posts from its own run, which a member started; an agent
	// without a human behind it may not wake a private agent at all.
	managerID := dbfx.Agent(t, "Manager", f.runtimeID)
	managerTask := dbfx.Task(t, managerID, testutil.Cols{
		"issue_id": f.issueID, "runtime_id": f.runtimeID, "status": "running", "started_at": testutil.Raw("now()"),
		"originator_source": "direct_human", "originator_user_id": testUserID, "accountable_user_id": testUserID,
	})
	var created CommentResponse
	postInterruptComment(t, f, map[string]any{
		"content": agentMention("Supplement", f.agentID) + " 改成写 hello", "mode": "interrupt",
	}, managerID, managerTask).Want(http.StatusCreated).JSON(&created)
	if created.AuthorType != "agent" || len(created.Supplements) != 1 || created.Supplements[0].DeliveryMode != "interrupt" {
		t.Fatalf("comment = %s/%+v, want an agent-authored interrupt", created.AuthorType, created.Supplements)
	}
	if claim := daemonClaimSupplement(t, f.taskID); claim["author_name"] != "Manager" {
		t.Fatalf("claim author = %v, want the manager agent's name", claim["author_name"])
	}
}

func TestCreateCommentInterruptRefusedWhenTheTurnTakesNoInput(t *testing.T) {
	f := newSupplementFixture(t, "codex", "running", false)
	dbfx.Exec(t, `UPDATE comment SET content = $2 WHERE id = $1`, f.triggerID, agentMention("Supplement", f.agentID)+" go")
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id = $1`, f.issueID)
	resp := postInterruptComment(t, f, map[string]any{"content": "stop", "mode": "interrupt"}, "", "")
	resp.Want(http.StatusConflict)
	var body map[string]any
	resp.JSON(&body)
	if body["code"] != errCodeSteerUnsupported {
		t.Fatalf("refusal = %+v", body)
	}
}

func TestCreateCommentInterruptsByDefaultOnlyForListedAuthors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		listed   bool
		wantMode string
	}{
		{name: "unlisted author keeps the supplement default", listed: false, wantMode: "supplement"},
		{name: "listed author interrupts", listed: true, wantMode: "interrupt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := interruptFixture(t, true)
			authors := "00000000-0000-0000-0000-000000000000"
			if tc.listed {
				authors += ", " + testUserID
			}
			t.Setenv(interruptDefaultAuthorsEnv, authors)
			var created CommentResponse
			postInterruptComment(t, f, map[string]any{"content": "改成写 hello"}, "", "").Want(http.StatusCreated).JSON(&created)
			// A plain comment steers the running turn implicitly either way;
			// only the listed author's goes in as an interrupt.
			var mode string
			dbfx.QueryRow(t, `SELECT delivery_mode FROM task_supplement WHERE task_id = $1 AND comment_id = $2`, f.taskID, created.ID).Scan(&mode)
			if mode != tc.wantMode {
				t.Fatalf("delivery mode = %q, want %q", mode, tc.wantMode)
			}
		})
	}
}

func TestStartTaskRecordsInterruptOnlyWhenOfferedForAnInterruptProvider(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	for _, tc := range []struct {
		provider string
		offered  bool
		want     bool
	}{
		{provider: "codex", offered: true, want: true},
		{provider: "claude", offered: true, want: true},
		{provider: "codex", offered: false, want: false},
		{provider: "grok", offered: true, want: false},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			f := newSupplementFixture(t, tc.provider, "dispatched", false)
			dbfx.Cleanup(t, `DELETE FROM task_supplement_capability WHERE task_id = $1`, f.taskID)
			capabilities := []string{protocol.DaemonCapabilityTaskSupplementV1}
			if tc.offered {
				capabilities = append(capabilities, protocol.DaemonCapabilitySteerInterruptV1)
			}
			if _, err := testHandler.TaskService.StartTask(t.Context(), parseUUID(f.taskID), capabilities...); err != nil {
				t.Fatalf("start: %v", err)
			}
			got := dbfx.Count(t, `SELECT count(*) FROM task_supplement_capability WHERE task_id = $1 AND interrupt_supported`, f.taskID) == 1
			if got != tc.want {
				t.Fatalf("interrupt_supported = %v, want %v", got, tc.want)
			}
		})
	}
}
