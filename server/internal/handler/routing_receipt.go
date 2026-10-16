package handler

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/botsource"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// RoutingReceipt is the one answer to "who got this and why", returned by the
// writes that pick an agent on the caller's behalf — opening a chat without
// naming an agent, creating a ticket without an executor (DENE-1834). Source
// is the same closed word the ticket's 自动选派 comment prints, plus 人工 for
// a seat the caller named itself. Every field
// means what it says without a second lookup.
type RoutingReceipt struct {
	// Source is why this agent: 原话 / 人工 / 档位 / 兜底 / 接着做 / 负载, or
	// one of the non-picks below.
	Source string `json:"source"`
	// Routed is false when the caller named the agent (人工) or nothing was
	// picked; true when routing chose.
	Routed  bool   `json:"routed"`
	AgentID string `json:"agent_id,omitempty"`
	Agent   string `json:"agent,omitempty"`
	// Tier is the ladder rung the decision named (strongest / strong / …);
	// TierLabel is its display word.
	Tier      string `json:"tier,omitempty"`
	TierLabel string `json:"tier_label,omitempty"`
	// Reason is the one-sentence detail behind Source.
	Reason string `json:"reason"`
	// Availability is the chosen agent's runtime word (online / unreachable /
	// dead / unknown …); Offline is true when it cannot run right now.
	Availability string `json:"availability,omitempty"`
	Offline      bool   `json:"offline,omitempty"`
	// Candidates lists every seat routing considered and why one was skipped.
	Candidates []routing.PickCandidate `json:"candidates,omitempty"`
	// Category, Rule and Series are the 分工规则 part (DENE-1834): the kind of
	// work the ticket was read as, the sentence of the policy that matched,
	// and the series the candidates were filtered to. SeriesFallback says
	// why the series was not used when it names nobody available.
	Category       string `json:"category,omitempty"`
	Rule           string `json:"rule,omitempty"`
	Series         string `json:"series,omitempty"`
	SeriesFallback string `json:"series_fallback,omitempty"`
}

func (rr *RoutingReceipt) withDispatch(m routing.DispatchMatch) {
	rr.Category, rr.Rule = m.Category, m.Rule
	if m.Filtered {
		rr.Series = m.Series
	} else if m.Series != "" {
		rr.SeriesFallback = m.Series + " 系列" + m.Fallback + "，换系列"
	}
}

// Receipt sources that are not a pick.
const (
	ReceiptSourceHuman    = string(routing.PickReasonHuman)
	ReceiptSourceInactive = "路由未启用"
	ReceiptSourceNone     = "无可用席位"
	ReceiptSourceFailed   = "路由失败"
)

func (rr RoutingReceipt) Picked() bool { return rr.AgentID != "" }

// receiptFromPick turns a ticketless pick into a receipt.
func receiptFromPick(pick routing.Pick) RoutingReceipt {
	out := RoutingReceipt{Candidates: pick.Candidates, Tier: pick.Tier}
	out.withDispatch(pick.Dispatch)
	if pick.Seat == nil {
		out.Source = ReceiptSourceNone
		out.Reason = pick.Declined
		if offline := offlineCandidates(pick.Candidates); offline != "" {
			out.Reason += "；离线：" + offline
		}
		return out
	}
	out.Routed = true
	out.Source = string(pick.Reason)
	out.AgentID, out.Agent = pick.Seat.ID, pick.Seat.Name
	out.TierLabel = pick.Seat.TierLabel
	if out.Tier == "" {
		out.Tier = pick.Seat.TierKey
	}
	out.Reason = pick.Detail
	for _, c := range pick.Candidates {
		if c.ID == pick.Seat.ID {
			out.Availability = c.Availability
		}
	}
	return out
}

