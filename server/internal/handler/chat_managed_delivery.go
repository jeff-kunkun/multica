package handler

import (
	"errors"
	"net/http"
	"strings"

	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/workspacelink"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type ManagedChatDeliverySource struct {
	OriginatorID        string `json:"originator_id"`
	OriginatorName      string `json:"originator_name"`
	ViewerWorkspaceID   string `json:"viewer_workspace_id"`
	ViewerWorkspaceName string `json:"viewer_workspace_name"`
	AgentID             string `json:"agent_id"`
	AgentName           string `json:"agent_name"`
}

// A managed chat send is limited to the originator's own source chat. It is
// queued external text, never a local member utterance or an inline steering
// instruction. ManagedMiddleware records the normal managed_write audit.
func (h *Handler) sendManagedChatMessage(w http.ResponseWriter, r *http.Request, session db.ChatSession, grant workspacelink.ManagedGrant, req SendChatMessageRequest) {
	deny := func() { writeWorkspaceLinkError(w, workspacelink.ErrManagedDenied) }
	if session.CreatorID != grant.Originator || session.WorkspaceID != grant.Source.ID || session.Status != "active" {
		deny()
		return
	}
	if len(req.AttachmentIDs) > 0 || (req.Mode != "" && req.Mode != sendModeQueue) {
		writeError(w, http.StatusBadRequest, "managed chat sends support text queued as a new turn; omit attachments and use mode queue")
		return
	}
	if strings.TrimSpace(req.Content) == "" || len(req.Content) > 16384 {
		writeError(w, http.StatusBadRequest, "content must be non-empty text of at most 16384 bytes")
		return
	}
	agent, err := h.Queries.GetAgent(r.Context(), session.AgentID)
	if err != nil || agent.ArchivedAt.Valid || !agent.WorkEnabled || !agent.RuntimeID.Valid {
		deny()
		return
	}
	if verdict, err := service.AgentReadiness(r.Context(), h.runtimeLookup(obsmetrics.RuntimeLookupSourceChat), agent); err == nil && verdict.Blocked() {
		deny()
		return
	}
	originator := uuidToString(grant.Originator)
	if !h.canInvokeAgent(r.Context(), agent, "member", originator, originator, uuidToString(session.WorkspaceID)) {
		deny()
		return
	}
	user, err := h.Queries.GetUser(r.Context(), grant.Originator)
	if err != nil {
		deny()
		return
	}
	content := strings.Join(strings.Fields(user.Name), " ") + " 经 " + strings.Join(strings.Fields(grant.Viewer.Name), " ") + "·" + strings.Join(strings.Fields(grant.Agent.Name), " ") + " 代发\n\n" + req.Content
	sent, err := h.TaskService.SendManagedChatMessage(r.Context(), session, grant.Originator, content)
	if err != nil {
		if errors.Is(err, service.ErrChatSessionArchived) || errors.Is(err, service.ErrChatTaskAgentArchived) || errors.Is(err, service.ErrChatTaskAgentDisabled) || errors.Is(err, service.ErrChatTaskAgentNoRuntime) {
			deny()
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to send managed chat message")
		return
	}
	applied := "start"
	if sent.Queued {
		applied = sendModeQueue
	}
	writeJSON(w, http.StatusCreated, SendChatMessageResponse{
		ChatSessionID: uuidToString(session.ID), MessageID: uuidToString(sent.Message.ID), TaskID: uuidToString(sent.Task.ID), SupportsQueue: true, Queued: sent.Queued, Mode: applied,
		CreatedAt: timestampToString(sent.Task.CreatedAt), MessageKind: protocol.ChatMessageKindLinkedDelivery,
		LinkedDelivery: &ManagedChatDeliverySource{OriginatorID: originator, OriginatorName: user.Name, ViewerWorkspaceID: uuidToString(grant.Viewer.ID), ViewerWorkspaceName: grant.Viewer.Name, AgentID: uuidToString(grant.Agent.ID), AgentName: grant.Agent.Name},
	})
}
