package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Issue relay (kun-main #717, adapted): a hand-off mention when an agent
// finishes, and manager alerts when work is stuck or silent. Alerts ride the
// office event stream as issue.alert, so they reach owners and stream
// subscribers through the existing Bot push; nothing here sends directly.
// The old automatic resumes (block-wait sweeper, routing stale review) stay
// as they are: an alert only tells someone, it never moves work.

// StallAfter is how long an in_progress / in_review issue may stay silent
// before the manager is told.
const StallAfter = 15 * time.Minute

var stuckKeywords = []string{"卡住", "卡点", "被阻塞", "blocked", "stuck"}

// RegisterRelay wires the hand-off and the stuck-comment alert.
func (h *Handler) RegisterRelay(bus *events.Bus) {
	bus.Subscribe(protocol.EventIssueUpdated, h.relayOnIssueUpdated)
	bus.Subscribe(protocol.EventCommentCreated, h.relayOnCommentCreated)
}

func (h *Handler) relayOnIssueUpdated(e events.Event) {
	p, ok := e.Payload.(map[string]any)
	if !ok {
		return
	}
	if changed, _ := p["status_changed"].(bool); !changed {
		return
	}
	issue, ok := p["issue"].(IssueResponse)
	if !ok || (issue.Status != "in_review" && issue.Status != "done") {
		return
	}
	// A workflow step already handed this issue on; the description line
	// must not wake a second agent for the same move.
	if _, handled := p["workflow_handoff"]; handled {
		return
	}
	// A blocked status is not alerted here: issue.status_changed → blocked
	// already reaches the owners as a must-relay event.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h.relayHandoff(ctx, e, issue)
}

// relayCommentID reads the comment id from any comment:created payload shape
// (handler response, raw row, or a field map).
func relayCommentID(payload any) (pgtype.UUID, bool) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return pgtype.UUID{}, false
	}
	var c struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return pgtype.UUID{}, false
	}
	id, err := util.ParseUUID(c.ID)
	return id, err == nil
}

func (h *Handler) relayOnCommentCreated(e events.Event) {
	p, ok := e.Payload.(map[string]any)
	if !ok || e.ActorType != "agent" {
		return
	}
	id, ok := relayCommentID(p["comment"])
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := h.Queries.GetComment(ctx, id)
	if err != nil || c.Type != "comment" || c.AuthorType != "agent" || !c.IssueID.Valid {
		return
	}
	lower := strings.ToLower(c.Content)
	hit := false
	for _, k := range stuckKeywords {
		if strings.Contains(lower, k) {
			hit = true
			break
		}
	}
	if !hit {
		return
	}
	issue, err := h.Queries.GetIssue(ctx, c.IssueID)
	if err != nil || issue.WorkspaceID != c.WorkspaceID || issue.Status == "blocked" {
		return
	}
	h.emitIssueAlert(ctx, issue, h.getIssuePrefix(ctx, issue.WorkspaceID), "stuck", c.Content, c.ID)
}

func (h *Handler) relayAssigneeName(ctx context.Context, assigneeType pgtype.Text, assigneeID pgtype.UUID) string {
	if !assigneeType.Valid || !assigneeID.Valid {
		return "无人认领"
	}
	switch assigneeType.String {
	case "agent":
		if a, err := h.Queries.GetAgent(ctx, assigneeID); err == nil {
			return a.Name
		}
	case "member":
		if u, err := h.Queries.GetUser(ctx, assigneeID); err == nil {
			return u.Name
		}
	}
	return "负责人"
}

func clipAlert(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

// emitIssueAlert writes one issue.alert office event. kind is "stuck" or
// "stalled"; reason says where it is stuck.
func (h *Handler) emitIssueAlert(ctx context.Context, issue db.Issue, prefix, kind, reason string, commentID pgtype.UUID) {
	who := h.relayAssigneeName(ctx, issue.AssigneeType, issue.AssigneeID)
	identifier := issueIdentifier(prefix, issue.Number)
	var text string
	if kind == "stalled" {
		text = fmt.Sprintf("【停摆】%s %s 已 %d 分钟没有动静（状态 %s），请 %s 跟进或经理介入。", identifier, issue.Title, int(StallAfter.Minutes()), issue.Status, who)
	} else {
		text = fmt.Sprintf("【卡住】%s %s 卡在：%s。该由 %s 去解，解不了请经理介入。", identifier, issue.Title, clipAlert(reason, 160), who)
	}
	changes, err := json.Marshal([]map[string]string{
		{"field": "alert", "new": kind},
		{"field": "text", "new": text},
		{"field": "assignee", "new": who},
		{"field": "status", "new": issue.Status},
	})
	if err != nil {
		return
	}
	key := "issue-alert:" + uuidToString(issue.ID) + ":" + kind + ":" + fmt.Sprint(time.Now().UnixMicro())
	if err := h.Queries.EmitIssueAlert(ctx, db.EmitIssueAlertParams{
		WorkspaceID: issue.WorkspaceID, IssueID: issue.ID, CommentID: commentID, Changes: changes, BusinessKey: key,
	}); err != nil {
		slog.Warn("issue relay: alert failed", "kind", kind, "issue_id", uuidToString(issue.ID), "error", err)
	}
}

// RunStallSweep alerts once per silent period for every stalled issue.
func (h *Handler) RunStallSweep(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			h.sweepStalled(ctx)
		}
	}
}

