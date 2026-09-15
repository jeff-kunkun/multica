package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func (h *Handler) authorizeWorkspaceConfig(w http.ResponseWriter, r *http.Request) (wsID, userID string, ok bool) {
	id := workspaceIDFromURL(r, "id")
	if _, ok := parseUUIDOrBadRequest(w, id, "workspace id"); !ok {
		return "", "", false
	}
	userID, ok = requireUserID(w, r)
	if !ok {
		return "", "", false
	}
	actorType, _ := h.resolveActor(r, userID, id)
	if actorType == "agent" {
		writeError(w, http.StatusForbidden, "agents may not access workspace config transfer")
		return "", "", false
	}
	if _, ok := h.requireWorkspaceRole(w, r, id, "workspace not found", "owner", "admin"); !ok {
		return "", "", false
	}
	return id, userID, true
}

func (h *Handler) ExportWorkspaceConfig(w http.ResponseWriter, r *http.Request) {
	wsID, userID, ok := h.authorizeWorkspaceConfig(w, r)
	if !ok {
		return
	}
	wsUUID, _ := parseUUIDOrBadRequest(w, wsID, "workspace id")

	include := []string{}
	if raw := strings.TrimSpace(r.URL.Query().Get("include")); raw != "" {
		for _, p := range strings.Split(raw, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if !service.ValidEntityType(p) {
				writeErrorCode(w, http.StatusBadRequest, "invalid_include", "unknown entity type: "+p)
				return
			}
			include = append(include, p)
		}
	}
	includeArchived := r.URL.Query().Get("include_archived") == "true"

	bundle, err := service.ExportWorkspaceConfig(r.Context(), h.Queries, service.ConfigExportOptions{
		WorkspaceID:     wsUUID,
		ExportedBy:      userID,
		ServerVersion:   h.cfg.ServerVersion,
		Include:         include,
		IncludeArchived: includeArchived,
	})
	if err != nil {
		var ie *service.ImportError
		if errors.As(err, &ie) {
			writeErrorCode(w, ie.Status, ie.Code, ie.Msg)
			return
		}
		slog.Error("workspace config export failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to export workspace config")
		return
	}

	body, err := json.Marshal(bundle)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode bundle")
		return
	}
	if len(body) > service.ConfigBundleMaxBytes {
		writeErrorCode(w, http.StatusRequestEntityTooLarge, "config_bundle_too_large", "exported bundle exceeds 20 MiB")
		return
	}

	details, _ := json.Marshal(map[string]any{
		"bundle_id": bundle.BundleID,
		"stats":     bundle.Stats,
	})
	if _, err := h.Queries.CreateActivity(r.Context(), db.CreateActivityParams{
		ID:          dbid.NewV7(),
		WorkspaceID: wsUUID,
		ActorType:   pgtype.Text{String: "member", Valid: true},
		ActorID:     parseUUID(userID),
		Action:      "workspace_config_exported",
		Details:     details,
	}); err != nil {
		slog.Info("workspace config export audit write failed", append(logger.RequestAttrs(r), "error", err)...)
	}

	filename := fmt.Sprintf("multica-config-%s-%s.json", bundle.Source.Slug, time.Now().UTC().Format("20060102"))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)+1))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	_, _ = w.Write([]byte("\n"))
}

func (h *Handler) ImportWorkspaceConfig(w http.ResponseWriter, r *http.Request) {
	wsID, userID, ok := h.authorizeWorkspaceConfig(w, r)
	if !ok {
		return
	}
	wsUUID, _ := parseUUIDOrBadRequest(w, wsID, "workspace id")
	importer, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, service.ConfigBundleMaxBytes)
	var req service.ConfigImportRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeErrorCode(w, http.StatusRequestEntityTooLarge, "config_bundle_too_large", "import payload exceeds 20 MiB")
			return
		}
		writeErrorCode(w, http.StatusBadRequest, "config_bundle_invalid", "invalid request body")
		return
	}

	ws, err := h.Queries.GetWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}

	report, err := service.ImportWorkspaceConfig(r.Context(), service.ConfigImportEnv{
		Queries:    h.Queries,
		TxStarter:  h.TxStarter,
		TargetID:   wsUUID,
		TargetSlug: ws.Slug,
		ImporterID: importer,
		PublicURL:  h.cfg.PublicURL,
	}, req)
	if err != nil {
		var ie *service.ImportError
		if errors.As(err, &ie) {
			if ie.Report != nil {
				writeJSON(w, ie.Status, map[string]any{
					"error":  ie.Msg,
					"code":   ie.Code,
					"report": ie.Report,
				})
				return
			}
			writeErrorCode(w, ie.Status, ie.Code, ie.Msg)
			return
		}
		slog.Error("workspace config import failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to import workspace config")
		return
	}
	writeJSON(w, http.StatusOK, report)
}
