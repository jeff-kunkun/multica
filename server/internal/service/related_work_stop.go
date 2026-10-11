package service

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// StopWorkTargets stops an already authorized relation closure. All run state,
// recovery receipts and issue guards commit together before notifications.
func (s *TaskService) StopWorkTargets(ctx context.Context, workspaceID pgtype.UUID, issueIDs, chatIDs []pgtype.UUID, actor TaskCancellationActor) ([]db.AgentTaskQueue, error) {
	if (actor.Type != "member" && actor.Type != "agent") || !actor.ID.Valid {
		return nil, errors.New("stop work requires an authenticated cancellation actor")
	}
	var cancelled []db.AgentTaskQueue
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		var err error
		cancelled, err = s.StopWorkTargetsInTransaction(ctx, qtx, workspaceID, issueIDs, chatIDs, actor)
		return err
	}); err != nil {
		return nil, err
	}
	s.PublishWorkStopped(ctx, cancelled, actor)
	return cancelled, nil
}

// StopWorkTargetsInTransaction lets an explicit cancellation status and the
// related run stop share a single commit. The caller has authorized all targets.
func (s *TaskService) StopWorkTargetsInTransaction(ctx context.Context, qtx *db.Queries, workspaceID pgtype.UUID, issueIDs, chatIDs []pgtype.UUID, actor TaskCancellationActor) ([]db.AgentTaskQueue, error) {
	cancelled := make([]db.AgentTaskQueue, 0)
	tasks, err := s.LockWorkStopTargets(ctx, qtx, workspaceID, issueIDs, chatIDs)
	if err != nil {
		return nil, err
	}
	for _, id := range issueIDs {
		_, err := qtx.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
			ID: id, WorkspaceID: workspaceID, Key: "agent_halted", Value: []byte("true"),
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	for _, task := range tasks {
		stopped, err := qtx.CancelAgentTaskByUser(ctx, db.CancelAgentTaskByUserParams{
			ID:              task.ID,
			CancelledByType: pgtype.Text{String: actor.Type, Valid: true},
			CancelledByID:   actor.ID,
			CancelledByName: pgtype.Text{String: actor.Name, Valid: actor.Name != ""},
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := SettleTerminalTaskState(ctx, qtx, stopped); err != nil {
			return nil, err
		}
		if stopped.ChatSessionID.Valid {
			if err := qtx.AdvanceCancelledChatSessionPointer(ctx, stopped.ID); err != nil {
				return nil, err
			}
		}
		cancelled = append(cancelled, stopped)
	}
	return cancelled, nil
}

// LockWorkStopTargets preserves the same chat -> issue lock order for a
// standalone stop and a stop combined with a cancellation status change.
func (s *TaskService) LockWorkStopTargets(ctx context.Context, qtx *db.Queries, workspaceID pgtype.UUID, issueIDs, chatIDs []pgtype.UUID) ([]db.AgentTaskQueue, error) {
	params := db.ListActiveRunsForStopTargetsParams{WorkspaceID: workspaceID, IssueIds: issueIDs, ChatSessionIds: chatIDs}
	tasks, err := qtx.ListActiveRunsForStopTargets(ctx, params)
	if err != nil {
		return nil, err
	}
	// Chat -> agent -> task is the established lock order. Lock every
	// targeted run's chat before updating its tasks, without widening
	// the cancellation target to unrelated runs in that chat.
	lockIDs := make(map[pgtype.UUID]bool)
	for _, id := range chatIDs {
		lockIDs[id] = true
	}
	for _, task := range tasks {
		if task.ChatSessionID.Valid {
			lockIDs[task.ChatSessionID] = true
		}
	}
	ordered := make([]pgtype.UUID, 0, len(lockIDs))
	for id := range lockIDs {
		ordered = append(ordered, id)
	}
	sort.Slice(ordered, func(i, j int) bool { return string(ordered[i].Bytes[:]) < string(ordered[j].Bytes[:]) })
	for _, id := range ordered {
		if _, err := qtx.LockChatSessionForDelete(ctx, id); err != nil {
			return nil, fmt.Errorf("lock stop chat: %w", err)
		}
	}
	orderedIssues := append([]pgtype.UUID(nil), issueIDs...)
	sort.Slice(orderedIssues, func(i, j int) bool { return string(orderedIssues[i].Bytes[:]) < string(orderedIssues[j].Bytes[:]) })
	for _, id := range orderedIssues {
		if _, err := qtx.LockIssueRunControl(ctx, id); err != nil {
			return nil, fmt.Errorf("lock stop issue: %w", err)
		}
	}
	return qtx.ListActiveRunsForStopTargets(ctx, params)
}

func (s *TaskService) PublishWorkStopped(ctx context.Context, cancelled []db.AgentTaskQueue, actor TaskCancellationActor) {

	for _, task := range cancelled {
		s.captureTaskCancelled(ctx, task)
		s.finalizeCancelledChatMessage(ctx, task, CancelTaskOptions{ClientSupportsDraftRestore: true, UserInitiated: true, CancelledBy: actor})
		s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, task)
	}
	for _, id := range distinctAgentIDs(cancelled) {
		s.ReconcileAgentStatus(ctx, id)
	}
	s.notifyTasksFinished(cancelled)
}
