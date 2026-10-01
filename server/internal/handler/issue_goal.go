package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// GoalResponse is the stable API representation consumed by CLI and clients.
type GoalResponse struct {
	ID       string              `json:"id"`
	IssueID  string              `json:"issue_id"`
	Status   string              `json:"status"`
	Round    int32               `json:"round"`
	Checks   []GoalCheckResponse `json:"checks"`
	Budget   GoalBudgetResponse  `json:"budget"`
	Usage    GoalUsageResponse   `json:"usage"`
	Evidence []any               `json:"evidence"`
}
type GoalCheckResponse struct {
	ID          string `json:"id"`
	Position    int32  `json:"position"`
	Description string `json:"description"`
	Method      string `json:"method"`
	Status      string `json:"status"`
	Passed      bool   `json:"passed"`
	Evidence    []any  `json:"evidence"`
}
type GoalBudgetResponse struct {
	TokenLimit      int64 `json:"token_limit"`
	RunLimit        int32 `json:"run_limit"`
	DurationSeconds int64 `json:"duration_seconds"`
}
type GoalUsageResponse struct {
	TokensUsed          int64 `json:"tokens_used"`
	RunsUsed            int32 `json:"runs_used"`
	DurationSecondsUsed int64 `json:"duration_seconds_used"`
	// Short aliases keep the CLI table and older clients readable while the
	// *_used names make the wire contract explicit.
	Tokens          int64 `json:"tokens"`
	Runs            int32 `json:"runs"`
	DurationSeconds int64 `json:"duration_seconds"`
}

type goalCheckInput struct {
	Description string `json:"description"`
	Method      string `json:"method"`
}
type goalBudgetInput struct {
	TokenLimit      int64 `json:"token_limit"`
	RunLimit        int32 `json:"run_limit"`
	DurationSeconds int64 `json:"duration_seconds"`
}
type createGoalRequest struct {
	Checks []goalCheckInput `json:"checks"`
	Budget goalBudgetInput  `json:"budget"`
}
type finishGoalRequest struct {
	Status string `json:"status"`
}

func goalJSON(raw []byte) []any {
	if len(raw) == 0 {
		return []any{}
	}
	var out []any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return []any{}
	}
	return out
}
func goalUUID(v pgtype.UUID) string { return uuidToString(v) }

func makeGoalResponse(goal db.IssueGoal, checks []db.IssueGoalCheck) GoalResponse {
	out := GoalResponse{
		ID: goalUUID(goal.ID), IssueID: goalUUID(goal.IssueID), Status: goal.Status, Round: goal.Round,
		Budget: GoalBudgetResponse{TokenLimit: goal.TokenLimit, RunLimit: goal.RunLimit, DurationSeconds: goal.DurationSeconds},
		Usage: GoalUsageResponse{
			TokensUsed: goal.TokensUsed, RunsUsed: goal.RunsUsed, DurationSecondsUsed: goal.DurationSecondsUsed,
			Tokens: goal.TokensUsed, Runs: goal.RunsUsed, DurationSeconds: goal.DurationSecondsUsed,
		},
		Evidence: goalJSON(goal.Evidence), Checks: make([]GoalCheckResponse, 0, len(checks)),
	}
	for _, c := range checks {
		out.Checks = append(out.Checks, GoalCheckResponse{ID: goalUUID(c.ID), Position: c.Position, Description: c.Description, Method: c.Method, Status: c.Status, Passed: c.Status == "passed", Evidence: goalJSON(c.Evidence)})
	}
	return out
}

func (h *Handler) loadIssueGoal(r *http.Request, issue db.Issue) (db.IssueGoal, []db.IssueGoalCheck, error) {
	goal, err := h.Queries.GetIssueGoal(r.Context(), db.GetIssueGoalParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		return db.IssueGoal{}, nil, err
	}
	checks, err := h.Queries.ListIssueGoalChecks(r.Context(), goal.ID)
	return goal, checks, err
}

// GetIssueGoal returns the completion line attached to an issue.
func (h *Handler) GetIssueGoal(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	goal, checks, err := h.loadIssueGoal(r, issue)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "goal not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load goal")
		return
	}
	writeJSON(w, http.StatusOK, makeGoalResponse(goal, checks))
}

func goalActorIsAgent(h *Handler, r *http.Request, issue db.Issue, userID string) bool {
	actorType, _ := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	return actorType == "agent"
}

