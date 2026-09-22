package handler

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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

type workThreadRow struct {
	ThreadID, AgentID, IssueID, ChatSessionID pgtype.UUID
	Generation, MessageLimit, TokenBudget     int32
	LastSessionID, LastTurnID                 pgtype.UUID
	BreakReason                               pgtype.Text
	UpdatedAt                                 time.Time
	CurrentID, CurrentSessionID               pgtype.UUID
	CurrentStatus                             pgtype.Text
	CurrentStartedAt                          pgtype.Timestamptz
}

const workThreadByIssueSQL = `
SELECT wt.id, wt.agent_id, wt.issue_id, wt.chat_session_id,
       wt.context_generation, wt.context_message_limit, wt.context_token_budget,
       wt.last_session_id, wt.last_turn_id, wt.continuity_break_reason, wt.updated_at,
       active.id, active.session_id, active.status, active.started_at
FROM work_thread wt
LEFT JOIN LATERAL (
  SELECT id, session_id, status, started_at
  FROM agent_task_queue
  WHERE work_thread_id = wt.id
    AND status IN ('dispatched', 'running', 'waiting_local_directory')
  ORDER BY created_at DESC, id DESC
  LIMIT 1
) active ON true
WHERE wt.issue_id = $1
ORDER BY wt.updated_at DESC, wt.id DESC
LIMIT 1`

const workThreadByChatSQL = `
SELECT wt.id, wt.agent_id, wt.issue_id, wt.chat_session_id,
       wt.context_generation, wt.context_message_limit, wt.context_token_budget,
       wt.last_session_id, wt.last_turn_id, wt.continuity_break_reason, wt.updated_at,
       active.id, active.session_id, active.status, active.started_at
FROM work_thread wt
LEFT JOIN LATERAL (
  SELECT id, session_id, status, started_at
  FROM agent_task_queue
  WHERE work_thread_id = wt.id
    AND status IN ('dispatched', 'running', 'waiting_local_directory')
  ORDER BY created_at DESC, id DESC
  LIMIT 1
) active ON true
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
		SessionID:  uuidToString(row.CurrentSessionID), QueuedInputs: []WorkThreadInput{},
		Context: WorkThreadContext{Generation: row.Generation, MessageLimit: row.MessageLimit, TokenBudget: row.TokenBudget,
			SummaryAvailable: false, BreakReason: row.BreakReason.String},
		UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if !snapshot.CurrentTurnExists() && row.LastTurnID.Valid {
		snapshot.SessionID = uuidToString(row.LastSessionID)
	}
	if row.CurrentID.Valid {
		snapshot.CurrentTurn = &WorkThreadTurn{ID: uuidToString(row.CurrentID), Status: row.CurrentStatus.String,
			SessionID: uuidToString(row.CurrentSessionID), StartedAt: timestampToString(row.CurrentStartedAt)}
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
	writeJSON(w, http.StatusOK, snapshot)
}

func (s WorkThreadSnapshot) CurrentTurnExists() bool { return s.CurrentTurn != nil }
