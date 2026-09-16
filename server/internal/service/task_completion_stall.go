package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// CompletionStallMarker is the stable substring every completion-stall signal
// carries. It is what makes the signal greppable for the dispatcher's patrol
// and what the repeat guard keys on, so it must not change without a migration
// of the existing comments.
const CompletionStallMarker = "completion-stall:run-completed-without-terminal-status"

// completionStallRepeatWindow collapses repeated signals for the same issue.
// The completion path — unlike the failure path — does not change the issue,
// so a row that stays in_progress would otherwise collect one identical system
// comment per finished run. Same 30-minute window the stagnation watchdog uses
// for its own repeat guard.
const completionStallRepeatWindow = 30 * time.Minute

// HandleCompletedTasks is the completion-path mirror of HandleFailedTasks.
//
// A finished run is NOT a finished issue. CompleteTask deliberately only
// terminates the agent_task_queue row — the executor owns the issue status via
// the CLI — so "run completed, issue still in_progress, nothing queued behind
// it" is a state the failure path's sweep cannot see: that sweep only runs for
// FAILED tasks, where it resets a stuck in_progress issue to todo. A run that
// ends cleanly after delivering part of its acceptance criteria used to leave
// no event at all (issue_child_done only fires on a terminal transition plus a
// closed stage barrier), which is how a partial delivery went unnoticed until a
// human happened to mention it.
//
// This method closes that gap with the same guards the failure path uses:
//
//   - the issue's EFFECTIVE status must be in_progress, so in_review, blocked,
//     every terminal status, and any custom status inheriting one of them are
//     excluded (MUL-6243 semantics — a custom review gate is excluded for the
//     same reason In Review is);
//   - the issue must have no active task. A pending auto-retry is a queued row,
//     so it is covered by the same check rather than by a separate retry probe;
//   - one issue is reported at most once per round, exactly like the failure
//     path's processedIssues set, plus a repeat window across rounds.
//
// The disposition is NOT this method's to make. It never writes the issue
// status — completion is neither todo nor done, and a direct write would also
// race the executor's own in-flight status update. It publishes an observable
// signal instead: a system comment naming the current assignee and the parent
// issue, broadcast as comment:created so the board picks it up immediately.
// Whether to re-wake the executor or move the issue is the dispatcher's call,
// which is why the comment is written directly rather than through the HTTP
// comment path — a mention here must not enqueue a run on its own.
//
// Returns the number of issues signalled.
func (s *TaskService) HandleCompletedTasks(ctx context.Context, tasks []db.AgentTaskQueue) int {
	if len(tasks) == 0 {
		return 0
	}
	processedIssues := make(map[string]bool)
	signalled := 0
	for _, t := range tasks {
		if !t.IssueID.Valid {
			continue
		}
		issueKey := util.UUIDToString(t.IssueID)
		if processedIssues[issueKey] {
			continue
		}
		processedIssues[issueKey] = true
		if s.signalCompletionStall(ctx, issueKey, t.IssueID) {
			signalled++
		}
	}
	return signalled
}

// completionStallEligible is the whole detection rule, kept pure so both sides
// are covered without a database. effectiveStatus is the canonical status the
// issue's key inherits; hasActiveTask reports whether anything is queued
// (including a queued auto-retry), dispatched, running, or waiting on a local
// directory for the issue.
func completionStallEligible(effectiveStatus string, hasActiveTask bool) bool {
	return effectiveStatus == issuestatus.InProgress && !hasActiveTask
}

