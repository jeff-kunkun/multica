package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/coderesolve"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// RuntimeDefaultDirResponse is a machine's default working directory: where a
// run goes when its project binds no folder (DENE-2667 PR 8). Exists is what
// the machine last reported for this path: whether the folder is there, not
// whether a run may use it (the daemon decides that per run). Null until it
// has looked.
type RuntimeDefaultDirResponse struct {
	Path   string `json:"path"`
	Exists *bool  `json:"exists"`
}

// maxDefaultDirLen bounds the stored path; real paths are far shorter.
const maxDefaultDirLen = 4096

// validateDefaultDir trims the requested path. "" clears the setting.
func validateDefaultDir(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", nil
	}
	if len(path) > maxDefaultDirLen {
		return "", errors.New("default_work_dir is too long")
	}
	if !isAbsoluteLocalPath(path) {
		return "", errors.New("default_work_dir must be an absolute path")
	}
	return path, nil
}

// setRuntimeDefaultDir stores or clears the default directory of the machine
// behind runtimeID.
func (h *Handler) setRuntimeDefaultDir(ctx context.Context, runtimeID pgtype.UUID, path string) error {
	key, err := h.Queries.GetRuntimeMachineKey(ctx, runtimeID)
	if err != nil {
		return err
	}
	if path == "" {
		return h.Queries.ClearRuntimeMachineDefaultDir(ctx, key)
	}
	return h.Queries.SetRuntimeMachineDefaultDir(ctx, db.SetRuntimeMachineDefaultDirParams{MachineKey: key, Path: path})
}

// hydrateRuntimeDefaultDirs fills default_work_dir for runtimes the viewer
// owns. The path names a folder on the owner's disk, account name included, so
// other members of the workspace do not see it.
func (h *Handler) hydrateRuntimeDefaultDirs(ctx context.Context, viewerID string, rows []AgentRuntimeResponse) {
	if viewerID == "" {
		return
	}
	ids := []pgtype.UUID{}
	for _, row := range rows {
		if row.OwnerID != nil && *row.OwnerID == viewerID {
			ids = append(ids, parseUUID(row.ID))
		}
	}
	if len(ids) == 0 {
		return
	}
	dirs, err := h.Queries.ListRuntimeMachineDefaultDirs(ctx, ids)
	if err != nil {
		slog.Error("read machine default dirs", "error", err)
		return
	}
	byID := map[string]*RuntimeDefaultDirResponse{}
	for _, d := range dirs {
		byID[uuidToString(d.RuntimeID)] = defaultDirResponse(d.Path, d.PathExists)
	}
	for i := range rows {
		if rows[i].OwnerID != nil && *rows[i].OwnerID == viewerID {
			rows[i].DefaultWorkDir = byID[rows[i].ID]
		}
	}
}

func defaultDirResponse(path string, exists pgtype.Bool) *RuntimeDefaultDirResponse {
	resp := &RuntimeDefaultDirResponse{Path: path}
	if exists.Valid {
		v := exists.Bool
		resp.Exists = &v
	}
	return resp
}

// defaultWorkDirForClaim is the machine's default directory when this run has
// no code to work in — no bound folder and no repository. A run with a
// repository keeps its Multica-managed checkout, so a default folder never
// fills up with clones.
func (h *Handler) defaultWorkDirForClaim(ctx context.Context, resp AgentTaskResponse, runtimeID pgtype.UUID) string {
	if resp.IsLeaderTask || resp.CodeDecision == nil || resp.CodeDecision.Kind() != coderesolve.KindSharedScratch {
		return ""
	}
	dir, err := h.Queries.GetRuntimeMachineDefaultDir(ctx, runtimeID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("task claim: read machine default dir failed", "runtime_id", uuidToString(runtimeID), "error", err)
		}
		return ""
	}
	return dir.Path
}

// ReportRuntimeDefaultDir is the daemon's half of the default directory: it
// reports whether the path it last looked at exists, and learns the current
// setting in the reply. A report about a path that is no longer the setting
// changes nothing.
func (h *Handler) ReportRuntimeDefaultDir(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.requireDaemonRuntimeAccess(w, r, chi.URLParam(r, "runtimeId"))
	if !ok {
		return
	}
	var req struct {
		Path   string `json:"path"`
		Exists *bool  `json:"exists"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Path != "" && req.Exists != nil {
		changed, err := h.Queries.ReportRuntimeMachineDefaultDir(r.Context(), db.ReportRuntimeMachineDefaultDirParams{
			RuntimeID:  rt.ID,
			Path:       req.Path,
			PathExists: pgtype.Bool{Bool: *req.Exists, Valid: true},
		})
		if err != nil {
			slog.Warn("store default dir report failed", "runtime_id", uuidToString(rt.ID), "error", err)
			writeError(w, http.StatusInternalServerError, "failed to store default dir report")
			return
		}
		if changed > 0 {
			h.publish(protocol.EventDaemonRegister, uuidToString(rt.WorkspaceID), "system", "", map[string]any{
				"action": "default_dir",
			})
		}
	}
	path := ""
	dir, err := h.Queries.GetRuntimeMachineDefaultDir(r.Context(), rt.ID)
	switch {
	case err == nil:
		path = dir.Path
	case !errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusInternalServerError, "failed to read default dir")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path})
}
