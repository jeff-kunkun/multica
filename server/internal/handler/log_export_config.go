package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/logexport"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The log repository is the workspace-level Git repository that reported log
// bundles are committed to, so an issue comment carries a link instead of a
// large attachment.
//
// It lives under `log_export` in `workspace.settings` and follows the same
// three rules as the routing gateway key (routing_secrets.go): the token is
// sealed before it is stored, the sealed value never leaves the server, and —
// because a client therefore cannot echo it back — a generic settings write
// carries the stored block forward instead of taking the client's copy.
//
// Unlike the routing key, the block is written through its own endpoint and
// its own single-key query, so the generic settings write never has to merge
// it: it only has to not drop it.

const logExportSettingsKey = "log_export"

// logExportStored is the block as persisted.
type logExportStored struct {
	RepoURL  string `json:"repo_url"`
	Branch   string `json:"branch,omitempty"`
	TokenEnc string `json:"token_enc,omitempty"`
}

// LogExportConfigResponse is what a client sees: never the token, only
// whether one is stored.
type LogExportConfigResponse struct {
	RepoURL  string `json:"repo_url"`
	Branch   string `json:"branch"`
	HasToken bool   `json:"has_token"`
	// TokenStorable is false on a deployment with no secret to seal with;
	// the settings form says so instead of failing on save.
	TokenStorable bool `json:"token_storable"`
}

func storedLogExport(settings []byte) (logExportStored, bool) {
	if len(settings) == 0 {
		return logExportStored{}, false
	}
	var envelope struct {
		LogExport *logExportStored `json:"log_export"`
	}
	if err := json.Unmarshal(settings, &envelope); err != nil || envelope.LogExport == nil {
		return logExportStored{}, false
	}
	return *envelope.LogExport, true
}

// redactLogExportSettings replaces the stored block with its client-safe form
// on the way out. Nothing the caller owns is mutated.
func redactLogExportSettings(settings any) any {
	root, ok := settings.(map[string]any)
	if !ok {
		return settings
	}
	block, ok := root[logExportSettingsKey].(map[string]any)
	if !ok {
		return settings
	}
	clean := make(map[string]any, len(block))
	for k, v := range block {
		if k == "token_enc" || k == "token" {
			continue
		}
		clean[k] = v
	}
	enc, _ := block["token_enc"].(string)
	clean["has_token"] = strings.TrimSpace(enc) != ""
	out := make(map[string]any, len(root))
	for k, v := range root {
		out[k] = v
	}
	out[logExportSettingsKey] = clean
	return out
}

// carryLogExportSettings makes a generic settings write keep the stored block.
// Whatever the client sent under the key is discarded: the dedicated endpoint
// is the only writer.
func carryLogExportSettings(incoming any, stored []byte) any {
	root, ok := incoming.(map[string]any)
	if !ok {
		return incoming
	}
	out := make(map[string]any, len(root)+1)
	for k, v := range root {
		if k == logExportSettingsKey {
			continue
		}
		out[k] = v
	}
	if len(stored) > 0 {
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(stored, &envelope); err == nil {
			if raw, present := envelope[logExportSettingsKey]; present {
				var block any
				if json.Unmarshal(raw, &block) == nil {
					out[logExportSettingsKey] = block
				}
			}
		}
	}
	return out
}

// logExportTarget resolves the workspace's push target. ok=false means no
// repository is configured; a configured repository whose token cannot be
// opened still returns ok=true with an empty token, so the push fails loudly
// and the report falls back, rather than silently behaving as unconfigured.
func (h *Handler) logExportTarget(ws db.Workspace) (logexport.RepoTarget, bool) {
	stored, ok := storedLogExport(ws.Settings)
	if !ok || strings.TrimSpace(stored.RepoURL) == "" {
		return logexport.RepoTarget{}, false
	}
	return logexport.RepoTarget{
		RepoURL: stored.RepoURL,
		Branch:  stored.Branch,
		Token:   h.openRoutingKey(stored.TokenEnc),
	}, true
}

func (h *Handler) logExportConfigResponse(stored logExportStored) LogExportConfigResponse {
	return LogExportConfigResponse{
		RepoURL:       stored.RepoURL,
		Branch:        stored.Branch,
		HasToken:      strings.TrimSpace(stored.TokenEnc) != "",
		TokenStorable: h.RoutingSecrets != nil,
	}
}

// GetLogExportConfig — GET /api/workspaces/{id}/log-export-config
func (h *Handler) GetLogExportConfig(w http.ResponseWriter, r *http.Request) {
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace_id")
	if !ok {
		return
	}
	ws, err := h.Queries.GetWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	stored, _ := storedLogExport(ws.Settings)
	writeJSON(w, http.StatusOK, h.logExportConfigResponse(stored))
}

// UpdateLogExportConfig — PUT /api/workspaces/{id}/log-export-config
//
// `token` is write-only: absent keeps the stored one, empty clears it, a
// value replaces it. An empty `repo_url` removes the whole block.
func (h *Handler) UpdateLogExportConfig(w http.ResponseWriter, r *http.Request) {
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace_id")
	if !ok {
		return
	}
	var req struct {
		RepoURL string  `json:"repo_url"`
		Branch  string  `json:"branch"`
		Token   *string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ws, err := h.Queries.GetWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}

	repoURL := strings.TrimSpace(req.RepoURL)
	if repoURL == "" {
		if err := h.Queries.DeleteWorkspaceSettingsKey(r.Context(), db.DeleteWorkspaceSettingsKeyParams{ID: wsUUID, Key: logExportSettingsKey}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save log repository")
			return
		}
		writeJSON(w, http.StatusOK, h.logExportConfigResponse(logExportStored{}))
		return
	}
	if _, _, err := logexport.ParseGitHubRepo(repoURL); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	prev, _ := storedLogExport(ws.Settings)
	next := logExportStored{RepoURL: repoURL, Branch: strings.TrimSpace(req.Branch), TokenEnc: prev.TokenEnc}
	if req.Token != nil {
		next.TokenEnc = ""
		if plain := strings.TrimSpace(*req.Token); plain != "" {
			sealed, sealedOK := h.sealRoutingKey(plain)
			if !sealedOK {
				writeError(w, http.StatusServiceUnavailable,
					"this deployment cannot store a log repository token (no MULTICA_ROUTING_SECRET_KEY or JWT_SECRET)")
				return
			}
			next.TokenEnc = sealed
		}
	}
	value, err := json.Marshal(next)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save log repository")
		return
	}
	if err := h.Queries.SetWorkspaceSettingsKey(r.Context(), db.SetWorkspaceSettingsKeyParams{ID: wsUUID, Key: logExportSettingsKey, Value: value}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save log repository")
		return
	}
	writeJSON(w, http.StatusOK, h.logExportConfigResponse(next))
}
