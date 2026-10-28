package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Plan first (DENE-2670). An issue created with plan_first carries an
// issue_plan_gate row. Its agent proposes a plan (a card on the thread); until
// a person approves a version, the server refuses the agent's moves to review
// or a done status and its PR links. Approval and "change it, then do it" are
// replies under the card, so the agent wakes on that comment and resumes the
// same session. Issues without a gate row see no change, and with the
// office_plan_first server flag off nothing here applies: plan_first is
// ignored, existing gates hold nothing back and the endpoints are refused.

const (
	maxPlanSteps        = 5
	planNotApprovedCode = "plan_not_approved"
	planNotApprovedMsg  = "this issue asks for a plan first and no plan is approved yet: propose one with `multica issue plan propose`, end your turn, and continue after a person approves it"
)

type PlanStep struct {
	Title string `json:"title"`
}

type PlanResponse struct {
	ID                 string     `json:"id"`
	IssueID            string     `json:"issue_id"`
	Version            int32      `json:"version"`
	Status             string     `json:"status"`
	AuthorType         string     `json:"author_type"`
	AuthorID           string     `json:"author_id"`
	Goal               string     `json:"goal"`
	OutOfScope         string     `json:"out_of_scope"`
	Steps              []PlanStep `json:"steps"`
	DoneWhen           string     `json:"done_when"`
	CommentID          *string    `json:"comment_id"`
	ApprovedBy         *string    `json:"approved_by"`
	ApprovedAt         *string    `json:"approved_at"`
	ChangesRequestedBy *string    `json:"changes_requested_by"`
	ChangesRequestedAt *string    `json:"changes_requested_at"`
	CreatedAt          string     `json:"created_at"`
}

type IssuePlansResponse struct {
	// Gated is true when the issue asks for a plan first; Approved when a
	// version has been approved. Gated && !Approved is the only blocking state.
	Gated      bool           `json:"gated"`
	Approved   bool           `json:"approved"`
	CanApprove bool           `json:"can_approve"`
	Plans      []PlanResponse `json:"plans"`
}

// PlanAnswerResponse is an approve or send-back answer: the plan, and whom the
// reply under its card woke. Wake.NoneWoken means the agent was not reached;
// approving again retries the wake.
type PlanAnswerResponse struct {
	PlanResponse
	Wake *CommentWake `json:"wake,omitempty"`
}

func (h *Handler) planFirstEnabled(ctx context.Context) bool {
	return featureflags.OfficePlanFirstEnabled(ctx, h.FeatureFlags)
}

// requirePlanFirst refuses the plan endpoints while the server flag is off.
func (h *Handler) requirePlanFirst(w http.ResponseWriter, r *http.Request) bool {
	if h.planFirstEnabled(r.Context()) {
		return true
	}
	writeFeatureDisabled(w, "plan_first_disabled", "plan first is not enabled on this server")
	return false
}

func planToResponse(p db.Plan) PlanResponse {
	var steps []PlanStep
	_ = json.Unmarshal(p.Steps, &steps)
	if steps == nil {
		steps = []PlanStep{}
	}
	return PlanResponse{
		ID:                 uuidToString(p.ID),
		IssueID:            uuidToString(p.IssueID),
		Version:            p.Version,
		Status:             p.Status,
		AuthorType:         p.AuthorType,
		AuthorID:           uuidToString(p.AuthorID),
		Goal:               p.Goal,
		OutOfScope:         p.OutOfScope,
		Steps:              steps,
		DoneWhen:           p.DoneWhen,
		CommentID:          uuidToPtr(p.CommentID),
		ApprovedBy:         uuidToPtr(p.ApprovedBy),
		ApprovedAt:         timestampToPtr(p.ApprovedAt),
		ChangesRequestedBy: uuidToPtr(p.ChangesRequestedBy),
		ChangesRequestedAt: timestampToPtr(p.ChangesRequestedAt),
		CreatedAt:          timestampToString(p.CreatedAt),
	}
}

// planBlocksAgent reports whether the issue asks for a plan first and none is
// approved yet.
func (h *Handler) planBlocksAgent(ctx context.Context, issue db.Issue) (bool, error) {
	if !h.planFirstEnabled(ctx) {
		return false, nil
	}
	state, err := h.Queries.GetIssuePlanGateState(ctx, db.GetIssuePlanGateStateParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		return false, err
	}
	return state.Gated && !state.Approved, nil
}

