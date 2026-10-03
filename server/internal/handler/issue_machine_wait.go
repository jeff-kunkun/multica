package handler

import (
	"context"
	"log/slog"
	"time"

	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/progress"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// machineWait is the close gate's answer to a stop the executor can clear by
// itself (DENE-1212): checks running or red, a conflict, a failed merge, a
// read that failed, a draft. The ticket stays in progress on a clock and the
// patrol wakes the executor; blocked is written only on the escalation round,
// and then it names the person who has to look. lead opens the timeline
// sentence.
func (h *Handler) machineWait(ctx context.Context, issue db.Issue, kind, condition, lead string) statusTransition {
	park := blockwait.ParkMachineWait(parseIssueMetadata(issue.Metadata), kind, condition, h.machineWaitOwner(ctx, issue), time.Now())
	return statusTransition{
		status:       park.Status,
		persistBlock: true,
		block:        park.Record,
		note:         lead + park.Note,
		park:         &park,
	}
}

// machineWaitOwner is the person an escalated machine wait asks: the member
// who opened the ticket, else the first workspace manager. Empty keeps the
// ticket waiting in progress, since a block nobody is named on helps nobody.
func (h *Handler) machineWaitOwner(ctx context.Context, issue db.Issue) string {
	if issue.CreatorType == "member" && issue.CreatorID.Valid {
		return uuidToString(issue.CreatorID)
	}
	managers, err := h.Queries.ListWorkspaceManagerUserIDs(ctx, issue.WorkspaceID)
	if err != nil {
		slog.Warn("machine wait: list managers failed", "issue_id", uuidToString(issue.ID), "error", err)
		return ""
	}
	if len(managers) > 0 && managers[0].Valid {
		return uuidToString(managers[0])
	}
	return ""
}

// applyMachinePark writes what the wait record alone does not: the
// escalation counter, the watched stamp that lets the patrol wake an
// in-progress pause (DENE-1002), and the wait reason as the board's progress
// line. Call it after the status row and the wait record are written.
func (h *Handler) applyMachinePark(ctx context.Context, issue db.Issue, park *blockwait.MachinePark, actorType, actorID string) db.Issue {
	if park == nil {
		return issue
	}
	for key, value := range park.Pairs() {
		h.setIssueMetaString(ctx, issue, key, value)
	}
	// The park's record is the whole wait. A clock left by an earlier round
	// would otherwise outlive it: a stale wake on an escalated block pages
	// the executor for a decision only a person can make.
	h.deleteIssueMeta(ctx, issue, blockwait.KeyWaitTimeout)
	if park.Escalated {
		h.deleteIssueMeta(ctx, issue, blockwait.KeyWakeAt)
		h.deleteIssueMeta(ctx, issue, blockwait.KeyWatched)
	}
	if park.Status == issuestatus.InProgress {
		h.setIssueMetaString(ctx, issue, blockwait.KeyWatched, blockwait.WatchedYes)
	}
	tone := progress.ToneWaiting
	if park.Escalated {
		tone = progress.ToneStuck
	}
	updated, err := h.recordIssueProgress(ctx, issue, progressEntry{
		Text: progress.Clip(park.Progress), Source: progress.SourceWait, Tone: tone, AuthorType: "system",
	})
	if err != nil {
		slog.Warn("machine wait: write progress failed", "issue_id", uuidToString(issue.ID), "error", err)
		return issue
	}
	h.publishIssueProgress(ctx, updated, actorType, actorID)
	return updated
}
