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
	"github.com/multica-ai/multica/server/internal/botsource"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/permission"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Plans (kun-main office). One member puts a plan card on an issue's thread;
// anyone unsure @mentions another member under it to cut scope (plain
// comments); only a workspace owner or an approver the owner granted can
// approve, and approval is the only thing that turns the steps into child
// issues and starts them. Every plan names the project its work belongs to,
// or says it is general work outside any project.

const maxPlanSteps = 5

type PlanStep struct {
	Title        string  `json:"title"`
	AssigneeType string  `json:"assignee_type"`
	AssigneeID   string  `json:"assignee_id"`
	IssueID      *string `json:"issue_id,omitempty"`
}

type PlanResponse struct {
	ID         string     `json:"id"`
	IssueID    string     `json:"issue_id"`
	Version    int32      `json:"version"`
	Status     string     `json:"status"`
	AuthorType string     `json:"author_type"`
	AuthorID   string     `json:"author_id"`
	Goal       string     `json:"goal"`
	OutOfScope string     `json:"out_of_scope"`
	Steps      []PlanStep `json:"steps"`
	DoneWhen   string     `json:"done_when"`
	// ProjectID is where approval files the work; null means general work
	// outside any project.
	ProjectID  *string `json:"project_id"`
	CommentID  *string `json:"comment_id"`
	ApprovedBy *string `json:"approved_by"`
	ApprovedAt *string `json:"approved_at"`
	CreatedAt  string  `json:"created_at"`
}

func planToResponse(p db.Plan) PlanResponse {
	var steps []PlanStep
	_ = json.Unmarshal(p.Steps, &steps)
	if steps == nil {
		steps = []PlanStep{}
	}
	return PlanResponse{
		ID:         uuidToString(p.ID),
		IssueID:    uuidToString(p.IssueID),
		Version:    p.Version,
		Status:     p.Status,
		AuthorType: p.AuthorType,
		AuthorID:   uuidToString(p.AuthorID),
		Goal:       p.Goal,
		OutOfScope: p.OutOfScope,
		Steps:      steps,
		DoneWhen:   p.DoneWhen,
		ProjectID:  uuidToPtr(p.ProjectID),
		CommentID:  uuidToPtr(p.CommentID),
		ApprovedBy: uuidToPtr(p.ApprovedBy),
		ApprovedAt: timestampToPtr(p.ApprovedAt),
		CreatedAt:  timestampToString(p.CreatedAt),
	}
}

type ProposePlanRequest struct {
	Goal       string     `json:"goal"`
	OutOfScope string     `json:"out_of_scope"`
	Steps      []PlanStep `json:"steps"`
	DoneWhen   string     `json:"done_when"`
	// A plan states exactly one of these: the project its work belongs to,
	// or general (no project).
	ProjectID *string `json:"project_id"`
	General   bool    `json:"general"`
}

const planProjectRequired = "say where this work belongs: project_id for a project, or general: true for work outside any project (CLI: --project <project id> or --general)"

