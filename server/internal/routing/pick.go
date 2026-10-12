package routing

import (
	"context"
	"errors"
	"strings"
)

// ErrRoutingInactive reports that the workspace has not switched routing on
// (or its settings are incomplete), so nothing can pick a seat on a caller's
// behalf. The caller names a seat itself or turns routing on.
var ErrRoutingInactive = errors.New("routing: not enabled in this workspace")

// PickRequest describes work that has no ticket behind it yet — a chat about
// to open (DENE-1834). The judge reads it the way it reads a ticket: the
// title and the opening text stand in for title and description.
type PickRequest struct {
	Title string
	Text  string
	// Project is the context the chat carries; empty routes generically.
	ProjectID      string
	ProjectName    string
	ProjectDomains []string
}

// PickCandidate is one seat routing considered, with the reason it was set
// aside when it was. Availability is the snapshot word (online / unreachable
// / dead / quota_exhausted / unknown …), so a receipt can say "离线" without a
// second lookup.
type PickCandidate struct {
	ID           string `json:"agent_id"`
	Name         string `json:"agent"`
	Tier         string `json:"tier"`
	TierLabel    string `json:"tier_label,omitempty"`
	Direction    string `json:"direction,omitempty"`
	Availability string `json:"availability"`
	// Excluded is empty for an eligible seat; otherwise the reason it was
	// not pickable right now (the availability word, or a seat-state reason).
	Excluded string `json:"excluded,omitempty"`
}

// Pick is PickSeat's answer. Seat is nil when nothing could be picked, and
// Declined then says why; Reason is the same closed word the ticket comment
// prints (档位 / 兜底 / 负载 …), with Detail its one sentence.
type Pick struct {
	Seat       *Seat
	Reason     PickReason
	Detail     string
	Tier       string
	JudgedTier string
	Declined   string
	Candidates []PickCandidate
	// StrongestGate is what the 最强档门槛 did (DENE-1834).
	StrongestGate StrongestGate
	Trace         *Trace
	// Dispatch is what the 分工规则 did (DENE-1834).
	Dispatch DispatchMatch
}

