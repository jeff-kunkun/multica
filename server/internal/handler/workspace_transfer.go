package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (h *Handler) loadTransferEnv(w http.ResponseWriter, r *http.Request) (service.TransferImportEnv, bool) {
	wsID, userID, ok := h.authorizeWorkspaceConfig(w, r)
	if !ok {
		return service.TransferImportEnv{}, false
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace id")
	if !ok {
		return service.TransferImportEnv{}, false
	}
	importer, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return service.TransferImportEnv{}, false
	}
	ws, err := h.Queries.GetWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return service.TransferImportEnv{}, false
	}
	return service.TransferImportEnv{
		Queries:    h.Queries,
		TxStarter:  h.TxStarter,
		TargetID:   wsUUID,
		TargetSlug: ws.Slug,
		ImporterID: importer,
		PublicURL:  h.cfg.PublicURL,
		Storage:    h.Storage,
	}, true
}

func (h *Handler) ImportWorkspaceTransferConfig(w http.ResponseWriter, r *http.Request) {
	env, ok := h.loadTransferEnv(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, service.ConfigBundleMaxBytes)
	var req service.TransferConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeErrorCode(w, http.StatusRequestEntityTooLarge, "config_bundle_too_large", "import payload exceeds 20 MiB")
			return
		}
		writeErrorCode(w, http.StatusBadRequest, "transfer_bundle_invalid", "invalid request body")
		return
	}
	report, err := service.ImportTransferConfig(r.Context(), env, req)
	if err != nil {
		h.writeTransferError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (h *Handler) ImportWorkspaceTransferConversations(w http.ResponseWriter, r *http.Request) {
	env, ok := h.loadTransferEnv(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, service.TransferConversationsMaxBytes)
	var req service.TransferConversationsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeErrorCode(w, http.StatusRequestEntityTooLarge, "transfer_bundle_too_large", "conversations payload exceeds 20 MiB")
			return
		}
		writeErrorCode(w, http.StatusBadRequest, "transfer_bundle_invalid", "invalid request body")
		return
	}
	report, err := service.ImportTransferConversations(r.Context(), env, req)
	if err != nil {
		h.writeTransferError(w, r, err)
		return
	}
	if report.Finalized {
		h.publish(protocol.EventChatSessionUpdated, uuidToString(env.TargetID), "member", uuidToString(env.ImporterID), map[string]any{
			"reason": "transfer_import_finalize",
		})
	}
	writeJSON(w, http.StatusOK, report)
}

func (h *Handler) ImportWorkspaceTransferAttachment(w http.ResponseWriter, r *http.Request) {
	env, ok := h.loadTransferEnv(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, service.TransferAttachmentMaxBytes+1<<20)
	if err := r.ParseMultipartForm(service.TransferAttachmentMaxBytes + 1<<20); err != nil {
		writeErrorCode(w, http.StatusBadRequest, "transfer_bundle_invalid", "invalid multipart form")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	rawMeta := r.FormValue("meta")
	if rawMeta == "" {
		writeErrorCode(w, http.StatusBadRequest, "transfer_bundle_invalid", "missing meta field")
		return
	}
	var meta service.TransferAttachmentMeta
	if err := json.Unmarshal([]byte(rawMeta), &meta); err != nil {
		writeErrorCode(w, http.StatusBadRequest, "transfer_bundle_invalid", "invalid meta json")
		return
	}
	var body []byte
	if file, _, err := r.FormFile("file"); err == nil {
		defer file.Close()
		body, err = io.ReadAll(io.LimitReader(file, service.TransferAttachmentMaxBytes+1))
		if err != nil {
			writeError(w, http.StatusBadRequest, "failed to read file")
			return
		}
		if int64(len(body)) > service.TransferAttachmentMaxBytes {
			writeErrorCode(w, http.StatusRequestEntityTooLarge, "transfer_bundle_too_large", "attachment exceeds 25 MiB")
			return
		}
	}
	report, err := service.ImportTransferAttachment(r.Context(), env, meta, body)
	if err != nil {
		h.writeTransferError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (h *Handler) writeTransferError(w http.ResponseWriter, r *http.Request, err error) {
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
	slog.Error("workspace transfer import failed", append(logger.RequestAttrs(r), "error", err)...)
	writeError(w, http.StatusInternalServerError, "failed to import transfer bundle")
}
