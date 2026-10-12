package handler

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Counts are device-wide, including reservations made in other workspaces.
// No identities, task payloads or workspace names cross this boundary.
func (h *Handler) machineCapacities(ctx context.Context, ids []pgtype.UUID) map[string]*MachineCapacityResponse {
	result := map[string]*MachineCapacityResponse{}
	if len(ids) == 0 {
		return result
	}
	rows, err := h.Queries.ListRuntimeMachineCapacities(ctx, ids)
	if err != nil {
		slog.Error("read machine capacities", "error", err)
		return result
	}
	for _, row := range rows {
		result[uuidToString(row.RuntimeID)] = &MachineCapacityResponse{ActiveRuns: row.ActiveRuns, MaxConcurrentRuns: row.MaxConcurrentRuns}
	}
	return result
}

func (h *Handler) hydrateRuntimeMachineCapacities(ctx context.Context, rows []AgentRuntimeResponse) {
	ids := make([]pgtype.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, parseUUID(row.ID))
	}
	capacities := h.machineCapacities(ctx, ids)
	for i := range rows {
		rows[i].MachineCapacity = capacities[rows[i].ID]
	}
}

// hydrateMachineQueueReasons says why a queued row has not started. Most
// specific first: the same agent already has a live run on this issue
// ("issue_serial", 1 of 1), then the agent is at its own limit
// ("agent_capacity"), then the whole machine is full ("machine_capacity").
// The first two are read only for rows that carry a task id.
func (h *Handler) hydrateMachineQueueReasons(ctx context.Context, rows []AgentTaskResponse) {
	ids := []pgtype.UUID{}
	taskIDs := []pgtype.UUID{}
	seen := map[string]bool{}
	for _, row := range rows {
		if row.Status != "queued" {
			continue
		}
		if row.RuntimeID != "" && !seen[row.RuntimeID] {
			ids = append(ids, parseUUID(row.RuntimeID))
			seen[row.RuntimeID] = true
		}
		if row.ID != "" {
			taskIDs = append(taskIDs, parseUUID(row.ID))
		}
	}
	capacities := h.machineCapacities(ctx, ids)
	blockers := map[string]db.ListQueuedTaskBlockersRow{}
	if len(taskIDs) > 0 {
		found, err := h.Queries.ListQueuedTaskBlockers(ctx, taskIDs)
		if err != nil {
			slog.Error("read queued task blockers", "error", err)
		}
		for _, b := range found {
			blockers[uuidToString(b.TaskID)] = b
		}
	}
	for i := range rows {
		if rows[i].Status != "queued" {
			continue
		}
		if b, ok := blockers[rows[i].ID]; ok {
			if b.IssueSerial {
				rows[i].QueueReason = &MachineQueueReason{Code: "issue_serial", MachineCapacityResponse: MachineCapacityResponse{ActiveRuns: 1, MaxConcurrentRuns: 1}}
				continue
			}
			if b.AgentMax > 0 && b.AgentRunning >= int64(b.AgentMax) {
				rows[i].QueueReason = &MachineQueueReason{Code: "agent_capacity", MachineCapacityResponse: MachineCapacityResponse{ActiveRuns: b.AgentRunning, MaxConcurrentRuns: b.AgentMax}}
				continue
			}
		}
		c := capacities[rows[i].RuntimeID]
		if c != nil && c.ActiveRuns >= int64(c.MaxConcurrentRuns) {
			rows[i].QueueReason = &MachineQueueReason{Code: "machine_capacity", MachineCapacityResponse: *c}
		}
	}
}
