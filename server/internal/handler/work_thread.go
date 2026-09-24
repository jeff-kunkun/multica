package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// WorkThreadSnapshot is the bounded read model shared by Issue and Chat.
// Queue rows are intentionally projected as short inputs; callers never get
// the full task context or provider session payload from this endpoint.
type WorkThreadSnapshot struct {
	ThreadID       string            `json:"thread_id"`
	AgentID        string            `json:"agent_id"`
	IssueID        string            `json:"issue_id,omitempty"`
	ChatSessionID  string            `json:"chat_session_id,omitempty"`
	Continuous     bool              `json:"continuous"`
	CurrentTurn    *WorkThreadTurn   `json:"current_turn,omitempty"`
	LastTurn       *WorkThreadTurn   `json:"last_turn,omitempty"`
	State          string            `json:"state"`
	CanResume      bool              `json:"can_resume"`
	SessionID      string            `json:"session_id,omitempty"`
	QueuedInputs   []WorkThreadInput `json:"queued_inputs"`
	QueueTruncated bool              `json:"queue_truncated"`
	Context        WorkThreadContext `json:"context"`
	UpdatedAt      string            `json:"updated_at"`
}

type WorkThreadTurn struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	SessionID string `json:"session_id,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
}

type WorkThreadInput struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Summary   string `json:"summary,omitempty"`
	CreatedAt string `json:"created_at"`
}

type WorkThreadContext struct {
	Generation       int32  `json:"generation"`
	MessageLimit     int32  `json:"message_limit"`
	TokenBudget      int32  `json:"token_budget"`
	SummaryAvailable bool   `json:"summary_available"`
	BreakReason      string `json:"break_reason,omitempty"`
}

const workThreadQueueLimit = 50

type WorkThreadActionRequest struct {
	Action  string `json:"action"`
	Summary string `json:"summary,omitempty"`
}

// WorkThreadAction mutates one Issue work thread while preserving its
// continuity key. Continue clones the latest resumable turn inside the same
// transaction; interrupt cancels active turns; queue appends a new input.
// The database unique pending-task fence is the final concurrency guard.
func (h *Handler) WorkThreadAction(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req WorkThreadActionRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid work thread action")
			return
		}
	}
	switch req.Action {
	case "interrupt":
		if err := h.TaskService.CancelTasksForIssue(r.Context(), issue.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to interrupt work thread")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"action": req.Action, "state": "interrupted"})
		return
	case "queue":
		task, err := h.TaskService.EnqueueTaskForIssue(r.Context(), issue)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if req.Summary != "" {
			if _, err := h.DB.Exec(r.Context(), `UPDATE agent_task_queue SET trigger_summary = $2 WHERE id = $1`, task.ID, req.Summary); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to record queued input")
				return
			}
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"action": req.Action, "state": "queued", "task_id": uuidToString(task.ID), "thread_id": uuidToString(task.WorkThreadID)})
		return
	case "continue":
		task, err := h.continueWorkThread(r, issue.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "work thread has no resumable turn or already has a pending turn")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to continue work thread")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"action": req.Action, "state": "queued", "task_id": uuidToString(task.ID), "thread_id": uuidToString(task.WorkThreadID), "session_id": task.SessionID.String})
		return
	default:
		writeError(w, http.StatusBadRequest, "action must be continue, interrupt, or queue")
	}
}

func (h *Handler) ChatWorkThreadAction(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	session, ok := h.gatePublicChatSessionForUser(w, r, userID, ctxWorkspaceID(r.Context()), chi.URLParam(r, "sessionId"))
	if !ok {
		return
	}
	var req WorkThreadActionRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	switch req.Action {
	case "interrupt":
		var ids []pgtype.UUID
		rows, err := h.DB.Query(r.Context(), `SELECT id FROM agent_task_queue WHERE chat_session_id = $1 AND status IN ('queued','dispatched','running','waiting_local_directory')`, session.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to inspect chat work thread")
			return
		}
		for rows.Next() {
			var id pgtype.UUID
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		for _, id := range ids {
			if _, err := h.TaskService.CancelTask(r.Context(), id); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to interrupt work thread")
				return
			}
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"action": req.Action, "state": "interrupted"})
	case "continue":
		task, err := h.continueChatWorkThread(r, session.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "work thread has no resumable turn or already has a pending turn")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to continue work thread")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"action": req.Action, "state": "queued", "task_id": uuidToString(task.ID), "thread_id": uuidToString(task.WorkThreadID), "session_id": task.SessionID.String})
	case "queue":
		writeError(w, http.StatusConflict, "chat queue inputs must be sent through the composer")
	default:
		writeError(w, http.StatusBadRequest, "action must be continue, interrupt, or queue")
	}
}

func (h *Handler) continueChatWorkThread(r *http.Request, sessionID pgtype.UUID) (db.AgentTaskQueue, error) {
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	defer tx.Rollback(r.Context())
	var taskID pgtype.UUID
	var status string
	var sid pgtype.Text
	err = tx.QueryRow(r.Context(), `SELECT latest.id, latest.status, latest.session_id FROM work_thread wt JOIN LATERAL (SELECT id,status,session_id FROM agent_task_queue WHERE work_thread_id=wt.id ORDER BY created_at DESC,id DESC LIMIT 1) latest ON true WHERE wt.chat_session_id=$1 AND NOT EXISTS (SELECT 1 FROM agent_task_queue WHERE work_thread_id=wt.id AND status IN ('queued','dispatched','running','waiting_local_directory')) FOR UPDATE OF wt`, sessionID).Scan(&taskID, &status, &sid)
	if err != nil || (status != "cancelled" && status != "failed") || !sid.Valid || sid.String == "" {
		return db.AgentTaskQueue{}, pgx.ErrNoRows
	}
	task, err := h.Queries.WithTx(tx).CreateRetryTask(r.Context(), db.CreateRetryTaskParams{ID: taskID})
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	if err := tx.Commit(r.Context()); err != nil {
		return db.AgentTaskQueue{}, err
	}
	return task, nil
}

func (h *Handler) continueWorkThread(r *http.Request, issueID pgtype.UUID) (db.AgentTaskQueue, error) {
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	defer tx.Rollback(r.Context())
	var threadID, taskID pgtype.UUID
	var status string
	var sessionID pgtype.Text
	err = tx.QueryRow(r.Context(), `
		SELECT wt.id, latest.id, latest.status, latest.session_id
		FROM work_thread wt
		JOIN LATERAL (
			SELECT id, status, session_id FROM agent_task_queue
			WHERE work_thread_id = wt.id ORDER BY created_at DESC, id DESC LIMIT 1
		) latest ON true
		WHERE wt.issue_id = $1
		  AND NOT EXISTS (SELECT 1 FROM agent_task_queue WHERE work_thread_id = wt.id AND status IN ('queued','dispatched','running','waiting_local_directory'))
		FOR UPDATE OF wt`, issueID).Scan(&threadID, &taskID, &status, &sessionID)
	if err != nil || (status != "cancelled" && status != "failed") || !sessionID.Valid || sessionID.String == "" {
		return db.AgentTaskQueue{}, pgx.ErrNoRows
	}
	qtx := h.Queries.WithTx(tx)
	task, err := qtx.CreateRetryTask(r.Context(), db.CreateRetryTaskParams{ID: taskID})
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	if err := tx.Commit(r.Context()); err != nil {
		return db.AgentTaskQueue{}, err
	}
	return task, nil
}

type workThreadRow struct {
	ThreadID, AgentID, IssueID, ChatSessionID pgtype.UUID
	Generation, MessageLimit, TokenBudget     int32
	LastTurnID                                pgtype.UUID
	LastSessionID                             pgtype.Text
	BreakReason                               pgtype.Text
	UpdatedAt                                 time.Time
	CurrentID                                 pgtype.UUID
	CurrentSessionID                          pgtype.Text
	CurrentStatus                             pgtype.Text
	CurrentStartedAt                          pgtype.Timestamptz
	LastID                                    pgtype.UUID
	LastTaskSessionID                         pgtype.Text
	LastStatus                                pgtype.Text
	LastCompletedAt                           pgtype.Timestamptz
}

const workThreadByIssueSQL = `
SELECT wt.id, wt.agent_id, wt.issue_id, wt.chat_session_id,
       wt.context_generation, wt.context_message_limit, wt.context_token_budget,
       wt.last_session_id, wt.last_turn_id, wt.continuity_break_reason, wt.updated_at,
       active.id, active.session_id, active.status, active.started_at,
       latest.id, latest.session_id, latest.status, latest.completed_at
