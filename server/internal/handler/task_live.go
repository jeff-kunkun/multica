package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
)

// Live view of a running issue run (DENE-2667 PR 4). The daemon reports the
// command in flight, the last output line and the run's silence every 15
// seconds; the server keeps only the latest report per run, pushes it as
// task_live:updated, and serves the workspace's running runs from
// GET /api/task-live. Every field is redacted by the daemon before it leaves
// the machine and again here, because a daemon of any version may call in.
const (
	taskLiveToolMaxRunes    = 100
	taskLiveCommandMaxRunes = 500
	taskLiveOutputMaxRunes  = 300
	taskLiveMaxBodyBytes    = 16 << 10
	// taskLiveListCap bounds one read; a workspace never runs this many runs
	// at once.
	taskLiveListCap = 200
	// taskLiveRetention is how long a report outlives its last update. A row
	// is served only while its run is running, so this only bounds the table.
	taskLiveRetention = time.Hour
)

// TaskLiveReportRequest is the daemon's 15-second live report.
type TaskLiveReportRequest struct {
	Tool          string `json:"tool"`
	Command       string `json:"command"`
	LastOutput    string `json:"last_output"`
	ToolsInFlight int    `json:"tools_in_flight"`
	IdleSeconds   int64  `json:"idle_seconds"`
}

// TaskLiveListResponse lists the live views of a workspace's running runs.
type TaskLiveListResponse struct {
	Live []protocol.TaskLivePayload `json:"live"`
}

// taskLiveText redacts a reported field and bounds it. Redaction runs on the
// whole field before the cut so a cut cannot split a secret into a shape the
// patterns no longer match.
func taskLiveText(s string, maxRunes int) string {
	return truncateRunes(redact.Text(util.SanitizeTextForPostgres(s)), maxRunes)
}

// ReportTaskLive stores a running issue run's latest live view and pushes it.
// A run that is not running, or that has no issue, has no live view anyone
// can open, so its report is accepted and dropped.
func (h *Handler) ReportTaskLive(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	r.Body = http.MaxBytesReader(w, r.Body, taskLiveMaxBodyBytes)
	var req TaskLiveReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	task, workspaceID, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, taskID)
	if !ok {
		return
	}
	if task.Status != "running" || !task.IssueID.Valid || workspaceID == "" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}
	idle := max(req.IdleSeconds, 0)
	live := protocol.TaskLivePayload{
		TaskID:        uuidToString(task.ID),
		IssueID:       uuidToString(task.IssueID),
		AgentID:       uuidToString(task.AgentID),
		Tool:          taskLiveText(req.Tool, taskLiveToolMaxRunes),
		Command:       taskLiveText(req.Command, taskLiveCommandMaxRunes),
		LastOutput:    taskLiveText(req.LastOutput, taskLiveOutputMaxRunes),
		ToolsInFlight: max(req.ToolsInFlight, 0),
		IdleSeconds:   idle,
	}
	// The silence is turned into a server-clock instant here, so readers never
	// depend on the daemon's clock.
	if err := h.DB.QueryRow(r.Context(), `
INSERT INTO task_live_status (task_id, workspace_id, issue_id, agent_id, tool, command,
    last_output, tools_in_flight, last_activity_at, reported_at)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7, $8,
    now() - make_interval(secs => $9::double precision), now())
ON CONFLICT (task_id) DO UPDATE SET
    tool = EXCLUDED.tool, command = EXCLUDED.command, last_output = EXCLUDED.last_output,
    tools_in_flight = EXCLUDED.tools_in_flight, last_activity_at = EXCLUDED.last_activity_at,
    reported_at = EXCLUDED.reported_at
RETURNING last_activity_at, reported_at`,
		live.TaskID, workspaceID, live.IssueID, live.AgentID, live.Tool, live.Command,
		live.LastOutput, live.ToolsInFlight, float64(idle),
	).Scan(&live.LastActivityAt, &live.ReportedAt); err != nil {
		slog.Warn("store task live status failed", "task_id", live.TaskID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to store live status")
		return
	}
	h.pruneTaskLive(r.Context())
	h.publish(protocol.EventTaskLiveUpdated, workspaceID, "system", "", live)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// pruneTaskLive drops reports nobody updated within the retention. Failing
// to prune only leaves rows that are never served.
func (h *Handler) pruneTaskLive(ctx context.Context) {
	if _, err := h.DB.Exec(ctx,
		`DELETE FROM task_live_status WHERE reported_at < now() - make_interval(secs => $1::double precision)`,
		taskLiveRetention.Seconds()); err != nil {
		slog.Debug("prune task live status failed", "error", err)
	}
}

// ListTaskLive returns the live views of the workspace's running issue runs
// the caller may see: the issue must be visible to them, and the agent
// accessible, exactly as for the run's transcript and the agent snapshot.
func (h *Handler) ListTaskLive(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	wsUUID := parseUUID(workspaceID)
	viewer, err := h.visibilityViewerFor(r, wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve visibility")
		return
	}
	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
		return
	}

	args := []any{wsUUID}
	addArg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	rows, err := h.DB.Query(r.Context(), `
SELECT l.task_id::text, l.issue_id::text, l.agent_id::text, l.tool, l.command, l.last_output,
       l.tools_in_flight, l.last_activity_at, l.reported_at,
       GREATEST(0, floor(extract(epoch FROM now() - l.last_activity_at)))::bigint
FROM task_live_status l
JOIN agent_task_queue t ON t.id = l.task_id AND t.status = 'running'
JOIN issue i ON i.id = l.issue_id AND i.workspace_id = l.workspace_id
WHERE l.workspace_id = $1::uuid AND `+viewer.issueVisibilitySQL("i", addArg)+`
ORDER BY l.reported_at DESC, l.task_id
LIMIT `+strconv.Itoa(taskLiveListCap), args...)
	if err != nil {
		slog.Warn("list task live status failed", "workspace_id", workspaceID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list live status")
		return
	}
	defer rows.Close()
	resp := TaskLiveListResponse{Live: []protocol.TaskLivePayload{}}
	for rows.Next() {
		var live protocol.TaskLivePayload
		if err := rows.Scan(&live.TaskID, &live.IssueID, &live.AgentID, &live.Tool, &live.Command,
			&live.LastOutput, &live.ToolsInFlight, &live.LastActivityAt, &live.ReportedAt,
			&live.IdleSeconds); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list live status")
			return
		}
		if _, ok := allowed[live.AgentID]; !ok {
			continue
		}
		resp.Live = append(resp.Live, live)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list live status")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
