package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// HandoffIssueRequest is the server-owned handoff command. "reviewer" and
// "dispatcher" deliberately go through the existing router; an agent name is
// an explicit named handoff and uses the same mention queue as comments.
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

// HandoffIssue performs routing and explicit agent handoff atomically from
// the caller's point of view, and reports the writes that actually happened.
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
	if target == "reviewer" || target == "dispatcher" {
		if issue.ReviewerType.Valid && issue.ReviewerType.String == "member" {
			writeError(w, http.StatusConflict, "reviewer seat is filled by a person; handoff cannot replace a human reviewer")
			return
		}
		if h.Routing == nil {
			writeError(w, http.StatusConflict, "routing is unavailable")
			return
		}
		out, err := h.Routing.Route(r.Context(), uuidToString(issue.WorkspaceID), uuidToString(issue.ID))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp := HandoffIssueResponse{Target: target, TargetType: "seat", Routed: out.ExecutorWritten != nil || !out.ReviewerWritten.Empty(), Reason: out.Reason}
		if out.ReviewerWritten.ID != "" {
			resp.TargetID = out.ReviewerWritten.ID
			resp.TargetName = out.ReviewerWritten.Name
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	agents, err := h.Queries.ListAgents(r.Context(), issue.WorkspaceID)
	if err != nil {
		writeError(w, 500, "load agents failed")
		return
	}
	var agent db.Agent
	for _, candidate := range agents {
		if strings.EqualFold(candidate.Name, target) || uuidToString(candidate.ID) == target {
			agent = candidate
			break
		}
	}
	if !agent.ID.Valid {
		writeError(w, http.StatusBadRequest, "target must be reviewer, dispatcher, or an agent name")
		return
	}
	resp := HandoffIssueResponse{Target: target, TargetType: "agent", TargetID: uuidToString(agent.ID), TargetName: agent.Name}
	pending, err := h.Queries.HasPendingTaskForIssueAndAgent(r.Context(), db.HasPendingTaskForIssueAndAgentParams{IssueID: issue.ID, AgentID: agent.ID, HeadSha: pgtype.Text{}})
	if err != nil {
		writeError(w, 500, "check existing run failed")
		return
	}
	comments, err := h.Queries.ListCommentsForIssue(r.Context(), db.ListCommentsForIssueParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, Limit: 500})
	if err != nil {
		writeError(w, 500, "check existing handoff failed")
		return
	}
	mention := "mention://agent/" + uuidToString(agent.ID)
	for _, c := range comments {
		if strings.Contains(c.Content, mention) {
			resp.Duplicate = true
			resp.Reason = "agent already mentioned"
			writeJSON(w, http.StatusOK, resp)
			return
		}
	}
	if pending {
		resp.Duplicate = true
		resp.Reason = "agent already has an active run"
		writeJSON(w, http.StatusOK, resp)
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
	created, err := h.Queries.CreateComment(r.Context(), db.CreateCommentParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, AuthorType: authorType, AuthorID: authorUUID, Content: content, Type: "comment", ParentID: pgtype.UUID{}, SourceTaskID: pgtype.UUID{}, QuickActionID: pgtype.UUID{}, ViaPluginID: pgtype.UUID{}})
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
	writeJSON(w, http.StatusOK, resp)
}
