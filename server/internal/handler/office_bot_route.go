package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func officeRouteView(row db.OfficeBotRoute) map[string]any {
	var project any
	if row.ProjectID.Valid {
		project = uuidToString(row.ProjectID)
	}
	return map[string]any{"id": uuidToString(row.ID), "project_id": project, "bot_id": uuidToString(row.BotID), "channel_ref": row.ChannelRef}
}

func (h *Handler) ListOfficeBotRoutes(w http.ResponseWriter, r *http.Request) {
	ws, user, ok := h.mcpInventoryReader(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListOfficeBotRoutes(r.Context(), db.ListOfficeBotRoutesParams{WorkspaceID: ws, UserID: user})
	if err != nil {
		writeError(w, 500, "could not list Bot routes")
		return
	}
	out := []map[string]any{}
	for _, row := range rows {
		out = append(out, officeRouteView(row))
	}
	writeJSON(w, 200, out)
}

func (h *Handler) PutOfficeBotRoute(w http.ResponseWriter, r *http.Request) {
	ws, user, ok := h.mcpManager(w, r)
	if !ok {
		return
	}
	var req struct {
		ProjectID  string `json:"project_id"`
		BotID      string `json:"bot_id"`
		ChannelRef string `json:"channel_ref"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
		writeError(w, 400, "invalid request")
		return
	}
	var project pgtype.UUID
	if req.ProjectID != "" {
		project, ok = parseUUIDOrBadRequest(w, req.ProjectID, "project id")
		if !ok {
			return
		}
		if uuidToString(project) == "00000000-0000-0000-0000-000000000000" {
			writeError(w, 400, "invalid project id")
			return
		}
		if _, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{ID: project, WorkspaceID: ws}); err != nil {
			writeError(w, 404, "project not found")
			return
		}
	}
	botID, ok := parseUUIDOrBadRequest(w, req.BotID, "bot id")
	if !ok {
		return
	}
	bot, err := h.Queries.GetWakeBot(r.Context(), db.GetWakeBotParams{ID: botID, WorkspaceID: ws})
	if err != nil || bot.UserID != user || bot.DeletedAt.Valid || bot.Revoked || (bot.ExpiresAt.Valid && !bot.ExpiresAt.Time.After(time.Now())) {
		writeError(w, 404, "active owned Bot not found")
		return
	}
	req.ChannelRef = strings.TrimSpace(req.ChannelRef)
	if len(req.ChannelRef) == 0 || len([]rune(req.ChannelRef)) > 500 {
		writeError(w, 400, "channel_ref must contain 1-500 characters")
		return
	}
	row, err := h.Queries.UpsertOfficeBotRoute(r.Context(), db.UpsertOfficeBotRouteParams{WorkspaceID: ws, UserID: user, ProjectID: project, BotID: botID, ChannelRef: req.ChannelRef})
	if err != nil {
		writeError(w, 500, "could not save Bot route")
		return
	}
	writeJSON(w, 200, officeRouteView(row))
}

func (h *Handler) DeleteOfficeBotRoute(w http.ResponseWriter, r *http.Request) {
	ws, user, ok := h.mcpManager(w, r)
	if !ok {
		return
	}
	var project pgtype.UUID
	if id := r.URL.Query().Get("project_id"); id != "" {
		project, ok = parseUUIDOrBadRequest(w, id, "project id")
		if !ok {
			return
		}
	}
	_, err := h.Queries.DeleteOfficeBotRoute(r.Context(), db.DeleteOfficeBotRouteParams{WorkspaceID: ws, UserID: user, ProjectID: project})
	if err != nil {
		writeError(w, 500, "could not delete Bot route")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// officeRouteProject uses a ticket's project, otherwise the chat's primary
// project. Secondary chat projects grant context, not competing responsibility.
func (h *Handler) officeRouteProject(ctx context.Context, ev db.OfficeEvent) (pgtype.UUID, error) {
	if ev.IssueID.Valid {
		issue, err := h.Queries.GetIssue(ctx, ev.IssueID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, err
		}
		if err == nil && issue.WorkspaceID == ev.WorkspaceID && issue.ProjectID.Valid {
			return issue.ProjectID, nil
		}
	}
	if ev.ChatSessionID.Valid {
		chat, err := h.Queries.GetChatSession(ctx, ev.ChatSessionID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, err
		}
		if err == nil && chat.WorkspaceID == ev.WorkspaceID {
			return chat.ProjectID, nil
		}
	}
	return pgtype.UUID{}, nil
}

// restoreOfficeRoutes repairs undelivered gaps and re-enables the exact frozen
// destination. It never transfers a sent/uncertain relay to a different Bot.
func (w *OfficeEventWorker) restoreOfficeRoutes(ctx context.Context) int {
	n := 0
	gaps, err := w.h.Queries.ListOfficeRouteGapsRestorable(ctx, 100)
	if err != nil {
		slog.Warn("office events: route gap scan failed", "error", err)
	} else {
		for _, row := range gaps {
			ev, gap := row.OfficeEvent, row.OfficeEventGap
			project, err := w.h.officeRouteProject(ctx, ev)
			if err != nil {
				continue
			}
			route, err := w.h.Queries.ResolveOfficeBotRoute(ctx, db.ResolveOfficeBotRouteParams{WorkspaceID: gap.WorkspaceID, UserID: gap.UserID, ProjectID: project})
			if err != nil {
				continue
			}
			visible, err := w.h.officeEventVisibleErr(ctx, ev, gap.UserID)
			if err != nil {
				continue
			}
			if visible {
				if err := w.h.Queries.CreateOfficeEventDelivery(ctx, db.CreateOfficeEventDeliveryParams{EventID: ev.EventID, WorkspaceID: ev.WorkspaceID, BotID: route.BotID, TargetUserID: gap.UserID, Reason: "owner", MustRelay: ev.MustRelay, Status: "pending", RelayTarget: route.ChannelRef}); err != nil {
					continue
				}
			}
			if err := w.h.Queries.ResolveOfficeGap(ctx, db.ResolveOfficeGapParams{ID: gap.ID, BotID: route.BotID}); err == nil {
				n++
			}
		}
	}
	blocked, err := w.h.Queries.ListOfficeRouteDeliveriesRestorable(ctx, db.ListOfficeRouteDeliveriesRestorableParams{BlockedReason: officeBlockedRoute, Lim: 100})
	if err != nil {
		slog.Warn("office events: route restore scan failed", "error", err)
	} else {
		for _, d := range blocked {
			if err := w.h.Queries.RequeueOfficeDelivery(ctx, d.ID); err == nil {
				n++
			}
		}
	}
	return n
}
