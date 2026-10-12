package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// routeMentionToBot hands a direct @ of a person to their Bot when they have a
// verified binding (DENE-1933), and returns the inbox row to publish: read
// when the Bot is the channel, untouched otherwise. The comment path's summon
// shares the dedupe key, so one @ is one push whichever side writes first.
func routeMentionToBot(queries *db.Queries, e events.Event, recipientID, issueID, issueTitle string, item db.InboxItem, details []byte) db.InboxItem {
	ctx := context.Background()
	target, err := util.ParseUUID(recipientID)
	if err != nil {
		return item
	}
	issue, err := queries.GetIssue(ctx, parseUUID(issueID))
	if err != nil {
		return item
	}
	source := issue.ID
	question := issueTitle
	var d struct {
		CommentID          string `json:"comment_id"`
		DescriptionVersion string `json:"description_version"`
	}
	var comment db.Comment
	if json.Unmarshal(details, &d) == nil && d.CommentID != "" {
		if cid, parseErr := util.ParseUUID(d.CommentID); parseErr == nil {
			source = cid
			if c, getErr := queries.GetComment(ctx, cid); getErr == nil {
				comment = c
				question = c.Content
			}
		}
	}
	key := service.HumanMentionKey(source, target)
	if d.DescriptionVersion != "" && !comment.ID.Valid {
		// An @ added to the description: the edit that added it is its
		// identity, so the same edit never pushes twice and taking the @ out
		// and putting it back later is a new ask carrying what was written.
		key = service.HumanDescriptionMentionKey(issue.ID, target, d.DescriptionVersion)
		if issue.Description.Valid {
			question = descriptionMentionLine(issue.Description.String, recipientID)
		}
	}
	route, err := service.RouteHumanNeeded(ctx, queries, service.HumanNeededInput{
		WorkspaceID: issue.WorkspaceID,
		Target:      target,
		Kind:        service.SummonSourceMention,
		DedupeKey:   key,
		IssueID:     issue.ID,
		InboxItemID: item.ID,
		CommentID:   comment.ID,
		CallerType:  e.ActorType,
		CallerID:    optionalUUID(e.ActorID),
		Question:    service.MentionQuestion(question),
	})
	if err != nil {
		slog.Warn("human needed: route mention failed", "error", err, "issue_id", issueID)
		return item
	}
	if route.Silent() {
		if read, readErr := queries.MarkInboxRead(ctx, item.ID); readErr == nil {
			return read
		}
	}
	return item
}

// descriptionMentionLine is the part of a description that asks the person: the
// line carrying their @, or the whole text when it cannot be told apart.
func descriptionMentionLine(description, userID string) string {
	needle := "mention://member/" + userID
	for _, line := range strings.Split(description, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return description
}
