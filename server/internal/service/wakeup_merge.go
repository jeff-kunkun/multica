package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type separateIssueRunKey struct{}

// WithSeparateIssueRun preserves deliberate queue, parallel, handoff and
// restart requests. Automatic wakes use the existing execution instead.
func WithSeparateIssueRun(ctx context.Context) context.Context {
	return context.WithValue(ctx, separateIssueRunKey{}, true)
}
func SeparateIssueRun(ctx context.Context) bool {
	separate, _ := ctx.Value(separateIssueRunKey{}).(bool)
	return separate
}

// lockDefaultIssueEnqueue serializes the active read and insert across comment
// roots and replicas. Callers build network overlays before taking this lock.
func (s *TaskService) lockDefaultIssueEnqueue(ctx context.Context, issueID, agentID pgtype.UUID, mergeActive bool, triggerCommentID, actorUserID pgtype.UUID, attr attribution.Result) (*db.Queries, pgx.Tx, error) {
	if s.TxStarter == nil {
		return s.Queries, nil, nil
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	q := s.Queries.WithTx(tx)
	current, err := q.LockIssueRunControl(ctx, issueID)
	if err == nil {
		automatic := !actorUserID.Valid && attr.Source != attribution.SourceDirectHuman
		if !actorUserID.Valid && triggerCommentID.Valid && !automatic {
			if comment, readErr := q.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{ID: triggerCommentID, WorkspaceID: current.WorkspaceID}); readErr != nil {
				err = readErr
			} else {
				automatic = comment.AuthorType != "member"
			}
		}
		if automatic && (current.Status == "cancelled" || issueExecutionHalted(current)) {
			err = ErrAgentChainBudgetExceeded
		}
	}
	if err == nil && mergeActive && !SeparateIssueRun(ctx) {
		var active bool
		active, err = q.HasActiveTaskForIssueAndAgent(ctx, db.HasActiveTaskForIssueAndAgentParams{IssueID: issueID, AgentID: agentID})
		if err == nil && active {
			err = ErrDuplicatePendingTask
		}
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, nil, fmt.Errorf("merge issue wake: %w", err)
	}
	return q, tx, nil
}

// A retry uses the original input owner's batch; live supplements are bound
// to the actual run so their delivery receipt can distinguish them.
func chatMergeInputOwner(task db.AgentTaskQueue, steered bool) pgtype.UUID {
	if !steered && task.ChatInputTaskID.Valid {
		return task.ChatInputTaskID
	}
	return task.ID
}

func issueExecutionHalted(issue db.Issue) bool {
	halted, _ := util.JSONObjectOrEmpty(issue.Metadata)[agentHaltedMetadataKey].(bool)
	return halted
}

// Call before locking a task row, after any chat row. Stops and retry inserts
// must share the same issue fence so an old failed task cannot revive work.
func lockIssueRetry(ctx context.Context, q *db.Queries, task db.AgentTaskQueue) (bool, error) {
	if !task.IssueID.Valid {
		return true, nil
	}
	issue, err := q.LockIssueRunControl(ctx, task.IssueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return issue.Status != "cancelled" && !issueExecutionHalted(issue), nil
}
