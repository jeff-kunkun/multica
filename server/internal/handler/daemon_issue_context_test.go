package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestClaimTaskByRuntime_InlinesIssueContextComments(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := dbfx.Runtime(t, "issue context runtime")
	agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "issue context agent")
	rootID := dbfx.Comment(t, issueID, "中文根评论", testutil.Cols{"author_type": "member", "author_id": testUserID})
	dbfx.Comment(t, issueID, "回复内容", testutil.Cols{"parent_id": rootID, "author_type": "agent", "author_id": agentID})
	taskID := createDispatchedClaimFixtureTask(t, ctx, agentID, runtimeID, issueID, "120 seconds", false)
	dbfx.Exec(t, "UPDATE agent_task_queue SET trigger_comment_id = $2 WHERE id = $1", taskID, rootID)

	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil, testWorkspaceID, "issue-context-claim")
	req = withURLParam(req, "runtimeId", runtimeID)
	var response struct {
		Task *AgentTaskResponse `json:"task"`
	}
	testutil.Call(t, testHandler.ClaimTaskByRuntime, req).Want(http.StatusOK).JSON(&response)
	if response.Task == nil {
		t.Fatal("expected claimed task")
	}
	if len(response.Task.IssueCommentSummaries) != 1 {
		t.Fatalf("issue_comment_summaries = %#v, want one root", response.Task.IssueCommentSummaries)
	}
	summary := response.Task.IssueCommentSummaries[0]
	if summary.Content != "中文根评论" || summary.AuthorType != "member" || summary.ReplyCount != 1 || summary.LastActivityAt == "" {
		t.Fatalf("root summary = %#v, want content/author/reply stats", summary)
	}
	if len(response.Task.IssueTriggerThread) != 2 || response.Task.IssueTriggerThread[0].Content != "中文根评论" || response.Task.IssueTriggerThread[1].Content != "回复内容" {
		t.Fatalf("issue_trigger_thread = %#v, want root and reply", response.Task.IssueTriggerThread)
	}
	if len(response.Task.IssueNewComments) != 0 {
		t.Fatalf("issue_new_comments = %#v, want empty on cold claim", response.Task.IssueNewComments)
	}
}

func TestTruncateUTF8(t *testing.T) {
	got, truncated := truncateUTF8("前缀中文内容", 9)
	if !truncated || got != "前缀中" {
		t.Fatalf("truncateUTF8 = %q, %t; want valid rune boundary", got, truncated)
	}
}
