package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// mergeChannelChatInputs uses the enqueue transaction's session/agent/context
// locks. A generation change keeps its own execution and input boundary.
func mergeChannelChatInputs(ctx context.Context, q *db.Queries, session db.ChatSession, revision int64) (*db.AgentTaskQueue, error) {
	rev := pgtype.Int8{Int64: revision, Valid: revision > 0}
	task, err := q.GetChannelChatMergeTarget(ctx, db.GetChannelChatMergeTargetParams{ChatSessionID: session.ID, ChannelContextRevision: rev})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	messages, err := q.ListUnownedChannelChatMessagesForMerge(ctx, db.ListUnownedChannelChatMessagesForMergeParams{ChatSessionID: session.ID, ChannelContextRevision: rev})
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return &task, nil
	}
	live := task.Status != "queued" && task.Status != "deferred"
	steered := false
	if live {
		capability, err := q.GetTaskSupplementCapability(ctx, task.ID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		steered = task.Status == "running" && err == nil && capability.Capability == protocol.DaemonCapabilityTaskSupplementV1
		for _, message := range messages {
			if message.ChannelMediaPendingUntil.Valid {
				steered = false
			}
			attachments, err := q.ListAttachmentsByChatMessage(ctx, db.ListAttachmentsByChatMessageParams{ChatMessageID: message.ID, WorkspaceID: session.WorkspaceID})
			if err != nil {
				return nil, err
			}
			if len(attachments) != 0 {
				steered = false
			}
		}
	} else if !task.ChatInputTaskID.Valid {
		task, err = q.SetChatTaskInputOwnerSelf(ctx, task.ID)
		if err != nil {
			return nil, err
		}
	}
	for _, message := range messages {
		if err := q.LinkChatMessageToTask(ctx, db.LinkChatMessageToTaskParams{ID: message.ID, TaskID: chatMergeInputOwner(task, live)}); err != nil {
			return nil, err
		}
		if steered {
			if _, err := q.CreateMergedChatTaskSupplement(ctx, db.CreateMergedChatTaskSupplementParams{TaskID: task.ID, ChatMessageID: message.ID, WorkspaceID: session.WorkspaceID, ChatSessionID: session.ID, AuthorID: message.SenderUserID}); err != nil {
				return nil, fmt.Errorf("supplement channel input: %w", err)
			}
		} else if live {
			if _, err := q.RegisterDeferredMergedChatInput(ctx, db.RegisterDeferredMergedChatInputParams{TaskID: task.ID, ChatMessageID: message.ID, WorkspaceID: session.WorkspaceID, ChatSessionID: session.ID, AuthorID: message.SenderUserID}); err != nil {
				return nil, err
			}
		}
	}
	if !live {
		deferred, err := q.DeferMergedChannelChatTaskForPendingMedia(ctx, task.ID)
		if err == nil {
			task = deferred
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	return &task, nil
}
