package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/botsource"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// HandoffIssueRequest is the server-owned handoff command. "reviewer" moves
// the issue to in_review and hands it to the issue's agent reviewer; an agent
// name is an explicit named handoff. Both use the same mention queue as
// comments.
type HandoffIssueRequest struct {
	To string `json:"to"`
}

type HandoffIssueResponse struct {
	Target     string `json:"target"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id,omitempty"`
	TargetName string `json:"target_name,omitempty"`
	Routed     bool   `json:"routed"`
	RunCreated bool   `json:"run_created"`
	Duplicate  bool   `json:"duplicate"`
	Reason     string `json:"reason,omitempty"`
}

// HandoffIssue performs an explicit agent handoff atomically from the
// caller's point of view, and reports the writes that actually happened.
func (h *Handler) HandoffIssue(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req HandoffIssueRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	target := strings.TrimSpace(req.To)
	if target == "" {
		writeError(w, http.StatusBadRequest, "--to is required")
		return
	}
	finish := func(resp HandoffIssueResponse) {
		writeJSON(w, http.StatusOK, resp)
	}
	var agent db.Agent
	targetType := "agent"
	if target == "reviewer" {
		if !issue.ReviewerType.Valid || issue.ReviewerType.String != "agent" || !issue.ReviewerID.Valid {
			writeError(w, http.StatusConflict, "this issue has no agent reviewer; set one or hand off to an agent by name")
			return
		}
		reviewer, err := h.Queries.GetAgent(r.Context(), issue.ReviewerID)
		if err != nil {
			writeError(w, 500, "load reviewer failed")
			return
		}
		// A reviewer handoff is a real status transition. Go through UpdateIssue
		// so the review delivery gate runs with the request's actor identity.
		if issue.Status != "in_review" {
			if !h.handoffSetInReview(w, r) {
				return
			}
			issue, ok = h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
			if !ok {
				return
			}
		}
		agent, targetType = reviewer, "reviewer"
	} else {
		agents, err := h.Queries.ListAgents(r.Context(), issue.WorkspaceID)
		if err != nil {
			writeError(w, 500, "load agents failed")
			return
		}
		for _, candidate := range agents {
			if strings.EqualFold(candidate.Name, target) || uuidToString(candidate.ID) == target {
				agent = candidate
				break
			}
		}
		if !agent.ID.Valid {
			writeError(w, http.StatusBadRequest, "target must be reviewer or an agent name")
			return
		}
	}
	resp := HandoffIssueResponse{Target: target, TargetType: targetType, TargetID: uuidToString(agent.ID), TargetName: agent.Name}
	pending, err := h.Queries.HasActiveTaskForIssueAndAgent(r.Context(), db.HasActiveTaskForIssueAndAgentParams{IssueID: issue.ID, AgentID: agent.ID})
	if err != nil {
		writeError(w, 500, "check existing run failed")
		return
	}
	if pending {
		resp.Duplicate = true
		resp.Reason = "agent already has an active run"
		finish(resp)
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	authorType, authorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	content := "交棒给 " + agent.Name + "：" + " [@" + agent.Name + "](mention://agent/" + uuidToString(agent.ID) + ")"
	authorUUID, err := util.ParseUUID(authorID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid actor")
		return
	}
	// An agent-initiated handoff must carry its own run on the comment row:
	// EnqueueTaskForMention walks comment.source_task_id → parent task →
	// originator_user_id to attribute the new run (`delegation`). Without the
	// stamp the handed-off run resolves as unattributed and the chain the
	// human started is lost (DENE-896). Same stamp CreateComment applies.
	var sourceTaskID pgtype.UUID
	if authorType == "agent" {
		if task, ok := h.taskFromRequestHeader(r); ok {
			sourceTaskID = task.ID
		}
	}
	created, err := h.Queries.CreateComment(r.Context(), db.CreateCommentParams{
		ViaBotID: botsource.FromContext(r.Context()).ID, ViaBotName: pgtype.Text{String: botsource.FromContext(r.Context()).Name, Valid: botsource.FromContext(r.Context()).ID.Valid}, IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, AuthorType: authorType, AuthorID: authorUUID, Content: content, Type: "comment", ParentID: pgtype.UUID{}, SourceTaskID: sourceTaskID, QuickActionID: pgtype.UUID{}, ViaPluginID: pgtype.UUID{}})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 404, "issue not found")
		} else {
			writeError(w, 500, "create handoff comment failed")
		}
		return
	}
	if _, err = h.TaskService.EnqueueTaskForMention(r.Context(), issue, agent.ID, created.ID, service.OriginNamed); err != nil {
		writeError(w, 500, "enqueue handoff run failed")
		return
	}
	resp.Routed, resp.RunCreated = true, true
	finish(resp)
}

// handoffSetInReview reuses the canonical issue update path so reviewer
// handoffs cannot skip the PR/no-code gate.
func (h *Handler) handoffSetInReview(w http.ResponseWriter, r *http.Request) bool {
	req := r.Clone(r.Context())
	req.Method = http.MethodPut
	req.Body = io.NopCloser(bytes.NewReader([]byte(`{"status":"in_review"}`)))
	req.ContentLength = int64(len(`{"status":"in_review"}`))
	rec := httptest.NewRecorder()
	h.UpdateIssue(rec, req)
	if rec.Code >= http.StatusOK && rec.Code < http.StatusMultipleChoices {
		return true
	}
	for key, values := range rec.Header() {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(rec.Code)
	_, _ = io.Copy(w, rec.Result().Body)
	return false
}
