package routing

import (
	"context"
)

// Advisor refusals (DENE-1721). The asking run keeps working either way; the
// reason only tells it why no advice is coming.
const (
	// AdvisorNoStrongSeat: no eligible target or adjacent-tier seat is available.
	AdvisorNoStrongSeat = "no_strong_seat"
	// AdvisorAllUnavailable: there are strong seats, but every one is out of
	// quota, offline or switched off right now.
	AdvisorAllUnavailable = "all_unavailable"
)

// AdvisorCandidates adapts consult availability to the common purpose selector.
// Runtime capability and free-slot checks remain the consult handler's responsibility.
func (r *Router) AdvisorCandidates(ctx context.Context, workspaceID, issueID, askerID string) ([]Seat, string, error) {
	settings, err := r.Store.Settings(ctx, workspaceID)
	if err != nil {
		return nil, "", err
	}
	issue, err := r.Store.Issue(ctx, workspaceID, issueID)
	if err != nil {
		return nil, "", err
	}
	roster, err := r.Store.Roster(ctx, workspaceID)
	if err != nil {
		return nil, "", err
	}
	ladder := r.Ladder.For(settings)
	roster = ForProject(roster, issue.ProjectID)

	issue.AssigneeType, issue.AssigneeID = "agent", askerID
	candidates := ladder.assignmentCandidates(ladder.IssueScene(issue), roster)
	eligible, _, err := r.decisionContext(ctx, workspaceID, settings, issue, ladder.IssueScene(issue), candidates)
	if err != nil {
		return nil, "", err
	}
	selection := r.routeTicket(ladder, settings, ticketRoute{Ticket: issue, Candidates: eligible, Roster: roster}, PurposeConsult)
	if len(selection.Seats) == 0 {
		return nil, AdvisorNoStrongSeat, nil
	}
	return selection.Seats, "", nil
}
