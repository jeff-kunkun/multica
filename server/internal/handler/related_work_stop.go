package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type stopWorkRequest struct {
	Related bool `json:"related"`
}

func decodeStopWorkRequest(w http.ResponseWriter, r *http.Request) (stopWorkRequest, bool) {
	var req stopWorkRequest
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil && err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid stop request")
		return req, false
	}
	return req, true
}

// StopChatSession is the chat-level stop, including queued/deferred replies.
// Like a single-run stop, only the chat creator may interrupt its work.
func (h *Handler) StopChatSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	session, ok := h.gateChatSessionForUser(w, r, userID, ctxWorkspaceID(r.Context()), chi.URLParam(r, "sessionId"))
	if !ok {
		return
	}
	req, ok := decodeStopWorkRequest(w, r)
	if !ok {
		return
	}
	h.stopRelatedWork(w, r, "chat", session.ID, session.WorkspaceID, req.Related)
}

type workStopPlan struct {
	WorkspaceID        pgtype.UUID
	IssueIDs, ChatIDs  []pgtype.UUID
	ActorType, ActorID string
}

func (h *Handler) authorizeWorkStop(w http.ResponseWriter, r *http.Request, rootKind string, rootID, workspaceID pgtype.UUID, related bool) (*workStopPlan, bool) {
	userID := requestUserID(r)
	ws := uuidToString(workspaceID)
	issueIDs := make([]pgtype.UUID, 0)
	chatIDs := make([]pgtype.UUID, 0)
	if related {
		targets, err := h.Queries.ListRelatedWorkStopTargets(r.Context(), db.ListRelatedWorkStopTargetsParams{
			WorkspaceID: workspaceID, RootKind: rootKind, RootID: rootID,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load related work")
			return nil, false
		}
		for _, target := range targets {
			if target.Kind == "issue" {
				issueIDs = append(issueIDs, target.ID)
			} else {
				chatIDs = append(chatIDs, target.ID)
			}
		}
	} else if rootKind == "issue" {
		issueIDs = append(issueIDs, rootID)
	} else {
		chatIDs = append(chatIDs, rootID)
	}

	// Every target passes its own access/stop policy before the first write.
	// A relation is never a permission grant and never crosses a workspace.
	for _, id := range issueIDs {
		if _, ok := h.loadIssueForUser(w, r, uuidToString(id)); !ok {
			return nil, false
		}
	}
	for _, id := range chatIDs {
		session, ok := h.gateChatSessionForUser(w, r, userID, ws, uuidToString(id))
		if !ok {
			return nil, false
		}
		if uuidToString(session.CreatorID) != userID {
			writeError(w, http.StatusForbidden, "only the person who started each related chat can stop it")
			return nil, false
		}
	}
	// Runs on private agents retain the single-run stop gate. Inspect them
	// before mutations, including historical dual issue/chat-linked runs.
	tasks, err := h.Queries.ListActiveRunsForStopTargets(r.Context(), db.ListActiveRunsForStopTargetsParams{
		WorkspaceID: workspaceID, IssueIds: issueIDs, ChatSessionIds: chatIDs,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load runs")
		return nil, false
	}
	actorType, actorID := h.resolveActor(r, userID, ws)
	chatSet := map[pgtype.UUID]bool{}
	for _, id := range chatIDs {
		chatSet[id] = true
	}
	for _, task := range tasks {
		if task.ChatSessionID.Valid && !chatSet[task.ChatSessionID] {
			session, ok := h.gateChatSessionForUser(w, r, userID, ws, uuidToString(task.ChatSessionID))
			if !ok {
				return nil, false
			}
			if uuidToString(session.CreatorID) != userID {
				writeError(w, http.StatusForbidden, "not your task")
				return nil, false
			}
			chatSet[session.ID] = true
		}
		if !task.ChatSessionID.Valid {
			agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: task.AgentID, WorkspaceID: workspaceID})
			if err != nil || !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, ws) {
				writeError(w, http.StatusForbidden, "you do not have access to this agent")
				return nil, false
			}
		}
	}
	sort.Slice(chatIDs, func(i, j int) bool { return uuidToString(chatIDs[i]) < uuidToString(chatIDs[j]) })
	return &workStopPlan{WorkspaceID: workspaceID, IssueIDs: issueIDs, ChatIDs: chatIDs, ActorType: actorType, ActorID: actorID}, true
}

