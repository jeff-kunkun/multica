package handler

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/routing"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// applyReviewRejection consumes an explicit reviewer hold once per review stay.
// Failed reviewer runs are a different signal: CoverFailedReview still retries
// infrastructure failures without treating them as a rejected implementation.
func (h *Handler) applyReviewRejection(ctx context.Context, comment db.Comment) error {
	if h.Routing == nil {
		return nil
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var locked string
	if err = tx.QueryRow(ctx, "SELECT id::text FROM issue WHERE id=$1 FOR UPDATE", comment.IssueID).Scan(&locked); err != nil {
		return err
	}
	q := h.Queries.WithTx(tx)
	issue, err := q.GetIssue(ctx, comment.IssueID)
	if err != nil {
		return err
	}
	if issuestatus.Effective(ctx, q, issue.WorkspaceID, issue.Status) != "in_review" || !h.authorIsReviewer(issue, comment) {
		return nil
	}
	settings, err := (routingStore{h: h}).Settings(ctx, uuidToString(issue.WorkspaceID))
	if err != nil {
		return err
	}
	if !settings.State().Active() {
		return nil
	}
	meta := parseIssueMetadata(issue.Metadata)
	// The closing evidence identifies who delivered, even after handoff changes
	// assignee to the reviewer. Never infer an executor from an arbitrary worker.
	evidenceID, _ := meta[closeprotocol.KeyEvidenceCommentID].(string)
	evidence, err := q.GetComment(ctx, parseUUID(evidenceID))
	if err != nil {
		return fmt.Errorf("cannot identify original executor from close evidence: %w", err)
	}
	if evidence.AuthorType != "agent" || evidence.AuthorID == issue.ReviewerID {
		return fmt.Errorf("review return needs a distinct agent executor")
	}
	executor := uuidToString(evidence.AuthorID)
	count := 0
	if prior, _ := meta["routing.review_executor"].(string); prior == executor {
		if n, ok := meta["routing.review_returns"].(float64); ok {
			count = int(n)
		}
	}
	count++
	meta["routing.review_executor"], meta["routing.review_returns"] = executor, count
	meta["routing.review_hold_comment"] = uuidToString(comment.ID)
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE issue SET status='in_progress', metadata=$2, updated_at=now(), revision=revision+1 WHERE id=$1", issue.ID, raw); err != nil {
		return err
	}
	updated, err := q.GetIssue(ctx, issue.ID)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	(routingStore{h: h}).publishIssueUpdated(issue, updated)
	if count > settings.ReviewReturns() {
		_, err = h.Routing.EscalateReview(ctx, uuidToString(issue.WorkspaceID), uuidToString(issue.ID), executor, comment.Content)
	} else {
		err = h.Routing.Store.Handoff(ctx, uuidToString(issue.WorkspaceID), uuidToString(issue.ID), "agent", executor)
	}
	if err != nil {
		// If no handoff happened, retain the review stay so a missing next-tier
		// seat cannot leave the reviewer as an in-progress executor.
		_, restoreErr := h.DB.Exec(ctx, "UPDATE issue SET status=$2,metadata=$3,updated_at=now(),revision=revision+1 WHERE id=$1 AND status='in_progress' AND assignee_id=$4", issue.ID, issue.Status, issue.Metadata, issue.AssigneeID)
		if restoreErr != nil {
			return fmt.Errorf("%v; restore review state: %w", err, restoreErr)
		}
		if restored, readErr := h.Queries.GetIssue(ctx, issue.ID); readErr == nil {
			(routingStore{h: h}).publishIssueUpdated(updated, restored)
		}
		_, _ = (routingStore{h: h}).PostComment(ctx, uuidToString(issue.WorkspaceID), uuidToString(issue.ID), routing.CommentKind("review_return_unavailable"), "验收未通过，但无法改派："+err.Error()+"。请 Grok 协调")
	}
	return err
}
