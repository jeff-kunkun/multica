package handler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	"github.com/multica-ai/multica/server/internal/parking"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// statusTransition is the adjustment a move into in_review or done needs
// before the row is written. An empty value leaves the request alone.
type statusTransition struct {
	status       string
	reviewerType pgtype.Text
	reviewerID   pgtype.UUID
	setReviewer  bool
	block        blockwait.Record
	persistBlock bool
	note         string
	handoff      bool
	refuse       string
	// reviewSkip is set when an agent's in_review was written as done
	// because the PR is already merged and reviewed (DENE-1678).
	reviewSkip *service.ReviewSkip
}

func reviewerIsAssigned(issue db.Issue) bool {
	return issue.ReviewerType.Valid && issue.ReviewerType.String != "" && issue.ReviewerType.String != "none" && issue.ReviewerID.Valid
}

// guardSilentStall stops quiet stalls at the status write.
//
// An issue moving into in_review gets its empty reviewer slot filled with a
// different-family acceptance seat, for a child issue as much as for a
// parent, or the write is turned into a structured block. An agent executor
// may not mark done an issue whose acceptance seat is someone else.
//
// The child rule (DENE-869): routing.Route still treats a sub-issue as
// execution-only and never starts an independent judge chain for it. That is
// about *who decides*, not about whether the slot may stay empty. DENE-860 sat
// in in_review for two hours with reviewer_id NULL because this guard skipped
// children; the seat is now filled here by the same ladder fallback the parent
// gets, and ensureAcceptanceRunning starts it once the executor's run is gone.
//
// Every refusal it returns is also kept in issue_rejection, so the parking
// record (DENE-881) can say "送审被拒" after the run is gone.
func (h *Handler) guardSilentStall(ctx context.Context, issue db.Issue, projectID pgtype.UUID, nextStatus string, actorType, actorID string, assigneeType pgtype.Text, assigneeID pgtype.UUID, reviewerType pgtype.Text, reviewerID pgtype.UUID, reviewerExplicit bool) statusTransition {
	tr := h.decideSilentStall(ctx, issue, projectID, nextStatus, actorType, actorID, assigneeType, assigneeID, reviewerType, reviewerID, reviewerExplicit)
	if tr.refuse != "" {
		h.recordRejection(ctx, issue, nextStatus, actorType, actorID, tr.refuse)
	}
	return tr
}

// recordRejection keeps a refused status move. Best effort: losing the row
// only costs the parking record one reason, never the refusal itself.
func (h *Handler) recordRejection(ctx context.Context, issue db.Issue, nextStatus, actorType, actorID, reason string) {
	kind := parking.RejectClose
	if nextStatus == issuestatus.InReview {
		kind = parking.RejectReview
	}
	actor := pgtype.UUID{}
	if id, err := util.ParseUUID(actorID); err == nil {
		actor = id
	}
	if err := h.Queries.CreateIssueRejection(ctx, db.CreateIssueRejectionParams{
		ID:          dbid.NewV7(),
		WorkspaceID: issue.WorkspaceID,
		IssueID:     issue.ID,
		ActorType:   actorType,
		ActorID:     actor,
		Action:      "status:" + nextStatus,
		Kind:        kind,
		Reason:      reason,
	}); err != nil {
		slog.Warn("record rejection failed", "issue_id", uuidToString(issue.ID), "error", err)
	}
}

func (h *Handler) decideSilentStall(ctx context.Context, issue db.Issue, projectID pgtype.UUID, nextStatus string, actorType, actorID string, assigneeType pgtype.Text, assigneeID pgtype.UUID, reviewerType pgtype.Text, reviewerID pgtype.UUID, reviewerExplicit bool) statusTransition {
	var tr statusTransition
	switch nextStatus {
	case issuestatus.InReview:
		if issue.Status == issuestatus.InReview {
			return tr
		}
		if h.workflowOwnsReview(ctx, issue.WorkspaceID, projectID) {
			return tr
		}
		if actorType == "agent" {
			if skip, ok := h.findReviewSkip(ctx, issue, actorID); ok {
				// Already reviewed and merged: the acceptance seat would only
				// re-read a verdict that exists (DENE-1678).
				tr.status = issuestatus.Done
				tr.reviewSkip = &skip
				tr.note = "已审已合，跳过验收席：" + skip.Reason() + "。票直接完成。"
				return tr
			}
		}
		// `none` is a routing verdict meaning that this ticket does not need
		// acceptance. It is not a reviewer who can receive an in_review handoff.
		// The executor marks the ticket done instead of creating an unowned
		// in_review ticket. Explicit reviewer fields remain supported for
		// callers that deliberately override routing (DENE-1156).
		if strings.EqualFold(strings.TrimSpace(reviewerType.String), "none") && !reviewerExplicit {
			tr.refuse = "这张票的路由判定为不需要验收，不能进入 in_review；请改用 `multica issue status <issue> done`。如果确实需要验收，先指定 reviewer 再重试。"
			return tr
		}
		if reviewerChosen(reviewerType, reviewerID, reviewerExplicit) {
			return tr
		}
		if assigneeType.String != "agent" || !assigneeID.Valid {
			return tr
		}
		seat := h.fillAcceptanceSeat(ctx, issue, assigneeID)
		return seat
	case issuestatus.Done:
		if issue.Status == issuestatus.Done {
			return tr
		}
		// An agent may not close a ticket whose acceptance seat is someone
		// else. A child is the exception: its seat accepts it through the
		// parent's tree review (DENE-928/931).
		if actorType == "agent" && !issue.ParentIssueID.Valid && reviewerIsAssigned(issue) && actorID != uuidToString(issue.ReviewerID) {
			tr.refuse = "执行人不能直接关单：请用 `multica issue status <issue> in_review` 交给验收席。"
		}
		return tr
	default:
		return tr
	}
}