// ProposePlan writes a new plan version on the issue and posts its card as a
// comment. The card never triggers anyone; it is approval that starts work.
func (h *Handler) ProposePlan(w http.ResponseWriter, r *http.Request) {
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
	if req.ProjectID != nil && strings.TrimSpace(*req.ProjectID) == "" {
		req.ProjectID = nil
	}
	switch {
	case req.ProjectID == nil && !req.General:
		writeError(w, http.StatusBadRequest, planProjectRequired)
		return
	case req.ProjectID != nil && req.General:
		writeError(w, http.StatusBadRequest, "a plan names one project or is general, not both")
		return
	}
	var projectID pgtype.UUID
	if req.ProjectID != nil {
		id, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(*req.ProjectID), "project_id")
		if !ok {
			return
		}
		if _, ok := h.visibleProjectInWorkspace(w, r, issue.WorkspaceID, id); !ok {
			return
		}
		projectID = id
	}

	workspaceID := uuidToString(issue.WorkspaceID)
	seenTitles := map[string]bool{}
	steps := make([]PlanStep, len(req.Steps))
	for i, step := range req.Steps {
		title := strings.TrimSpace(step.Title)
		if title == "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("step %d needs a title", i+1))
			return
		}
		if seenTitles[title] {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("step %d repeats the title %q", i+1, title))
			return
		}
		seenTitles[title] = true
		if step.AssigneeType != "agent" && step.AssigneeType != "member" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("step %d needs exactly one owner (an agent or a member)", i+1))
			return
		}
		assigneeID, ok := parseUUIDOrBadRequest(w, step.AssigneeID, fmt.Sprintf("steps[%d].assignee_id", i))
		if !ok {
			return
		}
		if status, msg := h.validateAssigneePair(r.Context(), r, workspaceID, pgtype.Text{String: step.AssigneeType, Valid: true}, assigneeID); status != 0 {
			writeError(w, status, fmt.Sprintf("step %d: %s", i+1, msg))
			return
		}
		steps[i] = PlanStep{Title: title, AssigneeType: step.AssigneeType, AssigneeID: uuidToString(assigneeID)}
	}
	stepsJSON, err := json.Marshal(steps)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode steps")
		return
	}

	authorType, authorID := h.resolveActor(r, requestUserID(r), workspaceID)
	var sourceTaskID pgtype.UUID
	if authorType == "agent" {
		sourceTaskID, _ = util.ParseUUID(r.Header.Get("X-Task-ID"))
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
		ProjectID:   projectID,
	})
	if err != nil {
		slog.Warn("create plan failed", append(logger.RequestAttrs(r), "error", err, "issue_id", uuidToString(issue.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to propose plan")
		return
	}
	issueKey := h.getIssuePrefix(r.Context(), issue.WorkspaceID) + "-" + strconv.Itoa(int(issue.Number))
	created, err := qtx.CreateComment(r.Context(), db.CreateCommentParams{
		ID:           dbid.NewV7(),
		IssueID:      issue.ID,
		WorkspaceID:  issue.WorkspaceID,
		AuthorType:   authorType,
		AuthorID:     parseUUID(authorID),
		Content:      h.planCardMarkdown(r.Context(), issue.WorkspaceID, plan, steps, issueKey),
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
	writeJSON(w, http.StatusCreated, planToResponse(plan))
}

// ListPlans returns every version on the issue, newest first.
func (h *Handler) ListPlans(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
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
	writeJSON(w, http.StatusOK, map[string]any{"plans": out, "can_approve": h.canApprovePlan(r.Context(), r, uuidToString(issue.WorkspaceID))})
}

// ApprovePlan approves one plan version and creates a child issue per step,
// each assigned to its owner, which starts the work. Only the newest version
// can be approved, and only by someone who can see the project it files work
// under. Approval and filing the plan's own issue under that project commit
// together. Re-approving an approved version creates any step issue a failed
// earlier attempt left out, and nothing else.
func (h *Handler) ApprovePlan(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	version, err := strconv.Atoi(chi.URLParam(r, "version"))
	if err != nil || version < 1 {
		writeError(w, http.StatusBadRequest, "invalid version")
		return
	}
	workspaceID := uuidToString(issue.WorkspaceID)
	if !h.canApprovePlan(r.Context(), r, workspaceID) {
		writeError(w, http.StatusForbidden, "only the workspace owner or an approver the owner granted can approve plans")
		return
	}
	approverID := requestUserID(r)

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to approve plan")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	// Issue before plan, the order ProposePlan locks in.
	if _, err := qtx.LockIssueForPlan(r.Context(), db.LockIssueForPlanParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "issue not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to approve plan")
		return
	}
	plan, err := qtx.GetPlanVersionForUpdate(r.Context(), db.GetPlanVersionForUpdateParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, Version: int32(version)})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "plan version not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to approve plan")
		return
	}
	if plan.Status == "superseded" {
		writeError(w, http.StatusConflict, fmt.Sprintf("plan v%d was replaced by a newer version", version))
		return
	}
	if plan.ProjectID.Valid {
		project, err := qtx.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{ID: plan.ProjectID, WorkspaceID: plan.WorkspaceID})
		if err != nil {
			if !isNotFound(err) {
				writeError(w, http.StatusInternalServerError, "failed to approve plan")
				return
			}
			writeError(w, http.StatusConflict, fmt.Sprintf("the project plan v%d names no longer exists; propose a new version with a project or general", version))
			return
		}
		if !h.requestCanSeeProject(r, plan.WorkspaceID, project) {
			writeError(w, http.StatusForbidden, fmt.Sprintf("plan v%d files its work under a project you cannot see; ask someone in that project to approve it", version))
			return
		}
	}
	newlyApproved := false
	var moved *db.Issue
	if plan.Status == "proposed" {
		latest, err := qtx.GetLatestPlanVersion(r.Context(), db.GetLatestPlanVersionParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to approve plan")
			return
		}
		if latest != plan.Version {
			writeError(w, http.StatusConflict, fmt.Sprintf("plan v%d is not the newest version (v%d)", version, latest))
			return
		}
		moved, err = movePlanIssueToProject(r.Context(), qtx, issue, plan)
		if err != nil {
			slog.Warn("move plan issue to project failed", append(logger.RequestAttrs(r), "error", err, "issue_id", uuidToString(issue.ID))...)
			writeError(w, http.StatusInternalServerError, "failed to approve plan: could not file the issue under the plan's project")
			return
		}
		plan, err = qtx.ApprovePlan(r.Context(), db.ApprovePlanParams{ID: plan.ID, WorkspaceID: plan.WorkspaceID, ApprovedBy: parseUUID(approverID)})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to approve plan")
			return
		}
		newlyApproved = true
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to approve plan")
		return
	}

	var steps []PlanStep
	if err := json.Unmarshal(plan.Steps, &steps); err != nil {
		writeError(w, http.StatusInternalServerError, "plan steps are unreadable")
		return
	}
	prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID)
	issueKey := prefix + "-" + strconv.Itoa(int(issue.Number))
	fill := h.newStatusCategoryFiller(r.Context(), issue.WorkspaceID)
	if moved != nil {
		settled := h.settleIssueDomain(r.Context(), *moved, nil)
		moved = &settled
		resp := issueToResponse(*moved, prefix)
		fill(&resp)
		h.publish(protocol.EventIssueUpdated, workspaceID, "member", approverID, map[string]any{
			"issue":           resp,
			"project_changed": true,
			"creator_type":    moved.CreatorType,
			"creator_id":      uuidToString(moved.CreatorID),
		})
	}
	created := make([]IssueResponse, 0, len(steps))
	var createErr error
	for i := range steps {
		if steps[i].IssueID != nil {
			continue
		}
		child, err := h.createPlanStepIssue(r, issue, plan, steps[i], issueKey, approverID, prefix, fill)
		if err != nil {
			createErr = fmt.Errorf("step %d: %w", i+1, err)
			break
		}
		id := uuidToString(child.ID)
		steps[i].IssueID = &id
		resp := issueToResponse(child, prefix)
		fill(&resp)
		created = append(created, resp)
	}
	if len(created) > 0 {
		stepsJSON, _ := json.Marshal(steps)
		if updated, err := h.Queries.SetPlanSteps(r.Context(), db.SetPlanStepsParams{ID: plan.ID, WorkspaceID: plan.WorkspaceID, Steps: stepsJSON}); err == nil {
			plan = updated
		} else {
			slog.Warn("record plan step issues failed", append(logger.RequestAttrs(r), "error", err, "plan_id", uuidToString(plan.ID))...)
		}
	}
	if createErr != nil {
		slog.Warn("create plan step issue failed", append(logger.RequestAttrs(r), "error", createErr, "plan_id", uuidToString(plan.ID))...)
		writeError(w, http.StatusInternalServerError, "plan approved but not every step issue was created; approve again to finish")
		return
	}
	if newlyApproved || len(created) > 0 {
		h.postPlanApprovedComment(r.Context(), issue, plan, steps, prefix, approverID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"plan": planToResponse(plan), "issues": created})
}

