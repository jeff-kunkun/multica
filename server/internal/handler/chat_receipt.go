package handler

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/receipt"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// dispatchedReceiptLimit bounds the per-turn "tasks you dispatched" list and
// the default page of `multica chat issues`.
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

// knowledgeLine is the close's knowledge audit in one line.
func knowledgeLine(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	audit, err := closeprotocol.ParseStoredKnowledgeAudit(raw)
	if err != nil {
		return ""
	}
	if audit.None {
		return "无够格知识"
	}
	parts := make([]string, 0, len(audit.Changes))
	for _, c := range audit.Changes {
		parts = append(parts, c.Location+"："+c.Summary)
	}
	return receipt.Clip(strings.Join(parts, "；"), 200)
}

// postSourceChatReceipt puts a receipt card into the chat an issue was
// dispatched from when the issue enters a status the dispatcher must hear
// about. It sits beside notifyParentOfChildDone on every status-transition
// path. Best-effort: a failure never undoes the status change.
func (h *Handler) postSourceChatReceipt(ctx context.Context, prev, issue db.Issue) {
	if !issue.SourceChatSessionID.Valid {
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
	session, err := h.Queries.GetChatSession(ctx, issue.SourceChatSessionID)
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
	if msg, err = h.Queries.SetChatMessageLinkedIssue(ctx, db.SetChatMessageLinkedIssueParams{ID: msg.ID, LinkedIssueID: issue.ID}); err != nil {
		slog.Warn("receipt: link issue failed", "issue_id", r.IssueID, "error", err)
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

// dispatchedReceipts lists the tasks one chat dispatched, newest first.
func (h *Handler) dispatchedReceipts(ctx context.Context, session db.ChatSession, limit int32, visible func(db.Issue) bool) ([]receipt.Receipt, error) {
	issues, err := h.Queries.ListIssuesBySourceChat(ctx, db.ListIssuesBySourceChatParams{
		ChatSessionID: session.ID, WorkspaceID: session.WorkspaceID, Lim: limit,
	})
	if err != nil {
		return nil, err
	}
	prefix := h.getIssuePrefix(ctx, session.WorkspaceID)
	out := make([]receipt.Receipt, 0, len(issues))
	for _, issue := range issues {
		if visible != nil && !visible(issue) {
			continue
		}
		out = append(out, h.issueReceipt(ctx, issue, prefix))
	}
	return out, nil
}

// chatDispatchedLines is the per-turn "tasks you dispatched" list a chat run
// opens with, limited to what the chat's owner can see.
func (h *Handler) chatDispatchedLines(ctx context.Context, session db.ChatSession) []string {
	viewer, err := h.visibilityViewerForUser(ctx, session.WorkspaceID, session.CreatorID)
	if err != nil {
		return nil
	}
	receipts, err := h.dispatchedReceipts(ctx, session, dispatchedReceiptLimit, viewer.canSeeIssue)
	if err != nil {
		slog.Warn("receipt: list dispatched issues failed", "chat_session_id", uuidToString(session.ID), "error", err)
		return nil
	}
	lines := make([]string, 0, len(receipts))
	for _, r := range receipts {
		lines = append(lines, r.Line())
	}
	return lines
}

// ChatSessionIssuesResponse is GET /api/chat/sessions/{id}/issues.
type ChatSessionIssuesResponse struct {
	ChatSessionID string            `json:"chat_session_id"`
	Issues        []receipt.Receipt `json:"issues"`
}

// ListChatSessionIssues answers `multica chat issues`: the tasks this chat
// dispatched and where each one stands.
func (h *Handler) ListChatSessionIssues(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	session, ok := h.gatePublicChatSessionForUser(w, r, userID, workspaceID, chi.URLParam(r, "sessionId"))
	if !ok {
		return
	}
	viewer, err := h.visibilityViewerFor(r, session.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve visibility")
		return
	}
	limit := int32(50)
	receipts, err := h.dispatchedReceipts(r.Context(), session, limit, viewer.canSeeIssue)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list dispatched issues")
		return
	}
	writeJSON(w, http.StatusOK, ChatSessionIssuesResponse{ChatSessionID: uuidToString(session.ID), Issues: receipts})
}
