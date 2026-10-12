package routing

import "sort"

// Purpose is the reason for selecting a seat. Operational endpoints (dispatch,
// consult and acceptance) are adapters to routeTicket, not separate selectors.
type Purpose string

const (
	PurposeDo      Purpose = "do"
	PurposeConsult Purpose = "consult"
	PurposeReview  Purpose = "review"
)

type ticketRoute struct {
	Ticket       Issue
	TargetTier   string
	PreferredID  string
	ExplicitTier bool
	MaxTier      string
	Candidates   []Seat
	Roster       map[string]Agent
	Facts        *Facts
}
type ticketSelection struct {
	Seats    []Seat
	Tier     string
	Fallback bool
	Why      string
}

// routeTicket is the single seat selector for do, consult and review. Inputs
// have already passed runtime availability checks. Model inference is needed
// only to supply do's target; the other purposes derive it from the executor.
func (r *Router) routeTicket(l Ladder, settings Settings, in ticketRoute, purpose Purpose) ticketSelection {
	if l.dispatchPolicy != nil {
		settings.DispatchPolicy = l.dispatchPolicy
	}
	if in.Facts == nil && in.Ticket.Analysis != nil {
		in.Facts = &in.Ticket.Analysis.Facts
	}
	scene := l.IssueScene(in.Ticket)
	target := in.TargetTier
	strongPeerFirst := false
	holderSeries := ""
	if purpose != PurposeDo {
		holder := seatFromRoster(l, in.Roster, in.Ticket.AssigneeID)
		rank := tierRank(l, holder.TierKey)
		strongPeerFirst = rank == 1
		if a, ok := agentByID(in.Roster, holder.ID); ok {
			holderSeries = l.agentSeries(a)
		}
		if rank < 0 {
			target = "medium"
			in.ExplicitTier = false
		} else if rank == 0 {
			target = l.Tiers[0].Key
			in.ExplicitTier = false
		} else {
			target = l.Tiers[rank-1].Key
			in.ExplicitTier = true
		}
	}
	rank := tierRank(l, target)
	if rank < 0 {
		rank = tierRank(l, "medium")
		if rank < 0 {
			return ticketSelection{Why: "没有可识别的目标档位"}
		}
		target = l.Tiers[rank].Key
		in.ExplicitTier = false
	}
	seats, _, _, _ := l.dispatch(settings.EffectiveDispatchPolicy(), in.Facts, in.Candidates, in.Roster, Verdict{Reasoned: true, ExecutorTier: target})
	eligible := func(seat Seat, at int) bool {
		a, ok := agentByID(in.Roster, seat.ID)
		if !ok || !a.autoPickable() || seat.Upshifted || l.AgentFit(scene, a) == FitOther {
			return false
		}
		if purpose != PurposeDo && seat.ID == in.Ticket.AssigneeID {
			return false
		}
		if in.MaxTier != "" && at < tierRank(l, in.MaxTier) {
			return false
		}
		if at == 0 {
			return at == rank && in.ExplicitTier && a.Usage == UsageAmple
		}
		if at != rank && !(strongPeerFirst && at == 1) && (a.Usage == UsageTight || !l.fallbackSafe(seat)) {
			return false
		}
		return true
	}
	// The requested rung, then exactly one rung above, then one below. Never
	// silently climb multiple rungs or use the strongest as a substitute.
	order := []int{rank, rank - 1, rank + 1}
	// Strong executors consult/review with a peer first. Only then may an
	// explicitly targeted strongest seat with ample quota take this purpose.
	if strongPeerFirst {
		order = []int{1, 0}
	}
	for _, at := range order {
		if at < 0 || at >= len(l.Tiers) || (at == 0 && at != rank) {
			continue
		}
		pool := []Seat{}
		for _, seat := range seats {
			if seat.TierKey == l.Tiers[at].Key && eligible(seat, at) {
				pool = append(pool, seat)
			}
		}
		if len(pool) == 0 {
			continue
		}
		sort.SliceStable(pool, func(i, j int) bool {
			a, _ := agentByID(in.Roster, pool[i].ID)
			b, _ := agentByID(in.Roster, pool[j].ID)
			if l.AgentFit(scene, a) != l.AgentFit(scene, b) {
				return l.AgentFit(scene, a).Rank() < l.AgentFit(scene, b).Rank()
			}
			if strongPeerFirst && at == 1 && holderSeries != "" {
				return l.agentSeries(a) != holderSeries && l.agentSeries(b) == holderSeries
			}
			return false
		})
		best, _ := agentByID(in.Roster, pool[0].ID)
		for i, seat := range pool {
			a, _ := agentByID(in.Roster, seat.ID)
			if l.AgentFit(scene, a).Rank() > l.AgentFit(scene, best).Rank() {
				pool = pool[:i]
				break
			}
		}

		if at == rank && in.PreferredID != "" {
			for i, seat := range pool {
				if seat.ID == in.PreferredID {
					pool[0], pool[i] = pool[i], pool[0]
					break
				}
			}
		}
		why := "目标档可用席位；系列偏好只参与排序"
		fallback := at != rank && !(strongPeerFirst && at == 1)
		if strongPeerFirst && at == 1 {
			why = "强档执行人优先由同档另一位咨询或验收，优先其他系列"
		}
		if fallback {
			why = "目标档无可用席位（已包含其他系列），改用相邻档"
		}
		return ticketSelection{Seats: pool, Tier: l.Tiers[at].Key, Fallback: fallback, Why: why}
	}
	return ticketSelection{Tier: target, Why: "目标档及相邻档均无可用席位；最强档不作兜底，请 Grok 协调"}
}