// CreateIssueGoal attaches one draft completion line to an issue.
func (h *Handler) CreateIssueGoal(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req createGoalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Checks) == 0 {
		writeError(w, http.StatusBadRequest, "checks must contain at least one item")
		return
	}
	if req.Budget.TokenLimit < 0 || req.Budget.RunLimit < 0 || req.Budget.DurationSeconds < 0 {
		writeError(w, http.StatusBadRequest, "budget values must be non-negative")
		return
	}
	for i := range req.Checks {
		req.Checks[i].Description = strings.TrimSpace(req.Checks[i].Description)
		req.Checks[i].Method = strings.ToLower(strings.TrimSpace(req.Checks[i].Method))
		if req.Checks[i].Description == "" {
			writeError(w, http.StatusBadRequest, "check description is required")
			return
		}
		if req.Checks[i].Method == "" {
			req.Checks[i].Method = "acceptance"
		}
		switch req.Checks[i].Method {
		case "command", "test", "screenshot", "acceptance":
		default:
			writeError(w, http.StatusBadRequest, "check method must be command, test, screenshot, or acceptance")
			return
		}
	}
	if _, _, err := h.loadIssueGoal(r, issue); err == nil {
		writeError(w, http.StatusConflict, "issue already has a goal")
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to check existing goal")
		return
	}
	actorType := "member"
	if goalActorIsAgent(h, r, issue, userID) {
		actorType = "agent"
	}
	creatorID, _ := util.ParseUUID(userID)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start goal transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := db.New(tx)
	goal, err := qtx.CreateIssueGoal(r.Context(), db.CreateIssueGoalParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, TokenLimit: req.Budget.TokenLimit, RunLimit: req.Budget.RunLimit, DurationSeconds: req.Budget.DurationSeconds, CreatedByType: actorType, CreatedByID: creatorID})
	if err != nil {
		writeError(w, http.StatusConflict, "issue already has a goal")
		return
	}
	checks := make([]db.IssueGoalCheck, 0, len(req.Checks))
	for i, c := range req.Checks {
		row, e := qtx.CreateIssueGoalCheck(r.Context(), db.CreateIssueGoalCheckParams{GoalID: goal.ID, Position: int32(i), Description: c.Description, Method: c.Method})
		if e != nil {
			writeError(w, http.StatusInternalServerError, "failed to create goal check")
			return
		}
		checks = append(checks, row)
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save goal")
		return
	}
	writeJSON(w, http.StatusCreated, makeGoalResponse(goal, checks))
}

func (h *Handler) requireHumanGoalActor(w http.ResponseWriter, r *http.Request, issue db.Issue, goal db.IssueGoal) bool {
	userID, ok := requireUserID(w, r)
	if !ok {
		return false
	}
	if goalActorIsAgent(h, r, issue, userID) {
		writeError(w, http.StatusForbidden, "only a human can confirm or modify this goal")
		return false
	}
	return true
}

// ConfirmIssueGoal locks a draft and starts its execution state.
func (h *Handler) ConfirmIssueGoal(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	goal, _, err := h.loadIssueGoal(r, issue)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "goal not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load goal")
		return
	}
	if !h.requireHumanGoalActor(w, r, issue, goal) {
		return
	}
	goal, err = h.Queries.ConfirmIssueGoal(r.Context(), db.ConfirmIssueGoalParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "goal is already locked or finished")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to confirm goal")
		return
	}
	checks, err := h.Queries.ListIssueGoalChecks(r.Context(), goal.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load goal checks")
		return
	}
	writeJSON(w, http.StatusOK, makeGoalResponse(goal, checks))
}

// AppendIssueGoalBudget adds to the limits. Once locked, only a human may do this.
func (h *Handler) AppendIssueGoalBudget(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	goal, _, err := h.loadIssueGoal(r, issue)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "goal not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load goal")
		return
	}
	if goal.Status != "draft" && !h.requireHumanGoalActor(w, r, issue, goal) {
		return
	}
	var req goalBudgetInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.TokenLimit < 0 || req.RunLimit < 0 || req.DurationSeconds < 0 {
		writeError(w, http.StatusBadRequest, "budget values must be non-negative")
		return
	}
	goal, err = h.Queries.AppendIssueGoalBudget(r.Context(), db.AppendIssueGoalBudgetParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, TokenLimit: req.TokenLimit, RunLimit: req.RunLimit, DurationSeconds: req.DurationSeconds})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "goal cannot accept budget in its current state")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to append budget")
		return
	}
	checks, err := h.Queries.ListIssueGoalChecks(r.Context(), goal.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load goal checks")
		return
	}
	writeJSON(w, http.StatusOK, makeGoalResponse(goal, checks))
}

// FinishIssueGoal stops or marks a goal achieved.
func (h *Handler) FinishIssueGoal(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	goal, _, err := h.loadIssueGoal(r, issue)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "goal not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load goal")
		return
	}
	if !h.requireHumanGoalActor(w, r, issue, goal) {
		return
	}
	var req finishGoalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Status = strings.ToLower(strings.TrimSpace(req.Status))
	if req.Status != "stopped" && req.Status != "achieved" {
		writeError(w, http.StatusBadRequest, "status must be stopped or achieved")
		return
	}
	goal, err = h.Queries.FinishIssueGoal(r.Context(), db.FinishIssueGoalParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, Status: req.Status})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "goal cannot be finished in its current state")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to finish goal")
		return
	}
	checks, err := h.Queries.ListIssueGoalChecks(r.Context(), goal.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load goal checks")
		return
	}
	writeJSON(w, http.StatusOK, makeGoalResponse(goal, checks))
}
