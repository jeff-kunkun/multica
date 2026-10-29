package handler

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

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
	// sessionColdGateProvider is the only runtime whose size this gate reads.
	// Codex reports its size too since DENE-2756; reading it here would quietly
	// change Codex's default.
	sessionColdGateProvider = "claude"
	// sessionHandoffOutputRunes is how much of the old session's last report
	// the new session is handed.
	sessionHandoffOutputRunes = 600
)

// sessionHandoffCold is why a claim set the agent's last session aside.
const sessionHandoffCold = "cold"

// SessionHandoff tells a run that the server set its agent's last session on
// this issue aside, and hands over what that session last reported. Mirror
// type: internal/daemon/types.go, same JSON names.
type SessionHandoff struct {
	Reason     string `json:"reason"`
	LastOutput string `json:"last_output,omitempty"`
}

// sessionColdAndThick reports whether a session that ended at endedAt, whose
// last step carried lastContextTokens of context, should be set aside rather
// than resumed. A missing size never trips it.
func sessionColdAndThick(endedAt time.Time, lastContextTokens *int64, now time.Time) bool {
	if lastContextTokens == nil || endedAt.IsZero() {
		return false
	}
	return now.Sub(endedAt) > sessionColdAfter && *lastContextTokens > sessionThickTokens
}

// clipHandoffOutput keeps the head of a report within the handoff budget.
func clipHandoffOutput(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= sessionHandoffOutputRunes {
		return s
	}
	runes := []rune(s)
	return strings.TrimSpace(string(runes[:sessionHandoffOutputRunes])) + "…"
}

// sessionSetAside decides whether a comment follow-up should leave the
// agent's last session on the issue and start a new one, and returns the
// handoff for that new session; nil resumes. Any read failure resumes.
func (h *Handler) sessionSetAside(ctx context.Context, task db.AgentTaskQueue, sessionID string) *SessionHandoff {
	var endedAt pgtype.Timestamptz
	var tokens pgtype.Int8
	var lastOutput string
	err := h.DB.QueryRow(ctx, `
WITH runs AS (
    SELECT t.id, t.status, t.result,
           COALESCE(t.completed_at, t.started_at, t.dispatched_at, t.created_at) AS ended_at
    FROM agent_task_queue t
    WHERE t.agent_id = $1 AND t.issue_id = $2 AND t.session_id = $3
      AND t.status IN ('completed', 'failed', 'cancelled')
)
SELECT r.ended_at,
       (SELECT MAX(u.last_context_tokens) FROM task_usage u WHERE u.task_id = r.id AND u.provider = $4),
       COALESCE((SELECT c.result->>'output' FROM runs c
                 WHERE c.status = 'completed' ORDER BY c.ended_at DESC LIMIT 1), '')
FROM runs r
ORDER BY r.ended_at DESC
LIMIT 1`, task.AgentID, task.IssueID, sessionID, sessionColdGateProvider).Scan(&endedAt, &tokens, &lastOutput)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("claim: read session weight failed; resuming", "task_id", uuidToString(task.ID), "error", err)
		}
		return nil
	}

	if !endedAt.Valid || !tokens.Valid || !sessionColdAndThick(endedAt.Time, &tokens.Int64, time.Now()) {
		return nil
	}
	slog.Info("claim: cold, thick session set aside; starting a new one",
		"task_id", uuidToString(task.ID), "context_tokens", tokens.Int64)
	return &SessionHandoff{Reason: sessionHandoffCold, LastOutput: clipHandoffOutput(lastOutput)}
}
