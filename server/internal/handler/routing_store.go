package handler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ReviewerPropertyName is the workspace property the routing layer fills.
//
// It is a `select`, not an `actor`. Actor values are members only today (see
// actorPropertyKinds), so an actor slot cannot name a seat at all — and it
// could not hold "needs no review" either, which has to be a written value
// rather than an empty slot for the fill-only-empty-slots rule to ever close.
const ReviewerPropertyName = "验收席"

// routingStore binds the routing module to this server. Everything the module
// is allowed to touch passes through here, which is also why the module cannot
// write a status: there is no method for it.
type routingStore struct{ h *Handler }

// RoutingStore returns the module's view of this handler.
func (h *Handler) RoutingStore() routing.Store { return routingStore{h: h} }

func (s routingStore) Settings(ctx context.Context, workspaceID string) (routing.Settings, error) {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return routing.Settings{}, err
	}
	ws, err := s.h.Queries.GetWorkspace(ctx, wsID)
	if err != nil {
		return routing.Settings{}, err
	}
	return routing.ParseSettings(ws.Settings), nil
}

func (s routingStore) Issue(ctx context.Context, workspaceID, issueID string) (routing.Issue, error) {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return routing.Issue{}, err
	}
	id, err := util.ParseUUID(issueID)
	if err != nil {
		return routing.Issue{}, err
	}
	row, err := s.h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: wsID})
	if err != nil {
		return routing.Issue{}, err
	}
	return s.issueView(ctx, row)
}

// descriptionSummaryLimit bounds what leaves the deployment. The judge needs
// enough to tell a one-liner from a project; it does not need the body.
const descriptionSummaryLimit = 800

func (s routingStore) issueView(ctx context.Context, row db.Issue) (routing.Issue, error) {
	out := routing.Issue{
		ID:                 util.UUIDToString(row.ID),
		Title:              row.Title,
		DescriptionSummary: clipRunes(row.Description.String, descriptionSummaryLimit),
		// The state table is written against the seven status categories, so a
		// workspace that renamed or added statuses routes identically to one
		// that did not.
		Status:       issuestatus.Effective(ctx, s.h.Queries, row.WorkspaceID, row.Status),
		AssigneeType: row.AssigneeType.String,
		CreatorType:  row.CreatorType,
		CreatorID:    util.UUIDToString(row.CreatorID),
	}
	if !row.AssigneeType.Valid {
		out.AssigneeType = ""
	}
	if row.AssigneeID.Valid {
		out.AssigneeID = util.UUIDToString(row.AssigneeID)
	}
	if row.ProjectID.Valid {
		out.ProjectID = util.UUIDToString(row.ProjectID)
		if p, err := s.h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{
			ID: row.ProjectID, WorkspaceID: row.WorkspaceID,
		}); err == nil {
			out.ProjectName = p.Title
		}
	}
	if labels, err := s.h.Queries.ListLabelsByIssue(ctx, db.ListLabelsByIssueParams{
		IssueID: row.ID, WorkspaceID: row.WorkspaceID,
	}); err == nil {
		for _, l := range labels {
			out.Labels = append(out.Labels, l.Name)
		}
	}
	if row.ParentIssueID.Valid {
		if parent, err := s.h.Queries.GetIssue(ctx, row.ParentIssueID); err == nil &&
			parent.AssigneeType.Valid && parent.AssigneeType.String == "agent" && parent.AssigneeID.Valid {
			if agent, err := s.h.Queries.GetAgent(ctx, parent.AssigneeID); err == nil {
				out.ParentExecutor = agent.Name
			}
		}
	}
	prop, ok, err := s.Reviewer(ctx, util.UUIDToString(row.WorkspaceID))
	if err != nil {
		return out, err
	}
	if ok {
		out.Reviewer = reviewerOptionName(prop, row.Properties)
	}
	return out, nil
}

// reviewerOptionName resolves the stored option id back to its name. The
// routing module addresses options by name, so option ids never leave here.
// An unknown id reads as an empty slot on purpose: a value this server cannot
// interpret is not something the module should hand off to.
func reviewerOptionName(prop routing.ReviewerProperty, properties []byte) string {
	if len(properties) == 0 {
		return ""
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(properties, &values); err != nil {
		return ""
	}
	raw, ok := values[prop.ID]
	if !ok {
		return ""
	}
	var optionID string
	if err := json.Unmarshal(raw, &optionID); err != nil {
		return ""
	}
	for name, id := range prop.Options {
		if id == optionID {
			return name
		}
	}
	return ""
}

func (s routingStore) Roster(ctx context.Context, workspaceID string) (map[string]routing.Agent, error) {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return nil, err
	}
	agents, err := s.h.Queries.ListAgents(ctx, wsID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]routing.Agent, len(agents))
	for _, a := range agents {
		out[a.Name] = routing.Agent{ID: util.UUIDToString(a.ID), Name: a.Name}
	}
	return out, nil
}