FROM work_thread wt
LEFT JOIN LATERAL (
  SELECT id, session_id, status, started_at
  FROM agent_task_queue
  WHERE work_thread_id = wt.id
    AND status IN ('dispatched', 'running', 'waiting_local_directory')
  ORDER BY created_at DESC, id DESC
  LIMIT 1
) active ON true
LEFT JOIN LATERAL (
  SELECT id, session_id, status, completed_at
  FROM agent_task_queue
  WHERE work_thread_id = wt.id
  ORDER BY created_at DESC, id DESC
  LIMIT 1
) latest ON true
WHERE wt.issue_id = $1
ORDER BY wt.updated_at DESC, wt.id DESC
LIMIT 1`

const workThreadByChatSQL = `
SELECT wt.id, wt.agent_id, wt.issue_id, wt.chat_session_id,
       wt.context_generation, wt.context_message_limit, wt.context_token_budget,
       wt.last_session_id, wt.last_turn_id, wt.continuity_break_reason, wt.updated_at,
       active.id, active.session_id, active.status, active.started_at,
       latest.id, latest.session_id, latest.status, latest.completed_at
FROM work_thread wt
LEFT JOIN LATERAL (
  SELECT id, session_id, status, started_at
  FROM agent_task_queue
  WHERE work_thread_id = wt.id
    AND status IN ('dispatched', 'running', 'waiting_local_directory')
  ORDER BY created_at DESC, id DESC
  LIMIT 1
) active ON true
LEFT JOIN LATERAL (
  SELECT id, session_id, status, completed_at
  FROM agent_task_queue
  WHERE work_thread_id = wt.id
  ORDER BY created_at DESC, id DESC
  LIMIT 1
) latest ON true
WHERE wt.chat_session_id = $1
ORDER BY wt.updated_at DESC, wt.id DESC
LIMIT 1`

const workThreadInputsSQL = `
SELECT id, status, COALESCE(trigger_summary, ''), created_at
FROM agent_task_queue
WHERE work_thread_id = $1 AND status IN ('queued', 'deferred')
ORDER BY created_at ASC, id ASC
LIMIT $2`

func (h *Handler) GetIssueWorkThread(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var row workThreadRow
	err := h.DB.QueryRow(r.Context(), workThreadByIssueSQL, issue.ID).Scan(
		&row.ThreadID, &row.AgentID, &row.IssueID, &row.ChatSessionID,
		&row.Generation, &row.MessageLimit, &row.TokenBudget, &row.LastSessionID,
		&row.LastTurnID, &row.BreakReason, &row.UpdatedAt, &row.CurrentID,
		&row.CurrentSessionID, &row.CurrentStatus, &row.CurrentStartedAt,
		&row.LastID, &row.LastTaskSessionID, &row.LastStatus, &row.LastCompletedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeJSON(w, http.StatusOK, nil)
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load work thread")
		return
	}
	h.writeWorkThreadSnapshot(w, r, row)
}

func (h *Handler) GetChatWorkThread(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	session, ok := h.gatePublicChatSessionForUser(w, r, userID, ctxWorkspaceID(r.Context()), chi.URLParam(r, "sessionId"))
	if !ok {
		return
	}
	var row workThreadRow
	err := h.DB.QueryRow(r.Context(), workThreadByChatSQL, session.ID).Scan(
		&row.ThreadID, &row.AgentID, &row.IssueID, &row.ChatSessionID,
		&row.Generation, &row.MessageLimit, &row.TokenBudget, &row.LastSessionID,
		&row.LastTurnID, &row.BreakReason, &row.UpdatedAt, &row.CurrentID,
		&row.CurrentSessionID, &row.CurrentStatus, &row.CurrentStartedAt,
		&row.LastID, &row.LastTaskSessionID, &row.LastStatus, &row.LastCompletedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeJSON(w, http.StatusOK, nil)
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load work thread")
		return
	}
	h.writeWorkThreadSnapshot(w, r, row)
}

func (h *Handler) writeWorkThreadSnapshot(w http.ResponseWriter, r *http.Request, row workThreadRow) {
	snapshot := WorkThreadSnapshot{
		ThreadID: uuidToString(row.ThreadID), AgentID: uuidToString(row.AgentID),
		IssueID: uuidToString(row.IssueID), ChatSessionID: uuidToString(row.ChatSessionID),
		Continuous: row.Generation > 0 || row.LastSessionID.Valid || row.LastTurnID.Valid,
		SessionID:  row.CurrentSessionID.String, QueuedInputs: []WorkThreadInput{},
		Context: WorkThreadContext{Generation: row.Generation, MessageLimit: row.MessageLimit, TokenBudget: row.TokenBudget,
			SummaryAvailable: false, BreakReason: row.BreakReason.String},
		UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339Nano),
		State:     "idle",
	}
	if !snapshot.CurrentTurnExists() && row.LastTurnID.Valid {
		snapshot.SessionID = row.LastSessionID.String
	}
	if row.CurrentID.Valid {
		snapshot.State = "active"
		snapshot.CurrentTurn = &WorkThreadTurn{ID: uuidToString(row.CurrentID), Status: row.CurrentStatus.String,
			SessionID: row.CurrentSessionID.String, StartedAt: timestampToString(row.CurrentStartedAt)}
	}
	if row.LastID.Valid {
		snapshot.LastTurn = &WorkThreadTurn{ID: uuidToString(row.LastID), Status: row.LastStatus.String,
			SessionID: row.LastTaskSessionID.String, StartedAt: timestampToString(row.LastCompletedAt)}
		if snapshot.State == "idle" {
			switch row.LastStatus.String {
			case "cancelled", "failed":
				if row.LastTaskSessionID.Valid && !row.BreakReason.Valid {
					snapshot.State = "resumable"
					snapshot.CanResume = true
				} else if row.BreakReason.Valid {
					snapshot.State = "rebuild_required"
				}
			case "completed":
				snapshot.State = "completed"
			}
		}
	}
	rows, err := h.DB.Query(r.Context(), workThreadInputsSQL, row.ThreadID, workThreadQueueLimit+1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load queued work thread inputs")
		return
	}
	defer rows.Close()
	for rows.Next() {
		if len(snapshot.QueuedInputs) >= workThreadQueueLimit {
			snapshot.QueueTruncated = true
			break
		}
		var in WorkThreadInput
		var id pgtype.UUID
		var status, summary string
		var createdAt time.Time
		if scanErr := rows.Scan(&id, &status, &summary, &createdAt); scanErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to load queued work thread inputs")
			return
		}
		in.ID, in.Status, in.Summary, in.CreatedAt = uuidToString(id), status, summary, createdAt.UTC().Format(time.RFC3339Nano)
		snapshot.QueuedInputs = append(snapshot.QueuedInputs, in)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load queued work thread inputs")
		return
	}
	if snapshot.State == "idle" && len(snapshot.QueuedInputs) > 0 {
		snapshot.State = "queued"
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s WorkThreadSnapshot) CurrentTurnExists() bool { return s.CurrentTurn != nil }
