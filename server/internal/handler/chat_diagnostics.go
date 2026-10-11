package handler

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ChatRuntimeResponse exposes only the reachability needed by chat senders.
type ChatRuntimeResponse struct {
	Status     string  `json:"status"`
	Name       string  `json:"name"`
	LastSeenAt *string `json:"last_seen_at"`
}

type ChatDiagnostics struct {
	TaskID     string               `json:"task_id,omitempty"`
	TaskStatus string               `json:"task_status,omitempty"`
	Reason     string               `json:"reason,omitempty"`
	Runtime    *ChatRuntimeResponse `json:"runtime"`
	Warning    string               `json:"warning,omitempty"`
	QueueAhead int64                `json:"queue_ahead"`
}

// chatDiagnostics is shared by send and progress (including wait timeouts).
// The task has already been scoped to the authorized session by the caller.
func (h *Handler) chatDiagnostics(ctx context.Context, runtimeID pgtype.UUID, task *db.AgentTaskQueue) (ChatDiagnostics, error) {
	out := ChatDiagnostics{}
	if task != nil {
		out.TaskID, out.TaskStatus = uuidToString(task.ID), task.Status
		if task.RuntimeID.Valid {
			runtimeID = task.RuntimeID
		}
	}
	if runtimeID.Valid {
		runtime, err := h.getAgentRuntime(ctx, obsmetrics.RuntimeLookupSourceChat, runtimeID)
		if err != nil {
			return out, err
		}
		out.Runtime = &ChatRuntimeResponse{Status: runtime.Status, Name: runtime.Name}
		if runtime.LastSeenAt.Valid {
			seen := timestampToString(runtime.LastSeenAt)
			out.Runtime.LastSeenAt = &seen
		}
		if runtime.Status == "offline" {
			seen := "unknown"
			if out.Runtime.LastSeenAt != nil {
				seen = *out.Runtime.LastSeenAt
			}
			out.Warning = fmt.Sprintf("runtime_offline: %s last_seen %s; message accepted, replies wait for reconnection", runtime.Name, seen)
		}
	}
	if task == nil {
		return out, nil
	}
	hasLogs := false
	if task.Status == "running" {
		var err error
		hasLogs, err = h.Queries.HasTaskMessages(ctx, task.ID)
		if err != nil {
			return out, err
		}
	}
	if task.Status == "queued" {
		var err error
		out.QueueAhead, err = h.Queries.CountChatTasksAhead(ctx, task.ID)
		if err != nil {
			return out, err
		}
	}
	out.Reason = chatTaskReason(*task, out.Runtime, hasLogs, out.QueueAhead)
	return out, nil
}

func chatTaskReason(task db.AgentTaskQueue, runtime *ChatRuntimeResponse, hasLogs bool, ahead int64) string {
	switch task.Status {
	case "failed", "cancelled":
		if task.FailureReason.Valid && task.FailureReason.String != "" {
			return task.FailureReason.String
		}
		if task.Error.Valid && task.Error.String != "" {
			return task.Error.String
		}
		return task.Status
	case "completed":
		return "completed"
	}
	if runtime != nil && runtime.Status == "offline" {
		return "runtime_offline"
	}
	switch task.Status {
	case "running":
		if !hasLogs {
			return "running_no_logs"
		}
		return "running"
	case "queued":
		if ahead > 0 {
			return fmt.Sprintf("queued_behind_%d", ahead)
		}
		return "awaiting_dispatch"
	case "waiting_local_directory":
		return "waiting_local_directory"
	case "deferred":
		return "retry_scheduled"
	default:
		return task.Status
	}
}
