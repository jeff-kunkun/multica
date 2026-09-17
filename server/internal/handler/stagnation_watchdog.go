package handler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const watchdogSweepBatchSize = 200

// SweepStagnationWatchdog is the Stage 4 (DENE-233) compensation scan.
// workspaceID.Valid=false scans every workspace (production). Tests pass a
// workspace to stay isolated. now is injected so hit/miss windows are
// deterministic. The only mutation is a system comment; enqueue is skipped
// when (issue, agent) already has queued/dispatched/running/waiting_local_directory.
// Status, backlog promotion, and model selection are never written.
func (h *Handler) SweepStagnationWatchdog(ctx context.Context, now time.Time, workspaceID pgtype.UUID) (candidates, changed int) {
	if h == nil || h.Queries == nil {
		return 0, 0
	}
	now = now.UTC()
	effective := h.childStatusResolver(ctx)

	n, c := h.sweepScanA(ctx, now, workspaceID, effective)
	candidates += n
	changed += c
	n, c = h.sweepScanB(ctx, now, workspaceID, effective)
	candidates += n
	changed += c
	n, c = h.sweepScanC(ctx, now, workspaceID, effective)
	candidates += n
	changed += c
	n, c = h.sweepScanD(ctx, now, workspaceID, effective)
	candidates += n
	changed += c
	return candidates, changed
}

func (h *Handler) sweepScanA(ctx context.Context, now time.Time, workspaceID pgtype.UUID, effective func(db.Issue) (string, error)) (int, int) {
	parents, err := h.Queries.ListWatchdogParentIssues(ctx, db.ListWatchdogParentIssuesParams{
		WorkspaceID: workspaceID,
		MaxRows:     watchdogSweepBatchSize,
	})
	if err != nil {
		slog.Warn("stagnation watchdog: list parents failed", "error", err)
		return 0, 0
	}
	changed := 0
	for _, parent := range parents {
		if ctx.Err() != nil {
			return len(parents), changed
		}
		status, err := effective(parent)
		if err != nil || !parentWatchdogEligible(status) {
			continue
		}
		children, err := h.Queries.ListChildIssues(ctx, parent.ID)
		if err != nil {
			slog.Warn("stagnation watchdog: list children failed", "error", err, "parent_id", uuidToString(parent.ID))
			continue
		}
		hit, err := classifyScanA(children, effective, now)
		if err != nil || hit.empty() {
			continue
		}
		if hit.Kind == scanABarrier {
			active, err := h.assigneeHasActiveTask(ctx, parent)
			if err != nil || active {
				continue
			}
		}
		if h.actOnWatchdogHit(ctx, parent, hit, parent.AssigneeType.String, parent.AssigneeID) {
			changed++
		}
	}
	return len(parents), changed
}

func (h *Handler) sweepScanB(ctx context.Context, now time.Time, workspaceID pgtype.UUID, effective func(db.Issue) (string, error)) (int, int) {
	issues, err := h.Queries.ListWatchdogInProgressIssues(ctx, db.ListWatchdogInProgressIssuesParams{
		StaleBefore: pgtype.Timestamptz{Time: now.Add(-watchdogStaleAfter), Valid: true},
		WorkspaceID: workspaceID,
		MaxRows:     watchdogSweepBatchSize,
	})
	if err != nil {
		slog.Warn("stagnation watchdog: list in_progress failed", "error", err)
		return 0, 0
	}
	changed := 0
	for _, issue := range issues {
		if ctx.Err() != nil {
			return len(issues), changed
		}
		hit, err := classifyScanB(issue, effective, now)
		if err != nil || hit.empty() {
			continue
		}
		active, err := h.Queries.HasActiveTaskForIssue(ctx, issue.ID)
		if err != nil || active {
			continue
		}
		if h.actOnWatchdogHit(ctx, issue, hit, issue.AssigneeType.String, issue.AssigneeID) {
			changed++
		}
	}
	return len(issues), changed
}