// PickSeat chooses an executor seat for work that is not a ticket, with the
// same ladder, facts and judge as the todo row, and writes nothing: there is
// no slot to fill. A chat opened without a named agent asks it; so would any
// other caller that must not choose a seat by itself. The 接着做 rule is not
// consulted — a fresh chat relates to no earlier ticket — and 从结果里学 is
// not, because nothing is recorded to learn from.
//
// An inactive workspace is ErrRoutingInactive. A judge that cannot be reached
// is an error wrapping ErrJudgeUnavailable, counted on the breaker like a
// ticket route; while the breaker is open the call declines at once.
func (r *Router) PickSeat(ctx context.Context, workspaceID string, req PickRequest) (Pick, error) {
	settings, err := r.Store.Settings(ctx, workspaceID)
	if err != nil {
		return Pick{}, err
	}
	if !settings.State().Active() {
		return Pick{}, ErrRoutingInactive
	}
	if open, _, reason := r.Breaker.Open(workspaceID); open {
		return Pick{Declined: reason}, nil
	}

	title := clipRunes(strings.TrimSpace(req.Title), 200)
	text := strings.TrimSpace(req.Text)
	issue := Issue{
		Title:              title,
		DescriptionSummary: clipRunes(text, 800),
		Description:        clipRunes(text, 6000),
		ContentHash:        ContentHash(title, text),
		Status:             "todo",
		CreatorType:        "member",
		ProjectID:          req.ProjectID,
		ProjectName:        req.ProjectName,
		ProjectDomains:     req.ProjectDomains,
	}

	ladder := r.Ladder.For(settings)
	scene := ladder.IssueScene(issue)
	roster, err := r.Store.Roster(ctx, workspaceID)
	if err != nil {
		return Pick{}, err
	}
	roster = ForProject(roster, issue.ProjectID)
	candidates := ladder.assignmentCandidates(scene, roster)
	if len(candidates) == 0 {
		return Pick{Declined: "ladder has no seat in this workspace"}, nil
	}
	facts, err := r.Store.RoutingFacts(ctx, workspaceID, seatIDs(candidates), settings.ProviderKeys())
	if err != nil {
		return Pick{}, err
	}
	pick := Pick{Candidates: pickCandidates(candidates, facts.Seats)}
	eligible := EligibleSeats(candidates, facts.Seats)
	if len(eligible) == 0 {
		pick.Declined = "no eligible seat"
		return pick, nil
	}

	state := r.judgeState(issue, scene, eligible)
	state.RoutingPolicy = DefaultRoutingPolicy
	state.PolicyPrompt = settings.EffectivePolicyPrompt()
	state.Seats = OrderSeatViews(candidates, facts.Seats)
	state.ProviderQuotas = EnsureProviderQuotas(facts.Providers, settings.ProviderKeys())
	state.CandidateSeries = ladder.candidateSeries(roster)
	state.EligibleSeats = reasoningSeats(ladder, eligible, roster)

	d, err := r.decide(ctx, workspaceID, settings, issue, state)
	if err != nil {
		r.Breaker.Fail(workspaceID, err)
		return pick, err
	}
	if d.Decider != DeciderNone {
		r.Breaker.Succeed(workspaceID)
	}
	pick.Tier, pick.JudgedTier, pick.Trace = d.Verdict.ExecutorTier, d.RaisedFrom, d.Trace

	verdict := d.Verdict
	eligible, pickRoster, verdict, dispatch := ladder.dispatch(settings.EffectiveDispatchPolicy(), d.Facts, eligible, roster, verdict)
	pick.Dispatch = dispatch
	if dispatch.Capped != "" {
		pick.Tier = verdict.ExecutorTier
	}
	seat, source, why := r.pickConfiguredExecutor(settings, req.ProjectID, ladder, eligible, pickRoster, Seat{}, false, verdict, settings.Threshold(), issue)
	if !verdict.Reasoned && settings.StrongestGateOn() {
		var gate StrongestGate
		seat, gate = ladder.gateStrongest(seat, candidates, eligible, pickRoster, facts.Seats)
		pick.StrongestGate = gate
	}
	tierSource := source
	load, err := r.load(ctx, workspaceID, settings, ladder, pickRoster, seat)
	if err != nil {
		return pick, err
	}
	if source != pickFallback && !verdict.Reasoned && load.Moved && settings.PreferIdle {
		seat, source = load.Seat, pickLoad
		seat.Balanced = load.Detail()
	}
	if source != pickFallback && verdict.PreferredSeries != "" && !dispatch.Filtered {
		preferred, matched, err := r.availableSeriesSeat(ctx, workspaceID, settings, ladder, scene, roster, seat, verdict.PreferredSeries)
		if err != nil {
			return pick, err
		}
		if matched {
			if preferred.ID != seat.ID {
				source = tierSource
			}
			seat = preferred
		}
	}
	if seat.ID == "" {
		pick.Declined = why
		return pick, nil
	}
	if pick.Tier == "" {
		pick.Tier = seat.TierKey
	}
	reason, detail := executorPickReason(issue, true, &seat, source, d.RaisedFrom != "")
	if d.Decider == DeciderReasoning && source == pickJudge {
		detail = "推理模型定档选人：" + verdict.Reason
	}
	if source == pickFallback && why != "" {
		detail += "（" + why + "）"
	}
	if line := pick.StrongestGate.Line(ladder); line != "" {
		detail += "；" + line
	}
	if pick.Trace != nil {
		pick.Trace.Seat = seat.Name
	}
	pick.Seat, pick.Reason, pick.Detail = &seat, reason, detail
	return pick, nil
}

// pickCandidates lists every ladder candidate with its snapshot word and, for
// a seat the snapshot rules out, the reason — so a receipt that ends up on an
// unexpected seat shows the one that was skipped and why.
func pickCandidates(candidates []Seat, snaps map[string]SeatSnapshot) []PickCandidate {
	out := make([]PickCandidate, 0, len(candidates))
	for _, c := range candidates {
		pc := PickCandidate{ID: c.ID, Name: c.Name, Tier: c.TierKey, TierLabel: c.TierLabel, Direction: c.Direction, Availability: AvailabilityUnknown}
		if snap, ok := snaps[c.ID]; ok && snap.Availability != "" {
			pc.Availability = snap.Availability
			if Unselectable(snap.Availability) {
				pc.Excluded = snap.Availability
			}
		}
		out = append(out, pc)
	}
	return out
}