// receiptFromOutcome turns a ticket route into a receipt. When this pass
// wrote nothing because the slot was already filled (the detached route won
// the race), the holder on the ticket is reported with its recorded source.
func (h *Handler) receiptFromOutcome(ctx context.Context, workspaceID, issueID string, out routing.Outcome, err error) RoutingReceipt {
	rr := RoutingReceipt{Tier: out.Tier}
	rr.withDispatch(out.Dispatch)
	if err != nil {
		rr.Source, rr.Reason = ReceiptSourceFailed, err.Error()
		return rr
	}
	if out.ExecutorWritten != nil {
		seat := out.ExecutorWritten
		rr.Routed = true
		rr.Source = string(out.ExecutorReason)
		rr.AgentID, rr.Agent, rr.TierLabel = seat.ID, seat.Name, seat.TierLabel
		if rr.Tier == "" {
			rr.Tier = seat.TierKey
		}
		rr.Reason = out.ExecutorDetail
		rr.Availability, rr.Offline = h.agentAvailability(ctx, workspaceID, seat.ID)
		return rr
	}
	if out.State != routing.StateEnabled && out.Action == routing.ActionSkipped {
		rr.Source, rr.Reason = ReceiptSourceInactive, out.Reason
		return rr
	}
	// Nothing written by this call: report the slot as it stands.
	issue, ierr := h.RoutingStore().Issue(ctx, workspaceID, issueID)
	if ierr == nil && issue.AssigneeType == "agent" && issue.AssigneeID != "" {
		rr.Source = string(heldSource(issue.AssigneeSource))
		rr.Routed = issue.AssigneeSource == routing.SourceRouter
		rr.AgentID = issue.AssigneeID
		if agent, aerr := h.agentInWorkspace(ctx, workspaceID, issue.AssigneeID); aerr == nil {
			rr.Agent = agent.Name
		}
		rr.Reason = "执行席已有人：" + out.Reason
		rr.Availability, rr.Offline = h.agentAvailability(ctx, workspaceID, issue.AssigneeID)
		return rr
	}
	if ierr == nil && issue.AssigneeType == "member" {
		rr.Source, rr.Reason = ReceiptSourceHuman, "执行人是人"
		return rr
	}
	rr.Source, rr.Reason = ReceiptSourceNone, out.Reason
	return rr
}

func heldSource(source string) routing.PickReason {
	switch source {
	case routing.SourceQuote:
		return routing.PickReasonQuote
	case routing.SourceRouter:
		return routing.PickReasonTier
	case routing.SourceSelfClaim:
		return routing.PickReasonSelfClaim
	}
	return routing.PickReasonHuman
}

// receiptForNamedAgent is the receipt for an agent the caller chose itself:
// 人工, with the runtime word so a seat on an offline machine is said now.
func (h *Handler) receiptForNamedAgent(ctx context.Context, workspaceID string, agent db.Agent) RoutingReceipt {
	rr := RoutingReceipt{Source: ReceiptSourceHuman, AgentID: util.UUIDToString(agent.ID), Agent: agent.Name, Reason: namedByCaller(ctx)}
	rr.Availability, rr.Offline = h.agentAvailability(ctx, workspaceID, rr.AgentID)
	if rr.Offline {
		rr.Reason += "；该智能体的运行时离线，消息会排队等它上线"
	}
	return rr
}

// agentAvailability reads one seat's routing snapshot word. Unknown is not
// offline: only a definite unreachable/dead runtime is.
func (h *Handler) agentAvailability(ctx context.Context, workspaceID, agentID string) (string, bool) {
	if h.Routing == nil || agentID == "" {
		return "", false
	}
	facts, err := h.RoutingStore().RoutingFacts(ctx, workspaceID, []string{agentID}, nil)
	if err != nil {
		return "", false
	}
	snap, ok := facts.Seats[agentID]
	if !ok || snap.Availability == "" {
		return routing.AvailabilityUnknown, false
	}
	offline := snap.Availability == routing.AvailabilityUnreachable || snap.Availability == routing.AvailabilityDead
	return snap.Availability, offline
}

func (h *Handler) agentInWorkspace(ctx context.Context, workspaceID, agentID string) (db.Agent, error) {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return db.Agent{}, err
	}
	aID, err := util.ParseUUID(agentID)
	if err != nil {
		return db.Agent{}, err
	}
	return h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: aID, WorkspaceID: wsID})
}

