package handler

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ChatHistoryMessage is one transcript row as `multica chat history` prints it.
type ChatHistoryMessage struct {
	ID     string `json:"id"`
	Author string `json:"author"`
	Role   string `json:"role"`
	Text   string `json:"text"`
	TS     string `json:"ts"`
}

// ChatHistoryResponse is the payload of GET /api/chat/history.
type ChatHistoryResponse struct {
	Messages   []ChatHistoryMessage `json:"messages"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

// GetChatHistory serves `multica chat history`: the calling task's own chat
// session transcript, oldest-first, honoring ?limit / ?before paging. It lets an
// agent reconstruct the conversation after a lost resume (see the
// continuity-notice split in execenv). It pages through the same
// (created_at, id) cursor the frontend's message list uses, so an agent can walk
// a long session back without re-reading the recent window each time.
func (h *Handler) GetChatHistory(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := h.chatHistorySession(w, r)
	if !ok {
		return
	}
	limit := clampTranscriptLimit(parseHistoryLimit(r.URL.Query().Get("limit")))
	beforeCreatedAt, beforeID := parseTranscriptCursor(r.URL.Query().Get("before"))
	messages, err := h.Queries.ListChatMessagesPage(r.Context(), db.ListChatMessagesPageParams{
		ChatSessionID: sessionID, Limit: int32(limit),
		BeforeCreatedAt: beforeCreatedAt, BeforeID: beforeID,
	})
	if err != nil {
		slog.Error("chat history read failed", append(logger.RequestAttrs(r),
			"error", err, "chat_session_id", uuidToString(sessionID))...)
		writeError(w, http.StatusInternalServerError, "failed to read chat history")
		return
	}
	// ListChatMessagesPage returns newest-first; the response is oldest-first,
	// so emit the rows in reverse.
	out := make([]ChatHistoryMessage, 0, len(messages))
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		role, author := "user", "User"
		if m.Role == "assistant" {
			role, author = "assistant", "Bot"
		}
		out = append(out, ChatHistoryMessage{
			ID:     uuidToString(m.ID),
			Role:   role,
			Text:   m.Content,
			Author: author,
			TS:     m.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
		})
	}
	resp := ChatHistoryResponse{Messages: out}
	// Advertise a cursor when a full page came back, so the agent can page to
	// older messages.
	if len(messages) == limit && len(out) > 0 {
		oldest := messages[len(messages)-1]
		resp.NextCursor = transcriptCursor(oldest.CreatedAt.Time, oldest.ID)
	}
	writeJSON(w, http.StatusOK, resp)
}

// Transcript paging bounds, so an agent cannot dump a long session's whole
// transcript into its context.
const (
	defaultTranscriptLimit = 30
	maxTranscriptLimit     = 50
)

func clampTranscriptLimit(n int) int {
	if n <= 0 {
		return defaultTranscriptLimit
	}
	if n > maxTranscriptLimit {
		return maxTranscriptLimit
	}
	return n
}

// transcriptCursor encodes a (created_at, id) pair into the opaque ?before
// cursor. RFC3339Nano keeps the timestamp lossless and readable to an agent
// that inspects it.
func transcriptCursor(createdAt time.Time, id pgtype.UUID) string {
	return createdAt.UTC().Format(time.RFC3339Nano) + "|" + uuidToString(id)
}

// parseTranscriptCursor splits a ?before cursor back into the (created_at, id)
// tuple ListChatMessagesPage pages by. A missing or malformed cursor returns
// zero values so the read starts at the most recent messages.
func parseTranscriptCursor(before string) (pgtype.Timestamptz, pgtype.UUID) {
	ts, id, ok := strings.Cut(before, "|")
	if !ok {
		return pgtype.Timestamptz{}, pgtype.UUID{}
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return pgtype.Timestamptz{}, pgtype.UUID{}
	}
	uid, err := util.ParseUUID(id)
	if err != nil {
		return pgtype.Timestamptz{}, pgtype.UUID{}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}, uid
}

// chatHistorySession authorizes the request and returns the caller's own chat
// session. It is authorized by the task-scoped token alone: the auth middleware
// deletes client-supplied X-Actor-Source / X-Agent-ID / X-Task-ID from every
// request and re-stamps them only on the mat_ branch (MUL-3428), so a normal
// JWT / mul_ PAT arrives here carrying no task context at all.
//
// The actor check stays anyway, and stays load-bearing: this endpoint returns
// another member's chat history if its task binding is ever wrong, so it names
// the credential it requires rather than inheriting that guarantee silently
// from a middleware two layers away.
func (h *Handler) chatHistorySession(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	if r.Header.Get("X-Actor-Source") != "task_token" {
		writeError(w, http.StatusForbidden, "chat history is only available from within an agent task")
		return pgtype.UUID{}, false
	}
	taskIDHeader := r.Header.Get("X-Task-ID")
	if taskIDHeader == "" {
		writeError(w, http.StatusBadRequest, "missing task context")
		return pgtype.UUID{}, false
	}
	taskUUID, err := util.ParseUUID(taskIDHeader)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid task id")
		return pgtype.UUID{}, false
	}
	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return pgtype.UUID{}, false
	}
	if !task.ChatSessionID.Valid {
		writeError(w, http.StatusBadRequest, "this task is not a chat task")
		return pgtype.UUID{}, false
	}
	// Defense in depth: load the session and confirm it lives in the token's
	// stamped workspace. The token→task binding already guarantees the agent can
	// only reach its own task; this makes a future wiring regression fail closed.
	session, err := h.Queries.GetChatSession(r.Context(), task.ChatSessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "chat session not found")
		return pgtype.UUID{}, false
	}
	if ws := ctxWorkspaceID(r.Context()); ws != "" && uuidToString(session.WorkspaceID) != ws {
		writeError(w, http.StatusForbidden, "chat session does not belong to this workspace")
		return pgtype.UUID{}, false
	}
	return task.ChatSessionID, true
}

// parseHistoryLimit reads the ?limit query param, ignoring junk (the handler
// clamps the range). 0 means "use the default".
func parseHistoryLimit(raw string) int {
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
