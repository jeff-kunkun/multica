package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Timed agent access passes (DENE-808). The agent owner hands a member a pass;
// invokeAgentDecision honours it like a member target until it expires or is
// revoked.

const (
	accessPassMaxDuration = 30 * 24 * time.Hour

	// Inbox types written by the retired doorbell request flow. Old items
	// still exist and stay personal notices.
	inboxTypeAgentAccessRequest  = "agent_access_request"
	inboxTypeAgentAccessApproved = "agent_access_approved"
	inboxTypeAgentAccessDeclined = "agent_access_declined"
)

// inboxItemIsPersonalNotice reports whether an inbox item is addressed to the
// recipient personally rather than being a projection of an issue they follow.
// Doorbell rings and receipts are about the recipient's own agent (or their
// own request), so the issue-visibility filter that scopes ordinary issue
// notifications (DENE-698) must not swallow them: the owner has to hear the
// bell even when the ticket it rang on is not shared with them. The item only
// carries the saved summary and metadata, never the issue body. Keep in step
// with the type list in CountUnreadInbox / CountUnreadInboxByWorkspace.
func inboxItemIsPersonalNotice(itemType string) bool {
	switch itemType {
	case inboxTypeAgentAccessRequest, inboxTypeAgentAccessApproved, inboxTypeAgentAccessDeclined:
		return true
	}
	return false
}

// AgentAccessPassResponse is the wire shape of one timed pass.
type AgentAccessPassResponse struct {
	ID            string  `json:"id"`
	WorkspaceID   string  `json:"workspace_id"`
	AgentID       string  `json:"agent_id"`
	UserID        string  `json:"user_id"`
	UserName      string  `json:"user_name"`
	UserEmail     string  `json:"user_email"`
	UserAvatarURL *string `json:"user_avatar_url"`
	GrantedBy     string  `json:"granted_by"`
	ExpiresAt     string  `json:"expires_at"`
	RevokedAt     *string `json:"revoked_at"`
	RequestID     *string `json:"request_id"`
	CreatedAt     string  `json:"created_at"`
	Active        bool    `json:"active"`
}

func accessPassFromRow(p db.ListAgentAccessPassesRow, now time.Time) AgentAccessPassResponse {
	return AgentAccessPassResponse{
		ID:            uuidToString(p.ID),
		WorkspaceID:   uuidToString(p.WorkspaceID),
		AgentID:       uuidToString(p.AgentID),
		UserID:        uuidToString(p.UserID),
		UserName:      p.UserName,
		UserEmail:     p.UserEmail,
		UserAvatarURL: textToPtr(p.UserAvatarUrl),
		GrantedBy:     uuidToString(p.GrantedBy),
		ExpiresAt:     timestampToString(p.ExpiresAt),
		RevokedAt:     timestampToPtr(p.RevokedAt),
		RequestID:     uuidToPtr(p.RequestID),
		CreatedAt:     timestampToString(p.CreatedAt),
		Active:        !p.RevokedAt.Valid && p.ExpiresAt.Valid && p.ExpiresAt.Time.After(now),
	}
}

func accessPassFromPlainRow(p db.AgentAccessPass, userName, userEmail string, now time.Time) AgentAccessPassResponse {
	return accessPassFromRow(db.ListAgentAccessPassesRow{
		ID: p.ID, WorkspaceID: p.WorkspaceID, AgentID: p.AgentID, UserID: p.UserID,
		GrantedBy: p.GrantedBy, ExpiresAt: p.ExpiresAt, RevokedAt: p.RevokedAt,
		RequestID: p.RequestID, CreatedAt: p.CreatedAt, UserName: userName, UserEmail: userEmail,
	}, now)
}

// resolvePassExpiry turns the two accepted expiry spellings into a deadline.
// Returns zero time when neither was given.
func resolvePassExpiry(expiresAt *string, durationMinutes *int, now time.Time) (time.Time, string) {
	switch {
	case expiresAt != nil && *expiresAt != "":
		t, err := time.Parse(time.RFC3339, *expiresAt)
		if err != nil {
			return time.Time{}, "pass_expires_at must be an RFC3339 timestamp"
		}
		if !t.After(now) {
			return time.Time{}, "pass_expires_at must be in the future"
		}
		if t.Sub(now) > accessPassMaxDuration {
			return time.Time{}, "a pass may last at most 30 days"
		}
		return t, ""
	case durationMinutes != nil:
		if *durationMinutes <= 0 {
			return time.Time{}, "pass_duration_minutes must be positive"
		}
		d := time.Duration(*durationMinutes) * time.Minute
		if d > accessPassMaxDuration {
			return time.Time{}, "a pass may last at most 30 days"
		}
		return now.Add(d), ""
	}
	return time.Time{}, ""
}

