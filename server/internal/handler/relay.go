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
	"github.com/multica-ai/multica/server/pkg/dbid"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// StallAfter is how long an in_progress / in_review issue may stay silent
// before the manager is told.
const StallAfter = 15 * time.Minute

// alertEventType is the bot-event type carrying manager alerts. It rides the
// existing bot event log, so the Bot webhook / pull feed delivers it with no
// new channel and no cursor change.
const alertEventType = "issue:alert"

var stuckKeywords = []string{"卡住", "卡点", "被阻塞", "blocked", "stuck"}

// RegisterRelay wires the relay: hand-off on completion, and manager alerts
// when work is blocked or stuck.
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
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch issue.Status {
	case "blocked":
		reason := ""
		if id, err := util.ParseUUID(issue.ID); err == nil {
			reason, _ = h.Queries.GetLatestCommentTextForIssue(ctx, id)
		}
		h.pushAlert(ctx, e.WorkspaceID, "blocked", issue, reason)
	case "in_review", "done":
		h.relayHandoff(ctx, e, issue)
	}
}

func (h *Handler) relayOnCommentCreated(e events.Event) {
	p, ok := e.Payload.(map[string]any)
	if !ok || e.ActorType != "agent" {
		return
	}
	c, ok := p["comment"].(CommentResponse)
	if !ok || c.Type != "comment" {
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id, err := util.ParseUUID(c.IssueID)
	if err != nil {
		return
	}
	issue, err := h.Queries.GetIssue(ctx, id)
	if err != nil {
		return
	}
	resp := issueToResponse(issue, h.getIssuePrefix(ctx, issue.WorkspaceID))
	h.pushAlert(ctx, e.WorkspaceID, "blocked", resp, c.Content)
}

func (h *Handler) assigneeName(ctx context.Context, assigneeType *string, assigneeID *string) string {
	if assigneeType == nil || assigneeID == nil {
		return "无人认领"
	}
	id, err := util.ParseUUID(*assigneeID)
	if err != nil {
		return "未知"
	}
	if *assigneeType == "agent" {
		if a, err := h.Queries.GetAgent(ctx, id); err == nil {
			return a.Name
		}
	}
	return "负责人"
}

func clip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

// pushAlert writes one alert into the Bot event log. kind is "blocked" or
// "stalled"; reason says where it is stuck.
func (h *Handler) pushAlert(ctx context.Context, workspaceID, kind string, issue IssueResponse, reason string) {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return
	}
	who := h.assigneeName(ctx, issue.AssigneeType, issue.AssigneeID)
	var text string
	if kind == "stalled" {
		text = fmt.Sprintf("【停摆】%s %s 已 %d 分钟没有动静（状态 %s），请 %s 跟进或经理介入。", issue.Identifier, issue.Title, int(StallAfter.Minutes()), issue.Status, who)
	} else {
		text = fmt.Sprintf("【卡住】%s %s 卡在：%s。该由 %s 去解，解不了请经理介入。", issue.Identifier, issue.Title, clip(reason, 160), who)
	}
	payload, _ := json.Marshal(map[string]any{
		"kind": kind, "issue_id": issue.ID, "identifier": issue.Identifier,
		"title": issue.Title, "status": issue.Status, "assignee": who, "text": text,
	})
	if err := h.Queries.InsertBotEvent(ctx, db.InsertBotEventParams{
		WorkspaceID: wsID, Type: alertEventType, ActorType: "system", ActorID: "relay", Payload: payload,
	}); err != nil {
		slog.Warn("relay alert failed", "kind", kind, "issue_id", issue.ID, "error", err)
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
	rows, err := h.Queries.ListStalledIssuesForBot(ctx, pgtype.Timestamptz{Time: time.Now().Add(-StallAfter), Valid: true})
	if err != nil {
		slog.Warn("stall sweep failed", "error", err)
		return
	}
	for _, r := range rows {
		resp := IssueResponse{
			ID: uuidToString(r.ID), Identifier: fmt.Sprintf("%s-%d", r.IssuePrefix, r.Number),
			Title: r.Title, Status: r.Status, AssigneeType: textToPtr(r.AssigneeType), AssigneeID: uuidToPtr(r.AssigneeID),
		}
		h.pushAlert(ctx, uuidToString(r.WorkspaceID), "stalled", resp, "")
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
	target, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: tid, WorkspaceID: from.WorkspaceID})
	if err != nil {
		return
	}
	content := fmt.Sprintf("接力：[%s](mention://issue/%s) 已转「%s」，请 [@接棒](mention://agent/%s) 接着做。", fromResp.Identifier, fromResp.ID, fromResp.Status, agentID)
	created, err := h.Queries.CreateComment(ctx, db.CreateCommentParams{
		ID: dbid.NewV7(), IssueID: target.ID, WorkspaceID: target.WorkspaceID,
		AuthorType: e.ActorType, AuthorID: parseUUID(e.ActorID), Content: content, Type: "comment",
	})
	if err != nil {
		slog.Warn("relay handoff comment failed", "issue_id", targetIssueID, "error", err)
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
