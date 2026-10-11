package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// RecoverMergedChatInputs runs under the terminal transaction's session lock.
// Completion hands only unread supplements to one successor. Failure first
// returns them to the original input owner, so a standard retry still reads the
// original ask. finalFailure is true only after the caller decided no retry was
// created; that phase can start the unread supplements as their own turn.
func RecoverMergedChatInputs(ctx context.Context, q *db.Queries, task db.AgentTaskQueue, finalFailure bool) error {
	if !task.ChatSessionID.Valid || (task.Status != "completed" && task.Status != "failed") {
		return nil
	}
	session, err := q.GetChatSession(ctx, task.ChatSessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if session.Status != "active" || session.ConvertedIssueID.Valid {
		return nil
	}
	messages, err := q.ListUndeliveredMergedChatMessages(ctx, task.ID)
	if err != nil {
		return err
	}
	if len(messages) == 0 {
		return nil
	}
	ids := make([]pgtype.UUID, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	if task.Status == "failed" && !finalFailure {
		_, err := q.RebindMergedChatMessages(ctx, db.RebindMergedChatMessagesParams{SuccessorTaskID: chatInputOwnerID(task), MessageIds: ids})
		return err
	}
	carrier, err := q.GetAgent(ctx, task.AgentID)
	if err != nil {
		return err
	}
	if carrier.ArchivedAt.Valid || !carrier.WorkEnabled || !carrier.RuntimeID.Valid {
		return nil
	}
	var successor db.AgentTaskQueue
	if task.ChannelContextRevision.Valid {
		successor, err = q.GetQueuedChannelChatMergeTarget(ctx, db.GetQueuedChannelChatMergeTargetParams{ChatSessionID: task.ChatSessionID, ChannelContextRevision: task.ChannelContextRevision})
	} else {
		successor, err = q.GetQueuedChatMergeTarget(ctx, task.ChatSessionID)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	created := false
	if errors.Is(err, pgx.ErrNoRows) {
		created = true
		successor, err = q.CreateChatTask(ctx, db.CreateChatTaskParams{
			ID: dbid.NewV7(), AgentID: task.AgentID, RuntimeID: carrier.RuntimeID,
			Priority: 2, ChatSessionID: task.ChatSessionID, ChannelContextRevision: task.ChannelContextRevision,
			InitiatorUserID: task.InitiatorUserID, OriginatorUserID: task.OriginatorUserID,
			AccountableUserID: task.AccountableUserID, ViaBotID: task.ViaBotID, ViaBotName: task.ViaBotName,
			ForceFreshSession: pgtype.Bool{Bool: false, Valid: true},
			RuntimeMcpOverlay: task.RuntimeMcpOverlay, RuntimeConnectedApps: task.RuntimeConnectedApps,
			OriginatorSource: task.OriginatorSource, TriggerEvidenceKind: task.TriggerEvidenceKind,
			TriggerEvidenceRefID: task.TriggerEvidenceRefID,
		})
		if err != nil {
			return fmt.Errorf("create unread chat successor: %w", err)
		}
	}
	if created && task.ChannelContextRevision.Valid {
		if err := q.CopyChannelTaskDelivery(ctx, db.CopyChannelTaskDeliveryParams{ChildTaskID: successor.ID, ParentTaskID: task.ID}); err != nil {
			return err
		}
	}
	if !successor.ChatInputTaskID.Valid {
		successor, err = q.SetChatTaskInputOwnerSelf(ctx, successor.ID)
		if err != nil {
			return err
		}
	}
	if _, err := q.RebindMergedChatMessages(ctx, db.RebindMergedChatMessagesParams{SuccessorTaskID: chatInputOwnerID(successor), MessageIds: ids}); err != nil {
		return err
	}
	if task.ChannelContextRevision.Valid {
		if _, err := q.DeferMergedChannelChatTaskForPendingMedia(ctx, successor.ID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	_, err = q.MarkMergedChatMessagesRecovered(ctx, db.MarkMergedChatMessagesRecoveredParams{TaskID: task.ID, MessageIds: ids, SuccessorTaskID: successor.ID})
	return err
}

// RecoverFailedMergedChatInputs finishes the outbox handoff after the batch
// failure path decided no automatic retry was created. The new transaction
// rechecks that decision under the same locks as enqueue/claim.
func (s *TaskService) RecoverFailedMergedChatInputs(ctx context.Context, task db.AgentTaskQueue) error {
	if !task.ChatSessionID.Valid || task.Status != "failed" {
		return nil
	}
	if err := s.runInTx(ctx, func(q *db.Queries) error {
		if _, err := q.LockChatSessionForRuntimeBind(ctx, task.ChatSessionID); err != nil {
			return err
		}
		if _, err := q.GetAgentForClaimUpdate(ctx, task.AgentID); err != nil {
			return err
		}
		current, err := q.GetAgentTask(ctx, task.ID)
		if err != nil {
			return err
		}
		retried, err := q.HasRetryTaskForParent(ctx, task.ID)
		if err != nil {
			return err
		}
		if retried {
			return nil
		}
		return RecoverMergedChatInputs(ctx, q, current, true)
	}); err != nil {
		return err
	}
	s.FinalizeMergedChatRecovery(ctx, task.ID)
	return nil
}

// FinalizeMergedChatRecovery runs only after the terminal/handoff commit. A
// recovered task may bind to a different runtime than its predecessor: notify
// that runtime directly so an empty-queue cache cannot stall it. Deferred media
// input waits for its ordinary promotion fence and receives no queued event.
func (s *TaskService) FinalizeMergedChatRecovery(ctx context.Context, taskID pgtype.UUID) {
	successors, err := s.Queries.ListRecoveredChatMergeSuccessors(ctx, taskID)
	if err != nil {
		slog.Warn("load unread chat successors failed", "task_id", util.UUIDToString(taskID), "error", err)
		return
	}
	for _, successor := range successors {
		if successor.Status == "queued" {
			s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, successor)
			s.NotifyTaskEnqueued(ctx, successor)
		} else {
			s.notifyRuntimeMayHaveWork(successor.RuntimeID, "")
		}
	}
}
