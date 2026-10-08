package service

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// chatOriginTypes are the origins whose origin_id is the chat itself: the IM
// `/issue` commands and the alignment draft's root.
var chatOriginTypes = map[string]bool{
	"lark_chat": true, "slack_chat": true, "dingtalk_chat": true,
	"wecom_chat": true, "telegram_chat": true, "issue_draft": true,
}

// resolveSourceChat names the chat an issue was dispatched from (DENE-1672).
// An explicit source wins; otherwise a creating chat run (SourceTaskID, or
// the agent_create origin) gives its chat and the user message it answered,
// and a chat origin gives its own id. A sub-issue derives nothing: it
// reports to its parent, not to the chat. Lookup failures leave the issue
// without a source rather than failing the create.
func resolveSourceChat(ctx context.Context, q *db.Queries, p IssueCreateParams) (pgtype.UUID, pgtype.UUID) {
	if p.SourceChatSessionID.Valid {
		return p.SourceChatSessionID, p.SourceChatMessageID
	}
	if p.ParentIssueID.Valid {
		return pgtype.UUID{}, pgtype.UUID{}
	}
	taskID := p.SourceTaskID
	if !taskID.Valid && p.OriginType.Valid && p.OriginType.String == "agent_create" {
		taskID = p.OriginID
	}
	if taskID.Valid {
		task, err := q.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{ID: taskID, WorkspaceID: p.WorkspaceID})
		if err != nil || !task.ChatSessionID.Valid {
			return pgtype.UUID{}, pgtype.UUID{}
		}
		var message pgtype.UUID
		if m, err := q.GetChatInputMessageForTask(ctx, task.ID); err == nil {
			message = m.ID
		}
		return task.ChatSessionID, message
	}
	if p.OriginType.Valid && chatOriginTypes[p.OriginType.String] && p.OriginID.Valid {
		if cs, err := q.GetChatSession(ctx, p.OriginID); err == nil && cs.WorkspaceID == p.WorkspaceID {
			return cs.ID, pgtype.UUID{}
		}
	}
	return pgtype.UUID{}, pgtype.UUID{}
}
