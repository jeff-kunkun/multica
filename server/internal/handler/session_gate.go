package handler

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The cold-and-thick gate (DENE-668 step 3, DENE-1331). Resuming a session
// whose prompt cache has expired re-pays its whole context at full price; past
// the thickness threshold that costs more than starting over in the same
// working directory.
const (
	// sessionColdAfter is the prompt-cache lifetime: past it nothing of the
	// old session is cached any more.
	sessionColdAfter = time.Hour
	// sessionThickTokens is the last step's context size above which a cold
	// resume costs more than a fresh start.
	sessionThickTokens = 100_000
)

// sessionColdAndThick reports whether a session that ended at endedAt, whose
// last step carried lastContextTokens of context, should be set aside rather
// than resumed. A missing size never trips it: only runtimes that report the
// size (Claude today) are gated.
func sessionColdAndThick(endedAt time.Time, lastContextTokens *int64, now time.Time) bool {
	if lastContextTokens == nil || endedAt.IsZero() {
		return false
	}
	return now.Sub(endedAt) > sessionColdAfter && *lastContextTokens > sessionThickTokens
}

// sessionTooColdAndThick reads the latest finished run of the session and
// applies the gate. Any read failure keeps the session.
func (h *Handler) sessionTooColdAndThick(ctx context.Context, task db.AgentTaskQueue, sessionID string) bool {
	var endedAt pgtype.Timestamptz
	var tokens pgtype.Int8
	err := h.DB.QueryRow(ctx, `
SELECT COALESCE(t.completed_at, t.started_at, t.dispatched_at, t.created_at),
       (SELECT MAX(u.last_context_tokens) FROM task_usage u WHERE u.task_id = t.id)
FROM agent_task_queue t
WHERE t.agent_id = $1 AND t.issue_id = $2 AND t.session_id = $3
  AND t.status IN ('completed', 'failed', 'cancelled')
ORDER BY COALESCE(t.completed_at, t.started_at, t.dispatched_at, t.created_at) DESC
LIMIT 1`, task.AgentID, task.IssueID, sessionID).Scan(&endedAt, &tokens)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("claim: read session weight failed; resuming", "task_id", uuidToString(task.ID), "error", err)
		}
		return false
	}
	if !endedAt.Valid || !tokens.Valid {
		return false
	}
	return sessionColdAndThick(endedAt.Time, &tokens.Int64, time.Now())
}
