package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// lockChatRetry takes the same session fence as conversion before any task
// row. Recheck execution ownership inside the lock, because a sweeper's failed
// task snapshot may predate the transfer to an issue.
func lockChatRetry(ctx context.Context, q *db.Queries, taskID pgtype.UUID) (bool, error) {
	sessionID, err := q.LockChatSessionForTask(ctx, taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		// A non-chat task has no chat ownership fence.
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock chat retry: %w", err)
	}
	session, err := q.GetChatSession(ctx, sessionID)
	if err != nil {
		return false, err
	}
	return session.Status == "active" && !session.ConvertedIssueID.Valid, nil
}
