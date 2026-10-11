package handler

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
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

func (h *Handler) hydrateMachineQueueReasons(ctx context.Context, rows []AgentTaskResponse) {
	ids := []pgtype.UUID{}
	seen := map[string]bool{}
	for _, row := range rows {
		if row.Status == "queued" && row.RuntimeID != "" && !seen[row.RuntimeID] {
			ids = append(ids, parseUUID(row.RuntimeID))
			seen[row.RuntimeID] = true
		}
	}
	capacities := h.machineCapacities(ctx, ids)
	for i := range rows {
		c := capacities[rows[i].RuntimeID]
		if rows[i].Status == "queued" && c != nil && c.ActiveRuns >= int64(c.MaxConcurrentRuns) {
			rows[i].QueueReason = &MachineQueueReason{Code: "machine_capacity", MachineCapacityResponse: *c}
		}
	}
}
