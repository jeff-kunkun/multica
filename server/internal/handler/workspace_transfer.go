package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
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
		Queries:      h.Queries,
		TxStarter:    h.TxStarter,
		TargetID:     wsUUID,
		TargetSlug:   ws.Slug,
		ImporterID:   importer,
		PublicURL:    h.cfg.PublicURL,
		Storage:      h.Storage,
		Entitlements: h.Entitlements,
	}, true
}

// ImportWorkspaceTransferIssues takes one V3 task shard: issue rows, their
// comments, and the relation rows (labels, reactions) that hang off them.
//
// It writes through the dedicated transfer statements only, so importing
// history never allocates a number, publishes an issue event, or enqueues a
// task. The only broadcast is the single workspace-level invalidation at
// finalize, which makes clients refetch the issue list.
func (h *Handler) ImportWorkspaceTransferIssues(w http.ResponseWriter, r *http.Request) {
	env, ok := h.loadTransferEnv(w, r)
	if !ok {
		return
	}
	body, err := openTransferBody(w, r, service.TransferConversationsMaxBytes)
	if err != nil {
		writeTransferDecodeError(w, err, service.TransferConversationsMaxBytes, "transfer_bundle_too_large")
		return
	}
	defer body.Close()
	var req service.TransferIssuesRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		if !writeTransferDecodeError(w, err, service.TransferConversationsMaxBytes, "transfer_bundle_too_large") {
			writeErrorCode(w, http.StatusBadRequest, "transfer_bundle_invalid", "invalid request body")
		}
		return
	}
	report, err := service.ImportTransferIssues(r.Context(), env, req)
	if err != nil {
		h.writeTransferError(w, r, err)
		return
	}
	if report.Finalized {
		if ws, werr := h.Queries.GetWorkspace(r.Context(), env.TargetID); werr == nil {
			h.publish(protocol.EventWorkspaceUpdated, uuidToString(env.TargetID), "member", uuidToString(env.ImporterID), map[string]any{
				"workspace": h.workspaceToResponse(ws),
			})
		}
	}
	writeJSON(w, http.StatusOK, report)
}

func (h *Handler) ImportWorkspaceTransferConfig(w http.ResponseWriter, r *http.Request) {
	env, ok := h.loadTransferEnv(w, r)
	if !ok {
		return
	}
	body, err := openTransferBody(w, r, service.ConfigBundleMaxBytes)
	if err != nil {
		writeTransferDecodeError(w, err, service.ConfigBundleMaxBytes, "config_bundle_too_large")
		return
	}
	defer body.Close()
	var req service.TransferConfigRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		if !writeTransferDecodeError(w, err, service.ConfigBundleMaxBytes, "config_bundle_too_large") {
			writeErrorCode(w, http.StatusBadRequest, "transfer_bundle_invalid", "invalid request body")
		}
		return
	}
	report, err := service.ImportTransferConfig(r.Context(), env, req)
	if err != nil {
		h.writeTransferError(w, r, err)
		return
	}
	// Auto-bind runs after the config import committed: the agents exist by
	// now, and a binding failure must not roll back a successful import.
	// A dry run only plans, so the preview shows the same rows as the apply.
	if req.DryRun != nil && !*req.DryRun && req.Options.AutoBindRuntimesEnabled() {
		h.applyTransferRuntimeBindings(w, r, env, report)
	}
	writeJSON(w, http.StatusOK, report)
}

// BindWorkspaceTransferRuntimes applies the bindings the migration card
// collected for the agents the auto-bind rule left alone (several candidates,
// or the switch turned off). Each line is answered independently so one bad
// pick does not discard the rest.
func (h *Handler) BindWorkspaceTransferRuntimes(w http.ResponseWriter, r *http.Request) {
	env, ok := h.loadTransferEnv(w, r)
	if !ok {
		return
	}
	var req service.TransferBindRuntimesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorCode(w, http.StatusBadRequest, "transfer_bundle_invalid", "invalid request body")
		return
	}
	if len(req.Bindings) == 0 {
		writeErrorCode(w, http.StatusBadRequest, "transfer_bundle_invalid", "bindings is required")
		return
	}
	if len(req.Bindings) > transferBindRuntimesLimit {
		writeErrorCode(w, http.StatusRequestEntityTooLarge, "transfer_bundle_too_large", "too many bindings in one request")
		return
	}

	wsID := uuidToString(env.TargetID)
	member, ok := h.workspaceMember(w, r, wsID)
	if !ok {
		return
	}

	report := service.TransferBindRuntimesReport{Applied: true, Bindings: []service.TransferRuntimeBindingOutcome{}}
	for _, binding := range req.Bindings {
		outcome := service.TransferRuntimeBindingOutcome{AgentID: binding.AgentID, RuntimeID: binding.RuntimeID}
		agentUUID, agentErr := util.ParseUUID(binding.AgentID)
		runtimeUUID, runtimeErr := util.ParseUUID(binding.RuntimeID)
		if agentErr != nil || runtimeErr != nil {
			outcome.ErrorCode = "invalid_id"
			outcome.Error = "agent_id and runtime_id must be UUIDs"
			report.Failed++
			report.Bindings = append(report.Bindings, outcome)
			continue
		}
		name, runtimeName, code, msg := h.bindTransferAgentRuntime(r, member, env.TargetID, agentUUID, runtimeUUID)
		outcome.AgentName = name
		outcome.RuntimeName = runtimeName
		if code != "" {
			outcome.ErrorCode = code
			outcome.Error = msg
			report.Failed++
			report.Bindings = append(report.Bindings, outcome)
			continue
		}
		outcome.Bound = true
		report.Bound++
		report.Bindings = append(report.Bindings, outcome)
	}
	writeJSON(w, http.StatusOK, report)
}

