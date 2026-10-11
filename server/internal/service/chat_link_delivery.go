package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// DeliverLinkedChatInTx persists an authorized managed chat send. Message and
// optional task commit together. The caller validates the source chat creator
// and invocation grant; the managed middleware owns the existing write audit.
// Unlike a normal send, this never interprets mentions or attributes text to a
// human. It never invokes external integrations while holding database locks.
func (s *TaskService) DeliverLinkedChatInTx(ctx context.Context, qtx *db.Queries, session db.ChatSession, originator pgtype.UUID, content string, wake bool, overlay runtimeMCPOverlayData) (*DirectChatSendResult, error) {
	current, err := qtx.LockChatSessionForEnqueue(ctx, session.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrChatSessionArchived
	}
	if err != nil {
		return nil, fmt.Errorf("lock receiving chat: %w", err)
	}
	if current.Status != "active" || current.AgentID != session.AgentID || current.WorkspaceID != session.WorkspaceID || current.CreatorID != originator {
		return nil, ErrChatSessionArchived
	}
	out := &DirectChatSendResult{}
	messageID := dbid.NewV7()
	if wake {
		carrier, err := qtx.GetAgentForClaimUpdate(ctx, session.AgentID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrChatTaskAgentArchived
		}
		if err != nil {
			return nil, fmt.Errorf("load receiving agent: %w", err)
		}
		if carrier.ArchivedAt.Valid {
			return nil, ErrChatTaskAgentArchived
		}
		if !carrier.WorkEnabled {
			return nil, ErrChatTaskAgentDisabled
		}
		if !carrier.RuntimeID.Valid {
			return nil, ErrChatTaskAgentNoRuntime
		}
		out.Queued, err = qtx.HasPendingChatTurnForSession(ctx, session.ID)
		if err != nil {
			return nil, err
		}
		task, err := qtx.CreateChatTask(ctx, db.CreateChatTaskParams{
			ID: dbid.NewV7(), AgentID: session.AgentID, RuntimeID: carrier.RuntimeID,
			Priority: 2, ChatSessionID: session.ID,
			InitiatorUserID: originator, OriginatorUserID: originator, AccountableUserID: originator,
			OriginatorSource:    pgtype.Text{String: "workspace_link_delivery", Valid: true},
			TriggerEvidenceKind: pgtype.Text{String: "chat_message", Valid: true}, TriggerEvidenceRefID: messageID,
			ForceFreshSession: pgtype.Bool{Valid: true},
			RuntimeMcpOverlay: overlay.Overlay, RuntimeConnectedApps: overlay.ConnectedApps,
		})
		if err != nil {
			return nil, fmt.Errorf("create delivery wake: %w", err)
		}
		task, err = qtx.SetChatTaskInputOwnerSelf(ctx, task.ID)
		if err != nil {
			return nil, err
		}
		out.Task = task
	}
	out.Message, err = qtx.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ID: messageID, ChatSessionID: session.ID, Role: "user", Content: content,
		TaskID: out.Task.ID, MessageKind: pgtype.Text{String: protocol.ChatMessageKindLinkedDelivery, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("create delivered message: %w", err)
	}
	if err = qtx.TouchChatSession(ctx, session.ID); err != nil {
		return nil, err
	}
	return out, nil
}

// FinalizeLinkedChatDelivery runs only after the chat transaction commits.
func (s *TaskService) FinalizeLinkedChatDelivery(ctx context.Context, out *DirectChatSendResult) {
	if out == nil {
		return
	}
	session, err := s.Queries.GetChatSession(ctx, out.Message.ChatSessionID)
	if err != nil {
		return
	}
	m := out.Message
	if s.Bus != nil {
		s.Bus.Publish(events.Event{Type: protocol.EventChatMessage,
			WorkspaceID: util.UUIDToString(session.WorkspaceID), ChatSessionID: util.UUIDToString(session.ID), ActorType: "system",
			Payload: protocol.ChatMessagePayload{ChatSessionID: util.UUIDToString(session.ID), MessageID: util.UUIDToString(m.ID),
				Role: m.Role, Content: m.Content, MessageKind: m.MessageKind, TaskID: util.UUIDToString(m.TaskID), CreatedAt: m.CreatedAt.Time.UTC().Format(time.RFC3339Nano)},
		})
	}
	if out.Task.ID.Valid {
		s.FinalizeChatTaskEnqueue(ctx, out.Task)
	}
}

// SendManagedChatMessage sends an external agent's text using the originator's
// existing source chat permissions. It does not borrow another user's identity.
func (s *TaskService) SendManagedChatMessage(ctx context.Context, session db.ChatSession, originator pgtype.UUID, content string) (*DirectChatSendResult, error) {
	agent, err := s.Queries.GetAgent(ctx, session.AgentID)
	if err != nil {
		return nil, fmt.Errorf("load receiving agent for integrations: %w", err)
	}
	// Resolve the receiving agent's integrations for the originator before
	// opening a transaction: the builder can perform network I/O.
	overlay := s.buildRuntimeMCPOverlay(ctx, originator, agent)
	var out *DirectChatSendResult
	err = s.runInTx(ctx, func(qtx *db.Queries) error {
		var err error
		out, err = s.DeliverLinkedChatInTx(ctx, qtx, session, originator, content, true, overlay)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.FinalizeLinkedChatDelivery(ctx, out)
	return out, nil
}