func (s routingStore) Reviewer(ctx context.Context, workspaceID string) (routing.ReviewerProperty, bool, error) {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return routing.ReviewerProperty{}, false, err
	}
	props, err := s.h.Queries.ListIssueProperties(ctx, db.ListIssuePropertiesParams{
		WorkspaceID: wsID,
		// Archived definitions are excluded, which is also how the switch
		// hides the slot: archiving the property takes it out of the picker
		// and out of routing's reach while keeping every value already
		// written. No frontend change, no data loss.
		IncludeArchived: false,
	})
	if err != nil {
		return routing.ReviewerProperty{}, false, err
	}
	for _, p := range props {
		if p.Name != ReviewerPropertyName || p.Type != "select" {
			continue
		}
		cfg := parsePropertyConfig(p.Config)
		options := make(map[string]string, len(cfg.Options))
		for _, o := range cfg.Options {
			options[o.Name] = o.ID
		}
		return routing.ReviewerProperty{ID: util.UUIDToString(p.ID), Options: options}, true, nil
	}
	return routing.ReviewerProperty{}, false, nil
}

func (s routingStore) AssignAgentIfUnassigned(ctx context.Context, workspaceID, issueID string, seat routing.Seat) (bool, error) {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return false, err
	}
	id, err := util.ParseUUID(issueID)
	if err != nil {
		return false, err
	}
	seatID, err := util.ParseUUID(seat.ID)
	if err != nil {
		return false, err
	}
	issue, err := s.h.Queries.AssignIssueIfUnassigned(ctx, db.AssignIssueIfUnassignedParams{
		ID: id, WorkspaceID: wsID, AssigneeType: "agent", AssigneeID: seatID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Somebody else filled the slot between the read and this write —
		// which is exactly what the guard exists for. Not an error, and not
		// an assignment this call may claim.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s.publishIssueUpdated(issue)
	// Assignment IS the wake-up: the seat's run starts from the assignment,
	// so routing never needs to mention an agent it just dispatched.
	s.h.IssueService.StartAssignedAgent(ctx, issue)
	return true, nil
}

func (s routingStore) SetReviewerIfUnset(ctx context.Context, workspaceID, issueID, propertyID, optionID string) (bool, error) {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return false, err
	}
	id, err := util.ParseUUID(issueID)
	if err != nil {
		return false, err
	}
	value, err := json.Marshal(optionID)
	if err != nil {
		return false, err
	}
	issue, err := s.h.Queries.SetIssuePropertyValueIfUnset(ctx, db.SetIssuePropertyValueIfUnsetParams{
		ID: id, WorkspaceID: wsID, Key: propertyID, Value: value,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s.publishIssueUpdated(issue)
	return true, nil
}

func (s routingStore) Handoff(ctx context.Context, workspaceID, issueID, assigneeType, assigneeID string) error {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return err
	}
	id, err := util.ParseUUID(issueID)
	if err != nil {
		return err
	}
	target, err := util.ParseUUID(assigneeID)
	if err != nil {
		return err
	}
	issue, err := s.h.Queries.ReassignIssue(ctx, db.ReassignIssueParams{
		ID: id, WorkspaceID: wsID, AssigneeType: assigneeType, AssigneeID: target,
	})
	if err != nil {
		return err
	}
	s.publishIssueUpdated(issue)
	if assigneeType == "agent" {
		s.h.IssueService.StartAssignedAgent(ctx, issue)
	}
	return nil
}

func (s routingStore) HasComment(ctx context.Context, workspaceID, issueID string, kind routing.CommentKind) (bool, error) {
	id, err := util.ParseUUID(issueID)
	if err != nil {
		return false, err
	}
	return s.h.Queries.HasRoutingComment(ctx, db.HasRoutingCommentParams{
		IssueID: id, RoutingKind: string(kind),
	})
}

func (s routingStore) PostComment(ctx context.Context, workspaceID, issueID string, kind routing.CommentKind, body string) error {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return err
	}
	id, err := util.ParseUUID(issueID)
	if err != nil {
		return err
	}
	// author_type='system', author_id=zero UUID, matching the other
	// platform-authored comments. Clients branch on author_type.
	comment, err := s.h.Queries.CreateRoutingComment(ctx, db.CreateRoutingCommentParams{
		IssueID:     id,
		WorkspaceID: wsID,
		AuthorID:    pgtype.UUID{Valid: true},
		Content:     body,
		RoutingKind: string(kind),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The unique index rejected a duplicate: a concurrent Route call got
		// there first. Nothing to publish and nothing to report.
		return nil
	}
	if err != nil {
		return err
	}
	if s.h.Bus != nil {
		s.h.Bus.Publish(events.Event{
			Type:        protocol.EventCommentCreated,
			WorkspaceID: workspaceID,
			ActorType:   "system",
			Payload: map[string]any{
				"comment": map[string]any{
					"id":          util.UUIDToString(comment.ID),
					"issue_id":    util.UUIDToString(comment.IssueID),
					"author_type": comment.AuthorType,
					"author_id":   util.UUIDToString(comment.AuthorID),
					"content":     comment.Content,
					"type":        comment.Type,
					"revision":    comment.Revision,
				},
			},
		})
	}
	return nil
}

// Subscribe adds the notification target to the issue and writes the inbox row
// that actually reaches them.
//
// Both halves are needed. The mention in the comment body is what a reader
// sees, but the notification and subscriber listeners short-circuit on
// author_type='system' — so a routing comment's mention notifies nobody on its
// own, and nothing on the ticket shows the difference. The inbox row is the
// notification; the subscription is what keeps them on the thread afterwards.
func (s routingStore) Subscribe(ctx context.Context, workspaceID, issueID, userID string) error {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return err
	}
	id, err := util.ParseUUID(issueID)
	if err != nil {
		return err
	}
	uid, err := util.ParseUUID(userID)
	if err != nil {
		return err
	}
	if _, err := s.h.Queries.AddIssueSubscriber(ctx, db.AddIssueSubscriberParams{
		IssueID: id, UserType: "member", UserID: uid, Reason: "mentioned",
	}); err != nil {
		return err
	}
	issue, err := s.h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: wsID})
	if err != nil {
		return err
	}
	if _, err := s.h.Queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
		ID:            dbid.NewV7(),
		WorkspaceID:   wsID,
		RecipientType: "member",
		RecipientID:   uid,
		Type:          "routing_needs_you",
		Severity:      "action_required",
		IssueID:       id,
		Title:         issue.Title,
		Body:          pgtype.Text{String: "这张票需要你看一眼——路由没有人会继续推进它。", Valid: true},
		ActorType:     pgtype.Text{String: "system", Valid: true},
		Details:       []byte("{}"),
	}); err != nil {
		return err
	}
	return nil
}

