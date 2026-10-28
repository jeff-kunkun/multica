package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// A person's 长任务 label tells the daemon not to stop the run for going
// quiet; the same label attached by an agent does not. The daemon fixes the
// policy at claim, so the run list shows the claim's policy for a claimed run
// even after the label changes mid-run, and the current label for a run not
// yet claimed (DENE-2667).
func TestClaimTask_IdleUnlimitedFollowsPersonLabel(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	var labelID string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO issue_label (workspace_id, resource_type, name, color)
		 VALUES ($1, 'issue', $2, '#000000') RETURNING id`,
		testWorkspaceID, longRunningLabel,
	).Scan(&labelID); err != nil {
		t.Fatalf("insert label: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue_label WHERE id = $1`, labelID) })

	cases := []struct {
		name       string
		attachedBy string // "" leaves the issue unlabelled at claim
		midRun     string // "add" a person's label or "remove" it after the claim
		want       bool
	}{
		{"no label", "", "", false},
		{"labelled by a person", "member", "", true},
		{"labelled by an agent", "agent", "", false},
		{"person adds the label mid-run", "", "add", false},
		{"person removes the label mid-run", "member", "remove", true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agentID, runtimeID, _ := createRuntimeGuardAgent(t, ctx)
			issueID := dbfx.Issue(t, "idle unlimited "+tc.name, testutil.Cols{"number": 88201 + i})
			if tc.attachedBy != "" {
				attachIdleLabel(t, ctx, issueID, labelID, tc.attachedBy)
			}
			dbfx.Task(t, agentID, testutil.Cols{"runtime_id": runtimeID, "issue_id": issueID})

			// Before the claim the queued run shows what its claim will read.
			if got := listedIdleUnlimited(t, issueID); got != tc.want {
				t.Fatalf("queued run idle_unlimited = %v, want %v", got, tc.want)
			}

			req := newOwnerDaemonRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/claim", nil)
			req = withURLParam(req, "runtimeId", runtimeID)
			w := testutil.Call(t, testHandler.ClaimTaskByRuntime, req).Want(http.StatusOK)
			var resp struct {
				Task *struct {
					ID            string `json:"id"`
					IdleUnlimited *bool  `json:"idle_unlimited"`
				} `json:"task"`
			}
			w.JSON(&resp)
			if resp.Task == nil {
				t.Fatal("expected task in response")
			}
			got := resp.Task.IdleUnlimited != nil && *resp.Task.IdleUnlimited
			if got != tc.want {
				t.Fatalf("idle_unlimited = %v, want %v", got, tc.want)
			}
			// Off is omitted, so the claim older daemons read is unchanged.
			if !tc.want && resp.Task.IdleUnlimited != nil {
				t.Fatal("idle_unlimited present when off")
			}
			if got := listedIdleUnlimited(t, issueID); got != tc.want {
				t.Fatalf("dispatched run idle_unlimited = %v, want the claim's %v", got, tc.want)
			}

			dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, resp.Task.ID)
			switch tc.midRun {
			case "add":
				attachIdleLabel(t, ctx, issueID, labelID, "member")
			case "remove":
				dbfx.Exec(t, `DELETE FROM issue_to_label WHERE issue_id = $1 AND label_id = $2`, issueID, labelID)
			}
			if got := listedIdleUnlimited(t, issueID); got != tc.want {
				t.Fatalf("running run idle_unlimited = %v, want the claim's %v", got, tc.want)
			}
		})
	}
}

func attachIdleLabel(t *testing.T, ctx context.Context, issueID, labelID, attachedBy string) {
	t.Helper()
	if _, err := testPool.Exec(ctx,
		`INSERT INTO issue_to_label (issue_id, label_id, attached_by_type) VALUES ($1, $2, $3)`,
		issueID, labelID, attachedBy); err != nil {
		t.Fatalf("attach label: %v", err)
	}
}

// listedIdleUnlimited reads the issue's one run from the run list.
func listedIdleUnlimited(t *testing.T, issueID string) bool {
	t.Helper()
	req := withURLParam(newRequest("GET", "/api/issues/"+issueID+"/tasks", nil), "id", issueID)
	w := testutil.Call(t, testHandler.ListTasksByIssue, req).Want(http.StatusOK)
	var list []AgentTaskResponse
	w.JSON(&list)
	if len(list) != 1 {
		t.Fatalf("task list = %+v, want one run", list)
	}
	return list[0].IdleUnlimited
}