// workflowOwnsReview reports whether the project the write lands the issue in
// runs a workflow its workspace adopted: a non-default one, or a default one
// with handoffs. Such a workflow decides what in_review means and who takes
// it (MUL-7420), so the acceptance-seat rule steps aside (DENE-2315).
// Adopting a workflow needs project_workflows_v1, which is off by default, so
// a workspace that never adopts one keeps the acceptance seat on every
// in_review.
func (h *Handler) workflowOwnsReview(ctx context.Context, workspaceID, projectID pgtype.UUID) bool {
	def, _, err := issueworkflow.ForProject(ctx, h.Queries, workspaceID, projectID)
	if err != nil {
		slog.Warn("acceptance seat: load project workflow failed", "project_id", uuidToString(projectID), "error", err)
		return false
	}
	return def != nil && (!def.IsDefault || def.HandsOffAnywhere())
}

func reviewerChosen(reviewerType pgtype.Text, reviewerID pgtype.UUID, explicit bool) bool {
	if !reviewerType.Valid || strings.TrimSpace(reviewerType.String) == "" {
		return false
	}
	if reviewerType.String == "none" {
		return true
	}
	if explicit && reviewerID.Valid {
		return true
	}
	return reviewerID.Valid
}

// fillAcceptanceSeat picks the acceptance seat and, when none can be picked,
// leaves the refusal on the issue. The status write it guards keeps the issue
// where it was.
func (h *Handler) fillAcceptanceSeat(ctx context.Context, issue db.Issue, assigneeID pgtype.UUID) statusTransition {
	tr := h.pickAcceptanceSeat(ctx, issue, assigneeID)
	if tr.refuse != "" {
		// A close must never leave an in_review ticket with nobody to wake.
		// Convert an unseatable review into a structured human decision block;
		// the caller persists it together with the status write.
		why := tr.refuse
		tr.refuse = ""
		tr.status = issuestatus.Blocked
		tr.persistBlock = true
		tr.block, tr.note = h.reviewSeatBlock(ctx, issue, why)
	}
	return tr
}

func (h *Handler) reviewSeatBlock(ctx context.Context, issue db.Issue, why string) (blockwait.Record, string) {
	managers, err := h.Queries.ListWorkspaceManagerUserIDs(ctx, issue.WorkspaceID)
	if err != nil {
		slog.Warn("acceptance seat: list managers failed", "issue_id", uuidToString(issue.ID), "error", err)
	}
	rec := blockwait.Record{}
	if len(managers) > 0 && managers[0].Valid {
		rec.NeedsHuman = uuidToString(managers[0])
	} else {
		rec = blockwait.FailureWake(time.Now(), "验收席由人来定", 1)
	}
	reason := why + "。平台补不上验收席，这张票改成阻塞，等人指定验收席后再送审。"
	return rec, reason
}