func (h *Handler) createPlanStepIssue(r *http.Request, parent db.Issue, plan db.Plan, step PlanStep, parentKey, approverID, prefix string, fill func(*IssueResponse)) (db.Issue, error) {
	description := fmt.Sprintf("From the approved plan v%d on %s.\n\n**Do**\n%s\n\n**Not doing**\n%s\n\n**This step**\n%s\n\n**Done when**\n%s",
		plan.Version, parentKey, plan.Goal, plan.OutOfScope, step.Title, plan.DoneWhen)
	res, err := h.IssueService.Create(r.Context(), service.IssueCreateParams{
		WorkspaceID:   parent.WorkspaceID,
		Title:         step.Title,
		Description:   pgtype.Text{String: description, Valid: true},
		Status:        "todo",
		Priority:      parent.Priority,
		AssigneeType:  pgtype.Text{String: step.AssigneeType, Valid: true},
		AssigneeID:    parseUUID(step.AssigneeID),
		CreatorType:   "member",
		CreatorID:     parseUUID(approverID),
		ParentIssueID: parent.ID,
		ProjectID:     plan.ProjectID,
		// A general plan keeps its steps out of any project rather than
		// inheriting the parent's.
		ProjectPinned: true,
	}, service.IssueCreateOpts{
		ActorID:  approverID,
		Platform: func() string { p, _, _ := middleware.ClientMetadataFromContext(r.Context()); return p }(),
		BroadcastPayload: func(issue db.Issue, _ []db.Attachment, labels []db.IssueLabel) map[string]any {
			payload := issueToResponse(issue, prefix)
			fill(&payload)
			labelResponses := labelsToResponse(labels)
			payload.Labels = &labelResponses
			return map[string]any{"issue": payload}
		},
	})
	if errors.Is(err, service.ErrActiveDuplicate) && res.DuplicateIssue != nil {
		// A retry after a partial failure: the step's issue already exists.
		return *res.DuplicateIssue, nil
	}
	if err != nil {
		return db.Issue{}, err
	}
	return res.Issue, nil
}

