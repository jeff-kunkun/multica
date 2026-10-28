package service

import db "github.com/multica-ai/multica/server/pkg/db/generated"

// ShouldResumeIssueSession allows only comment-triggered issue follow-ups to
// continue an existing session. Retry and rerun lineage overrides legacy flags.
func ShouldResumeIssueSession(task db.AgentTaskQueue) bool {
	return task.IssueID.Valid &&
		task.TriggerCommentID.Valid &&
		!task.ForceFreshSession &&
		!task.RetryOfTaskID.Valid &&
		!task.RerunOfTaskID.Valid
}

// ShouldContinueInterruptedSession preserves the existing chat retry behavior.
// Issue retries always start a new CLI session, including legacy rows.
func ShouldContinueInterruptedSession(task db.AgentTaskQueue) bool {
	return !task.IssueID.Valid &&
		task.RetryOfTaskID.Valid &&
		!task.ForceFreshSession &&
		task.SessionID.Valid
}
