package service

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// AckMergedChatSupplement fences late delivery against a successor claim. If
// completion already handed an input off, a late successful ACK can withdraw
// just that input while the successor is waiting. Once claimed, that handoff
// owns delivery; the old ACK is refused instead of pretending delivery moved
// back and letting the successor repeat it.
func (s *TaskService) AckMergedChatSupplement(ctx context.Context, taskID, messageID pgtype.UUID) (db.ChatTaskSupplement, bool, error) {
	lookup := db.GetMergedChatInputReceiptParams{TaskID: taskID, ChatMessageID: messageID}
	receipt, err := s.Queries.GetMergedChatInputReceipt(ctx, lookup)
	if errors.Is(err, pgx.ErrNoRows) {
		return receipt, false, nil
	}
	if err != nil {
		return receipt, true, err
	}
	var cancelled []db.AgentTaskQueue
	err = s.runInTx(ctx, func(q *db.Queries) error {
		if _, err := q.LockChatSessionForRuntimeBind(ctx, receipt.ChatSessionID); err != nil {
			return err
		}
		original, err := q.GetAgentTask(ctx, taskID)
		if err != nil {
			return err
		}
		if _, err := q.GetAgentForClaimUpdate(ctx, original.AgentID); err != nil {
			return err
		}
		receipt, err = q.GetMergedChatInputReceipt(ctx, lookup)
		if err != nil {
			return err
		}
		if receipt.Status != "delivered" && receipt.RecoveredIntoTaskID.Valid {
			successor, err := q.LockChatMergeSuccessor(ctx, receipt.RecoveredIntoTaskID)
			if err != nil {
				return err
			}
			if successor.Status != "queued" && successor.Status != "deferred" {
				return pgx.ErrNoRows
			}
			if _, err := q.RebindMergedChatMessages(ctx, db.RebindMergedChatMessagesParams{SuccessorTaskID: chatInputOwnerID(original), MessageIds: []pgtype.UUID{messageID}}); err != nil {
				return err
			}
			remaining, err := q.CountChatMergeSuccessorInputs(ctx, successor.ID)
			if err != nil {
				return err
			}
			if remaining == 0 {
				cancelledTask, err := q.CancelAgentTask(ctx, successor.ID)
				if err != nil {
					return err
				}
				if err := SettleTerminalTaskState(ctx, q, cancelledTask); err != nil {
					return err
				}
				cancelled = append(cancelled, cancelledTask)
			}
		}
		receipt, err = q.AckChatTaskSupplementDelivered(ctx, db.AckChatTaskSupplementDeliveredParams{TaskID: taskID, ChatMessageID: messageID})
		if err != nil {
			return err
		}
		// A retry's live receipt names the actual run, while its replay batch
		// belongs to the original input owner. Keep acknowledged directives in
		// that batch if a later retry needs a fresh provider session.
		_, err = q.RebindMergedChatMessages(ctx, db.RebindMergedChatMessagesParams{SuccessorTaskID: chatInputOwnerID(original), MessageIds: []pgtype.UUID{messageID}})
		return err
	})
	if err == nil {
		s.BroadcastCancelledTasks(ctx, util.UUIDToString(receipt.WorkspaceID), cancelled)
	}
	return receipt, true, err
}
