package handler

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// UrgentChatRefusal is the 409 body of an urgent send that cannot be delivered
// right now (DENE-1844). Nothing was persisted; the sender picks a next step.
type UrgentChatRefusal struct {
	Error string `json:"error"`
	// Code is runtime_offline: the agent's runtime cannot claim and no online
	// fallback runtime is configured.
	Code string `json:"code"`
	// Reason names the machine and its last heartbeat, e.g.
	// "runtime_offline: Claude (MacBook-Pro.local) last_seen 2026-10-11T09:00:10Z".
	Reason    string   `json:"reason"`
	NextSteps []string `json:"next_steps"`
	// Runtime is the primary runtime's reachability, same shape as a send.
	Runtime *ChatRuntimeResponse `json:"runtime"`
	// FallbackRuntime, when set, explains why the configured fallback was not
	// used either.
	FallbackRuntime *ChatRuntimeResponse `json:"fallback_runtime,omitempty"`
	FallbackReason  string               `json:"fallback_reason,omitempty"`
}

// runtimeCanClaim mirrors the ClaimAgentTask fence: status online and a
// heartbeat inside the claim freshness window. A runtime that merely says
// "online" in the DB but stopped heartbeating would never pick the task up.
func runtimeCanClaim(rt db.AgentRuntime, now time.Time) bool {
	if rt.Status != "online" {
		return false
	}
	seen := rt.LastSeenAt
	if !seen.Valid {
		seen = rt.UpdatedAt
	}
	if !seen.Valid {
		return false
	}
	return now.Sub(seen.Time) <= service.RuntimeClaimFreshnessSeconds*time.Second
}

// fallbackRuntimeCompatible is the one rule for which runtime may stand in for
// another: same owner (never run Kun's agent on someone else's machine) and
// same provider (the agent's model, args and session format are per CLI).
// Shared by the agent-update validation and the urgent dispatch, so the two
// surfaces cannot disagree.
func fallbackRuntimeCompatible(primary, fallback db.AgentRuntime) error {
	if primary.ID == fallback.ID {
		return fmt.Errorf("fallback runtime must differ from the primary runtime")
	}
	if !primary.OwnerID.Valid || !fallback.OwnerID.Valid || primary.OwnerID != fallback.OwnerID {
		return fmt.Errorf("fallback runtime must belong to the same owner as the primary runtime")
	}
	if primary.Provider != fallback.Provider {
		return fmt.Errorf("fallback runtime must run the same provider as the primary runtime (%s)", primary.Provider)
	}
	return nil
}

func chatRuntimeResponse(rt db.AgentRuntime) *ChatRuntimeResponse {
	out := &ChatRuntimeResponse{Status: rt.Status, Name: rt.Name}
	if rt.LastSeenAt.Valid {
		seen := timestampToString(rt.LastSeenAt)
		out.LastSeenAt = &seen
	}
	return out
}

func urgentRuntimeOfflineReason(rt db.AgentRuntime) string {
	seen := "unknown"
	if rt.LastSeenAt.Valid {
		seen = timestampToString(rt.LastSeenAt)
	} else if rt.UpdatedAt.Valid {
		seen = timestampToString(rt.UpdatedAt)
	}
	name := rt.Name
	if rt.CustomName.Valid && rt.CustomName.String != "" {
		name = rt.CustomName.String + " · " + rt.Name
	}
	return fmt.Sprintf("runtime_offline: %s last_seen %s", name, seen)
}

// urgentChatDispatch picks where an urgent turn goes. Primary runtime able to
// claim → primary. Otherwise the configured fallback, if it is still
// compatible and able to claim → fallback. Otherwise a refusal body; the
// caller writes it with 409 and persists nothing.
func (h *Handler) urgentChatDispatch(ctx context.Context, agent db.Agent) (service.DirectChatSendOptions, *UrgentChatRefusal, error) {
	opts := service.DirectChatSendOptions{Urgent: true}
	if !agent.RuntimeID.Valid {
		return opts, nil, nil // AgentReadiness already refused an unbound agent
	}
	now := time.Now()
	primary, err := h.getAgentRuntime(ctx, obsmetrics.RuntimeLookupSourceChat, agent.RuntimeID)
	if err != nil {
		return opts, nil, err
	}
	if runtimeCanClaim(primary, now) {
		return opts, nil, nil
	}
	refusal := &UrgentChatRefusal{
		Error:   "agent runtime is offline and no online fallback runtime is configured",
		Code:    "runtime_offline",
		Reason:  urgentRuntimeOfflineReason(primary),
		Runtime: chatRuntimeResponse(primary),
		NextSteps: []string{
			"send to another agent whose runtime is online (multica agent list --output json shows runtime_availability)",
			"configure a fallback runtime for this agent: multica agent update <agent-id> --fallback-runtime <runtime-id> (same owner and provider as the primary)",
			"or drop --urgent to leave the message queued until the machine reconnects",
		},
	}
	if !agent.FallbackRuntimeID.Valid {
		return opts, refusal, nil
	}
	fallback, err := h.getAgentRuntime(ctx, obsmetrics.RuntimeLookupSourceChat, agent.FallbackRuntimeID)
	if err != nil {
		refusal.FallbackReason = "fallback runtime could not be loaded"
		return opts, refusal, nil
	}
	refusal.FallbackRuntime = chatRuntimeResponse(fallback)
	if err := fallbackRuntimeCompatible(primary, fallback); err != nil {
		// Configuration drifted after it was validated (owner or provider
		// changed). Refuse rather than quietly run on an incompatible machine.
		refusal.FallbackReason = err.Error()
		return opts, refusal, nil
	}
	if !runtimeCanClaim(fallback, now) {
		refusal.FallbackReason = urgentRuntimeOfflineReason(fallback)
		refusal.Error = "agent runtime and its fallback runtime are both offline"
		return opts, refusal, nil
	}
	opts.RuntimeID = pgtype.UUID(fallback.ID)
	return opts, nil, nil
}
