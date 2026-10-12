package routing

import (
	"context"
	"fmt"
	"time"
)

// ShadowComparison uses today's roster and policy against a historical ticket.
// It never changes an assignee, starts a run, writes a cache or posts a comment.
type ShadowComparison struct {
	ActualSeat         string             `json:"actual_seat,omitempty"`
	ReferenceTierLabel string             `json:"reference_tier_label"`
	IssueID            string             `json:"issue_id"`
	Title              string             `json:"title"`
	ActualSeatID       string             `json:"actual_seat_id,omitempty"`
	ReferenceTier      string             `json:"reference_tier"`
	Proposed           *ReasoningDecision `json:"proposed,omitempty"`
	Selected           *Seat              `json:"selected,omitempty"`
	Error              string             `json:"error,omitempty"`
	ComparedAt         string             `json:"compared_at"`
	Model              string             `json:"model"`
}

func (r *Router) CompareShadow(ctx context.Context, workspaceID, issueID string) (ShadowComparison, error) {
	settings, err := r.Store.Settings(ctx, workspaceID)
	if err != nil {
		return ShadowComparison{}, err
	}
	if !settings.AnalysisOn() || r.Analyst == nil {
		return ShadowComparison{}, fmt.Errorf("enable the analysis model before running a shadow comparison")
	}
	issue, err := r.Store.Issue(ctx, workspaceID, issueID)
	if err != nil {
		return ShadowComparison{}, err
	}
	roster, err := r.Store.Roster(ctx, workspaceID)
	if err != nil {
		return ShadowComparison{}, err
	}
	roster = ForProject(roster, issue.ProjectID)
	shadowSettings := settings
	shadowSettings.DecisionMode = "active"
	l := r.ladder().For(shadowSettings)
	scene := l.IssueScene(issue)
	eligible, state, err := r.decisionContext(ctx, workspaceID, settings, issue, scene, l.assignmentCandidates(scene, roster))
	if err != nil {
		return ShadowComparison{}, err
	}
	state.EligibleSeats = reasoningSeats(l, eligible, roster)
	policy, rules := settings.EffectiveDispatchPolicy(), r.rules()
	rec, err := r.Analyst.Analyze(ctx, settings.AnalysisTarget(), AnalysisState{JudgeState: state, Description: issue.Description, Categories: policy.CategoryOptions(), Reasoning: true, ReferenceRules: &rules, Preferences: &policy})
	out := ShadowComparison{IssueID: issue.ID, Title: issue.Title, ActualSeatID: issue.AssigneeID, ComparedAt: time.Now().UTC().Format(time.RFC3339), Model: settings.AnalysisTarget().Model}
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	_, row := rules.Match(rec.Facts)
	out.ReferenceTier = row.Tier
	out.ReferenceTierLabel = tierLabel(l, row.Tier)
	if actual, ok := agentByID(roster, issue.AssigneeID); ok {
		out.ActualSeat = actual.Name
	}
	out.Proposed = rec.Decision
	v, why := reasoningVerdict(rec.Decision, settings, l, state.EligibleSeats)
	if why != "" {
		out.Error = why
		return out, nil
	}
	preferred, roster, v, _ := l.dispatch(policy, &rec.Facts, eligible, roster, v)
	seat, _, why := r.pickConfiguredExecutor(settings, issue.ProjectID, l, preferred, roster, Seat{}, false, v, settings.Threshold(), issue)

	if seat.ID != "" {
		out.Selected = &seat
	} else {
		out.Error = why
	}
	return out, nil
}