// loadOwnedAgentForPasses resolves /api/agents/{id} for pass management:
// owner only.
func (h *Handler) loadOwnedAgentForPasses(w http.ResponseWriter, r *http.Request) (db.Agent, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return db.Agent{}, false
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "agent id")
	if !ok {
		return db.Agent{}, false
	}
	agent, err := h.Queries.GetAgent(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return db.Agent{}, false
	}
	if uuidToString(agent.OwnerID) != userID {
		writeError(w, http.StatusForbidden, "only the agent owner can manage access passes")
		return db.Agent{}, false
	}
	return agent, true
}

// ListAgentAccessPasses returns an agent's active passes and recent history.
func (h *Handler) ListAgentAccessPasses(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadOwnedAgentForPasses(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListAgentAccessPasses(r.Context(), agent.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list access passes")
		return
	}
	now := time.Now()
	out := make([]AgentAccessPassResponse, 0, len(rows))
	for _, p := range rows {
		out = append(out, accessPassFromRow(p, now))
	}
	writeJSON(w, http.StatusOK, out)
}

type createAccessPassBody struct {
	UserID          string  `json:"user_id"`
	ExpiresAt       *string `json:"expires_at"`
	DurationMinutes *int    `json:"duration_minutes"`
}

// CreateAgentAccessPass issues a pass to one workspace member.
func (h *Handler) CreateAgentAccessPass(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadOwnedAgentForPasses(w, r)
	if !ok {
		return
	}
	var body createAccessPassBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, body.UserID, "user_id")
	if !ok {
		return
	}
	if uuidToString(agent.OwnerID) == body.UserID {
		writeError(w, http.StatusBadRequest, "the owner does not need a pass")
		return
	}
	member, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{UserID: userUUID, WorkspaceID: agent.WorkspaceID})
	if err != nil {
		writeError(w, http.StatusBadRequest, "user_id does not refer to a member of this workspace")
		return
	}
	now := time.Now()
	expiry, msg := resolvePassExpiry(body.ExpiresAt, body.DurationMinutes, now)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if expiry.IsZero() {
		writeError(w, http.StatusBadRequest, "expires_at or duration_minutes is required")
		return
	}
	created, err := h.Queries.CreateAgentAccessPass(r.Context(), db.CreateAgentAccessPassParams{
		WorkspaceID: agent.WorkspaceID, AgentID: agent.ID, UserID: userUUID, GrantedBy: agent.OwnerID,
		ExpiresAt: pgtype.Timestamptz{Time: expiry, Valid: true},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue access pass")
		return
	}
	name, email := "", ""
	if u, err := h.Queries.GetUser(r.Context(), member.UserID); err == nil {
		name, email = u.Name, u.Email
	}
	writeJSON(w, http.StatusCreated, accessPassFromPlainRow(created, name, email, now))
}

// RevokeAgentAccessPass ends a pass immediately.
func (h *Handler) RevokeAgentAccessPass(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadOwnedAgentForPasses(w, r)
	if !ok {
		return
	}
	passID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "passId"), "pass id")
	if !ok {
		return
	}
	pass, err := h.Queries.GetAgentAccessPass(r.Context(), passID)
	if err != nil || uuidToString(pass.AgentID) != uuidToString(agent.ID) {
		writeError(w, http.StatusNotFound, "access pass not found")
		return
	}
	if pass.RevokedAt.Valid {
		writeJSON(w, http.StatusOK, accessPassFromPlainRow(pass, "", "", time.Now()))
		return
	}
	revoked, err := h.Queries.RevokeAgentAccessPass(r.Context(), passID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke access pass")
		return
	}
	writeJSON(w, http.StatusOK, accessPassFromPlainRow(revoked, "", "", time.Now()))
}