func (h *Handler) sweepStalled(ctx context.Context) {
	rows, err := h.Queries.ListStalledIssuesForAlert(ctx, pgtype.Timestamptz{Time: time.Now().Add(-StallAfter), Valid: true})
	if err != nil {
		slog.Warn("issue relay: stall sweep failed", "error", err)
		return
	}
	for _, r := range rows {
		issue := db.Issue{
			ID: r.ID, WorkspaceID: r.WorkspaceID, Number: r.Number, Title: r.Title, Status: r.Status,
			AssigneeType: r.AssigneeType, AssigneeID: r.AssigneeID,
		}
		h.emitIssueAlert(ctx, issue, r.IssuePrefix, "stalled", "", pgtype.UUID{})
	}
}

// relayHandoff posts the "after done, @next" mention the issue promised. The
// description line looks like: 完成后 @[Codex](mention://agent/<id>) — or, to
// hand off on another issue: 完成后 在 [DENE-9](mention://issue/<id>) @[Codex](mention://agent/<id>).
func (h *Handler) relayHandoff(ctx context.Context, e events.Event, issue IssueResponse) {
	if e.ActorType != "agent" {
		return
	}
	id, err := util.ParseUUID(issue.ID)
	if err != nil {
		return
	}
	row, err := h.Queries.GetIssue(ctx, id)
	if err != nil || !row.Description.Valid {
		return
	}
	for _, line := range strings.Split(row.Description.String, "\n") {
		idx := strings.Index(line, "完成后")
		if idx < 0 {
			continue
		}
		var targetIssue, targetAgent string
		for _, m := range util.ParseMentions(line[idx:]) {
			switch m.Type {
			case "issue":
				if targetIssue == "" {
					targetIssue = m.ID
				}
			case "agent":
				if targetAgent == "" {
					targetAgent = m.ID
				}
			}
		}
		if targetAgent == "" {
			continue
		}
		if targetIssue == "" {
			targetIssue = issue.ID
		}
		h.postHandoff(ctx, e, row, issue, targetIssue, targetAgent)
		return
	}
}

func (h *Handler) postHandoff(ctx context.Context, e events.Event, from db.Issue, fromResp IssueResponse, targetIssueID, agentID string) {
	tid, err := util.ParseUUID(targetIssueID)
	if err != nil {
		return
	}
	aid, err := util.ParseUUID(agentID)
	if err != nil {
		return
	}
	target, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: tid, WorkspaceID: from.WorkspaceID})
	if err != nil {
		return
	}
	if a, err := h.Queries.GetAgent(ctx, aid); err != nil || a.WorkspaceID != from.WorkspaceID {
		return
	}
	actorID, err := util.ParseUUID(e.ActorID)
	if err != nil {
		return
	}
	content := fmt.Sprintf("接力：[%s](mention://issue/%s) 已转「%s」，请 [@接棒](mention://agent/%s) 接着做。", fromResp.Identifier, fromResp.ID, fromResp.Status, agentID)
	created, err := h.Queries.CreateComment(ctx, db.CreateCommentParams{
		ID: dbid.NewV7(), IssueID: target.ID, WorkspaceID: target.WorkspaceID,
		AuthorType: e.ActorType, AuthorID: actorID, Content: content, Type: "comment",
	})
	if err != nil {
		slog.Warn("issue relay: handoff comment failed", "issue_id", targetIssueID, "error", err)
		return
	}
	comment := created.Comment()
	h.publish(protocol.EventCommentCreated, uuidToString(target.WorkspaceID), e.ActorType, e.ActorID, map[string]any{
		"comment": commentToResponse(comment, nil, nil), "issue_title": target.Title,
		"issue_assignee_type": textToPtr(target.AssigneeType), "issue_assignee_id": uuidToPtr(target.AssigneeID),
		"issue_status": target.Status, "issue_revision": created.IssueRevision,
	})
	h.triggerTasksForComment(ctx, target, comment, nil, e.ActorType, e.ActorID, "", nil, nil)
}