// transferBindRuntimesLimit caps one binding request. A workspace has far fewer
// agents than this; the cap only stops a hostile payload from turning into an
// unbounded number of writes.
const transferBindRuntimesLimit = 500

// applyTransferRuntimeBindings writes the bindings the plan resolved to exactly
// one candidate. Several candidates stay pending — the rule never guesses — and
// a failed write leaves the row pending with the reason attached so the card
// can offer the picker instead of hiding the agent.
func (h *Handler) applyTransferRuntimeBindings(w http.ResponseWriter, r *http.Request, env service.TransferImportEnv, report *service.TransferConfigReport) {
	if report == nil {
		return
	}
	wsID := uuidToString(env.TargetID)
	member, ok := h.workspaceMember(w, r, wsID)
	if !ok {
		return
	}
	for i := range report.RuntimesToBind {
		bind := &report.RuntimesToBind[i]
		if bind.Status != service.RuntimeBindPending || len(bind.Candidates) != 1 {
			continue
		}
		agentUUID, agentErr := util.ParseUUID(bind.AgentTargetID)
		runtimeUUID, runtimeErr := util.ParseUUID(bind.Candidates[0].ID)
		if agentErr != nil || runtimeErr != nil {
			continue
		}
		_, runtimeName, code, msg := h.bindTransferAgentRuntime(r, member, env.TargetID, agentUUID, runtimeUUID)
		if code != "" {
			bind.ReasonCode = service.RuntimeBindReasonBindFailed
			bind.Reason = msg
			continue
		}
		bind.Status = service.RuntimeBindBound
		bind.BoundRuntimeID = bind.Candidates[0].ID
		bind.BoundRuntimeName = runtimeName
	}
}

