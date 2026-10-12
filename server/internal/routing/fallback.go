package routing

import (
	"strings"
)

func (l Ladder) fallbackSafe(s Seat, a Agent) bool {
	return s.ID != "" && s.TierKey != "strongest" && (len(l.Tiers) == 0 || s.TierKey != l.Tiers[0].Key) && !s.Upshifted && !strings.HasPrefix(s.Name, "布尔玛") && a.Base != "布尔玛"
}

func fallbackCause(tier string, confidence, threshold float64) string {
	if tier == "" {
		return "没有启用模型"
	}
	if confidence < threshold {
		return "裁决置信度 " + pct(confidence) + " 低于阈值 " + pct(threshold)
	}
	return "本档没有可用席位"
}

func (r *Router) pickConfiguredExecutor(settings Settings, projectID string, l Ladder, candidates []Seat, roster map[string]Agent, label Seat, labelled bool, v Verdict, threshold float64, issues ...Issue) (Seat, string, string) {
	ticket := Issue{ProjectID: projectID}
	if len(issues) > 0 {
		ticket = issues[0]
	}
	if labelled {
		return label, pickLabel, ""
	}
	selection := r.routeTicket(l, settings, ticketRoute{Ticket: ticket, TargetTier: v.ExecutorTier, PreferredID: v.ExecutorID, ExplicitTier: v.ExecutorTier != "" && v.ExecutorConfidence >= threshold, MaxTier: v.MaxFallbackTier, Candidates: candidates, Roster: roster}, PurposeDo)
	if v.ExecutorConfidence >= threshold && len(selection.Seats) > 0 && !selection.Fallback {
		return selection.Seats[0], pickJudge, ""
	}
	why := fallbackCause(v.ExecutorTier, v.ExecutorConfidence, threshold)
	target := v.ExecutorTier
	if target == "" || v.ExecutorConfidence < threshold {
		target = "medium"
	}

	selection = r.routeTicket(l, settings, ticketRoute{Ticket: ticket, TargetTier: target, PreferredID: v.ExecutorID, ExplicitTier: v.ExecutorTier != "" && v.ExecutorConfidence >= threshold, MaxTier: v.MaxFallbackTier, Candidates: candidates, Roster: roster}, PurposeDo)
	if len(selection.Seats) > 0 {
		return selection.Seats[0], pickFallback, why + "；" + selection.Why
	}
	return Seat{}, pickFallback, why + "；" + selection.Why
}
