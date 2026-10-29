package handler

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	issueWatchdogCommentMarker = "<!-- office-issue-watchdog -->"
	// Mirrors issuewatchdog.RunningSilentEventType; that package imports this
	// one in its tests, so the constant cannot be shared without a cycle.
	issueWatchdogRunningSilentEvent = "employee_running_silent"
)

// WatchdogReminderResponse is what `multica issue get` and the issue header
// show about project-group reminders: when the last one went out, for which
// run, and how many were sent for this issue in total.
type WatchdogReminderResponse struct {
	GroupRemindedAt    string `json:"group_reminded_at"`
	GroupReminderCount int64  `json:"group_reminder_count"`
	TaskID             string `json:"task_id"`
}

// watchdogReminder reads delivered reminders from the watchdog's attempt
// log. Display metadata: a failed read leaves the field off.
func (h *Handler) watchdogReminder(ctx context.Context, workspaceID, issueID pgtype.UUID) *WatchdogReminderResponse {
	if h.DB == nil {
		return nil
	}
	var (
		lastAt pgtype.Timestamptz
		count  int64
		key    string
	)
	err := h.DB.QueryRow(ctx, `
SELECT count(*) OVER (), attempted_at, event_key
FROM office_issue_watchdog_attempt
WHERE workspace_id = $1 AND issue_id = $2 AND event_type = $3 AND outcome = 'sent'
ORDER BY attempted_at DESC, id DESC
LIMIT 1`, workspaceID, issueID, issueWatchdogRunningSilentEvent).Scan(&count, &lastAt, &key)
	if err != nil || !lastAt.Valid {
		return nil
	}
	// event_key is running-silent:<task id>:<reminder number for that run>.
	taskID := ""
	if parts := strings.Split(key, ":"); len(parts) == 3 {
		taskID = parts[1]
	}
	return &WatchdogReminderResponse{
		GroupRemindedAt:    timestampToString(lastAt),
		GroupReminderCount: count,
		TaskID:             taskID,
	}
}

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

	return h.postWatchdogMention(ctx, issue, issue.AssigneeType.String, uuidToString(issue.AssigneeID), "负责员工", reason)
}

// CreateWatchdogWakeComment is the review rules' wake: it @mentions any agent
// or member of the workspace on the issue, not only the assignee, and works
// on every open status.
func (h *Handler) CreateWatchdogWakeComment(ctx context.Context, issueID, targetType, targetID, role, reason string) error {
	id, err := util.ParseUUID(issueID)
	if err != nil {
		return errors.New("invalid issue id")
	}
	issue, err := h.Queries.GetIssue(ctx, id)
	if err != nil {
		return errors.New("issue is no longer available")
	}
	switch issue.Status {
	case "done", "cancelled":
		return nil
	}
	if targetType != "agent" && targetType != "member" {
		return errors.New("watchdog wake target must be an agent or a member")
	}
	if _, err := util.ParseUUID(targetID); err != nil {
		return errors.New("invalid watchdog wake target")
	}
	return h.postWatchdogMention(ctx, issue, targetType, targetID, role, reason)
}

// CreateWatchdogNoteComment leaves a marked system comment that @mentions
// nobody, so it starts no run: the review-round stop uses it to lay out
// options for Kun.
func (h *Handler) CreateWatchdogNoteComment(ctx context.Context, issueID, content string) error {
	id, err := util.ParseUUID(issueID)
	if err != nil {
		return errors.New("invalid issue id")
	}
	issue, err := h.Queries.GetIssue(ctx, id)
	if err != nil {
		return errors.New("issue is no longer available")
	}
	switch issue.Status {
	case "done", "cancelled":
		return nil
	}
	_, err = h.postWatchdogComment(ctx, issue, strings.TrimSpace(content+"\n\n"+issueWatchdogCommentMarker))
	return err
}

// CreateWatchdogLabel attaches the workspace's issue label named name (any
// case) to an open issue as the system, and publishes the change. The stop
// routing uses it for the Kun-decision label.
func (h *Handler) CreateWatchdogLabel(ctx context.Context, issueID, name string) error {
	id, err := util.ParseUUID(issueID)
	if err != nil {
		return errors.New("invalid issue id")
	}
	issue, err := h.Queries.GetIssue(ctx, id)
	if err != nil {
		return errors.New("issue is no longer available")
	}
	switch issue.Status {
	case "done", "cancelled":
		return nil
	}
	var labelID pgtype.UUID
	err = h.DB.QueryRow(ctx, `
SELECT id FROM issue_label
WHERE workspace_id = $1 AND resource_type = 'issue' AND lower(name) = lower($2)
ORDER BY id LIMIT 1`, issue.WorkspaceID, strings.TrimSpace(name)).Scan(&labelID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("watchdog label does not exist in the workspace")
	}
	if err != nil {
		return errors.New("failed to find watchdog label")
	}
	attached, err := h.Queries.AttachLabelToIssue(ctx, db.AttachLabelToIssueParams{
		IssueID:        issue.ID,
		LabelID:        labelID,
		WorkspaceID:    issue.WorkspaceID,
		AttachedByType: pgtype.Text{String: "system", Valid: true},
	})
	if err != nil {
		return errors.New("failed to attach watchdog label")
	}
	if !attached.Changed {
		return nil
	}
	labels, err := h.Queries.ListLabelsByIssue(ctx, db.ListLabelsByIssueParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		return nil // attached; subscribers refetch on their next read
	}
	h.publishWithSource(ctx, protocol.EventIssueLabelsChanged, uuidToString(issue.WorkspaceID), "system", "", map[string]any{
		"issue_id":       uuidToString(issue.ID),
		"labels":         labelsToResponse(labels),
		"issue_revision": attached.IssueRevision,
	})
	return nil
}

// postWatchdogMention leaves the marked system comment and, for an agent,
// admits the mention through the normal comment trigger path.
func (h *Handler) postWatchdogMention(ctx context.Context, issue db.Issue, targetType, targetID, role, reason string) error {
	mention := "[@" + role + "](mention://" + targetType + "/" + targetID + ")"
	body := strings.TrimSpace(fmt.Sprintf("%s %s %s", mention, reason, issueWatchdogCommentMarker))
	comment, err := h.postWatchdogComment(ctx, issue, body)
	if err != nil {
		return err
	}
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
				return errors.New("watchdog agent mention did not start or join a run")
			}
		}
	}
	return errors.New("watchdog agent mention did not produce a run")
}

// postWatchdogComment stores and publishes a system comment.
func (h *Handler) postWatchdogComment(ctx context.Context, issue db.Issue, body string) (db.Comment, error) {
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
		return db.Comment{}, errors.New("failed to add issue watchdog reminder")
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
	return comment, nil
}
