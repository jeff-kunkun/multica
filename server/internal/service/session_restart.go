package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

func freshSessionNoticeReason(reason string) bool {
	switch reason {
	case string(taskfailure.ReasonAgentProviderCapacityOrRateLimit),
		string(taskfailure.ReasonAgentProviderNetwork),
		string(taskfailure.ReasonAgentProviderServerError),
		string(taskfailure.ReasonTimeout),
		string(taskfailure.ReasonRuntimeOffline),
		string(taskfailure.ReasonRuntimeRecovery),
		serverInterruptFailureReason:
		return true
	default:
		return false
	}
}

// noteUnresumedRetry says the automatic retry opened a new CLI session
// because the failed run left nothing that can be resumed.
func (s *TaskService) noteUnresumedRetry(ctx context.Context, task db.AgentTaskQueue, rolloutMissing bool) {
	why := "上一轮在留下可恢复的 CLI session 之前就失败了。"
	if rolloutMissing {
		why = "上一轮的 Codex 会话文件没有落到本机，续不上。"
	}
	s.NoteSessionRestart(ctx, task, why)
}

// NoteSessionRestart posts the human explanation when a run had to open a
// new CLI session. Empty reason posts nothing. The same task reports at
// most once.
func (s *TaskService) NoteSessionRestart(ctx context.Context, task db.AgentTaskQueue, reason string) {
	reason = strings.TrimSpace(reason)
	if s == nil || s.Queries == nil || reason == "" || !task.IssueID.Valid {
		return
	}
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if err != nil {
		slog.Warn("session restart notice: load issue failed",
			"task_id", util.UUIDToString(task.ID),
			"error", err,
		)
		return
	}
	comment, err := s.Queries.CreateRoutingComment(ctx, db.CreateRoutingCommentParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorID:    pgtype.UUID{Valid: true},
		Content:     "这次运行新开了 CLI 对话，没有续上原来的会话。原因：" + reason,
		RoutingKind: "session_restart:" + util.UUIDToString(task.ID),
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("session restart notice: comment failed",
				"task_id", util.UUIDToString(task.ID),
				"error", err,
			)
		}
		return
	}
	if s.Bus == nil {
		return
	}
	s.Bus.Publish(events.Event{
		Type:        protocol.EventCommentCreated,
		WorkspaceID: util.UUIDToString(comment.WorkspaceID),
		ActorType:   "system",
		Payload: map[string]any{
			"comment": commentEventFields(comment),
		},
	})
}

// RequeueStrandedIssues gives a seat that has just been switched back on the
// issues it still owns but has no run for (DENE-1093). While the seat was
// off, a trigger on those issues was refused, so nothing would ever pick them
// up again. Each one gets one ordinary run; an issue that cannot be enqueued
// (triage, archived seat) is skipped and logged, never fatal.
func (s *TaskService) RequeueStrandedIssues(ctx context.Context, agentID pgtype.UUID) int {
	if s == nil || s.Queries == nil || !agentID.Valid {
		return 0
	}
	agent, err := s.Queries.GetAgent(ctx, agentID)
	if err != nil || !agent.WorkEnabled || agent.ArchivedAt.Valid {
		return 0
	}
	issues, err := s.Queries.ListStrandedAgentIssues(ctx, agentID)
	if err != nil {
		slog.Warn("requeue stranded issues: list failed", "agent_id", util.UUIDToString(agentID), "error", err)
		return 0
	}
	queued := 0
	for _, issue := range issues {
		if _, err := s.EnqueueTaskForIssue(ctx, issue); err != nil {
			slog.Warn("requeue stranded issues: enqueue failed",
				"agent_id", util.UUIDToString(agentID),
				"issue_id", util.UUIDToString(issue.ID),
				"error", err,
			)
			continue
		}
		queued++
	}
	return queued
}
