package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func callStageAdvance(t *testing.T, parentID string) (int, StageAdvanceResponse) {
	t.Helper()
	req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+parentID+"/stage-advance", map[string]any{}), "id", parentID)
	rec := httptest.NewRecorder()
	testHandler.AdvanceIssueStage(rec, req)
	var resp StageAdvanceResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode stage advance response (%d): %v", rec.Code, err)
	}
	return rec.Code, resp
}

// DENE-864: advance refuses while the running stage has open work and names
// the ticket, then promotes the next stage's backlog once it is terminal.
func TestStageAdvanceRefusesUntilStageTerminalThenPromotes(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "stage-advance-executor", []byte("[]"))
	parent := dbfx.Issue(t, "advance parent", testutil.Cols{"status": "in_progress"})
	dbfx.Issue(t, "advance s1 done", testutil.Cols{"status": "done", "parent_issue_id": parent, "stage": 1})
	s1open := dbfx.Issue(t, "advance s1 open", testutil.Cols{"status": "in_progress", "parent_issue_id": parent, "stage": 1})
	s2 := dbfx.Issue(t, "advance s2", testutil.Cols{
		"status": "backlog", "parent_issue_id": parent, "stage": 2,
		"assignee_type": "agent", "assignee_id": agentID,
	})
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, s2)
	})

	code, refused := callStageAdvance(t, parent)
	if code != http.StatusConflict || refused.Advanced {
		t.Fatalf("advance with open stage 1 = %d %+v, want 409 not advanced", code, refused)
	}
	if len(refused.Pending) != 1 || refused.Pending[0].ID != s1open || !strings.Contains(refused.Message, "in_progress") {
		t.Fatalf("refusal = %+v, want it to name the open stage-1 ticket and its status", refused)
	}
	var status string
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, s2).Scan(&status)
	if status != "backlog" {
		t.Fatalf("refused advance moved stage 2 to %q", status)
	}

	dbfx.Exec(t, `UPDATE issue SET status = 'done' WHERE id = $1`, s1open)
	code, advanced := callStageAdvance(t, parent)
	if code != http.StatusOK || !advanced.Advanced || advanced.Stage != 2 {
		t.Fatalf("advance after stage 1 done = %d %+v, want stage 2 advanced", code, advanced)
	}
	if len(advanced.Promoted) != 1 || advanced.Promoted[0].ID != s2 || !advanced.Promoted[0].RunStarted {
		t.Fatalf("promoted = %+v, want s2 with its run started", advanced.Promoted)
	}
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, s2).Scan(&status)
	if status != "todo" {
		t.Fatalf("stage 2 status after advance = %q, want todo", status)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, s2, agentID); n != 1 {
		t.Fatalf("stage 2 executor has %d runs, want 1", n)
	}

	// Stage 2 is now running: a second advance refuses and names it.
	code, again := callStageAdvance(t, parent)
	if code != http.StatusConflict || len(again.Pending) != 1 || again.Pending[0].ID != s2 {
		t.Fatalf("second advance = %d %+v, want 409 naming the running stage-2 ticket", code, again)
	}
}

type testStageChild struct {
	stage  int32
	status string
}

func testStageChildren(spec []testStageChild) ([]db.Issue, resolvedChildStatuses) {
	issues := make([]db.Issue, 0, len(spec))
	statuses := resolvedChildStatuses{}
	for i, c := range spec {
		issue := db.Issue{
			ID:     pgtype.UUID{Bytes: [16]byte{byte(i + 1)}, Valid: true},
			Stage:  pgtype.Int4{Int32: c.stage, Valid: true},
			Status: c.status,
		}
		issues = append(issues, issue)
		statuses[issue.ID] = c.status
	}
	return issues, statuses
}

func TestPlanStageAdvancePicksLowestOpenStage(t *testing.T) {
	issues, statuses := testStageChildren([]testStageChild{{1, "done"}, {2, "backlog"}, {2, "todo"}, {3, "backlog"}})
	stage, promote, pending := planStageAdvance(issues, statuses)
	if stage != 2 || len(promote) != 1 || promote[0].ID != issues[1].ID || len(pending) != 1 || pending[0].ID != issues[2].ID {
		t.Fatalf("stage=%d promote=%d pending=%d, want stage 2 promoting the held backlog item and reporting the running one", stage, len(promote), len(pending))
	}
	done, doneStatuses := testStageChildren([]testStageChild{{1, "done"}, {2, "cancelled"}})
	if stage, _, _ := planStageAdvance(done, doneStatuses); stage != 0 {
		t.Fatalf("all-terminal stage = %d, want 0", stage)
	}
}
