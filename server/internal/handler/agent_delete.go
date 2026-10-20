package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const agentDeleteTaskBatchSize = 500

// The restore statements live here rather than in sqlc: sqlc rewrites the
// `SELECT *` over jsonb_populate_record(set) into a column list it cannot
// derive. Re-creating rows from their stored JSON keeps every column without
// restating the schema.
const (
	restoreAgentFromBackupSQL = `
INSERT INTO agent
SELECT * FROM jsonb_populate_record(
    NULL::agent,
    (SELECT snapshot->'agent' FROM agent_backup WHERE id = $1)
        || jsonb_build_object('archived_at', NULL, 'archived_by', NULL, 'status', 'offline', 'updated_at', now())
)`
	restoreAgentConfigRowsSQL = `
WITH b AS (SELECT snapshot FROM agent_backup WHERE id = $1),
restored_targets AS (
    INSERT INTO agent_invocation_target
    SELECT * FROM jsonb_populate_recordset(NULL::agent_invocation_target, (SELECT snapshot->'invocation_targets' FROM b))
),
restored_mcp_servers AS (
    INSERT INTO agent_mcp_server
    SELECT * FROM jsonb_populate_recordset(NULL::agent_mcp_server, (SELECT snapshot->'mcp_servers' FROM b))
),
restored_labels AS (
    INSERT INTO agent_to_label
    SELECT * FROM jsonb_populate_recordset(NULL::agent_to_label, (SELECT snapshot->'labels' FROM b))
)
INSERT INTO agent_skill
SELECT s.* FROM jsonb_populate_recordset(NULL::agent_skill, COALESCE((SELECT snapshot->'skills' FROM b), '[]'::jsonb)) s
WHERE EXISTS (SELECT 1 FROM skill WHERE skill.id = s.skill_id)`
)

// AgentBackupResponse summarises one agent backup. The snapshot itself holds
// env values and never leaves the server; restoring it is the only way to use
// it.
type AgentBackupResponse struct {
	ID          string `json:"id"`
	AgentID     string `json:"agent_id"`
	AgentName   string `json:"agent_name"`
	DeletedBy   string `json:"deleted_by"`
	CreatedAt   string `json:"created_at"`
	EnvKeyCount int32  `json:"env_key_count"`
}

type DeleteAgentResponse struct {
	AgentID string              `json:"agent_id"`
	Backup  AgentBackupResponse `json:"backup"`
}

// DeleteAgent removes an agent for good, after writing a full backup of its
// configuration. Only workspaces in magpie mode delete agents; everywhere
// else archiving stays the only way to retire one. The steps run in an order
// that never loses configuration:
//
//  1. The backup commits on its own. If it fails nothing else happens.
//  2. The agent's runs are cancelled, as archiving did, so daemons stop them.
//  3. One transaction deletes the agent's tasks, everything that points at the
//     agent, and the agent row.
//
// The caller must be allowed to read the agent's env (authorizeAgentEnv: a
// human who owns the agent or the workspace), because the backup copies those
// values; an agent token cannot delete agents.
func (h *Handler) DeleteAgent(w http.ResponseWriter, r *http.Request) {
	agent, member, ok := h.authorizeAgentEnv(w, r)
	if !ok {
		return
	}
	if !h.workspaceMagpieEnabledByID(r.Context(), agent.WorkspaceID) {
		writeError(w, http.StatusConflict, "agents are deleted only when the workspace is connected to Magpie; archive the agent instead")
		return
	}
	if agent.Kind == "system" || (agent.SystemKey.Valid && agent.SystemKey.String != "") {
		writeError(w, http.StatusBadRequest, "this agent is built into Multica and cannot be deleted")
		return
	}
	children, err := h.Queries.CountSpecialisationsOfAgent(r.Context(), agent.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete agent")
		return
	}
	if children > 0 {
		writeError(w, http.StatusConflict, "this base role still has specialisations; solidify and unbind or delete them first")
		return
	}
	led, err := h.Queries.CountSquadsLedByAgent(r.Context(), agent.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete agent")
		return
	}
	if led > 0 {
		writeError(w, http.StatusConflict, "this agent leads a squad; give the squad another leader first")
		return
	}

	backup, err := h.Queries.CreateAgentBackup(r.Context(), db.CreateAgentBackupParams{
		DeletedBy: member.UserID,
		AgentID:   agent.ID,
	})
	if err != nil {
		slog.Error("agent backup failed; agent not deleted",
			append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(agent.ID))...)
		writeError(w, http.StatusInternalServerError, "backing up the agent failed, so it was not deleted")
		return
	}

	if _, err := h.TaskService.CancelTasksForArchivedAgent(r.Context(), agent.ID); err != nil {
		slog.Error("cancel agent tasks before delete failed",
			append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(agent.ID))...)
		writeError(w, http.StatusInternalServerError, "stopping the agent's runs failed, so it was not deleted")
		return
	}

	if err := h.deleteAgentRows(r, agent.WorkspaceID, agent.ID); err != nil {
		slog.Error("agent delete failed",
			append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(agent.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to delete agent")
		return
	}

	wsID := uuidToString(agent.WorkspaceID)
	agentID := uuidToString(agent.ID)
	slog.Info("agent deleted", append(logger.RequestAttrs(r),
		"agent_id", agentID, "workspace_id", wsID, "backup_id", uuidToString(backup.ID))...)
	h.publish(protocol.EventAgentDeleted, wsID, "member", uuidToString(member.UserID), map[string]any{"agent_id": agentID})

	writeJSON(w, http.StatusOK, DeleteAgentResponse{
		AgentID: agentID,
		Backup: AgentBackupResponse{
			ID:          uuidToString(backup.ID),
			AgentID:     agentID,
			AgentName:   backup.AgentName,
			DeletedBy:   uuidToString(backup.DeletedBy),
			CreatedAt:   timestampToString(backup.CreatedAt),
			EnvKeyCount: backup.EnvKeyCount,
		},
	})
}

func (h *Handler) deleteAgentRows(r *http.Request, workspaceID, agentID pgtype.UUID) error {
	ctx := r.Context()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)

	if _, err := qtx.GetAgentForUpdate(ctx, agentID); err != nil {
		return err
	}
	for {
		ids, err := qtx.ListTaskIDsByAgentFirstPage(ctx, db.ListTaskIDsByAgentFirstPageParams{
			AgentID: agentID,
			Limit:   agentDeleteTaskBatchSize,
		})
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			break
		}
		if err := qtx.DetachTaskBatchReferences(ctx, ids); err != nil {
			return err
		}
		if err := qtx.DeleteTaskBatch(ctx, ids); err != nil {
			return err
		}
	}
	if err := qtx.DeleteTaskTokensByAgent(ctx, agentID); err != nil {
		return err
	}
	if err := qtx.DeleteAgentReferences(ctx, agentID); err != nil {
		return err
	}
	// The old line's acceptance slot, released as archiving releases it.
	if _, err := qtx.ClearIssueReviewer(ctx, db.ClearIssueReviewerParams{
		WorkspaceID:  workspaceID,
		ReviewerType: "agent",
		ReviewerID:   agentID,
	}); err != nil {
		return err
	}
	n, err := qtx.DeleteUserAgent(ctx, agentID)
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("agent row was not deleted")
	}
	return tx.Commit(ctx)
}