func (h *Handler) stopRelatedWork(w http.ResponseWriter, r *http.Request, rootKind string, rootID, workspaceID pgtype.UUID, related bool) {
	plan, ok := h.authorizeWorkStop(w, r, rootKind, rootID, workspaceID, related)
	if !ok {
		return
	}
	ws := uuidToString(workspaceID)
	issueIDs, chatIDs := plan.IssueIDs, plan.ChatIDs
	actorType, actorID := plan.ActorType, plan.ActorID
	cancelled, err := h.TaskService.StopWorkTargets(r.Context(), workspaceID, issueIDs, chatIDs, h.taskCancellationActor(r.Context(), actorType, actorID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to stop work")
		return
	}
	issueStrings, chatStrings := make([]string, 0, len(issueIDs)), make([]string, 0, len(chatIDs))
	for _, id := range issueIDs {
		issueStrings = append(issueStrings, uuidToString(id))
		current, err := h.Queries.GetIssueMetadataInWorkspace(r.Context(), db.GetIssueMetadataInWorkspaceParams{ID: id, WorkspaceID: workspaceID})
		if err == nil {
			h.publishWithSource(r.Context(), protocol.EventIssueMetadataChanged, ws, actorType, actorID, map[string]any{
				"issue_id": uuidToString(id), "metadata": parseIssueMetadata(current.Metadata), "issue_revision": current.Revision,
			})
		}
	}
	for _, id := range chatIDs {
		chatStrings = append(chatStrings, uuidToString(id))
	}
	result := map[string]any{"halted": true, "related": related, "issue_ids": issueStrings, "chat_session_ids": chatStrings, "cancelled_count": len(cancelled)}
	if rootKind == "issue" {
		result["issue_id"] = uuidToString(rootID)
	} else {
		result["chat_session_id"] = uuidToString(rootID)
	}
	writeJSON(w, http.StatusOK, result)
}

// updateIssueAndStopRelatedWork applies the explicit cancelled status and its
// related stop in one transaction; a failure never leaves a partial stop.
func (h *Handler) updateIssueAndStopRelatedWork(r *http.Request, params db.UpdateIssueParams, plan *workStopPlan) (db.Issue, error) {
	ctx := r.Context()
	tx, err := h.beginWakeupWrite(ctx)
	if err != nil {
		return db.Issue{}, err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	if err := assertIssueStatusStillActive(ctx, qtx, plan.WorkspaceID, params.Status.String); err != nil {
		return db.Issue{}, err
	}
	if _, err := h.TaskService.LockWorkStopTargets(ctx, qtx, plan.WorkspaceID, plan.IssueIDs, plan.ChatIDs); err != nil {
		return db.Issue{}, err
	}

	issue, wakeups, err := updateIssueStoppingWakeups(ctx, qtx, params)
	if err != nil {
		return issue, err
	}
	actor := h.taskCancellationActor(ctx, plan.ActorType, plan.ActorID)
	cancelled, err := h.TaskService.StopWorkTargetsInTransaction(ctx, qtx, plan.WorkspaceID, plan.IssueIDs, plan.ChatIDs, actor)
	if err != nil {
		return issue, err
	}
	issue, err = qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: params.ID, WorkspaceID: plan.WorkspaceID})
	if err != nil {
		return issue, err
	}
	if err := tx.Commit(ctx); err != nil {
		return issue, err
	}
	h.broadcastCancelledWakeups(ctx, plan.WorkspaceID, wakeups)
	h.TaskService.PublishWorkStopped(ctx, cancelled, actor)
	h.publishWorkStopGuards(r, plan)
	return issue, nil
}

func (h *Handler) publishWorkStopGuards(r *http.Request, plan *workStopPlan) {
	for _, id := range plan.IssueIDs {
		current, err := h.Queries.GetIssueMetadataInWorkspace(r.Context(), db.GetIssueMetadataInWorkspaceParams{ID: id, WorkspaceID: plan.WorkspaceID})
		if err == nil {
			h.publishWithSource(r.Context(), protocol.EventIssueMetadataChanged, uuidToString(plan.WorkspaceID), plan.ActorType, plan.ActorID, map[string]any{
				"issue_id": uuidToString(id), "metadata": parseIssueMetadata(current.Metadata), "issue_revision": current.Revision,
			})
		}
	}
}
