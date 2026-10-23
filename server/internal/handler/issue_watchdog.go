package handler

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const issueWatchdogCommentMarker = "<!-- office-issue-watchdog -->"

// CreateWatchdogNudgeComment leaves an issue-local reminder and invokes the
// assigned agent through the normal mention admission path.
func (h *Handler) CreateWatchdogNudgeComment(ctx context.Context, issueID, assigneeType, assigneeID, reason string) error {
	id, err := util.ParseUUID(issueID)
	if err != nil {
		return errors.New("invalid issue id")
	}
	issue, err := h.Queries.GetIssue(ctx, id)
	if err != nil {
		return errors.New("issue is no longer available")
	}
	if !issue.AssigneeType.Valid || !issue.AssigneeID.Valid ||
		(issue.AssigneeType.String != "agent" && issue.AssigneeType.String != "member") {
		return nil
	}
	if issue.Status != "todo" && issue.Status != "in_progress" {
		return nil
	}
	if issue.AssigneeType.String != assigneeType || uuidToString(issue.AssigneeID) != assigneeID {
		return nil
	}
	if issue.Status == "todo" && issue.FirstExecutedAt.Valid && strings.Contains(reason, "未开工") {
		return nil
	}

	targetType := issue.AssigneeType.String
	targetID := uuidToString(issue.AssigneeID)
	mention := "[@负责员工](mention://" + targetType + "/" + targetID + ")"
	body := strings.TrimSpace(fmt.Sprintf("%s %s %s", mention, reason, issueWatchdogCommentMarker))
	created, err := h.Queries.CreateComment(ctx, db.CreateCommentParams{
		ID:          dbid.NewV7(),
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorType:  "system",
		AuthorID:    pgtype.UUID{Valid: true},
		Content:     body,
		Type:        "system",
	})
	if err != nil {
		return errors.New("failed to add issue watchdog reminder")
	}

	comment := created.Comment()
	h.publishWithSource(ctx, protocol.EventCommentCreated, uuidToString(issue.WorkspaceID), "system", "", map[string]any{
		"comment":             commentToResponse(comment, nil, nil),
		"issue_title":         issue.Title,
		"issue_assignee_type": textToPtr(issue.AssigneeType),
		"issue_assignee_id":   uuidToPtr(issue.AssigneeID),
		"issue_status":        issue.Status,
		"issue_revision":      created.IssueRevision,
	})
	if targetType != "agent" {
		return nil
	}

	if h.TaskService == nil {
		return errors.New("watchdog agent wake service is unavailable")
	}
	originator := h.TaskService.ResolveOriginatorForWatchdogIssue(ctx, issue)
	outcomes, _ := h.triggerTasksForCommentReport(ctx, issue, comment, nil, "system", "",
		uuidToString(originator), nil, nil)
	for _, outcome := range outcomes {
		if outcome.TargetType == "agent" && outcome.TargetID == targetID {
			switch outcome.Status {
			case DispatchQueued, DispatchCoalesced, DispatchDeferred, DispatchSteered:
				return nil
			default:
				return errors.New("assigned agent mention did not start or join a run")
			}
		}
	}
	return errors.New("assigned agent mention did not produce a run")
}
