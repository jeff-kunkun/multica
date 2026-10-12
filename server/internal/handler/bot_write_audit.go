package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/permission"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// BotWriteAuditResponse is one row of "what a Bot did in a person's name": the
// write as the audit middleware recorded it. Route and field names only.
type BotWriteAuditResponse struct {
	ID               string   `json:"id"`
	BotID            string   `json:"bot_id"`
	BotName          string   `json:"bot_name"`
	OnBehalfOfUserID string   `json:"on_behalf_of_user_id"`
	OnBehalfOfName   string   `json:"on_behalf_of_name"`
	Method           string   `json:"method"`
	Route            string   `json:"route"`
	Family           string   `json:"family"`
	EntityID         *string  `json:"entity_id"`
	Fields           []string `json:"fields"`
	StatusCode       int32    `json:"status_code"`
	Outcome          string   `json:"outcome"` // done | blocked
	Dangerous        *string  `json:"dangerous"`
	Confirmation     *string  `json:"confirmation"`
	// Label is the sentence the UI and CLI show: Bot〈名〉代表〈主人〉.
	Label     string `json:"label"`
	CreatedAt string `json:"created_at"`
}

// ListBotWriteAudit is GET /api/workspaces/{id}/bot-writes: the Bot writes of
// this workspace, newest first. Owners and admins read all of them; anyone
// else reads the ones made in their own name.
func (h *Handler) ListBotWriteAudit(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	ws, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	member, ok := middleware.MemberFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusForbidden, "not a member of this workspace")
		return
	}
	limit := int32(50)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
			limit = int32(n)
		}
	}
	before := pgtype.Timestamptz{}
	if raw := r.URL.Query().Get("before"); raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "before must be an RFC 3339 time")
			return
		}
		before = pgtype.Timestamptz{Time: t, Valid: true}
	}
	owner := parseUUID(userID)
	role := permission.Role(member.Role)
	if role == permission.RoleOwner || role == permission.RoleAdmin {
		owner = pgtype.UUID{}
		if r.URL.Query().Get("mine") == "1" {
			owner = parseUUID(userID)
		}
	}
	rows, err := h.Queries.ListBotWriteAudit(r.Context(), db.ListBotWriteAuditParams{
		WorkspaceID: ws, Limit: limit, OwnerFilter: owner, Before: before,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list bot writes")
		return
	}
	names := map[pgtype.UUID]string{}
	out := make([]BotWriteAuditResponse, 0, len(rows))
	next := ""
	for _, row := range rows {
		name, seen := names[row.OnBehalfOfUserID]
		if !seen {
			if u, uErr := h.Queries.GetUser(r.Context(), row.OnBehalfOfUserID); uErr == nil {
				name = u.Name
			}
			names[row.OnBehalfOfUserID] = name
		}
		out = append(out, BotWriteAuditResponse{
			ID: uuidToString(row.ID), BotID: uuidToString(row.BotID), BotName: row.BotName,
			OnBehalfOfUserID: uuidToString(row.OnBehalfOfUserID), OnBehalfOfName: name,
			Method: row.Method, Route: row.Route, Family: row.Family, EntityID: uuidToPtr(row.EntityID),
			Fields: append([]string{}, row.Fields...), StatusCode: row.StatusCode, Outcome: row.Outcome,
			Dangerous: textToPtr(row.Dangerous), Confirmation: textToPtr(row.Confirmation),
			Label:     "Bot〈" + row.BotName + "〉代表〈" + name + "〉",
			CreatedAt: timestampToString(row.CreatedAt),
		})
		next = row.CreatedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	if int32(len(rows)) < limit {
		next = ""
	}
	writeScrubbedJSON(w, map[string]any{"writes": out, "next_cursor": next})
}