// bindTransferAgentRuntime writes one agent → runtime pointer. It reuses the
// agent editor's own gates rather than a transfer-specific shortcut: the agent
// must live in the target workspace and be manageable by the caller, the
// runtime must belong to the workspace, and a private runtime may only be used
// by its owner (canUseRuntimeForAgent). A migration therefore cannot bind
// anything the UI could not bind by hand.
//
// It returns an empty code on success, plus the two display names for the
// report.
func (h *Handler) bindTransferAgentRuntime(r *http.Request, member db.Member, wsUUID pgtype.UUID, agentUUID, runtimeUUID pgtype.UUID) (agentName, runtimeName, code, msg string) {
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil || agent.Kind != "user" {
		return "", "", "agent_not_found", "agent not found in this workspace"
	}
	if !roleAllowed(member.Role, "owner", "admin") && uuidToString(agent.OwnerID) != uuidToString(member.UserID) {
		return agent.Name, "", "forbidden", "only the agent owner can bind this agent to a runtime"
	}
	runtime, err := h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{
		ID:          runtimeUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		return agent.Name, "", "runtime_not_found", "runtime not found in this workspace"
	}
	if !canUseRuntimeForAgent(member, runtime) {
		return agent.Name, runtimeDisplayName(runtime), "runtime_private", "this runtime is private; only its owner can bind agents to it"
	}
	if _, err := h.Queries.UpdateAgent(r.Context(), db.UpdateAgentParams{
		ID:          agent.ID,
		RuntimeID:   runtime.ID,
		RuntimeMode: pgtype.Text{String: runtime.RuntimeMode, Valid: true},
	}); err != nil {
		slog.Warn("transfer bind runtime failed", append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(agent.ID))...)
		return agent.Name, runtimeDisplayName(runtime), "bind_failed", "failed to bind the agent to this runtime"
	}
	h.publish(protocol.EventAgentStatus, uuidToString(wsUUID), "member", requestUserID(r), map[string]any{
		"reason": "transfer_runtime_bind",
	})
	return agent.Name, runtimeDisplayName(runtime), "", ""
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
	body, err := openTransferBody(w, r, service.TransferConversationsMaxBytes)
	if err != nil {
		writeTransferDecodeError(w, err, service.TransferConversationsMaxBytes, "transfer_bundle_too_large")
		return
	}
	defer body.Close()
	var req service.TransferConversationsRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		if !writeTransferDecodeError(w, err, service.TransferConversationsMaxBytes, "transfer_bundle_too_large") {
			writeErrorCode(w, http.StatusBadRequest, "transfer_bundle_invalid", "invalid request body")
		}
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
	// The multipart body may arrive gzip-compressed like the JSON ones; the
	// decoder has to be in place before ParseMultipartForm reads r.Body.
	attachmentBody, err := openTransferBody(w, r, service.TransferAttachmentMaxBytes+1<<20)
	if err != nil {
		writeTransferDecodeError(w, err, service.TransferAttachmentMaxBytes+1<<20, "transfer_bundle_too_large")
		return
	}
	defer attachmentBody.Close()
	r.Body = attachmentBody
	if err := r.ParseMultipartForm(service.TransferAttachmentMaxBytes + 1<<20); err != nil {
		if !writeTransferDecodeError(w, err, service.TransferAttachmentMaxBytes+1<<20, "transfer_bundle_too_large") {
			writeErrorCode(w, http.StatusBadRequest, "transfer_bundle_invalid", "invalid multipart form")
		}
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

// ImportWorkspaceTransferAttachmentStatus answers the resume question for a
// whole bundle in one round trip: for each (source_id, sha256) the client
// names, whether the attachment row already exists and how many bytes of the
// blob the target already holds.
func (h *Handler) ImportWorkspaceTransferAttachmentStatus(w http.ResponseWriter, r *http.Request) {
	env, ok := h.loadTransferEnv(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, service.TransferConversationsMaxBytes)
	var req service.TransferAttachmentStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeErrorCode(w, http.StatusRequestEntityTooLarge, "transfer_bundle_too_large", "attachment status payload is too large")
			return
		}
		writeErrorCode(w, http.StatusBadRequest, "transfer_bundle_invalid", "invalid request body")
		return
	}
	report, err := service.TransferAttachmentUploadStatuses(r.Context(), env, req.Entries)
	if err != nil {
		h.writeTransferError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// ImportWorkspaceTransferAttachmentChunk stages one slice of a blob. The
// request is multipart with the same `meta` field the one-shot endpoint takes,
// plus sha256 / offset / total_bytes and the slice as `file`.
func (h *Handler) ImportWorkspaceTransferAttachmentChunk(w http.ResponseWriter, r *http.Request) {
	env, ok := h.loadTransferEnv(w, r)
	if !ok {
		return
	}
	limit := int64(service.TransferAttachmentChunkMaxBytes) + 1<<20
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := r.ParseMultipartForm(limit); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeErrorCode(w, http.StatusRequestEntityTooLarge, "transfer_bundle_too_large",
				fmt.Sprintf("attachment chunk exceeds %d bytes", service.TransferAttachmentChunkMaxBytes))
			return
		}
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
	offset, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("offset")), 10, 64)
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, "transfer_attachment_offset_invalid", "offset must be an integer")
		return
	}
	total, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("total_bytes")), 10, 64)
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, "transfer_attachment_total_invalid", "total_bytes must be an integer")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, "transfer_attachment_chunk_empty", "missing file field")
		return
	}
	defer file.Close()
	chunk, err := io.ReadAll(io.LimitReader(file, int64(service.TransferAttachmentChunkMaxBytes)+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read chunk")
		return
	}
	report, err := service.StageTransferAttachmentChunk(r.Context(), env, service.TransferAttachmentChunkRequest{
		SHA256:     r.FormValue("sha256"),
		Offset:     offset,
		TotalBytes: total,
		Meta:       meta,
		Chunk:      chunk,
	})
	if err != nil {
		h.writeTransferError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// ImportWorkspaceTransferAttachmentCommit assembles a fully staged blob,
// verifies it against its sha256, and writes the attachment row.
func (h *Handler) ImportWorkspaceTransferAttachmentCommit(w http.ResponseWriter, r *http.Request) {
	env, ok := h.loadTransferEnv(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, service.TransferConversationsMaxBytes)
	var req struct {
		SHA256 string `json:"sha256"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorCode(w, http.StatusBadRequest, "transfer_bundle_invalid", "invalid request body")
		return
	}
	report, err := service.CommitTransferAttachmentUpload(r.Context(), env, req.SHA256)
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
