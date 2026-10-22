package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/botsource"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// blockAttributionRejection is the 400 an agent gets for moving a ticket to
// blocked without saying what kind of stop it is and what happens next. The
// sub-issue blocker card reads exactly these two fields; without them the row
// is blank and never counts as "needs you" (DENE-1301). A member dragging a
// card is not asked for them.
func blockAttributionRejection(req UpdateIssueRequest, actorType string) string {
	if actorType != "agent" {
		return ""
	}
	kind := strings.TrimSpace(deref(req.BlockKind))
	action := strings.TrimSpace(deref(req.BlockAction))
	const hint = "智能体把票改成 blocked 要写明卡点类型和下一步：--block-kind（decision / permission / external / dependency / capacity）配 --block-action（一句话，80 字以内）。"
	if kind == "" || action == "" {
		return hint
	}
	if !closeprotocol.AllowedBlockKind(kind) {
		return fmt.Sprintf("--block-kind %q 不是允许的值。", kind) + hint
	}
	if len([]rune(action)) > 80 {
		return "--block-action 超过 80 字。" + hint
	}
	return ""
}

// persistBlockAttribution writes the kind and next step a status move into
// blocked carried, so the blocker card has the same two fields a blocked
// close writes.
func (h *Handler) persistBlockAttribution(ctx context.Context, issue db.Issue, req UpdateIssueRequest) {
	kind := strings.TrimSpace(deref(req.BlockKind))
	action := strings.TrimSpace(deref(req.BlockAction))
	if kind == "" || action == "" || !closeprotocol.AllowedBlockKind(kind) {
		return
	}
	h.setIssueMetaString(ctx, issue, closeprotocol.KeyBlockKind, kind)
	h.setIssueMetaString(ctx, issue, closeprotocol.KeyBlockAction, truncateRunes(action, 80))
}

func (h *Handler) setIssueMetaString(ctx context.Context, issue db.Issue, key, value string) {
	raw, err := json.Marshal(value)
	if err != nil {
		return
	}
	if _, err := h.Queries.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Key:         key,
		Value:       raw,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("issue metadata write failed", "error", err, "issue_id", uuidToString(issue.ID), "key", key)
	}
}

// postBlockComment posts a system comment on the issue and broadcasts it.
func (h *Handler) postBlockComment(ctx context.Context, issue db.Issue, content string) db.Comment {
	created, err := h.Queries.CreateComment(ctx, db.CreateCommentParams{
		ViaBotID: botsource.FromContext(ctx).ID, ViaBotName: pgtype.Text{String: botsource.FromContext(ctx).Name, Valid: botsource.FromContext(ctx).ID.Valid},
		ID:          dbid.NewV7(),
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorType:  "system",
		AuthorID:    pgtype.UUID{Valid: true},
		Content:     content,
		Type:        "system",
		ParentID:    pgtype.UUID{Valid: false},
	})
	if err != nil {
		slog.Warn("system comment: create failed", "error", err, "issue_id", uuidToString(issue.ID))
		return db.Comment{}
	}
	comment := created.Comment()
	h.publishWithSource(ctx, protocol.EventCommentCreated, uuidToString(issue.WorkspaceID), "system", "", map[string]any{
		"comment":             commentToResponse(comment, nil, nil),
		"issue_title":         issue.Title,
		"issue_assignee_type": textToPtr(issue.AssigneeType),
		"issue_assignee_id":   uuidToPtr(issue.AssigneeID),
		"issue_status":        issue.Status,
		"issue_revision":      created.IssueRevision,
	})
	return comment
}

func (h *Handler) publishBlockStatus(prev, issue db.Issue) {
	if h.Bus == nil {
		return
	}
	h.Bus.Publish(events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   "system",
		Payload:     RoutingIssueUpdatedPayload(prev, issue),
	})
}

func (h *Handler) memberWakeMention(ctx context.Context, userID pgtype.UUID) string {
	user, err := h.Queries.GetUser(ctx, userID)
	if err != nil {
		return ""
	}
	name := sanitizeMentionLabel(user.Name)
	if name == "" {
		name = "member"
	}
	return fmt.Sprintf("[@%s](mention://member/%s) ", name, uuidToString(userID))
}

// sanitizeMentionLabel strips characters that would break the mention
// markdown if a name contained them. The mention regex is non-greedy on the
// label, so a stray `]` would short-circuit it.
func sanitizeMentionLabel(name string) string {
	cleaned := strings.ReplaceAll(name, "]", "")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return "assignee"
	}
	return cleaned
}

func (h *Handler) authorIsReviewer(issue db.Issue, comment db.Comment) bool {
	if !issue.ReviewerType.Valid || !issue.ReviewerID.Valid || !comment.AuthorID.Valid {
		return false
	}
	return issue.ReviewerType.String == comment.AuthorType && issue.ReviewerID == comment.AuthorID
}

// childStatusResolver resolves each issue's raw status to its effective one,
// reading each workspace's status catalog at most once per returned func. It
// rejects unresolved custom keys: a miss must not read as open or terminal.
func (h *Handler) childStatusResolver(ctx context.Context) func(db.Issue) (string, error) {
	resolvers := make(map[pgtype.UUID]*issuestatus.Resolver)
	return func(c db.Issue) (string, error) {
		if issuestatus.IsBuiltIn(c.Status) {
			return c.Status, nil
		}
		resolver := resolvers[c.WorkspaceID]
		if resolver == nil {
			resolver = issuestatus.NewResolver(c.WorkspaceID)
			resolvers[c.WorkspaceID] = resolver
		}
		status := resolver.Effective(ctx, h.issueStatusCatalog(), c.Status)
		if err := resolver.Err(); err != nil {
			return "", err
		}
		if !issuestatus.IsCategory(resolver.Category(ctx, h.issueStatusCatalog(), c.Status)) {
			return "", fmt.Errorf("unresolved status %q in workspace %s", c.Status, uuidToString(c.WorkspaceID))
		}
		return status, nil
	}
}