// NotifyTarget is the whole "who do we @" rule: the person who created the
// issue, or the workspace owner when an agent created it. No setting, because
// a setting here is a second place for the answer to be wrong.
func (s routingStore) NotifyTarget(ctx context.Context, workspaceID string, issue routing.Issue) (routing.Member, error) {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return routing.Member{}, err
	}
	if issue.CreatorType == "member" && issue.CreatorID != "" {
		if uid, err := util.ParseUUID(issue.CreatorID); err == nil {
			if m, err := s.h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
				UserID: uid, WorkspaceID: wsID,
			}); err == nil {
				return s.member(ctx, m), nil
			}
		}
	}
	members, err := s.h.Queries.ListMembers(ctx, wsID)
	if err != nil {
		return routing.Member{}, err
	}
	for _, m := range members {
		if m.Role == "owner" {
			return s.member(ctx, m), nil
		}
	}
	return routing.Member{}, nil
}

func (s routingStore) member(ctx context.Context, m db.Member) routing.Member {
	out := routing.Member{UserID: util.UUIDToString(m.UserID)}
	if u, err := s.h.Queries.GetUser(ctx, m.UserID); err == nil {
		out.Name = u.Name
	}
	if strings.TrimSpace(out.Name) == "" {
		out.Name = "there"
	}
	return out
}

func (s routingStore) publishIssueUpdated(issue db.Issue) {
	if s.h.Bus == nil {
		return
	}
	s.h.Bus.Publish(events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   "system",
		Payload:     map[string]any{"issue": issueToResponse(issue, "")},
	})
}

func clipRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}