func offlineCandidates(candidates []routing.PickCandidate) string {
	var names []string
	for _, c := range candidates {
		if c.Excluded == routing.AvailabilityUnreachable || c.Excluded == routing.AvailabilityDead {
			names = append(names, c.Name)
		}
	}
	return strings.Join(names, "、")
}

// pickAgentForChat asks routing for the seat a chat should open with. The
// error is the one the caller shows: routing off says so and names the
// override; no seat names the offline ones.
func (h *Handler) pickAgentForChat(ctx context.Context, workspaceID string, req routing.PickRequest) (RoutingReceipt, error) {
	if h.Routing == nil {
		return RoutingReceipt{Source: ReceiptSourceInactive, Reason: "routing is not available on this server"}, routing.ErrRoutingInactive
	}
	pick, err := h.Routing.PickSeat(ctx, workspaceID, req)
	switch {
	case errors.Is(err, routing.ErrRoutingInactive):
		return RoutingReceipt{Source: ReceiptSourceInactive, Reason: "routing is off in this workspace"}, err
	case err != nil:
		rr := receiptFromPick(pick)
		rr.Source, rr.Reason = ReceiptSourceFailed, err.Error()
		return rr, err
	}
	return receiptFromPick(pick), nil
}

// routeIssueSync runs the SAME Route the detached hook runs, inline, and
// turns the outcome into a receipt. Callers that want the answer in the
// create response (an external bot, the CLI) pay the judge's latency
// themselves; everyone else keeps the detached hook. Bounded by routeTimeout
// so a slow model cannot hold the request open forever.
func (h *Handler) routeIssueSync(ctx context.Context, workspaceID, issueID string) RoutingReceipt {
	if h.Routing == nil {
		return RoutingReceipt{Source: ReceiptSourceInactive, Reason: "routing is not available on this server"}
	}
	ctx, cancel := context.WithTimeout(ctx, routeTimeout)
	defer cancel()
	if err := h.Routing.PreAnalyze(ctx, workspaceID, issueID); err != nil {
		slog.Debug("routing pre-analysis unavailable", "workspace_id", workspaceID, "issue_id", issueID, "error", err)
	}
	out, err := h.Routing.Route(ctx, workspaceID, issueID)
	if err != nil {
		slog.Warn("sync routing pass failed", "workspace_id", workspaceID, "issue_id", issueID, "error", err)
	}
	h.ensureAcceptanceRunning(ctx, workspaceID, issueID)
	return h.receiptFromOutcome(ctx, workspaceID, issueID, out, err)
}

// receiptForHeldSlot is the receipt for an executor the caller set itself:
// 人工 for an agent (with its runtime word) or a person.
func (h *Handler) receiptForHeldSlot(ctx context.Context, workspaceID string, assigneeType pgtype.Text, assigneeID pgtype.UUID) RoutingReceipt {
	if assigneeType.String == "agent" && assigneeID.Valid {
		if agent, err := h.agentInWorkspace(ctx, workspaceID, util.UUIDToString(assigneeID)); err == nil {
			return h.receiptForNamedAgent(ctx, workspaceID, agent)
		}
		return RoutingReceipt{Source: ReceiptSourceHuman, AgentID: util.UUIDToString(assigneeID), Reason: namedByCaller(ctx)}
	}
	return RoutingReceipt{Source: ReceiptSourceHuman, Reason: "执行人是人，" + namedByCaller(ctx)}
}

// namedByCaller is the 人工 reason sentence: a bot key names the bot it came
// through, so the receipt says whose hand it was.
func namedByCaller(ctx context.Context) string {
	if src := botsource.FromContext(ctx); src.ID.Valid {
		name := strings.TrimSpace(src.Name)
		if name == "" {
			name = "Bot"
		}
		return "调用方经 " + name + " 指定"
	}
	return "调用方指定"
}