// movePlanIssueToProject files the plan's own issue under the project the plan
// names (or takes it out of any project for general work), so the parent sits
// with its step issues. It runs in the approval transaction with the issue
// locked and changes only the project: every other field is written back as it
// is now, not as the request first read it. Like a project move through
// UpdateIssue, an issue joining a project takes that project's current scope
// (DENE-698); the domain is settled after commit. Returns nil when nothing
// moved.
func movePlanIssueToProject(ctx context.Context, qtx *db.Queries, issue db.Issue, plan db.Plan) (*db.Issue, error) {
	current, err := qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		return nil, err
	}
	if uuidToString(current.ProjectID) == uuidToString(plan.ProjectID) {
		return nil, nil
	}
	params := db.UpdateIssueParams{
		ID:                 current.ID,
		ExpectedRevision:   pgtype.Int8{Int64: current.Revision, Valid: true},
		AssigneeType:       current.AssigneeType,
		AssigneeID:         current.AssigneeID,
		ReviewerType:       current.ReviewerType,
		ReviewerID:         current.ReviewerID,
		StartDate:          current.StartDate,
		DueDate:            current.DueDate,
		ParentIssueID:      current.ParentIssueID,
		ProjectID:          plan.ProjectID,
		Stage:              current.Stage,
		DuplicateOfIssueID: current.DuplicateOfIssueID,
	}
	if plan.ProjectID.Valid {
		target, err := qtx.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: plan.ProjectID, WorkspaceID: current.WorkspaceID})
		if err != nil {
			return nil, err
		}
		if permission.Visibility(target.Visibility).Valid() {
			params.Visibility = pgtype.Text{String: target.Visibility, Valid: true}
		}
	}
	updated, err := qtx.UpdateIssue(ctx, params)
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

