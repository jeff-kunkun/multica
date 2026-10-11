package handler

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Recover only the exact persisted source run, preserving unknown attribution.
func (w *BotWakeWorker) restoreEventActor(ctx context.Context, q *db.Queries, e db.BotWakeEvent) (db.BotWakeEvent, error) {
	if e.EventType != "wake_blocked" && (e.AuthorType == "system" || !e.AuthorID.Valid) && e.SourceTaskID.Valid {
		if task, err := q.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{ID: e.SourceTaskID, WorkspaceID: e.WorkspaceID}); err == nil {
			e.AuthorType, e.AuthorID = "agent", task.AgentID
			if err := q.RestoreBotWakeAuthor(ctx, db.RestoreBotWakeAuthorParams{Sequence: e.Sequence, AuthorID: e.AuthorID}); err != nil {
				return e, err
			}
		}
	}
	return e, nil
}

func (w *BotWakeWorker) eventHoldReason(ctx context.Context, q *db.Queries, e db.BotWakeEvent, bot db.PersonalAccessToken, rules BotWakeResponse) (string, error) {
	if !w.h.wakeVisible(ctx, e, bot.UserID) {
		return "access unavailable", nil
	}
	if e.EventType == "wake_blocked" {
		return "", nil
	}
	if e.AuthorType == "bot" && e.AuthorID == e.BotID {
		return "self wake suppressed", nil
	}
	if e.InheritedOwner {
		if !rules.InheritOwnerFollows {
			return "owner inheritance disabled", nil
		}
		follows, err := q.BotOwnerFollowsIssue(ctx, db.BotOwnerFollowsIssueParams{ID: e.IssueID, AssigneeID: bot.UserID})
		if err != nil {
			return "", err
		}
		if !follows.Bool {
			return "owner no longer follows source", nil
		}
	}
	rule, known := rules.WakeFrom[e.AuthorType]
	allowed := rule.Mode == "all"
	if rule.Mode == "list" {
		for _, id := range rule.IDs {
			if id == uuidToString(e.AuthorID) {
				allowed = true
			}
		}
	}
	if e.AuthorType != "system" && e.AuthorID.Valid && (!known || !allowed) {
		return "sender not allowed", nil
	}
	enabled := false
	for _, event := range rules.Events {
		if event == e.EventType {
			enabled = true
		}
	}
	if !enabled {
		return "event disabled", nil
	}
	if wakeQuiet(rules.QuietHours, time.Now()) {
		return "quiet hours", nil
	}
	return "", nil
}

// Keep the legacy envelope as the first admitted event, adding the complete
// ordered list. Even single-event deliveries expose events for one read path.
func marshalBotWakeBatch(id pgtype.UUID, events []map[string]any) ([]byte, error) {
	payload := make(map[string]any, len(events[0])+2)
	for key, value := range events[0] {
		payload[key] = value
	}
	payload["delivery_id"] = uuidToString(id)
	payload["events"] = events
	return json.Marshal(payload)
}

func (h *Handler) botWakePayload(ctx context.Context, e db.BotWakeEvent) ([]byte, error) {
	fields, err := h.botWakePayloadFields(ctx, e)
	if err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}

func (h *Handler) addBotWakeContent(ctx context.Context, e db.BotWakeEvent, payload map[string]any) error {
	if e.IssueID.Valid {
		issue, err := h.Queries.GetIssue(ctx, e.IssueID)
		if err != nil {
			return err
		}
		ws, err := h.Queries.GetWorkspace(ctx, e.WorkspaceID)
		if err != nil {
			return err
		}
		payload["issue"] = map[string]any{"id": uuidToString(issue.ID), "identifier": issueIdentifier(ws.IssuePrefix, issue.Number), "title": issue.Title}
		if e.EventType != "mention" {
			return nil
		}
		if e.SourceID == e.IssueID {
			payload["content"] = issue.Description.String
			payload["reply"] = map[string]any{"method": "POST", "url": payload["url"].(string) + "/comments", "headers": map[string]string{"X-Workspace-ID": uuidToString(e.WorkspaceID)}, "body": map[string]string{}, "cli": "multica issue comment add " + uuidToString(e.IssueID) + " --content-file ./reply.md"}
			return nil
		}
		comment, err := h.Queries.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{ID: e.SourceID, WorkspaceID: e.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if comment.IssueID != e.IssueID {
			return nil
		}
		payload["comment_id"] = uuidToString(comment.ID)
		payload["content"] = comment.Content
		if comment.DeletedAt.Valid {
			payload["content"] = ""
			payload["deleted_at"] = comment.DeletedAt.Time
			payload["attachments"] = []map[string]any{}
			return nil
		}
		payload["reply_to_comment_id"] = uuidToString(comment.ID)
		payload["reply"] = map[string]any{"method": "POST", "url": payload["url"].(string) + "/comments", "body": map[string]any{"parent_id": uuidToString(comment.ID)}, "headers": map[string]string{"X-Workspace-ID": uuidToString(e.WorkspaceID)}, "cli": "multica issue comment add " + uuidToString(e.IssueID) + " --parent " + uuidToString(comment.ID) + " --content-file ./reply.md"}
		rows, err := h.Queries.ListThreadCommentsForIssuePaged(ctx, db.ListThreadCommentsForIssuePagedParams{AnchorID: comment.ID, IssueID: e.IssueID, WorkspaceID: e.WorkspaceID, ReplyLimit: 5})
		if err != nil {
			return err
		}
		replies := []map[string]any{}
		thread := map[string]any{}
		for _, c := range rows {
			authorEvent := e
			authorEvent.AuthorType = c.AuthorType
			authorEvent.AuthorID = c.AuthorID
			authorEvent.ViaBotID = c.ViaBotID
			authorEvent.ViaBotName = c.ViaBotName
			author := h.wakeAuthor(ctx, authorEvent)
			content := c.Content
			if c.DeletedAt.Valid {
				content = ""
			}
			preview, truncated := summarizeContent(content)
			item := map[string]any{"id": uuidToString(c.ID), "author_type": author["type"], "author_id": author["id"], "content": preview, "content_truncated": truncated, "type": c.Type, "created_at": c.CreatedAt.Time, "revision": c.Revision}
			if name, ok := author["name"]; ok {
				item["author_name"] = name
			}
			if c.ParentID.Valid {
				item["parent_id"] = uuidToString(c.ParentID)
			}
			if c.DeletedAt.Valid {
				item["deleted_at"] = c.DeletedAt.Time
			}
			if c.ViaBotID.Valid {
				item["via_bot_id"] = uuidToString(c.ViaBotID)
				item["via_bot_name"] = c.ViaBotName.String
			}
			if !c.ParentID.Valid {
				thread["root"] = item
			} else {
				replies = append(replies, item)
			}
		}
		thread["replies"] = replies
		payload["thread"] = thread
		return nil
	}
	if e.EventType != "mention" && e.EventType != "chat" {
		return nil
	}
	message, err := h.Queries.GetChatMessage(ctx, e.SourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if message.ChatSessionID != e.ChatSessionID {
		return nil
	}
	payload["message_id"] = uuidToString(message.ID)
	payload["content"] = message.Content
	payload["reply_to_message_id"] = uuidToString(message.ID)
	payload["reply"] = map[string]any{"method": "POST", "url": payload["url"].(string) + "/messages", "headers": map[string]string{"X-Workspace-ID": uuidToString(e.WorkspaceID)}, "body": map[string]string{"mode": "queue"}, "cli": "multica chat send --session " + uuidToString(e.ChatSessionID) + " --content-file ./reply.md"}
	return nil
}
