package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// substituteAgent picks a same-tier other-family seat, or one tier down,
// that can take work the failed seat cannot.
func (h *Handler) substituteAgent(ctx context.Context, workspaceID pgtype.UUID, failed db.Agent, avoid []string, issue db.Issue) (db.Agent, routing.Seat, bool) {
	scene := service.IssueDomainScene(ctx, h.Queries, issue).Scene
	projectID := ""
	if issue.ProjectID.Valid {
		projectID = uuidToString(issue.ProjectID)
	}
	projectSeats, err := service.LoadProjectExecutorSeats(ctx, h.Queries, workspaceID, issue.ProjectID)
	if err != nil {
		return db.Agent{}, routing.Seat{}, false
	}
	agents, err := h.Queries.ListAgents(ctx, workspaceID)
	if err != nil {
		return db.Agent{}, routing.Seat{}, false
	}
	roster := make(map[string]routing.Agent, len(agents))
	byID := make(map[string]db.Agent, len(agents))
	seats := newSeatDomains(routingStore{h: h}.domainNames(ctx, workspaceID), agents)
	for _, agent := range agents {
		state := service.AgentSeatState(agent)
		if ok, _ := routing.SeatSelectable(state, routing.SelectContext{ProjectID: projectID, ProjectSeats: projectSeats}); !ok {
			continue
		}
		id := uuidToString(agent.ID)
		tier := ""
		if agent.RoutingTier.Valid {
			tier = agent.RoutingTier.String
		}
		series := service.AgentSeries(ctx, h.Queries, agent)
		seat := routing.Agent{ID: id, Name: agent.Name, Tier: tier, Usage: agent.RoutingUsage, Model: agent.Model.String, RuntimeMode: series.AutomaticSeries, Series: series.Series, State: state}
		seats.fill(&seat, agent)
		roster[agent.Name] = seat
		byID[id] = agent
	}
	// Keep the unavailable holder in the roster for family identity only.
	series := service.AgentSeries(ctx, h.Queries, failed)
	roster[failed.Name] = routing.Agent{ID: uuidToString(failed.ID), Name: failed.Name, Model: failed.Model.String, RuntimeMode: series.AutomaticSeries, Series: series.Series, Tier: failed.RoutingTier.String, State: service.AgentSeatState(failed)}
	holder := routing.Seat{ID: uuidToString(failed.ID), Name: failed.Name}
	if failed.RoutingTier.Valid {
		holder.TierKey = failed.RoutingTier.String
	}
	ladder := routing.DefaultLadder
	if ws, err := h.Queries.GetWorkspace(ctx, workspaceID); err == nil {
		settings := routing.ParseSettings(ws.Settings)
		ladder = ladder.WithDomains(h.domainNameList(ctx, workspaceID)).WithSeatOrder(settings.SeatOrder())
	}
	seat, _, ok := routing.SubstituteSeat(ladder, holder, roster, avoid, scene)
	if !ok {
		return db.Agent{}, routing.Seat{}, false
	}
	agent, found := byID[seat.ID]
	if !found {
		return db.Agent{}, routing.Seat{}, false
	}
	return agent, seat, true
}

// coverDisabledMention is the @ relay: the named seat is switched off, so
// the run goes to another family. ok is false when nobody can take it.
func (h *Handler) coverDisabledMention(ctx context.Context, issue db.Issue, failed db.Agent, authorType, authorID, originator, wsID string) (db.Agent, bool) {
	avoid := []string{}
	for range 4 {
		agent, _, ok := h.substituteAgent(ctx, issue.WorkspaceID, failed, avoid, issue)
		if !ok {
			return db.Agent{}, false
		}
		if h.canInvokeAgent(ctx, agent, authorType, authorID, originator, wsID) {
			return agent, true
		}
		avoid = append(avoid, uuidToString(agent.ID))
	}
	return db.Agent{}, false
}

func (h *Handler) noteMentionRelay(ctx context.Context, issue db.Issue, commentID pgtype.UUID, trigger commentAgentTrigger) {
	if trigger.RelayFromID == "" || !commentID.Valid {
		return
	}
	body := fmt.Sprintf("%s 已停用，不接新活。这条 @ 改由同档另一家模型的 %s 接手。原席位恢复后不会自动把这条叫醒抢回去。", trigger.RelayFromName, trigger.RelayToName)
	h.postSeatRelayComment(ctx, issue, "mention_relay:"+uuidToString(commentID)+":"+trigger.RelayFromID, body)
}

func (h *Handler) noteMissedMentionRelays(ctx context.Context, issue db.Issue, commentID pgtype.UUID, targets []commentMentionTarget) {
	if !commentID.Valid {
		return
	}
	for _, target := range targets {
		if !target.RelayMissed {
			continue
		}
		name := target.RelayedFromName
		if name == "" {
			name = "被 @ 的席位"
		}
		body := fmt.Sprintf("%s 已停用，不接新活，同档和下一档都没有能接的席位。这条 @ 没有人收到。", name)
		h.postSeatRelayComment(ctx, issue, "mention_relay_miss:"+uuidToString(commentID)+":"+target.TargetID, body)
	}
}

func (h *Handler) postSeatRelayComment(ctx context.Context, issue db.Issue, kind, body string) {
	_, err := h.Queries.CreateRoutingComment(ctx, db.CreateRoutingCommentParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorID:    pgtype.UUID{Valid: true},
		Content:     body,
		RoutingKind: kind,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("seat relay: comment failed", "error", err, "issue_id", uuidToString(issue.ID), "kind", kind)
	}
}
