package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// registerActivityStreamListeners feeds the per-person activity stream
// (DENE-1935): every comment, chat message, status change and reassignment is
// offered to service.RouteActivity, which writes it only for people who
// switched the stream on. The listeners do no work for anyone else, and a
// failure here is logged and dropped: the stream must never break the write
// that produced the event.
func registerActivityStreamListeners(bus *events.Bus, queries *db.Queries) {
	ctx := context.Background()
	route := func(in service.ActivityInput) {
		if _, err := service.RouteActivity(ctx, queries, in); err != nil {
			slog.Warn("activity stream: route failed", "error", err, "type", in.Type)
		}
	}

	bus.Subscribe(protocol.EventCommentCreated, func(e events.Event) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		var issueID, commentID, content, authorType, authorID, viaBot string
		switch c := payload["comment"].(type) {
		case handler.CommentResponse:
			issueID, commentID, content = c.IssueID, c.ID, c.Content
			authorType, authorID = c.AuthorType, c.AuthorID
			if c.ViaBotID != nil {
				viaBot = *c.ViaBotID
			}
		case map[string]any:
			issueID, _ = c["issue_id"].(string)
			commentID, _ = c["id"].(string)
			content, _ = c["content"].(string)
			authorType, _ = c["author_type"].(string)
			authorID, _ = c["author_id"].(string)
		default:
			return
		}
		if viaBot == "" {
			viaBot = e.ViaBotID
		}
		if issueID == "" || commentID == "" {
			return
		}
		route(service.ActivityInput{
			WorkspaceID: parseUUID(e.WorkspaceID),
			IssueID:     parseUUID(issueID),
			CommentID:   parseUUID(commentID),
			Type:        service.ActivityCommentCreated,
			DedupeKey:   "act:comment:" + commentID,
			SourceID:    commentID,
			ActorType:   authorType,
			ActorID:     optionalUUID(authorID),
			ViaBotID:    viaBot,
			Text:        content,
			OccurredAt:  time.Now(),
		})
	})

	bus.Subscribe(protocol.EventIssueUpdated, func(e events.Event) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		issue, ok := payload["issue"].(handler.IssueResponse)
		if !ok {
			return
		}
		statusChanged, _ := payload["status_changed"].(bool)
		assigneeChanged, _ := payload["assignee_changed"].(bool)
		base := service.ActivityInput{
			WorkspaceID: parseUUID(e.WorkspaceID),
			IssueID:     parseUUID(issue.ID),
			ActorType:   e.ActorType,
			ActorID:     optionalUUID(e.ActorID),
			ViaBotID:    e.ViaBotID,
			OccurredAt:  time.Now(),
		}
		if statusChanged {
			in := base
			prev, _ := payload["prev_status"].(string)
			in.Type = service.ActivityStatusChanged
			in.FromStatus, in.ToStatus = prev, issue.Status
			in.DedupeKey = "act:status:" + issue.ID + ":" + prev + ">" + issue.Status + ":" + issue.UpdatedAt
			route(in)
		}
		if assigneeChanged {
			in := base
			in.Type = service.ActivityAssigneeChanged
			in.Text = "负责人已变更"
			if issue.AssigneeType != nil {
				in.Text += "(新负责人类型:" + *issue.AssigneeType + ")"
			} else {
				in.Text += "(已取消负责人)"
			}
			in.DedupeKey = "act:assignee:" + issue.ID + ":" + issue.UpdatedAt
			route(in)
		}
	})

	bus.Subscribe(protocol.EventChatMessage, func(e events.Event) {
		var msg protocol.ChatMessagePayload
		switch p := e.Payload.(type) {
		case protocol.ChatMessagePayload:
			msg = p
		case *protocol.ChatMessagePayload:
			if p == nil {
				return
			}
			msg = *p
		default:
			return
		}
		// Pasted external text and empty "no answer" markers are not news.
		if msg.MessageID == "" || msg.ChatSessionID == "" || msg.Content == "" ||
			msg.MessageKind == protocol.ChatMessageKindLinkedDelivery ||
			msg.MessageKind == protocol.ChatMessageKindNoResponse {
			return
		}
		actorType, actorID := e.ActorType, e.ActorID
		if msg.SenderUserID != "" {
			actorType, actorID = "member", msg.SenderUserID
		}
		viaBot := msg.ViaBotID
		if viaBot == "" {
			viaBot = e.ViaBotID
		}
		route(service.ActivityInput{
			WorkspaceID:   parseUUID(e.WorkspaceID),
			ChatSessionID: parseUUID(msg.ChatSessionID),
			Type:          service.ActivityChatMessage,
			DedupeKey:     "act:chat:" + msg.MessageID,
			SourceID:      msg.MessageID,
			ActorType:     actorType,
			ActorID:       optionalUUID(actorID),
			ViaBotID:      viaBot,
			Text:          msg.Content,
			OccurredAt:    time.Now(),
		})
	})
}
