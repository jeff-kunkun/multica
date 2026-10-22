package handler

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/progress"
	"github.com/multica-ai/multica/server/internal/receipt"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Issue receipts back the child-done comment a parent gets when its
// sub-issues finish (issue_child_done.go).

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
		r.Summary = receipt.Clip(h.closeNote(ctx, issue, meta), receipt.MaxSummary)
	}
	if gh, err := h.Queries.ListPullRequestsByIssue(ctx, issue.ID); err == nil {
		for _, p := range gh {
			r.PRs = append(r.PRs, receipt.PR{Number: p.PrNumber, URL: p.HtmlUrl, State: prReceiptState(p.State, p.MergedAt.Valid)})
		}
	} else {
		slog.Warn("receipt: list pull requests failed", "issue_id", r.IssueID, "error", err)
	}
	if vcsRows, err := h.Queries.ListVCSPullRequestsByIssue(ctx, issue.ID); err == nil {
		for _, p := range vcsRows {
			r.PRs = append(r.PRs, receipt.PR{Number: p.PrNumber, URL: p.HtmlUrl, State: prReceiptState(p.State, p.MergedAt.Valid)})
		}
	} else {
		slog.Warn("receipt: list vcs pull requests failed", "issue_id", r.IssueID, "error", err)
	}
	return r
}

// closeNote is the latest close's summary: its progress line when the line
// belongs to this close, else the evidence comment's first paragraph.
func (h *Handler) closeNote(ctx context.Context, issue db.Issue, meta map[string]string) string {
	if !closeprotocol.Complete(meta) {
		return ""
	}
	row, err := h.Queries.GetLatestIssueProgressBySource(ctx, db.GetLatestIssueProgressBySourceParams{
		IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, Source: progress.SourceClose,
	})
	if err == nil && row.CreatedAt.Valid && summaryBelongsToClose(row.CreatedAt.Time, meta[closeprotocol.KeyAt]) {
		return row.Text
	}
	if id, err := parseUUIDStrict(strings.TrimSpace(meta[closeprotocol.KeyEvidenceCommentID])); err == nil {
		if c, err := h.Queries.GetComment(ctx, id); err == nil && c.IssueID == issue.ID && !c.DeletedAt.Valid {
			return firstParagraph(c.Content)
		}
	}
	return ""
}

// summaryBelongsToClose reports whether a close progress line written at
// progressAt was written by the close recorded at closeAt. The close writes
// close.at inside its transaction and the progress line right after commit,
// so a line from this close is never older than close.at; an older line is
// a previous close's summary and must not be shown as this one's.
func summaryBelongsToClose(progressAt time.Time, closeAt string) bool {
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(closeAt))
	if err != nil {
		return false
	}
	// close.at has second precision; allow the truncated second.
	return !progressAt.Before(at)
}

// firstParagraph returns the first non-empty paragraph of a markdown body,
// joined onto one line — the summary a close prepends to its evidence.
func firstParagraph(body string) string {
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			if len(lines) > 0 {
				break
			}
			continue
		}
		lines = append(lines, strings.TrimLeft(line, "# "))
	}
	return strings.Join(lines, " ")
}

// receiptsOf reads the receipts of the issues keep accepts. status names each
// issue's canonical status; an empty answer keeps the stored one.
func (h *Handler) receiptsOf(ctx context.Context, workspaceID pgtype.UUID, issues []db.Issue, keep func(db.Issue) bool, status func(db.Issue) string) []receipt.Receipt {
	prefix := h.getIssuePrefix(ctx, workspaceID)
	var out []receipt.Receipt
	for _, c := range issues {
		if !keep(c) {
			continue
		}
		r := h.issueReceipt(ctx, c, prefix)
		if s := status(c); s != "" {
			r.Status = s
		}
		out = append(out, r)
	}
	return out
}

// visibleWithParent keeps a sub-task that everyone who reads the parent can
// see: the system comment it lands in stays on the parent for every present
// and future reader. Two checks must both hold. The child is scoped no wider
// apart from its parent (future readers arrive through the parent's scope),
// and every member who can see the parent now can see the child, by the same
// rules the issue APIs use (an assignee or a share names the parent alone).
// Failing to establish the readers keeps nothing.
func (h *Handler) visibleWithParent(ctx context.Context, parent db.Issue) func(db.Issue) bool {
	none := func(db.Issue) bool { return false }
	members, err := h.Queries.ListMembers(ctx, parent.WorkspaceID)
	if err != nil {
		slog.Warn("receipt: list members failed", "issue_id", uuidToString(parent.ID), "error", err)
		return none
	}
	var readers []visibilityViewer
	for _, m := range members {
		v, err := h.visibilityViewerForUser(ctx, parent.WorkspaceID, m.UserID)
		if err != nil {
			slog.Warn("receipt: load member visibility failed", "issue_id", uuidToString(parent.ID), "error", err)
			return none
		}
		if v.canSeeIssue(parent) {
			readers = append(readers, v)
		}
	}
	scoped := scopedLikeParent(parent)
	return func(c db.Issue) bool {
		if !scoped(c) {
			return false
		}
		for _, v := range readers {
			if !v.canSeeIssue(c) {
				return false
			}
		}
		return true
	}
}

// scopedLikeParent keeps a sub-task whose scope reaches everyone the parent's
// scope does: one open to the workspace under a parent no guest can reach
// (a guest reaches a project, never the workspace), or one scoped exactly like
// its parent.
func scopedLikeParent(parent db.Issue) func(db.Issue) bool {
	return func(c db.Issue) bool {
		switch c.Visibility {
		case "workspace":
			return parent.Visibility != "project"
		case "project":
			return parent.Visibility == "project" && c.ProjectID == parent.ProjectID
		case "private":
			return parent.Visibility == "private" && c.CreatorType == parent.CreatorType && c.CreatorID == parent.CreatorID
		}
		return false
	}
}

func prReceiptState(state string, merged bool) string {
	if merged {
		return "merged"
	}
	return state
}