// pickAcceptanceSeat chooses a different-family acceptance seat for an agent
// executor. It writes nothing and posts nothing: the refusal, when there is
// one, is in tr.refuse for the caller to deliver in its own words.
func (h *Handler) pickAcceptanceSeat(ctx context.Context, issue db.Issue, assigneeID pgtype.UUID) statusTransition {
	var tr statusTransition
	if h.Routing == nil {
		tr.refuse = "进不了待验收：这台服务没有验收席名册"
		return tr
	}
	view := routing.Issue{
		ID:           uuidToString(issue.ID),
		Title:        issue.Title,
		Status:       issuestatus.InReview,
		AssigneeType: "agent",
		AssigneeID:   uuidToString(assigneeID),
	}
	if issue.ProjectID.Valid {
		if project, err := h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{
			ID: issue.ProjectID, WorkspaceID: issue.WorkspaceID,
		}); err == nil {
			view.ProjectName = project.Title
		}
	}
	ref, why, ok := h.Routing.PickAcceptanceSeat(ctx, uuidToString(issue.WorkspaceID), view)
	if !ok {
		if why == "" {
			why = "选不出和执行席不同的验收席"
		}
		tr.refuse = "进不了待验收：" + why
		return tr
	}
	reviewerID, err := util.ParseUUID(ref.ID)
	if err != nil {
		tr.refuse = "进不了待验收：选中的验收席没有有效身份"
		return tr
	}
	name := ref.Name
	if name == "" {
		name = "另一个席位"
	}
	tr.setReviewer = true
	tr.reviewerType = pgtype.Text{String: "agent", Valid: true}
	tr.reviewerID = reviewerID
	tr.note = fmt.Sprintf("验收席是空的。已补上 %s，和执行席不是同一个人（%s）。", name, why)
	active, err := h.Queries.HasActiveTaskForIssue(ctx, issue.ID)
	if err != nil {
		slog.Warn("acceptance seat: active check failed", "issue_id", uuidToString(issue.ID), "error", err)
	}
	if active {
		tr.note += "当前这轮还没结束，结束后再把票交给这个验收席。"
		return tr
	}
	tr.handoff = true
	return tr
}

// findReviewSkip is service.FindReviewSkip for a gate that must not fail the
// write: a read error means "no skip", and the ticket goes to review.
func (h *Handler) findReviewSkip(ctx context.Context, issue db.Issue, executorID string) (service.ReviewSkip, bool) {
	var executors []pgtype.UUID
	if id, err := util.ParseUUID(executorID); err == nil {
		executors = append(executors, id)
	}
	skip, ok, err := service.FindReviewSkip(ctx, h.Queries, issue, executors...)
	if err != nil {
		slog.Warn("review skip: read failed", "issue_id", uuidToString(issue.ID), "error", err)
		return service.ReviewSkip{}, false
	}
	return skip, ok
}

// finishStatusTransition applies a handoff and posts the human note after the
// status row is committed. It returns the issue to publish, which may be the
// reloaded row when the acceptance seat just took the ticket.
func (h *Handler) finishStatusTransition(ctx context.Context, issue db.Issue, tr statusTransition) db.Issue {
	if tr.handoff && tr.reviewerID.Valid {
		if err := (routingStore{h: h}).Handoff(ctx, uuidToString(issue.WorkspaceID), uuidToString(issue.ID), "agent", uuidToString(tr.reviewerID)); err != nil {
			slog.Warn("acceptance handoff failed", "issue_id", uuidToString(issue.ID), "error", err)
			tr.note += "验收席补上了，但这轮没能启动。"
		} else {
			tr.note += "验收已经开始。"
			if fresh, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
				ID: issue.ID, WorkspaceID: issue.WorkspaceID,
			}); err == nil {
				issue = fresh
			}
		}
	}
	if tr.reviewSkip != nil && issue.Status == issuestatus.Done {
		if err := service.RecordReviewSkip(ctx, h.Queries, issue, *tr.reviewSkip); err != nil {
			slog.Warn("review skip: record failed", "issue_id", uuidToString(issue.ID), "error", err)
		}
	} else if issue.Status == issuestatus.InReview {
		// A reopened ticket goes through acceptance this time; an earlier
		// skip must not be read as this round's reason.
		service.ClearReviewSkip(ctx, h.Queries, issue)
	}
	if tr.note != "" {
		h.postBlockComment(ctx, issue, tr.note)
	}
	return issue
}

// ensureAcceptanceRunning starts the acceptance seat once the executor's run
// is gone. The status write itself waits out that run; this is the later pass.
func (h *Handler) ensureAcceptanceRunning(ctx context.Context, workspaceID, issueID string) {
	if h == nil || h.Queries == nil || workspaceID == "" || issueID == "" {
		return
	}
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return
	}
	id, err := util.ParseUUID(issueID)
	if err != nil {
		return
	}
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: wsID})
	if err != nil || issue.Status != issuestatus.InReview {
		return
	}
	if !issue.ReviewerType.Valid || issue.ReviewerType.String != "agent" || !issue.ReviewerID.Valid {
		return
	}
	if issue.AssigneeType.String == "agent" && issue.AssigneeID == issue.ReviewerID {
		return
	}
	active, err := h.Queries.HasActiveTaskForIssue(ctx, issue.ID)
	if err != nil || active {
		return
	}
	if err := (routingStore{h: h}).Handoff(ctx, workspaceID, issueID, "agent", uuidToString(issue.ReviewerID)); err != nil {
		slog.Warn("acceptance handoff failed", "issue_id", issueID, "error", err)
		return
	}
	h.postBlockComment(ctx, issue, "执行这轮结束了，验收席开始检查。")
}