func (h *Handler) ListAgentBackups(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	rows, err := h.Queries.ListAgentBackups(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent backups")
		return
	}
	out := make([]AgentBackupResponse, 0, len(rows))
	for _, b := range rows {
		out = append(out, AgentBackupResponse{
			ID:          uuidToString(b.ID),
			AgentID:     uuidToString(b.AgentID),
			AgentName:   b.AgentName,
			DeletedBy:   uuidToString(b.DeletedBy),
			CreatedAt:   timestampToString(b.CreatedAt),
			EnvKeyCount: b.EnvKeyCount,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// RestoreAgentBackup re-creates a deleted agent from its backup, under its
// original id and with its full configuration. Same gate as deleting: a human
// who owned the agent or owns the workspace.
func (h *Handler) RestoreAgentBackup(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID := requestUserID(r)
	if actorType, _ := h.resolveActor(r, userID, workspaceID); actorType == "agent" {
		writeError(w, http.StatusForbidden, "agents may not restore agent backups")
		return
	}
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "backup not found")
	if !ok {
		return
	}
	backupID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "backupId"), "backup id")
	if !ok {
		return
	}
	backup, err := h.Queries.GetAgentBackupInWorkspace(r.Context(), db.GetAgentBackupInWorkspaceParams{
		ID:          backupID,
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "backup not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load backup")
		return
	}
	if !canManageAgentEnv(db.Agent{OwnerID: backup.OwnerID}, member) {
		writeError(w, http.StatusForbidden, "only the agent's owner or the workspace owner can restore it")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to restore agent")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	_, err = tx.Exec(r.Context(), restoreAgentFromBackupSQL, backup.ID)
	if err == nil {
		_, err = tx.Exec(r.Context(), restoreAgentConfigRowsSQL, backup.ID)
	}
	var restored db.Agent
	if err == nil {
		restored, err = qtx.GetAgent(r.Context(), backup.AgentID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		var pgErr *pgconn.PgError
		switch {
		case isUniqueViolation(err):
			writeError(w, http.StatusConflict, "this agent already exists, or another agent now uses its name")
		case errors.As(err, &pgErr) && pgErr.Code == "23503":
			writeError(w, http.StatusConflict, "the agent's runtime no longer exists")
		default:
			slog.Error("restore agent backup failed",
				append(logger.RequestAttrs(r), "error", err, "backup_id", uuidToString(backup.ID))...)
			writeError(w, http.StatusInternalServerError, "failed to restore agent")
		}
		return
	}

	slog.Info("agent restored from backup", append(logger.RequestAttrs(r),
		"agent_id", uuidToString(restored.ID), "backup_id", uuidToString(backup.ID))...)
	h.TaskService.ReconcileAgentStatus(r.Context(), restored.ID)
	if refreshed, err := h.Queries.GetAgent(r.Context(), restored.ID); err == nil {
		restored = refreshed
	}
	resp := h.agentToResponse(restored)
	if err := h.enrichAgentResponseWithTargets(r.Context(), &resp, restored.ID); err != nil {
		slog.Warn("restore agent: load invocation targets for response failed",
			append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(restored.ID))...)
	}
	h.publish(protocol.EventAgentCreated, workspaceID, "member", uuidToString(member.UserID), map[string]any{"agent": broadcastAgentResponse(resp)})
	redactAgentResponseForActor(&resp, "member")
	writeJSON(w, http.StatusOK, resp)
}