// planGatedStatus is a move the gate holds back: review, or any status in the
// done category. Cancelling stays open so an agent can still drop the work.
func (h *Handler) planGatedStatus(ctx context.Context, workspaceID pgtype.UUID, statusKey string) bool {
	if statusKey == "" {
		return false
	}
	if statusKey == issuestatus.InReview {
		return true
	}
	return issuestatus.Category(ctx, h.issueStatusCatalog(), workspaceID, statusKey) == issuestatus.CategoryDone
}

// planGateRejection is the reason an agent's status move is refused, or "".
func (h *Handler) planGateRejection(ctx context.Context, issue db.Issue, actorType, statusKey string) (string, error) {
	if actorType != "agent" || statusKey == issue.Status || !h.planGatedStatus(ctx, issue.WorkspaceID, statusKey) {
		return "", nil
	}
	blocked, err := h.planBlocksAgent(ctx, issue)
	if err != nil || !blocked {
		return "", err
	}
	return planNotApprovedMsg, nil
}

// Claim-time plan gate states (AgentTaskResponse.PlanGate).
const (
	planGateNeedsPlan        = "needs_plan"
	planGateAwaitingApproval = "awaiting_approval"
	planGateChangesRequested = "changes_requested"
)

// claimPlanGate tells a run on a gated issue where the plan stands. Best
// effort: a read failure sends nothing, and the status and PR gates still hold.
func (h *Handler) claimPlanGate(ctx context.Context, issue db.Issue) string {
	blocked, err := h.planBlocksAgent(ctx, issue)
	if err != nil || !blocked {
		return ""
	}
	plans, err := h.Queries.ListPlansByIssue(ctx, db.ListPlansByIssueParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil || len(plans) == 0 || plans[0].Status != "proposed" {
		return planGateNeedsPlan
	}
	if plans[0].ChangesRequestedAt.Valid {
		return planGateChangesRequested
	}
	return planGateAwaitingApproval
}

func writePlanNotApproved(w http.ResponseWriter) {
	writeErrorCode(w, http.StatusConflict, planNotApprovedCode, planNotApprovedMsg)
}

// batchPlanGated reports whether the plan gate refused any item of a batch.
func batchPlanGated(rejected []BatchUpdateIssueRejection) bool {
	for _, r := range rejected {
		if r.Code == planNotApprovedCode {
			return true
		}
	}
	return false
}

type ProposePlanRequest struct {
	Goal       string     `json:"goal"`
	OutOfScope string     `json:"out_of_scope"`
	Steps      []PlanStep `json:"steps"`
	DoneWhen   string     `json:"done_when"`
}

// ListIssuePlans (GET /api/issues/{id}/plans) returns the gate state and every
// plan version, newest first.
func (h *Handler) ListIssuePlans(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlanFirst(w, r) {
		return
	}
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	state, err := h.Queries.GetIssuePlanGateState(r.Context(), db.GetIssuePlanGateStateParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load plan state")
		return
	}
	plans, err := h.Queries.ListPlansByIssue(r.Context(), db.ListPlansByIssueParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list plans")
		return
	}
	out := make([]PlanResponse, len(plans))
	for i, p := range plans {
		out[i] = planToResponse(p)
	}
	writeJSON(w, http.StatusOK, IssuePlansResponse{
		Gated:      state.Gated,
		Approved:   state.Approved,
		CanApprove: h.canApproveIssuePlan(r.Context(), r, issue),
		Plans:      out,
	})
}

// ProposeIssuePlan (POST /api/issues/{id}/plans) writes the next plan version,
// supersedes the earlier proposed one and posts the plan card. The card wakes
// nobody; the reviewer's answer under it does.
func (h *Handler) ProposeIssuePlan(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlanFirst(w, r) {
		return
	}
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req ProposePlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Goal = strings.TrimSpace(req.Goal)
	req.OutOfScope = strings.TrimSpace(req.OutOfScope)
	req.DoneWhen = strings.TrimSpace(req.DoneWhen)
	switch {
	case req.Goal == "":
		writeError(w, http.StatusBadRequest, "goal is required")
		return
	case req.OutOfScope == "":
		writeError(w, http.StatusBadRequest, "out_of_scope is required: say what this plan will not do")
		return
	case req.DoneWhen == "":
		writeError(w, http.StatusBadRequest, "done_when is required")
		return
	case len(req.Steps) == 0 || len(req.Steps) > maxPlanSteps:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("a plan has 1 to %d steps", maxPlanSteps))
		return
	}
	steps := make([]PlanStep, len(req.Steps))
	for i, step := range req.Steps {
		title := strings.TrimSpace(step.Title)
		if title == "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("step %d needs a title", i+1))
			return
		}
		steps[i] = PlanStep{Title: title}
	}
	stepsJSON, err := json.Marshal(steps)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode steps")
		return
	}

	workspaceID := uuidToString(issue.WorkspaceID)
	authorType, authorID := h.resolveActor(r, requestUserID(r), workspaceID)
	var sourceTaskID pgtype.UUID
	if authorType == "agent" {
		if task, ok := h.taskFromRequestHeader(r); ok {
			sourceTaskID = task.ID
		}
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to propose plan")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if _, err := qtx.LockIssueForPlan(r.Context(), db.LockIssueForPlanParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "issue not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to propose plan")
		return
	}
	state, err := qtx.GetIssuePlanGateState(r.Context(), db.GetIssuePlanGateStateParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to propose plan")
		return
	}
	if !state.Gated {
		writeErrorCode(w, http.StatusConflict, "plan_not_requested", "this issue does not ask for a plan first; just do the work")
		return
	}
	if state.Approved {
		writeErrorCode(w, http.StatusConflict, "plan_already_approved", "a plan on this issue is already approved; discuss changes in a comment")
		return
	}
	if err := qtx.SupersedeProposedPlans(r.Context(), db.SupersedeProposedPlansParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to propose plan")
		return
	}
	plan, err := qtx.CreatePlan(r.Context(), db.CreatePlanParams{
		WorkspaceID: issue.WorkspaceID,
		IssueID:     issue.ID,
		AuthorType:  authorType,
		AuthorID:    parseUUID(authorID),
		Goal:        req.Goal,
		OutOfScope:  req.OutOfScope,
		Steps:       stepsJSON,
		DoneWhen:    req.DoneWhen,
		ProjectID:   issue.ProjectID,
	})
	if err != nil {
		slog.Warn("create plan failed", append(logger.RequestAttrs(r), "error", err, "issue_id", uuidToString(issue.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to propose plan")
		return
	}
	created, err := qtx.CreateComment(r.Context(), db.CreateCommentParams{
		ID:           dbid.NewV7(),
		IssueID:      issue.ID,
		WorkspaceID:  issue.WorkspaceID,
		AuthorType:   authorType,
		AuthorID:     parseUUID(authorID),
		Content:      planCardMarkdown(plan, steps, h.issueKey(r.Context(), issue)),
		Type:         "comment",
		SourceTaskID: sourceTaskID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to post plan card")
		return
	}
	if err := qtx.SetPlanComment(r.Context(), db.SetPlanCommentParams{ID: plan.ID, WorkspaceID: plan.WorkspaceID, CommentID: created.ID}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to propose plan")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to propose plan")
		return
	}
	plan.CommentID = created.ID
	h.publishPlanComment(issue, created, authorType, authorID)
	h.publishIssuePlanChanged(workspaceID, issue.ID)
	writeJSON(w, http.StatusCreated, planToResponse(plan))
}

// ApproveIssuePlan (POST /api/issues/{id}/plans/{version}/approve) approves
// the newest proposed version and replies under its card, which wakes the
// agent in the same session. Approving an approved version changes nothing.
func (h *Handler) ApproveIssuePlan(w http.ResponseWriter, r *http.Request) {
	h.answerIssuePlan(w, r, false)
}

// RequestIssuePlanChanges (POST /api/issues/{id}/plans/{version}/request-changes)
// is "change it, then do it": the reviewer's note goes under the card and wakes
// the agent to propose the next version.
func (h *Handler) RequestIssuePlanChanges(w http.ResponseWriter, r *http.Request) {
	h.answerIssuePlan(w, r, true)
}

func (h *Handler) answerIssuePlan(w http.ResponseWriter, r *http.Request, requestChanges bool) {
	if !h.requirePlanFirst(w, r) {
		return
	}
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	version, err := strconv.Atoi(chi.URLParam(r, "version"))
	if err != nil || version < 1 {
		writeError(w, http.StatusBadRequest, "invalid version")
		return
	}
	var req struct {
		Feedback string `json:"feedback"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	req.Feedback = strings.TrimSpace(req.Feedback)
	if requestChanges && req.Feedback == "" {
		writeError(w, http.StatusBadRequest, "feedback is required: say what to change")
		return
	}
	if !h.canApproveIssuePlan(r.Context(), r, issue) {
		writeError(w, http.StatusForbidden, "only a person can answer a plan: the issue's creator, a workspace owner or admin, or a plan approver")
		return
	}
	reviewerID := requestUserID(r)
	verb := "approve"
	if requestChanges {
		verb = "send back"
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to "+verb+" plan")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	// Issue before plan, the order ProposeIssuePlan locks in.
	if _, err := qtx.LockIssueForPlan(r.Context(), db.LockIssueForPlanParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "issue not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to "+verb+" plan")
		return
	}
	plan, err := qtx.GetPlanVersionForUpdate(r.Context(), db.GetPlanVersionForUpdateParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, Version: int32(version)})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "plan version not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to "+verb+" plan")
		return
	}
	switch plan.Status {
	case "approved":
		if requestChanges {
			writeErrorCode(w, http.StatusConflict, "plan_already_approved", fmt.Sprintf("plan v%d is already approved; discuss changes in a comment", version))
			return
		}
		// Approving again changes nothing, but retries the wake when no run
		// picked the approval up.
		tx.Rollback(r.Context())
		writeJSON(w, http.StatusOK, PlanAnswerResponse{PlanResponse: planToResponse(plan), Wake: h.retryPlanApprovalWake(r, issue, plan)})
		return
	case "superseded":
		writeErrorCode(w, http.StatusConflict, "plan_superseded", fmt.Sprintf("plan v%d was replaced by a newer version", version))
		return
	}
	if requestChanges {
		plan, err = qtx.RequestPlanChanges(r.Context(), db.RequestPlanChangesParams{ID: plan.ID, WorkspaceID: plan.WorkspaceID, RequestedBy: parseUUID(reviewerID)})
	} else {
		plan, err = qtx.ApprovePlan(r.Context(), db.ApprovePlanParams{ID: plan.ID, WorkspaceID: plan.WorkspaceID, ApprovedBy: parseUUID(reviewerID)})
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to "+verb+" plan")
		return
	}
	// The reply is written with the answer: a plan is never approved without
	// the comment that wakes its agent.
	content := fmt.Sprintf("**计划 v%d 已批准**，按计划开始做。", plan.Version)
	if requestChanges {
		content = fmt.Sprintf("**计划 v%d 改一下再做**\n\n%s\n\n改好后用 `multica issue plan propose` 交新版本。", plan.Version, req.Feedback)
	}
	card := h.planCard(r.Context(), issue, plan)
	params := db.CreateCommentParams{
		ID:          dbid.NewV7(),
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorType:  "member",
		AuthorID:    parseUUID(reviewerID),
		Content:     content,
		Type:        "comment",
	}
	if card != nil {
		params.ParentID = card.ID
	}
	reply, err := qtx.CreateComment(r.Context(), params)
	if err != nil {
		slog.Warn("plan answer comment failed", append(logger.RequestAttrs(r), "error", err, "plan_id", uuidToString(plan.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to "+verb+" plan")
		return
	}
	if !requestChanges {
		if err := qtx.SetPlanApprovalComment(r.Context(), db.SetPlanApprovalCommentParams{ID: plan.ID, WorkspaceID: plan.WorkspaceID, ApprovalCommentID: reply.ID}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to "+verb+" plan")
			return
		}
		plan.ApprovalCommentID = reply.ID
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to "+verb+" plan")
		return
	}

	h.publishPlanComment(issue, reply, "member", reviewerID)
	h.publishIssuePlanChanged(uuidToString(issue.WorkspaceID), issue.ID)
	wake := h.wakeForPlanAnswer(r, issue, reply.Comment(), card, reviewerID)
	writeJSON(w, http.StatusOK, PlanAnswerResponse{PlanResponse: planToResponse(plan), Wake: wake})
}

// planCard is the plan's card comment, the parent its answers reply under.
func (h *Handler) planCard(ctx context.Context, issue db.Issue, plan db.Plan) *db.Comment {
	if !plan.CommentID.Valid {
		return nil
	}
	card, err := h.Queries.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{ID: plan.CommentID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		return nil
	}
	return &card
}

// wakeForPlanAnswer runs the ordinary comment triggers for a reply under the
// plan card. The card's author (the agent) is woken by that reply, and a
// comment-triggered run resumes the issue's session.
func (h *Handler) wakeForPlanAnswer(r *http.Request, issue db.Issue, reply db.Comment, card *db.Comment, reviewerID string) *CommentWake {
	ctx := r.Context()
	_, woken := h.triggerTasksForCommentReport(ctx, issue, reply, card, "member", reviewerID, reviewerID, nil, nil)
	wake := h.commentWake(ctx, issue, reply, woken)
	if wake.NoneWoken {
		slog.Warn("plan answer woke no agent", append(logger.RequestAttrs(r), "issue_id", uuidToString(issue.ID), "comment_id", uuidToString(reply.ID))...)
	}
	return wake
}

// retryPlanApprovalWake wakes the agent again on the approval reply when no
// run on the issue is active or has run since the approval.
func (h *Handler) retryPlanApprovalWake(r *http.Request, issue db.Issue, plan db.Plan) *CommentWake {
	ctx := r.Context()
	if !plan.ApprovalCommentID.Valid || !plan.ApprovedAt.Valid {
		return nil
	}
	pickedUp, err := h.Queries.PlanApprovalPickedUp(ctx, db.PlanApprovalPickedUpParams{IssueID: issue.ID, Since: plan.ApprovedAt})
	if err != nil || pickedUp {
		return nil
	}
	reply, err := h.Queries.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{ID: plan.ApprovalCommentID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		return nil
	}
	return h.wakeForPlanAnswer(r, issue, reply, h.planCard(ctx, issue, plan), uuidToString(reply.AuthorID))
}

// canApproveIssuePlan: a person, never an agent or machine credential, who
// created the issue, is a workspace owner or admin, or holds a plan approver
// grant.
func (h *Handler) canApproveIssuePlan(ctx context.Context, r *http.Request, issue db.Issue) bool {
	if isMachineCredentialActor(r) {
		return false
	}
	userID := requestUserID(r)
	workspaceID := uuidToString(issue.WorkspaceID)
	if actorType, _ := h.resolveActor(r, userID, workspaceID); actorType != "member" {
		return false
	}
	userUUID, err := util.ParseUUID(userID)
	if err != nil {
		return false
	}
	if issue.CreatorType == "member" && issue.CreatorID == userUUID {
		return true
	}
	if member, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: userUUID, WorkspaceID: issue.WorkspaceID}); err == nil && roleAllowed(member.Role, "owner", "admin") {
		return true
	}
	ok, err := h.Queries.IsPlanApprover(ctx, db.IsPlanApproverParams{WorkspaceID: issue.WorkspaceID, UserID: userUUID})
	return err == nil && ok
}

func (h *Handler) issueKey(ctx context.Context, issue db.Issue) string {
	return h.getIssuePrefix(ctx, issue.WorkspaceID) + "-" + strconv.Itoa(int(issue.Number))
}

func planCardMarkdown(plan db.Plan, steps []PlanStep, issueKey string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**计划 v%d**，等人批准\n\n", plan.Version)
	fmt.Fprintf(&b, "**要做**\n%s\n\n**不做**\n%s\n\n**步骤**\n", plan.Goal, plan.OutOfScope)
	for i, step := range steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, step.Title)
	}
	fmt.Fprintf(&b, "\n**做完的标准**\n%s\n\n", plan.DoneWhen)
	fmt.Fprintf(&b, "批准：`multica issue plan approve %s --version %d`；要改：`multica issue plan request-changes %s --version %d --feedback \"...\"`", issueKey, plan.Version, issueKey, plan.Version)
	return b.String()
}

func (h *Handler) publishPlanComment(issue db.Issue, created db.CreateCommentRow, authorType, authorID string) {
	resp := commentToResponse(created.Comment(), nil, nil)
	resp.IssueRevision = created.IssueRevision
	h.publish(protocol.EventCommentCreated, uuidToString(issue.WorkspaceID), authorType, authorID, map[string]any{
		"comment":             resp,
		"issue_title":         issue.Title,
		"issue_assignee_type": textToPtr(issue.AssigneeType),
		"issue_assignee_id":   uuidToPtr(issue.AssigneeID),
		"issue_status":        issue.Status,
		"issue_revision":      created.IssueRevision,
	})
}

func (h *Handler) publishIssuePlanChanged(workspaceID string, issueID pgtype.UUID) {
	h.publish(protocol.EventIssuePlanChanged, workspaceID, "system", "", map[string]any{
		"issue_id": uuidToString(issueID),
	})
}
