package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
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

// BindWorkspaceTransferRuntimes applies the runtimes a human picked on the
// migration card. The automatic rule handles the unambiguous case; this is the
// multi-candidate path, and it answers per binding so one bad pair cannot
// discard the rest of the user's picks (DENE-364).
func (h *Handler) BindWorkspaceTransferRuntimes(w http.ResponseWriter, r *http.Request) {
	env, ok := h.loadTransferEnv(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, service.ConfigBundleMaxBytes)
	var req service.TransferBindRuntimesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeErrorCode(w, http.StatusRequestEntityTooLarge, "config_bundle_too_large", "bind payload exceeds 20 MiB")
			return
		}
		writeErrorCode(w, http.StatusBadRequest, "transfer_bundle_invalid", "invalid request body")
		return
	}
	// loadTransferEnv already proved owner/admin membership; keep the member row
	// for the per-runtime visibility rule below.
	member, ok := h.workspaceMember(w, r, uuidToString(env.TargetID))
	if !ok {
		return
	}
	report := &service.TransferBindRuntimesReport{
		Applied: true,
		Results: make([]service.TransferRuntimeBindResult, 0, len(req.Bindings)),
	}
	for _, binding := range req.Bindings {
		result := h.bindTransferRuntime(r.Context(), env, member, binding)
		if result.Ok {
			report.Bound++
		} else {
			report.Failed++
		}
		report.Results = append(report.Results, result)
	}
	writeJSON(w, http.StatusOK, report)
}

// bindTransferRuntime resolves and authorizes one agent→runtime pair, then
// writes it through the shared transfer bind. Every rejection is a result row,
// not a request error: the ids came from the client, so one stale pick must not
// fail the batch.
func (h *Handler) bindTransferRuntime(ctx context.Context, env service.TransferImportEnv, member db.Member, binding service.TransferRuntimeBinding) service.TransferRuntimeBindResult {
	result := service.TransferRuntimeBindResult{AgentID: binding.AgentID, RuntimeID: binding.RuntimeID}
	agentUUID, err := util.ParseUUID(binding.AgentID)
	if err != nil {
		return rejectTransferBind(result, service.TransferBindReasonAgentUnknown, "agent_id is not a uuid")
	}
	existing, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          agentUUID,
		WorkspaceID: env.TargetID,
	})
	if err != nil {
		return rejectTransferBind(result, service.TransferBindReasonAgentUnknown, "agent is not in this workspace")
	}
	result.AgentName = existing.Name
	runtimeUUID, err := util.ParseUUID(binding.RuntimeID)
	if err != nil {
		return rejectTransferBind(result, service.TransferBindReasonRuntimeUnknown, "runtime_id is not a uuid")
	}
	runtime, err := h.Queries.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{
		ID:          runtimeUUID,
		WorkspaceID: env.TargetID,
	})
	if err != nil {
		return rejectTransferBind(result, service.TransferBindReasonRuntimeUnknown, "runtime is not in this workspace")
	}
	if code := runtimeBindDeniedReason(member, runtime); code != "" {
		return rejectTransferBind(result, code, "this runtime is not available to you")
	}
	if err := service.BindTransferAgentRuntime(ctx, env, existing, runtime); err != nil {
		return rejectTransferBind(result, service.TransferBindReasonBindFailed, err.Error())
	}
	result.RuntimeName = runtimeDisplayName(runtime)
	result.Ok = true
	return result
}

func rejectTransferBind(result service.TransferRuntimeBindResult, code, reason string) service.TransferRuntimeBindResult {
	result.Ok = false
	result.ReasonCode = code
	result.Reason = reason
	return result
}

func runtimeDisplayName(rt db.AgentRuntime) string {
	if rt.CustomName.Valid && rt.CustomName.String != "" {
		return rt.CustomName.String
	}
	return rt.Name
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
