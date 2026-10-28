package handler

import (
	"context"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// longRunningLabel is the issue label a person adds to take a run off the
// office idle policy: a quiet run on such an issue is never stopped for its
// silence (DENE-2667).
const longRunningLabel = "长任务"

// issueIdleUnlimited reports whether a person labelled the issue 长任务. A
// label an agent attached does not count, so a run cannot exempt itself, and a
// failed read keeps the normal budgets.
func (h *Handler) issueIdleUnlimited(ctx context.Context, issue db.Issue) bool {
	names, err := h.Queries.ListIssueLabelNamesNotByAgent(ctx, db.ListIssueLabelNamesNotByAgentParams{
		IssueID: issue.ID, WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return false
	}
	for _, name := range names {
		if name == longRunningLabel {
			return true
		}
	}
	return false
}

// hydrateTaskIdleUnlimited marks the issue's active runs idle-unlimited for the
// run row. The daemon fixes a run's policy when it claims the run, so a claimed
// run shows the policy its claim recorded, and a label added or removed mid-run
// shows from the next run. A run not yet claimed shows the current label, which
// is what its claim will read.
func (h *Handler) hydrateTaskIdleUnlimited(ctx context.Context, issue db.Issue, tasks []db.AgentTaskQueue, resp []AgentTaskResponse) {
	if len(tasks) != len(resp) {
		return
	}
	var labelled *bool
	for i, task := range tasks {
		switch task.Status {
		case "dispatched", "waiting_local_directory", "running":
			snap, ok := decodeIssueStateSnapshot(task.IssueSnapshot)
			resp[i].IdleUnlimited = ok && snap.IdleUnlimited
		case "queued", "deferred":
			if labelled == nil {
				v := h.issueIdleUnlimited(ctx, issue)
				labelled = &v
			}
			resp[i].IdleUnlimited = *labelled
		}
	}
}
