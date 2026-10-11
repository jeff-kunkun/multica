package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/botsource"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var ErrChatConversionForbidden = errors.New("only the chat creator can transfer its execution")
var ErrChatSessionConverted = errors.New("chat execution has moved to an issue")

// ConvertChatToIssue transfers the chat's execution in the same commit as the
// issue creation. An ordinary issue created with chat provenance never calls
// this method: chats may create or follow several issues without being converted.
func (s *IssueService) ConvertChatToIssue(ctx context.Context, sessionID pgtype.UUID, p IssueCreateParams, opts IssueCreateOpts) (IssueCreateResult, error) {
	policy := ResolveIssueCountPolicy(ctx, s.Entitlements, p.WorkspaceID)
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return IssueCreateResult{}, err
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	if _, err := qtx.LockChatSessionForRuntimeBind(ctx, sessionID); err != nil {
		return IssueCreateResult{}, err
	}
	session, err := qtx.GetChatSessionInWorkspace(ctx, db.GetChatSessionInWorkspaceParams{ID: sessionID, WorkspaceID: p.WorkspaceID})
	if err != nil {
		return IssueCreateResult{}, err
	}
	if p.CreatorType != "member" || p.CreatorID != session.CreatorID {
		return IssueCreateResult{}, ErrChatConversionForbidden
	}
	if session.ConvertedIssueID.Valid {
		issue, err := qtx.GetChatConvertedIssue(ctx, db.GetChatConvertedIssueParams{ChatSessionID: sessionID, WorkspaceID: p.WorkspaceID})
		return IssueCreateResult{Issue: issue}, err
	}
	if session.Status != "active" {
		return IssueCreateResult{}, ErrChatSessionArchived
	}
	if _, err := qtx.GetAgentForClaimUpdate(ctx, session.AgentID); err != nil {
		return IssueCreateResult{}, err
	}
	p.OriginChatSessionID = sessionID
	outcome, err := s.createInTx(ctx, tx, qtx, p, policy, opts)
	if err != nil {
		return IssueCreateResult{}, err
	}
	if _, err := qtx.SetChatSessionConvertedIssue(ctx, db.SetChatSessionConvertedIssueParams{ChatSessionID: sessionID, WorkspaceID: p.WorkspaceID, IssueID: outcome.Issue.ID}); err != nil {
		return IssueCreateResult{}, err
	}
	cancelled, err := qtx.CancelAgentTasksByChatSession(ctx, sessionID)
	if err != nil {
		return IssueCreateResult{}, err
	}
	if err := SettleTerminalTaskState(ctx, qtx, cancelled...); err != nil {
		return IssueCreateResult{}, err
	}
	for _, task := range cancelled {
		if err := qtx.AdvanceCancelledChatSessionPointer(ctx, task.ID); err != nil {
			return IssueCreateResult{}, err
		}
	}
	_, err = qtx.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ChatSessionID: sessionID, Role: "assistant",
		Content:     fmt.Sprintf("已转成目标任务 [%s](mention://issue/%s)。后续执行和补充指令请到任务中继续。", outcome.Issue.Title, util.UUIDToString(outcome.Issue.ID)),
		MessageKind: pgtype.Text{String: "goal_link", Valid: true},
		ViaBotID:    botsource.FromContext(ctx).ID,
		ViaBotName:  pgtype.Text{String: botsource.FromContext(ctx).Name, Valid: botsource.FromContext(ctx).ID.Valid},
	})
	if err != nil {
		return IssueCreateResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IssueCreateResult{}, err
	}
	s.TaskService.BroadcastCancelledTasks(ctx, util.UUIDToString(p.WorkspaceID), cancelled)
	assignedTaskID, attachments := s.afterCommit(ctx, outcome.Issue, outcome.Labels, outcome.AssignedTask, p, opts)
	return IssueCreateResult{Issue: outcome.Issue, Labels: outcome.Labels, Attachments: attachments, AssignedTaskID: assignedTaskID}, nil
}
