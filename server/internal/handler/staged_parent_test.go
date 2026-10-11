package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// DENE-1777: with the child_done rule on and nobody holding the parent, a
// closed stage still promotes the next stage's clear sub-issues, and the one
// that needs a decision reaches a person's inbox instead of stopping silently.
func TestStageCloseOnUnheldParentAdvancesUnderSystemRule(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	fx := newChildDoneFixture(t, "in_progress")
	if w := putChildDoneRule(t, fx.parent.ID, map[string]any{"enabled": true}); w.Code != http.StatusOK {
		t.Fatalf("enable rule: %d %s", w.Code, w.Body.String())
	}
	if _, err := testPool.Exec(ctx, `UPDATE issue SET stage = 1 WHERE id = $1`, fx.child.ID); err != nil {
		t.Fatal(err)
	}
	clear := createStagedChild(t, fx.parent.ID, 2, "backlog")
	held := createStagedChild(t, fx.parent.ID, 2, "backlog")
	if _, err := testPool.Exec(ctx, `UPDATE issue SET description = '没有额外依赖' WHERE id = $1`, clear.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE issue SET description = '先确认数据口径再开工' WHERE id = $1`, held.ID); err != nil {
		t.Fatal(err)
	}
	if rules := listSystemWakeupsFor(t, fx.parent.ID); len(rules) != 1 || rules[0].OnMet != "advance" || rules[0].Blocked != "no_assignee" {
		t.Fatalf("rule before close = %+v, want on_met advance", rules)
	}

	updateChildStatus(t, fx.child.ID, "done")

	if got := readIssueStatus(t, clear.ID); got != "todo" {
		t.Fatalf("clear stage-2 sub-issue = %q, want todo", got)
	}
	if got := readIssueStatus(t, held.ID); got != "backlog" {
		t.Fatalf("held stage-2 sub-issue = %q, want backlog", got)
	}
	var inbox int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM inbox_item WHERE issue_id = $1 AND recipient_type = 'member'`, fx.parent.ID).Scan(&inbox); err != nil {
		t.Fatal(err)
	}
	if inbox == 0 {
		t.Fatal("no inbox item for the held sub-issue; the stage stopped silently")
	}
}

// DENE-1777: an agent that leaves the parent's executor empty and adds a
// staged sub-issue gets a coordinator on that parent, whether the parent was
// created with the default todo, parked in backlog, or already coordinating.
// The parent coordinates (in_progress) and no run starts on it.
func TestAgentStagedChildSeatsTheParent(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := handlerTestAgentID(t)
	for _, created := range []string{"", "backlog", "in_progress"} {
		t.Run("status="+created, func(t *testing.T) {
			// An empty status is CreateIssue's default (todo).
			parent := createIssueHTTP(t, "staged parent "+time.Now().Format(time.RFC3339Nano), created)
			t.Cleanup(func() { cleanupChildDoneIssue(parent.ID) })
			if created == "" && parent.Status != "todo" {
				t.Fatalf("default parent status = %q, want todo", parent.Status)
			}
			if _, err := testPool.Exec(ctx, `UPDATE issue SET creator_type = 'agent', creator_id = $2 WHERE id = $1`, parent.ID, agentID); err != nil {
				t.Fatal(err)
			}

			w := httptest.NewRecorder()
			req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
				"title": "stage one", "status": "todo", "parent_issue_id": parent.ID, "stage": 1,
			})
			asAgent(t, req, parent.ID)
			ownTask := req.Header.Get("X-Task-ID")
			testHandler.CreateIssue(w, req)
			if w.Code != http.StatusCreated {
				t.Fatalf("create staged child: %d %s", w.Code, w.Body.String())
			}
			var child IssueResponse
			if err := json.NewDecoder(w.Body).Decode(&child); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cleanupChildDoneIssue(child.ID) })

			var status string
			var assigneeType, assigneeID *string
			if err := testPool.QueryRow(ctx, `SELECT status, assignee_type, assignee_id::text FROM issue WHERE id = $1`, parent.ID).Scan(&status, &assigneeType, &assigneeID); err != nil {
				t.Fatal(err)
			}
			if status != "in_progress" {
				t.Fatalf("parent status = %q, want in_progress", status)
			}
			if assigneeType == nil || *assigneeType != "agent" || assigneeID == nil || *assigneeID != agentID {
				t.Fatalf("parent assignee = %v/%v, want the creating agent", assigneeType, assigneeID)
			}
			var runs int
			if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND id <> $2`, parent.ID, ownTask).Scan(&runs); err != nil {
				t.Fatal(err)
			}
			if runs != 0 {
				t.Fatalf("parent got %d runs, want none", runs)
			}
		})
	}
}