func (h *Handler) sweepScanC(ctx context.Context, now time.Time, workspaceID pgtype.UUID, effective func(db.Issue) (string, error)) (int, int) {
	rows, err := h.Queries.ListUnsweptStageWakeupFailures(ctx, db.ListUnsweptStageWakeupFailuresParams{
		Since:       pgtype.Timestamptz{Time: now.Add(-watchdogStaleAfter), Valid: true},
		WorkspaceID: workspaceID,
		MaxRows:     watchdogSweepBatchSize,
	})
	if err != nil {
		slog.Warn("stagnation watchdog: list wakeup failures failed", "error", err)
		return 0, 0
	}
	changed := 0
	for _, row := range rows {
		if ctx.Err() != nil {
			return len(rows), changed
		}
		if !row.ParentIssueID.Valid {
			_ = h.Queries.MarkStageWakeupFailureSwept(ctx, row.ID)
			continue
		}
		parent, err := h.Queries.GetIssue(ctx, row.ParentIssueID)
		if err != nil {
			continue
		}
		status, err := effective(parent)
		if err != nil || !parentWatchdogEligible(status) {
			_ = h.Queries.MarkStageWakeupFailureSwept(ctx, row.ID)
			continue
		}
		children, err := h.Queries.ListChildIssues(ctx, parent.ID)
		if err != nil {
			continue
		}
		hit, err := classifyScanA(children, effective, now)
		if err != nil || hit.Kind != scanABarrier {
			_ = h.Queries.MarkStageWakeupFailureSwept(ctx, row.ID)
			continue
		}
		cHit := watchdogHit{
			Kind:   scanCFailure,
			Marker: watchdogMarkerC,
			Reason: "child-done wake failed (" + row.Kind + ") and the parent still matches scan A barrier",
		}
		acted := h.actOnWatchdogHit(ctx, parent, cHit, parent.AssigneeType.String, parent.AssigneeID)
		_ = h.Queries.MarkStageWakeupFailureSwept(ctx, row.ID)
		if acted {
			changed++
		}
	}
	return len(rows), changed
}

func (h *Handler) sweepScanD(ctx context.Context, now time.Time, workspaceID pgtype.UUID, effective func(db.Issue) (string, error)) (int, int) {
	issues, err := h.Queries.ListWatchdogCloseProtocolIssues(ctx, db.ListWatchdogCloseProtocolIssuesParams{
		WorkspaceID: workspaceID,
		MaxRows:     watchdogSweepBatchSize,
	})
	if err != nil {
		slog.Warn("stagnation watchdog: list close-protocol issues failed", "error", err)
		return 0, 0
	}
	changed := 0
	for _, issue := range issues {
		if ctx.Err() != nil {
			return len(issues), changed
		}
		meta := closeMetadataStrings(issue.Metadata)
		if waitingOn := strings.TrimSpace(meta[closeprotocol.KeyWaitingOn]); waitingOn != "" {
			if h.sweepScanDWaitingOn(ctx, issue, waitingOn, effective) {
				changed++
			}
		}
		if hit := classifyScanDMention(meta, now); !hit.empty() {
			ownerType := meta[closeprotocol.KeyNextOwnerType]
			ownerID, err := util.ParseUUID(meta[closeprotocol.KeyNextOwnerID])
			if err != nil {
				continue
			}
			agentID, err := h.resolveWatchdogAgentID(ctx, issue, ownerType, ownerID)
			if err != nil {
				continue
			}
			active, err := h.Queries.HasActiveTaskForIssueAndAgent(ctx, db.HasActiveTaskForIssueAndAgentParams{
				IssueID: issue.ID,
				AgentID: agentID,
			})
			if err != nil || active {
				continue
			}
			if h.actOnWatchdogHit(ctx, issue, hit, ownerType, ownerID) {
				changed++
			}
		}
	}
	return len(issues), changed
}

