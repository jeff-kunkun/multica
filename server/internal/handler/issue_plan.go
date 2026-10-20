package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type StageAdvanceIssue struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	RunStarted bool   `json:"run_started,omitempty"`
}

// StageAdvanceResponse reports what the advance actually did. Advanced is
// false when nothing was promoted; Pending then names what the stage still
// waits on.
type StageAdvanceResponse struct {
	Parent   string              `json:"parent"`
	Stage    int32               `json:"stage,omitempty"`
	Advanced bool                `json:"advanced"`
	Promoted []StageAdvanceIssue `json:"promoted,omitempty"`
	Pending  []StageAdvanceIssue `json:"pending,omitempty"`
	Message  string              `json:"message"`
	// Error repeats Message on a refusal so every client that reads the
	// ordinary {"error": ...} shape shows the reason.
	Error string `json:"error,omitempty"`
}

// planStageAdvance decides the advance from the children's resolved statuses.
// The stage to open is the lowest one that still has open work; everything
// below it is terminal by construction. It advances when that stage still has
// backlog children — they are promoted. When its open children are all
// already active, the previous stage is done and this one is running: nothing
// to promote, and the caller is told which tickets it waits on.
func planStageAdvance(children []db.Issue, statuses resolvedChildStatuses) (stage int32, promote, pending []db.Issue) {
	for _, c := range children {
		if !c.Stage.Valid || statuses.isTerminal(c) {
			continue
		}
		if stage == 0 || c.Stage.Int32 < stage {
			stage = c.Stage.Int32
		}
	}
	if stage == 0 {
		return 0, nil, nil
	}
	for _, c := range children {
		if !c.Stage.Valid || c.Stage.Int32 != stage || statuses.isTerminal(c) {
			continue
		}
		if statuses.status(c) == "backlog" {
			promote = append(promote, c)
		} else {
			pending = append(pending, c)
		}
	}
	return stage, promote, pending
}

// AdvanceIssueStage is `multica issue stage advance <parent>`: if every stage
// below the next one is terminal, promote that stage's backlog children to
// todo and wake their executors; otherwise refuse and name what is missing.
func (h *Handler) AdvanceIssueStage(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	parent, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	workspaceID := uuidToString(parent.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	prefix := h.getIssuePrefix(r.Context(), parent.WorkspaceID)
	parentLabel := issueIdentifier(prefix, parent.Number)

	children, err := h.Queries.ListChildIssues(r.Context(), parent.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list sub-issues")
		return
	}
	if !siblingsAreStaged(children) {
		writeError(w, http.StatusBadRequest, parentLabel+" has no staged sub-issues; there is no stage to advance")
		return
	}
	statuses, err := resolveChildStatuses(children, h.childStatusResolver(r.Context()))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve sub-issue statuses")
		return
	}
	stage, promote, pending := planStageAdvance(children, statuses)
	row := func(c db.Issue, status string) StageAdvanceIssue {
		return StageAdvanceIssue{ID: uuidToString(c.ID), Identifier: issueIdentifier(prefix, c.Number), Title: c.Title, Status: status}
	}
	resp := StageAdvanceResponse{Parent: parentLabel, Stage: stage}
	if stage == 0 {
		resp.Message = "every stage of " + parentLabel + " is terminal; nothing to advance"
		writeJSON(w, http.StatusOK, resp)
		return
	}
	for _, c := range pending {
		resp.Pending = append(resp.Pending, row(c, statuses.status(c)))
	}
	if len(promote) == 0 {
		// The open stage is already running. Its predecessor is done; this
		// one is not, and the refusal says exactly which tickets hold it.
		labels := make([]string, 0, len(pending))
		for _, p := range resp.Pending {
			labels = append(labels, fmt.Sprintf("%s (%s)", p.Identifier, p.Status))
		}
		msg := fmt.Sprintf("stage %d of %s is not finished: %s", stage, parentLabel, strings.Join(labels, ", "))
		if next := stageAbove(children, stage); next > 0 {
			msg += fmt.Sprintf("; stage %d waits for it", next)
		}
		writeJSON(w, http.StatusConflict, StageAdvanceResponse{Parent: parentLabel, Stage: stage, Pending: resp.Pending, Message: msg, Error: msg})
		return
	}

	for _, c := range promote {
		updated, ran, err := h.promoteChildToTodo(r.Context(), c, actorType, actorID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // A concurrent advance already moved it.
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to promote "+issueIdentifier(prefix, c.Number))
			return
		}
		item := row(updated, updated.Status)
		item.RunStarted = ran
		resp.Promoted = append(resp.Promoted, item)
	}
	resp.Advanced = len(resp.Promoted) > 0
	labels := make([]string, 0, len(resp.Promoted))
	for _, p := range resp.Promoted {
		label := p.Identifier
		if !p.RunStarted {
			label += " (no run started)"
		}
		labels = append(labels, label)
	}
	if resp.Advanced {
		resp.Message = fmt.Sprintf("stage %d of %s promoted to todo: %s", stage, parentLabel, strings.Join(labels, ", "))
	} else {
		resp.Message = fmt.Sprintf("stage %d of %s was already promoted by a concurrent advance", stage, parentLabel)
	}
	writeJSON(w, http.StatusOK, resp)
}

func stageAbove(children []db.Issue, stage int32) int32 {
	stages := []int32{}
	for _, c := range children {
		if c.Stage.Valid && c.Stage.Int32 > stage {
			stages = append(stages, c.Stage.Int32)
		}
	}
	if len(stages) == 0 {
		return 0
	}
	sort.Slice(stages, func(i, j int) bool { return stages[i] < stages[j] })
	return stages[0]
}

// promoteChildToTodo moves one backlog child to todo, broadcasts it, and
// enqueues its assignee's run when the trigger rules say the move starts one.
// pgx.ErrNoRows means the child had already left backlog.
func (h *Handler) promoteChildToTodo(ctx context.Context, child db.Issue, actorType, actorID string) (db.Issue, bool, error) {
	updated, err := h.Queries.PromoteBacklogIssueToTodo(ctx, db.PromoteBacklogIssueToTodoParams{
		ID: child.ID, WorkspaceID: child.WorkspaceID,
	})
	if err != nil {
		return db.Issue{}, false, err
	}
	h.publishWithSource(ctx, protocol.EventIssueUpdated, uuidToString(updated.WorkspaceID), actorType, actorID, RoutingIssueUpdatedPayload(child, updated))
	if h.IssueService == nil {
		return updated, false, nil
	}
	trigger, ok := h.IssueService.WillEnqueueRun(ctx, service.IssueTriggerInput{
		Issue: updated, PrevStatus: child.Status, StatusChanged: true,
	}, service.IssueTriggerProbe{})
	if !ok {
		return updated, false, nil
	}
	h.dispatchIssueRun(ctx, updated, trigger, actorType, actorID, "")
	return updated, true, nil
}
