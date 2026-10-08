package handler

import (
	"context"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/receipt"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// dispatchedReceiptLimit bounds the per-turn list of a chat's tickets.
const dispatchedReceiptLimit = 10

// issueReceipt reads a task's result from what the issue records (DENE-1672).
// The close fields are kept only while the close still describes the current
// status; a stale close would report an old conclusion as the current one.
func (h *Handler) issueReceipt(ctx context.Context, issue db.Issue, prefix string) receipt.Receipt {
	r := receipt.Receipt{
		IssueID:    uuidToString(issue.ID),
		Identifier: issueToResponse(issue, prefix).Identifier,
		Title:      issue.Title,
		Status:     issue.Status,
		PRs:        []receipt.PR{},
		UpdatedAt:  timestampToString(issue.UpdatedAt),
	}
	meta := issueMetaStrings(issue.Metadata)
	if closeprotocol.Complete(meta) && closeprotocol.StatusMatchesIssue(meta[closeprotocol.KeyStatus], issue.Status) && !closeprotocol.Superseded(meta) {
		r.Conclusion = meta[closeprotocol.KeyConclusion]
		r.ClosedAt = strings.TrimSpace(meta[closeprotocol.KeyAt])
		r.Summary = receipt.Clip(h.closeNote(ctx, issue, meta).Summary, receipt.MaxSummary)
		r.Knowledge = knowledgeLine(meta[closeprotocol.KeyKnowledgeAudit])
	}
	if gh, vcsRows, err := h.loadDeliveryRows(ctx, issue.ID); err == nil {
		for _, p := range gh {
			r.PRs = append(r.PRs, receipt.PR{Number: p.PrNumber, URL: p.HtmlUrl, State: prReceiptState(p.State, p.MergedAt.Valid)})
		}
		for _, p := range vcsRows {
			r.PRs = append(r.PRs, receipt.PR{Number: p.PrNumber, URL: p.HtmlUrl, State: prReceiptState(p.State, p.MergedAt.Valid)})
		}
	} else {
		slog.Warn("receipt: list pull requests failed", "issue_id", r.IssueID, "error", err)
	}
	return r
}

func prReceiptState(state string, merged bool) string {
	if merged {
		return "merged"
	}
	return state
}

// knowledgeLine is what the close wrote into project memory, in one line;
// empty when it wrote nothing.
func knowledgeLine(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	audit, err := closeprotocol.ParseStoredKnowledgeAudit(raw)
	if err != nil {
		return ""
	}
	if audit.None {
		return "" // nothing worth a line on the card
	}
	parts := make([]string, 0, len(audit.Changes))
	for _, c := range audit.Changes {
		parts = append(parts, c.Location+"："+c.Summary)
	}
	return receipt.Clip(strings.Join(parts, "；"), 200)
}

// postSourceChatReceipt puts a receipt card into the chat an issue was opened
// from when the issue enters a status the chat must hear about. It sits
// beside notifyParentOfChildDone on every status-transition path. A
// sub-issue of a ticket from the same chat stays quiet: its parent reports.
// Best-effort: a failure never undoes the status change.
func (h *Handler) postSourceChatReceipt(ctx context.Context, prev, issue db.Issue) {
	if !issue.OriginChatSessionID.Valid {
		return
	}
	effective := h.childStatusResolver(ctx)
	prevStatus, err := effective(prev)
	if err != nil {
		return
	}
	nowStatus, err := effective(issue)
	if err != nil || prevStatus == nowStatus || !receipt.Reportable(nowStatus) {
		return
	}
	if issue.ParentIssueID.Valid {
		if parent, err := h.Queries.GetIssue(ctx, issue.ParentIssueID); err == nil && parent.OriginChatSessionID == issue.OriginChatSessionID {
			return
		}
	}
	session, err := h.Queries.GetChatSession(ctx, issue.OriginChatSessionID)
	if err != nil || session.WorkspaceID != issue.WorkspaceID {
		return
	}
	// The card names the issue to everyone in the chat; the chat's owner must
	// be able to see it.
	if viewer, err := h.visibilityViewerForUser(ctx, issue.WorkspaceID, session.CreatorID); err != nil || !viewer.canSeeIssue(issue) {
		return
	}
	r := h.issueReceipt(ctx, issue, h.getIssuePrefix(ctx, issue.WorkspaceID))
	r.Status = nowStatus
	msg, err := h.Queries.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ID:            dbid.NewV7(),
		ChatSessionID: session.ID,
		Role:          "assistant",
		Content:       r.Markdown(),
		MessageKind:   pgtype.Text{String: protocol.ChatMessageKindIssueReceipt, Valid: true},
	})
	if err != nil {
		slog.Warn("receipt: create chat message failed", "issue_id", r.IssueID, "chat_session_id", uuidToString(session.ID), "error", err)
		return
	}
	if err := h.Queries.TouchChatSession(ctx, session.ID); err != nil {
		slog.Warn("receipt: touch chat session failed", "chat_session_id", uuidToString(session.ID), "error", err)
	}
	sessionID := uuidToString(session.ID)
	h.publishChat(protocol.EventChatMessage, uuidToString(session.WorkspaceID), "system", "", sessionID, protocol.ChatMessagePayload{
		ChatSessionID: sessionID,
		MessageID:     uuidToString(msg.ID),
		Role:          "assistant",
		Content:       msg.Content,
		CreatedAt:     timestampToString(msg.CreatedAt),
	})
}

// chatDispatchedLines is the per-turn "tickets you opened" list a chat run
// opens with: the newest few, limited to what the chat's owner can see.
func (h *Handler) chatDispatchedLines(ctx context.Context, session db.ChatSession) []string {
	viewer, err := h.visibilityViewerForUser(ctx, session.WorkspaceID, session.CreatorID)
	if err != nil {
		return nil
	}
	issues, err := h.Queries.ListIssuesByOriginChatSession(ctx, db.ListIssuesByOriginChatSessionParams{
		WorkspaceID: session.WorkspaceID, ChatSessionID: session.ID,
	})
	if err != nil {
		slog.Warn("receipt: list chat tickets failed", "chat_session_id", uuidToString(session.ID), "error", err)
		return nil
	}
	prefix := h.getIssuePrefix(ctx, session.WorkspaceID)
	lines := []string{}
	for i := len(issues) - 1; i >= 0 && len(lines) < dispatchedReceiptLimit; i-- {
		if viewer.canSeeIssue(issues[i]) {
			lines = append(lines, h.issueReceipt(ctx, issues[i], prefix).Line())
		}
	}
	return lines
}