func (h *Handler) sweepScanDWaitingOn(ctx context.Context, waiter db.Issue, waitingOn string, effective func(db.Issue) (string, error)) bool {
	waiterStatus, err := effective(waiter)
	if err != nil {
		return false
	}
	waited, ok := h.resolveWaitingOnIssue(ctx, waiter, waitingOn)
	if !ok {
		return false
	}
	waitedStatus, err := effective(waited)
	if err != nil {
		return false
	}
	hit := classifyScanDWaitingOn(waiterStatus, waitedStatus)
	if hit.empty() {
		return false
	}
	active, err := h.assigneeHasActiveTask(ctx, waiter)
	if err != nil || active {
		return false
	}
	return h.actOnWatchdogHit(ctx, waiter, hit, waiter.AssigneeType.String, waiter.AssigneeID)
}

func (h *Handler) resolveWaitingOnIssue(ctx context.Context, waiter db.Issue, waitingOn string) (db.Issue, bool) {
	if id, err := util.ParseUUID(waitingOn); err == nil {
		issue, err := h.Queries.GetIssue(ctx, id)
		if err != nil || issue.WorkspaceID != waiter.WorkspaceID {
			return db.Issue{}, false
		}
		return issue, true
	}
	return h.resolveIssueByIdentifier(ctx, waitingOn, uuidToString(waiter.WorkspaceID))
}

func (h *Handler) assigneeHasActiveTask(ctx context.Context, issue db.Issue) (bool, error) {
	if !issue.AssigneeType.Valid || !issue.AssigneeID.Valid {
		return false, nil
	}
	agentID, err := h.resolveWatchdogAgentID(ctx, issue, issue.AssigneeType.String, issue.AssigneeID)
	if err != nil {
		return false, err
	}
	return h.Queries.HasActiveTaskForIssueAndAgent(ctx, db.HasActiveTaskForIssueAndAgentParams{
		IssueID: issue.ID,
		AgentID: agentID,
	})
}

func (h *Handler) resolveWatchdogAgentID(ctx context.Context, issue db.Issue, ownerType string, ownerID pgtype.UUID) (pgtype.UUID, error) {
	switch ownerType {
	case "agent":
		return ownerID, nil
	case "squad":
		squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID:          ownerID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return pgtype.UUID{}, err
		}
		return squad.LeaderID, nil
	default:
		return pgtype.UUID{}, fmt.Errorf("watchdog: owner type %q is not agent/squad", ownerType)
	}
}

func (h *Handler) actOnWatchdogHit(ctx context.Context, issue db.Issue, hit watchdogHit, mentionType string, mentionID pgtype.UUID) bool {
	if !issue.AssigneeType.Valid && mentionType == "" {
		return false
	}
	recent, err := h.Queries.HasRecentWatchdogComment(ctx, db.HasRecentWatchdogCommentParams{
		IssueID: issue.ID,
		Marker:  pgtype.Text{String: hit.Marker, Valid: true},
		Since:   pgtype.Timestamptz{Time: time.Now().UTC().Add(-watchdogStaleAfter), Valid: true},
	})
	if err != nil {
		slog.Warn("stagnation watchdog: recent-comment check failed", "error", err, "issue_id", uuidToString(issue.ID))
		return false
	}
	if recent {
		return false
	}

	agentID, err := h.resolveWatchdogAgentID(ctx, issue, mentionType, mentionID)
	if err != nil {
		return false
	}
	active, err := h.Queries.HasActiveTaskForIssueAndAgent(ctx, db.HasActiveTaskForIssueAndAgentParams{
		IssueID: issue.ID,
		AgentID: agentID,
	})
	if err != nil {
		return false
	}

	mentionPrefix := ""
	if !active {
		mentionPrefix = h.buildWatchdogMention(ctx, issue.WorkspaceID, mentionType, mentionID)
	}
	content := watchdogCommentBody(mentionPrefix, hit)

	created, err := h.Queries.CreateComment(ctx, db.CreateCommentParams{
		ID:          dbid.NewV7(),
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorType:  "system",
		AuthorID:    pgtype.UUID{Valid: true},
		Content:     content,
		Type:        "system",
		ParentID:    pgtype.UUID{Valid: false},
	})
	if err != nil {
		slog.Warn("stagnation watchdog: create comment failed",
			"error", err,
			"issue_id", uuidToString(issue.ID),
			"kind", string(hit.Kind))
		return false
	}
	comment := created.Comment()
	h.publish(protocol.EventCommentCreated, uuidToString(issue.WorkspaceID), "system", "", map[string]any{
		"comment":             commentToResponse(comment, nil, nil),
		"issue_title":         issue.Title,
		"issue_assignee_type": textToPtr(issue.AssigneeType),
		"issue_assignee_id":   uuidToPtr(issue.AssigneeID),
		"issue_status":        issue.Status,
		"issue_revision":      created.IssueRevision,
	})
	if active {
		return true
	}
	h.enqueueWatchdogTarget(ctx, issue, mentionType, mentionID, agentID, comment.ID)
	return true
}

