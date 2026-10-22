package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestMentionOfDisabledSeatRelaysToAnotherFamily(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	off := createHandlerTestAgent(t, "孙悟饭", nil)
	cover := createHandlerTestAgent(t, "布尔玛", nil)
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent SET work_enabled = false, routing_tier = 'strongest' WHERE id = $1
	`, off); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent SET routing_tier = 'strongest', work_enabled = true WHERE id = $1`, cover); err != nil {
		t.Fatal(err)
	}

	var number int
	if err := testPool.QueryRow(context.Background(), `
		UPDATE workspace
		SET issue_counter = GREATEST(issue_counter, (SELECT COALESCE(MAX(number), 0) FROM issue WHERE workspace_id = $1)) + 1
		WHERE id = $1 RETURNING issue_counter
	`, testWorkspaceID).Scan(&number); err != nil {
		t.Fatal(err)
	}
	issueID := dbfxInsertIssue(t, number)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, issueID)
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	req := newRequest("POST", "/api/issues/"+issueID+"/comments", map[string]any{
		"content": fmt.Sprintf("[@孙悟饭](mention://agent/%s) 看一下", off),
	})
	req = withURLParam(req, "id", issueID)
	testutil.Call(t, testHandler.CreateComment, req).Want(http.StatusCreated)

	var queuedOff, queuedCover int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'`, issueID, off).Scan(&queuedOff); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'`, issueID, cover).Scan(&queuedCover); err != nil {
		t.Fatal(err)
	}
	if queuedOff != 0 || queuedCover != 1 {
		t.Fatalf("queued off=%d cover=%d, want 0 and 1", queuedOff, queuedCover)
	}
	var bodies string
	if err := testPool.QueryRow(context.Background(), `SELECT coalesce(string_agg(content, '\n'), '') FROM comment WHERE issue_id = $1`, issueID).Scan(&bodies); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bodies, "已停用") || !strings.Contains(bodies, "布尔玛") {
		t.Fatalf("relay comment = %s", bodies)
	}
}

func dbfxInsertIssue(t *testing.T, number int) string {
	t.Helper()
	return dbfx.Insert(t, "issue", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"creator_type": "member",
		"creator_id":   testUserID,
		"title":        "disabled mention relay",
		"number":       number,
	})
}

// ADR-0008: the seat relay asks routing.SeatSelectable, so a mention_only
// seat on the right rung and family is passed over, while the same seat in
// auto mode is the pick.
func TestSeatRelaySkipsMentionOnlySeat(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	off := createHandlerTestAgent(t, "孙悟饭", nil)
	cover := createHandlerTestAgent(t, "布尔玛", nil)
	if _, err := testPool.Exec(ctx, `UPDATE agent SET work_enabled = false, routing_tier = 'strongest' WHERE id = $1`, off); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent SET routing_tier = 'strongest', dispatch_mode = 'auto' WHERE id = $1`, cover); err != nil {
		t.Fatal(err)
	}
	failed, err := testHandler.Queries.GetAgent(ctx, parseUUID(off))
	if err != nil {
		t.Fatal(err)
	}
	wsID := parseUUID(testWorkspaceID)

	got, _, ok := testHandler.substituteAgent(ctx, wsID, failed, nil, db.Issue{WorkspaceID: wsID})
	if !ok || uuidToString(got.ID) != cover {
		t.Fatalf("auto control: substitute = %s ok=%v, want 布尔玛 %s", uuidToString(got.ID), ok, cover)
	}

	if _, err := testPool.Exec(ctx, `UPDATE agent SET dispatch_mode = 'mention_only' WHERE id = $1`, cover); err != nil {
		t.Fatal(err)
	}
	got, _, ok = testHandler.substituteAgent(ctx, wsID, failed, nil, db.Issue{WorkspaceID: wsID})
	if ok && uuidToString(got.ID) == cover {
		t.Fatal("seat relay picked a mention_only seat")
	}
}
