package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Comment modes for an @agent who is not the one running (DENE-1350). The
// running agent is A, the mentioned one is B.
const (
	// commentModeHandoff stops A and starts B in a new session.
	commentModeHandoff = "handoff"
	// commentModeParallel leaves A running and starts B alongside it.
	commentModeParallel = "parallel"
)

type freshCommentSessionKey struct{}

// withFreshCommentSession marks the comment's triggers to start new sessions:
// a handed-to agent begins from the comment, not from an old conversation.
func withFreshCommentSession(ctx context.Context) context.Context {
	return context.WithValue(ctx, freshCommentSessionKey{}, true)
}

func freshCommentSession(ctx context.Context) bool {
	v, _ := ctx.Value(freshCommentSessionKey{}).(bool)
	return v
}

// stopForCommentHandoff stops every active run on the issue whose agent the
// comment does not wake. The caller's own run is left alone: an agent that
// hands off finishes its turn by itself.
func (h *Handler) stopForCommentHandoff(r *http.Request, issue db.Issue, triggers []commentAgentTrigger, actorType, actorID string) []string {
	ctx := r.Context()
	tasks, err := h.Queries.ListActiveTasksByIssue(ctx, issue.ID)
	if err != nil {
		slog.Warn("handoff comment: list active tasks failed", "issue_id", uuidToString(issue.ID), "error", err)
		return nil
	}
	woken := make(map[string]bool, len(triggers))
	for _, trigger := range triggers {
		woken[uuidToString(trigger.Agent.ID)] = true
	}
	var own pgtype.UUID
	if actorType == "agent" {
		if task, ok := h.taskFromRequestHeader(r); ok {
			own = task.ID
		}
	}
	var stopped []string
	for _, task := range tasks {
		if woken[uuidToString(task.AgentID)] || (own.Valid && task.ID == own) {
			continue
		}
		if _, err := h.TaskService.CancelTaskWithResult(ctx, task.ID, service.CancelTaskOptions{
			CancelledBy:   h.taskCancellationActor(ctx, actorType, actorID),
			UserInitiated: true,
		}); err != nil {
			slog.Warn("handoff comment: stop run failed", "task_id", uuidToString(task.ID), "error", err)
			continue
		}
		stopped = append(stopped, uuidToString(task.ID))
	}
	return stopped
}