func watchdogCommentBody(mentionPrefix string, hit watchdogHit) string {
	body := mentionPrefix + "Stagnation watchdog (" + string(hit.Kind) + "): " + hit.Reason + ". Action is mention-only; this scan never writes done, never promotes backlog children, and never changes models.\n\n" + hit.Marker
	return body
}

func (h *Handler) buildWatchdogMention(ctx context.Context, workspaceID pgtype.UUID, ownerType string, ownerID pgtype.UUID) string {
	label, ok := h.resolveAssigneeMentionLabel(ctx, workspaceID, ownerType, ownerID)
	if !ok {
		return ""
	}
	return fmt.Sprintf("[@%s](mention://%s/%s) ", label, ownerType, uuidToString(ownerID))
}

func (h *Handler) enqueueWatchdogTarget(ctx context.Context, issue db.Issue, ownerType string, ownerID, agentID, triggerCommentID pgtype.UUID) {
	if h.TaskService == nil {
		return
	}
	switch ownerType {
	case "agent":
		if _, err := h.TaskService.EnqueueTaskForMention(ctx, issue, agentID, triggerCommentID); err != nil {
			slog.Warn("stagnation watchdog: enqueue agent task failed",
				"error", err,
				"issue_id", uuidToString(issue.ID),
				"agent_id", uuidToString(agentID))
		}
	case "squad":
		if _, err := h.TaskService.EnqueueTaskForSquadLeader(ctx, issue, agentID, ownerID, triggerCommentID); err != nil {
			slog.Warn("stagnation watchdog: enqueue squad leader task failed",
				"error", err,
				"issue_id", uuidToString(issue.ID),
				"squad_id", uuidToString(ownerID),
				"leader_id", uuidToString(agentID))
		}
	}
}

func (h *Handler) recordStageWakeupFailure(ctx context.Context, workspaceID, parentID, childID pgtype.UUID, kind string, recErr error) {
	if h == nil || h.Queries == nil {
		return
	}
	errText := pgtype.Text{}
	if recErr != nil {
		errText = pgtype.Text{String: recErr.Error(), Valid: true}
	}
	if _, err := h.Queries.InsertStageWakeupFailure(ctx, db.InsertStageWakeupFailureParams{
		ID:            dbid.NewV7(),
		WorkspaceID:   workspaceID,
		ParentIssueID: parentID,
		ChildIssueID:  childID,
		Kind:          kind,
		Error:         errText,
	}); err != nil {
		slog.Warn("child done: failed to record stage wakeup failure",
			"error", err,
			"kind", kind,
			"parent_id", uuidToString(parentID),
			"child_id", uuidToString(childID))
	}
}