// canApprovePlan: a workspace owner, or a member the owner granted approval.
// Agents, machine credentials and Bot keys (which act with their creator's
// power) never approve: approval is a person's decision.
func (h *Handler) canApprovePlan(ctx context.Context, r *http.Request, workspaceID string) bool {
	if isMachineCredentialActor(r) || botsource.FromContext(r.Context()).ID.Valid {
		return false
	}
	userID := requestUserID(r)
	if actorType, _ := h.resolveActor(r, userID, workspaceID); actorType != "member" {
		return false
	}
	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return false
	}
	userUUID, err := util.ParseUUID(userID)
	if err != nil {
		return false
	}
	member, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: userUUID, WorkspaceID: wsUUID})
	if err != nil {
		return false
	}
	if member.Role == "owner" {
		return true
	}
	ok, err := h.Queries.IsPlanApprover(ctx, db.IsPlanApproverParams{WorkspaceID: wsUUID, UserID: userUUID})
	return err == nil && ok
}

// requestCanSeeProject applies the old-line project visibility rules
// (visibilityViewer) to the requester; a lookup failure denies.
func (h *Handler) requestCanSeeProject(r *http.Request, workspaceID pgtype.UUID, project db.Project) bool {
	if !project.CreatedBy.Valid {
		return true
	}
	viewer, err := h.visibilityViewerFor(r, workspaceID)
	if err != nil {
		slog.Warn("plan project visibility", append(logger.RequestAttrs(r), "project_id", uuidToString(project.ID), "error", err)...)
		return false
	}
	return viewer.canSeeProject(project)
}

// planActorName names a plan's author, step owner or approver, falling back to
// the raw id when the actor cannot be found.
func (h *Handler) planActorName(ctx context.Context, workspaceID pgtype.UUID, actorType, actorID string) string {
	id, err := util.ParseUUID(actorID)
	if err != nil {
		return actorID
	}
	if name := h.actorDisplayName(ctx, workspaceID, actorType, id); name != actorType {
		return name
	}
	return actorID
}

func (h *Handler) planCardMarkdown(ctx context.Context, workspaceID pgtype.UUID, plan db.Plan, steps []PlanStep, issueKey string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Plan v%d** — waiting for approval\n\n", plan.Version)
	fmt.Fprintf(&b, "**Project**\n%s\n\n", h.planProjectName(ctx, plan))
	fmt.Fprintf(&b, "**Do**\n%s\n\n**Not doing**\n%s\n\n**Steps**\n", plan.Goal, plan.OutOfScope)
	for i, step := range steps {
		fmt.Fprintf(&b, "%d. %s — %s\n", i+1, step.Title, h.planActorName(ctx, workspaceID, step.AssigneeType, step.AssigneeID))
	}
	fmt.Fprintf(&b, "\n**Done when**\n%s\n\n", plan.DoneWhen)
	fmt.Fprintf(&b, "Unsure? Reply here and @mention one member to say what can be cut. Approve: `multica plan approve %s --version %d`", issueKey, plan.Version)
	return b.String()
}

func (h *Handler) planProjectName(ctx context.Context, plan db.Plan) string {
	if !plan.ProjectID.Valid {
		return "General (no project)"
	}
	if project, err := h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: plan.ProjectID, WorkspaceID: plan.WorkspaceID}); err == nil {
		return project.Title
	}
	return uuidToString(plan.ProjectID)
}

