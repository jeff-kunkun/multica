package service

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestEnqueueCommentRunResumesOnlyWhenTriggeredByComment(t *testing.T) {
	for _, tc := range []struct {
		name               string
		withTriggerComment bool
		wantFresh          bool
	}{
		{name: "missing trigger starts fresh", wantFresh: true},
		{name: "issue comment may continue", withTriggerComment: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := newResolveOriginatorPool(t)
			ctx := context.Background()
			q := db.New(pool)
			workspaceID, userID, agentID, issueID := seedAttributionFixture(t, pool)
			t.Cleanup(func() {
				pool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
			})

			triggerCommentID := pgtype.UUID{}
			if tc.withTriggerComment {
				if err := pool.QueryRow(ctx, `
					INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, type)
					VALUES ($1, $2, 'member', $3, 'follow up', 'comment')
					RETURNING id`, issueID, workspaceID, userID).Scan(&triggerCommentID); err != nil {
					t.Fatalf("create trigger comment: %v", err)
				}
			}

			issue := db.Issue{
				ID:           util.MustParseUUID(issueID),
				AssigneeID:   util.MustParseUUID(agentID),
				Priority:     "medium",
				CreatorType:  "member",
				CreatorID:    util.MustParseUUID(userID),
				WorkspaceID:  util.MustParseUUID(workspaceID),
				AssigneeType: pgtype.Text{String: "agent", Valid: true},
			}
			svc := &TaskService{Queries: q, TxStarter: pool, Bus: events.New()}
			task, err := svc.EnqueueCommentRun(ctx, CommentRunRequest{
				Issue: issue, TriggerCommentID: triggerCommentID,
			})
			if err != nil {
				t.Fatalf("EnqueueCommentRun: %v", err)
			}
			if task.ForceFreshSession != tc.wantFresh {
				t.Errorf("ForceFreshSession = %t, want %t", task.ForceFreshSession, tc.wantFresh)
			}
			if task.TriggerCommentID.Valid != tc.withTriggerComment {
				t.Errorf("TriggerCommentID.Valid = %t, want %t", task.TriggerCommentID.Valid, tc.withTriggerComment)
			}
			if !task.ID.Valid {
				t.Fatal("task ID is empty")
			}
		})
	}
}

func TestShouldResumeIssueSession(t *testing.T) {
	issueID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	commentID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	retryID := pgtype.UUID{Bytes: [16]byte{3}, Valid: true}
	rerunID := pgtype.UUID{Bytes: [16]byte{4}, Valid: true}

	for _, tc := range []struct {
		name string
		task db.AgentTaskQueue
		want bool
	}{
		{
			name: "same-issue comment follow-up",
			task: db.AgentTaskQueue{IssueID: issueID, TriggerCommentID: commentID},
			want: true,
		},
		{
			name: "issue task without comment",
			task: db.AgentTaskQueue{IssueID: issueID},
		},
		{
			name: "explicit fresh issue comment",
			task: db.AgentTaskQueue{IssueID: issueID, TriggerCommentID: commentID, ForceFreshSession: true},
		},
		{
			name: "legacy issue retry without fresh marker",
			task: db.AgentTaskQueue{IssueID: issueID, TriggerCommentID: commentID, RetryOfTaskID: retryID},
		},
		{
			name: "legacy issue rerun without fresh marker",
			task: db.AgentTaskQueue{IssueID: issueID, TriggerCommentID: commentID, RerunOfTaskID: rerunID},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldResumeIssueSession(tc.task); got != tc.want {
				t.Errorf("ShouldResumeIssueSession() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestShouldContinueInterruptedSession(t *testing.T) {
	retryID := pgtype.UUID{Bytes: [16]byte{3}, Valid: true}
	issueID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}

	for _, tc := range []struct {
		name string
		task db.AgentTaskQueue
		want bool
	}{
		{
			name: "resume-safe chat retry",
			task: db.AgentTaskQueue{RetryOfTaskID: retryID, SessionID: pgtype.Text{String: "chat-session", Valid: true}},
			want: true,
		},
		{
			name: "legacy issue retry without fresh marker",
			task: db.AgentTaskQueue{
				IssueID: issueID, RetryOfTaskID: retryID,
				SessionID: pgtype.Text{String: "issue-session", Valid: true},
			},
		},
		{
			name: "fresh chat retry",
			task: db.AgentTaskQueue{
				RetryOfTaskID: retryID, ForceFreshSession: true,
				SessionID: pgtype.Text{String: "chat-session", Valid: true},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldContinueInterruptedSession(tc.task); got != tc.want {
				t.Errorf("ShouldContinueInterruptedSession() = %t, want %t", got, tc.want)
			}
		})
	}
}