func (s *TaskService) signalCompletionStall(ctx context.Context, issueKey string, issueID pgtype.UUID) bool {
	issue, err := s.Queries.GetIssue(ctx, issueID)
	if err != nil {
		slog.Warn("completion stall: load issue failed", "issue_id", issueKey, "error", err)
		return false
	}
	effectiveStatus := issuestatus.Effective(ctx, s.Queries, issue.WorkspaceID, issue.Status)

	hasActive, err := s.Queries.HasActiveTaskForIssue(ctx, issueID)
	if err != nil {
		slog.Warn("completion stall: active check failed", "issue_id", issueKey, "error", err)
		return false
	}
	if !completionStallEligible(effectiveStatus, hasActive) {
		return false
	}

	recent, err := s.Queries.HasRecentWatchdogComment(ctx, db.HasRecentWatchdogCommentParams{
		IssueID: issueID,
		Marker:  pgtype.Text{String: CompletionStallMarker, Valid: true},
		Since:   pgtype.Timestamptz{Time: time.Now().UTC().Add(-completionStallRepeatWindow), Valid: true},
	})
	if err != nil {
		slog.Warn("completion stall: repeat check failed", "issue_id", issueKey, "error", err)
		return false
	}
	if recent {
		return false
	}

	created, err := s.Queries.CreateComment(ctx, db.CreateCommentParams{
		ID:          dbid.NewV7(),
		IssueID:     issueID,
		WorkspaceID: issue.WorkspaceID,
		AuthorType:  "system",
		AuthorID:    pgtype.UUID{Valid: true},
		Content:     s.completionStallNotice(ctx, issue),
		Type:        "system",
		ParentID:    pgtype.UUID{Valid: false},
	})
	if err != nil {
		slog.Warn("completion stall: create signal comment failed", "issue_id", issueKey, "error", err)
		return false
	}

	comment := created.Comment()
	if s.Bus != nil {
		s.Bus.Publish(events.Event{
			Type:        protocol.EventCommentCreated,
			WorkspaceID: util.UUIDToString(issue.WorkspaceID),
			ActorType:   "system",
			ActorID:     "",
			Payload: map[string]any{
				"comment":        commentEventFields(comment),
				"issue_title":    issue.Title,
				"issue_status":   issue.Status,
				"issue_revision": created.IssueRevision,
			},
		})
	}

	slog.Info("completion stall: signalled run completed without a terminal issue",
		"issue_id", issueKey,
		"workspace_id", util.UUIDToString(issue.WorkspaceID),
		"status", issue.Status,
	)
	return true
}

// completionStallNotice renders the signal body. It names the current assignee
// and, when there is one, the parent issue the dispatcher has to fold the
// decision back into.
func (s *TaskService) completionStallNotice(ctx context.Context, issue db.Issue) string {
	assigneeLabel, assigneeLink := s.resolveAssigneeDisplay(ctx, issue)
	if assigneeLabel == "" {
		assigneeLabel = "the current assignee"
	}
	mention := ""
	if assigneeLink != "" {
		mention = assigneeLink + " "
	}

	parent := ""
	if issue.ParentIssueID.Valid {
		label := "parent issue"
		if p, err := s.Queries.GetIssue(ctx, issue.ParentIssueID); err == nil {
			label = IssueIdentifier(s.getIssuePrefix(issue.WorkspaceID), p.Number)
		}
		parent = fmt.Sprintf(" Parent: [%s](mention://issue/%s).",
			sanitizeMentionLabel(label), util.UUIDToString(issue.ParentIssueID))
	}

	return fmt.Sprintf(
		"%sStalled run: a run on this issue just completed, the issue is still `in_progress`, and nothing is "+
			"queued for it — so no executor is working on it. Run completion is not delivery completion: the "+
			"executor owns the issue status and did not terminate it. Assignee on record: %s.%s This signal is "+
			"mention-only — the status is deliberately left untouched and no run was started, so decide whether "+
			"to continue the work or close the issue out.\n\n%s",
		mention,
		assigneeLabel,
		parent,
		CompletionStallMarker,
	)
}

// resolveAssigneeDisplay returns a plain label and, for agent/squad assignees,
// a mention link. Member assignees are named without a mention:// link on
// purpose: this signal reports a stall, and paging a human is a decision for
// the dispatcher, not a side effect of detection.
func (s *TaskService) resolveAssigneeDisplay(ctx context.Context, issue db.Issue) (label, link string) {
	if !issue.AssigneeType.Valid || !issue.AssigneeID.Valid {
		return "", ""
	}
	switch issue.AssigneeType.String {
	case "agent":
		agent, err := s.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
			ID:          issue.AssigneeID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return "", ""
		}
		name := sanitizeMentionLabel(agent.Name)
		return name, fmt.Sprintf("[@%s](mention://agent/%s)", name, util.UUIDToString(issue.AssigneeID))
	case "squad":
		squad, err := s.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID:          issue.AssigneeID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return "", ""
		}
		name := sanitizeMentionLabel(squad.Name)
		return name, fmt.Sprintf("[@%s](mention://squad/%s)", name, util.UUIDToString(issue.AssigneeID))
	case "member":
		user, err := s.Queries.GetUser(ctx, issue.AssigneeID)
		if err != nil {
			return "", ""
		}
		return sanitizeMentionLabel(user.Name), ""
	default:
		return "", ""
	}
}

// sanitizeMentionLabel strips characters that would break the mention markdown
// if a name contained them. The mention regex is non-greedy on the label, so a
// stray `]` would short-circuit it. Kept local because the service has no other
// mention renderer; the handler's copy serves the child-done and watchdog
// comments.
func sanitizeMentionLabel(name string) string {
	cleaned := strings.ReplaceAll(name, "]", "")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return "assignee"
	}
	return cleaned
}