func (h *Handler) postPlanApprovedComment(ctx context.Context, issue db.Issue, plan db.Plan, steps []PlanStep, prefix, approverID string) {
	keys := make([]string, 0, len(steps))
	for _, step := range steps {
		if step.IssueID == nil {
			continue
		}
		id, err := util.ParseUUID(*step.IssueID)
		if err != nil {
			continue
		}
		if child, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: issue.WorkspaceID}); err == nil {
			keys = append(keys, prefix+"-"+strconv.Itoa(int(child.Number)))
		}
	}
	created, err := h.Queries.CreateComment(ctx, db.CreateCommentParams{
		ID:          dbid.NewV7(),
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorType:  "member",
		AuthorID:    parseUUID(approverID),
		Content:     fmt.Sprintf("**Plan v%d approved.** Issues: %s", plan.Version, strings.Join(keys, ", ")),
		Type:        "comment",
		ParentID:    plan.CommentID,
	})
	if err != nil {
		slog.Warn("plan approved comment failed", "error", err, "plan_id", uuidToString(plan.ID))
		return
	}
	h.publishPlanComment(issue, created, "member", approverID)
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

type PlanApproverResponse struct {
	UserID    string `json:"user_id"`
	Name      string `json:"name"`
	GrantedBy string `json:"granted_by"`
	CreatedAt string `json:"created_at"`
}

// ListPlanApprovers lists the members besides owners who can approve plans.
func (h *Handler) ListPlanApprovers(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	rows, err := h.Queries.ListPlanApprovers(r.Context(), member.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list approvers")
		return
	}
	out := make([]PlanApproverResponse, len(rows))
	for i, row := range rows {
		out[i] = PlanApproverResponse{
			UserID:    uuidToString(row.UserID),
			Name:      h.planActorName(r.Context(), row.WorkspaceID, "member", uuidToString(row.UserID)),
			GrantedBy: uuidToString(row.GrantedBy),
			CreatedAt: timestampToString(row.CreatedAt),
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvers": out})
}

// AddPlanApprover grants a member plan approval. Owner only.
func (h *Handler) AddPlanApprover(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.requirePlanApproverManager(w, r)
	if !ok {
		return
	}
	var req struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	userID, ok := parseUUIDOrBadRequest(w, req.UserID, "user_id")
	if !ok {
		return
	}
	if _, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{UserID: userID, WorkspaceID: owner.WorkspaceID}); err != nil {
		writeError(w, http.StatusBadRequest, "user_id does not refer to a member of this workspace")
		return
	}
	row, err := h.Queries.AddPlanApprover(r.Context(), db.AddPlanApproverParams{WorkspaceID: owner.WorkspaceID, UserID: userID, GrantedBy: owner.UserID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to add approver")
		return
	}
	writeJSON(w, http.StatusOK, PlanApproverResponse{
		UserID:    uuidToString(row.UserID),
		Name:      h.planActorName(r.Context(), row.WorkspaceID, "member", uuidToString(row.UserID)),
		GrantedBy: uuidToString(row.GrantedBy),
		CreatedAt: timestampToString(row.CreatedAt),
	})
}

// RemovePlanApprover revokes a member's plan approval. Owner only.
func (h *Handler) RemovePlanApprover(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.requirePlanApproverManager(w, r)
	if !ok {
		return
	}
	userID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "userId"), "user_id")
	if !ok {
		return
	}
	if _, err := h.Queries.RemovePlanApprover(r.Context(), db.RemovePlanApproverParams{WorkspaceID: owner.WorkspaceID, UserID: userID}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove approver")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) requirePlanApproverManager(w http.ResponseWriter, r *http.Request) (db.Member, bool) {
	if isMachineCredentialActor(r) {
		writeError(w, http.StatusForbidden, "this endpoint is only available to human actors")
		return db.Member{}, false
	}
	workspaceID := h.resolveWorkspaceID(r)
	if actorType, _ := h.resolveActor(r, requestUserID(r), workspaceID); actorType != "member" {
		writeError(w, http.StatusForbidden, "agents cannot manage plan approvers")
		return db.Member{}, false
	}
	return h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner")
}
