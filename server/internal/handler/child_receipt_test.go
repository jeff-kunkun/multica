package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/closeprotocol"
)

// seedDoneClose writes a done close whose evidence opens with summary, the
// record `multica issue close` leaves behind.
func seedDoneClose(t *testing.T, issueID, summary string) {
	t.Helper()
	ctx := context.Background()
	var commentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, type)
		VALUES ($1, $2, 'member', $3, $4, 'comment') RETURNING id
	`, issueID, testWorkspaceID, testUserID, summary+"\n\n## 证据\n测试").Scan(&commentID); err != nil {
		t.Fatalf("seed evidence: %v", err)
	}
	meta := map[string]string{}
	for _, k := range closeprotocol.Keys {
		meta[k] = ""
	}
	meta[closeprotocol.KeyConclusion] = "done"
	meta[closeprotocol.KeyStatus] = "done"
	meta[closeprotocol.KeyEvidenceCommentID] = commentID
	meta[closeprotocol.KeyNextOwnerType] = closeprotocol.OwnerNone
	meta[closeprotocol.KeyAt] = time.Now().UTC().Format(time.RFC3339)
	raw, _ := json.Marshal(meta)
	if _, err := testPool.Exec(ctx, `UPDATE issue SET metadata = COALESCE(metadata, '{}'::jsonb) || $2::jsonb WHERE id = $1`, issueID, raw); err != nil {
		t.Fatalf("seed close: %v", err)
	}
}

// A parent with three sub-tasks hears each one's conclusion (DENE-1679): the
// child-done comment lists them, the state card carries them, and the
// parent's receipt card in its source chat repeats them.
func TestParentReceivesChildReceipts(t *testing.T) {
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "Child Receipt Agent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	taskID, _ := createChatRunTask(t, agentID, sessionID, "拆三张子票做登录")
	parentID := createIssueFromChatRun(t, agentID, taskID, "Child receipt parent", nil)
	if _, err := testPool.Exec(ctx, `UPDATE issue SET status = 'in_progress', assignee_type = 'agent', assignee_id = $2 WHERE id = $1`, parentID, agentID); err != nil {
		t.Fatalf("assign parent: %v", err)
	}
	conclusions := []string{"接口改走新令牌", "界面加了登录页", "文档写进 AGENTS.md"}
	var children []string
	for i, c := range conclusions {
		id := createIssueFromChatRun(t, agentID, taskID, "Child receipt sub "+string(rune('A'+i)), map[string]any{"parent_issue_id": parentID})
		children = append(children, id)
		seedDoneClose(t, id, c)
	}
	for _, id := range children {
		setIssueStatusForTest(t, id, "done")
	}

	// The child-done comment carries every conclusion, not only a count.
	content := parentSystemCommentContent(t, parentID)
	if !strings.Contains(content, "子任务回执：") {
		t.Fatalf("child-done comment has no receipts:\n%s", content)
	}
	for i, c := range conclusions {
		if !strings.Contains(content, "mention://issue/"+children[i]+") 已完成："+c) {
			t.Fatalf("child-done comment misses %q:\n%s", c, content)
		}
	}

	// The state card lists them, in JSON and in text.
	card := getIssueContextHTTP(t, parentID, "", "")
	if len(card.Children) != 3 {
		t.Fatalf("card children = %d, want 3: %+v", len(card.Children), card.Children)
	}
	for i, c := range conclusions {
		if card.Children[i].Summary != c || card.Children[i].Status != "done" {
			t.Fatalf("card child %d = %+v", i, card.Children[i])
		}
		if !strings.Contains(card.Text, c) {
			t.Fatalf("card text misses %q:\n%s", c, card.Text)
		}
	}

	// The parent's receipt in the source chat carries them; children stay quiet.
	setIssueStatusForTest(t, parentID, "done")
	var receiptBody string
	if err := testPool.QueryRow(ctx, `
		SELECT content FROM chat_message
		WHERE chat_session_id = $1 AND message_kind = 'issue_receipt' AND content LIKE '%mention://issue/' || $2 || ')%'
	`, sessionID, parentID).Scan(&receiptBody); err != nil {
		t.Fatalf("parent receipt: %v", err)
	}
	for _, c := range conclusions {
		if !strings.Contains(receiptBody, c) {
			t.Fatalf("parent receipt misses %q:\n%s", c, receiptBody)
		}
	}
	for _, id := range children {
		if n := countReceipts(t, sessionID, id); n != 1 {
			t.Fatalf("child %s appears %d times in chat receipts, want once inside the parent's", id, n)
		}
	}
}

// A private sub-task stays off a workspace parent's comment and off the
// card of someone who cannot see it.
func TestChildReceiptsHidePrivateChildren(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Child Receipt Private Agent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	taskID, _ := createChatRunTask(t, agentID, sessionID, "私有子票")
	parentID := createIssueFromChatRun(t, agentID, taskID, "Child receipt private parent", nil)
	openID := createIssueFromChatRun(t, agentID, taskID, "Child receipt open sub", map[string]any{"parent_issue_id": parentID})
	privateID := createIssueFromChatRun(t, agentID, taskID, "Child receipt private sub", map[string]any{"parent_issue_id": parentID})
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET visibility = 'private' WHERE id = $1`, privateID); err != nil {
		t.Fatalf("make private: %v", err)
	}
	parent, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(parentID))
	if err != nil {
		t.Fatalf("load parent: %v", err)
	}
	got := testHandler.childReceipts(context.Background(), parent, visibleWithParent(parent))
	if len(got) != 1 || got[0].IssueID != openID {
		t.Fatalf("receipts with parent scope = %+v, want only the open child", got)
	}
}
