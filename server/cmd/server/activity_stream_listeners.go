package main

import (
	"context"
	"log/slog"
	"strconv"
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
		c, ok := activityCommentFromPayload(e)
		if !ok {
			return
		}
		route(service.ActivityInput{
			WorkspaceID: parseUUID(e.WorkspaceID),
			IssueID:     parseUUID(c.issueID),
			CommentID:   parseUUID(c.id),
			Type:        service.ActivityCommentCreated,
			DedupeKey:   "act:comment:" + c.id,
			SourceID:    c.id,
			ActorType:   c.authorType,
			ActorID:     optionalUUID(c.authorID),
			ViaBotID:    c.viaBot,
			Text:        c.content,
			OccurredAt:  time.Now(),
		})
	})

	// An edited body is news again. comment:updated is also published for
	// attachment changes, supplements, resolve and tombstones, none of which
	// change the text; only a revision above the first one does, and the
	// revision in the dedupe key keeps each edit to one event no matter how
	// many times the same state is re-published.
	bus.Subscribe(protocol.EventCommentUpdated, func(e events.Event) {
		c, ok := activityCommentFromPayload(e)
		if !ok || c.revision < 2 || c.deleted {
			return
		}
		rev := strconv.FormatInt(c.revision, 10)
		route(service.ActivityInput{
			WorkspaceID: parseUUID(e.WorkspaceID),
			IssueID:     parseUUID(c.issueID),
			CommentID:   parseUUID(c.id),
			Type:        service.ActivityCommentEdited,
			DedupeKey:   "act:comment-edit:" + c.id + ":" + rev,
			SourceID:    c.id,
			ActorType:   firstNonEmpty(e.ActorType, c.authorType),
			ActorID:     optionalUUID(firstNonEmpty(e.ActorID, c.authorID)),
			ViaBotID:    c.viaBot,
			Text:        c.content,
			OccurredAt:  time.Now(),
		})
	})

	bus.Subscribe(protocol.EventIssueUpdated, func(e events.Event) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		issue, ok := activityIssueFromPayload(payload["issue"])
		if !ok {
			return
		}
		statusChanged, _ := payload["status_changed"].(bool)
		assigneeChanged, _ := payload["assignee_changed"].(bool)
		base := service.ActivityInput{
			WorkspaceID: parseUUID(e.WorkspaceID),
			IssueID:     parseUUID(issue.id),
			ActorType:   e.ActorType,
			ActorID:     optionalUUID(e.ActorID),
			ViaBotID:    e.ViaBotID,
			OccurredAt:  time.Now(),
		}
		if statusChanged && issue.status != "" {
			in := base
			prev, _ := payload["prev_status"].(string)
			in.Type = service.ActivityStatusChanged
			in.FromStatus, in.ToStatus = prev, issue.status
			in.DedupeKey = "act:status:" + issue.id + ":" + prev + ">" + issue.status + ":" + issue.updatedAt
			if at, err := time.Parse(time.RFC3339Nano, issue.updatedAt); err == nil {
				in.BusinessKey = service.OfficeIssueStatusKey(issue.id, prev, issue.status, at)
			}
			route(in)
		}
		if assigneeChanged {
			in := base
			in.Type = service.ActivityAssigneeChanged
			in.Text = "负责人已变更"
			if issue.assigneeType != "" {
				in.Text += "(新负责人类型:" + issue.assigneeType + ")"
			} else {
				in.Text += "(已取消负责人)"
			}
			in.DedupeKey = "act:assignee:" + issue.id + ":" + issue.updatedAt
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
			BusinessKey:   "chat-message:" + msg.MessageID,
			SourceID:      msg.MessageID,
			ActorType:     actorType,
			ActorID:       optionalUUID(actorID),
			ViaBotID:      viaBot,
			Text:          msg.Content,
			OccurredAt:    time.Now(),
		})
	})
}

// activityIssue is the part of an issue:updated payload the stream needs.
type activityIssue struct {
	id, status, updatedAt, assigneeType string
}

// activityIssueFromPayload reads the "issue" value of an issue:updated event.
// Producers disagree on its type: the HTTP handlers send handler.IssueResponse
// (by value or pointer), while the task service, progress writer and state
// card send the map service.IssueToMap / IssueToMapResolved build. A shape the
// stream does not recognise would silently drop a status change, so every one
// production sends is handled here and covered by a test through the real
// constructors.
func activityIssueFromPayload(v any) (activityIssue, bool) {
	var out activityIssue
	switch i := v.(type) {
	case handler.IssueResponse:
		out = activityIssue{id: i.ID, status: i.Status, updatedAt: i.UpdatedAt}
		if i.AssigneeType != nil {
			out.assigneeType = *i.AssigneeType
		}
	case *handler.IssueResponse:
		if i == nil {
			return out, false
		}
		return activityIssueFromPayload(*i)
	case map[string]any:
		out.id, _ = i["id"].(string)
		out.status, _ = i["status"].(string)
		out.updatedAt, _ = i["updated_at"].(string)
		switch a := i["assignee_type"].(type) {
		case string:
			out.assigneeType = a
		case *string:
			if a != nil {
				out.assigneeType = *a
			}
		}
	default:
		return out, false
	}
	return out, out.id != ""
}

// activityComment is the part of a comment event the stream needs.
type activityComment struct {
	id, issueID, content, authorType, authorID, viaBot string
	revision                                           int64
	deleted                                            bool
}

// activityCommentFromPayload reads payload["comment"] of a comment:created or
// comment:updated event, which is a handler.CommentResponse or a plain map
// depending on the publisher.
func activityCommentFromPayload(e events.Event) (activityComment, bool) {
	var out activityComment
	payload, ok := e.Payload.(map[string]any)
	if !ok {
		return out, false
	}
	switch c := payload["comment"].(type) {
	case handler.CommentResponse:
		out = activityCommentFromResponse(c)
	case *handler.CommentResponse:
		if c == nil {
			return out, false
		}
		out = activityCommentFromResponse(*c)
	case map[string]any:
		out.issueID, _ = c["issue_id"].(string)
		out.id, _ = c["id"].(string)
		out.content, _ = c["content"].(string)
		out.authorType, _ = c["author_type"].(string)
		out.authorID, _ = c["author_id"].(string)
		switch r := c["revision"].(type) {
		case int64:
			out.revision = r
		case int:
			out.revision = int64(r)
		case float64:
			out.revision = int64(r)
		}
		if d, ok := c["deleted_at"].(string); ok && d != "" {
			out.deleted = true
		}
		if v, ok := c["via_bot_id"].(string); ok {
			out.viaBot = v
		}
	default:
		return out, false
	}
	if out.viaBot == "" {
		out.viaBot = e.ViaBotID
	}
	return out, out.issueID != "" && out.id != ""
}

func activityCommentFromResponse(c handler.CommentResponse) activityComment {
	out := activityComment{
		id: c.ID, issueID: c.IssueID, content: c.Content,
		authorType: c.AuthorType, authorID: c.AuthorID,
		revision: c.Revision, deleted: c.DeletedAt != nil,
	}
	if c.ViaBotID != nil {
		out.viaBot = *c.ViaBotID
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
