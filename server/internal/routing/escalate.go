package routing

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrNotEscalatable is returned when a ticket has no agent executor to move
// up from, or is already over.
var ErrConsultRequired = errors.New("先用 multica issue consult 咨询高一档顾问；仍做不下去再 issue escalate")

var ErrNotEscalatable = errors.New("routing: ticket has no agent executor to escalate from")

// Escalation is the answer to "the executor says this is too hard".
type Escalation struct {
	// Changed is true when the executor slot now holds a different seat.
	Changed bool
	From    string
	To      string
	// AtTop is true when the holder already sits on the strongest rung, so
	// there is nowhere to move it. Nothing is written and nobody is pinged:
	// the reason stays on the ticket for whoever reads it.
	AtTop bool
	// Commented reports that the routing comment for this move was posted.
	Commented bool
}

// Escalate is the only thing an executor may say about who should work on its
// ticket mid-flight (DENE-1033): "this is too hard", with a reason. It takes
// no person and no tier, so it cannot be used to pick a seat. Routing
// re-judges from scratch with the reason in front of it and moves the ticket
// up only if the answer is a stronger seat; a judge that does not say so, or
// is unavailable, still moves it one rung, because the request is exactly
// that.
func (r *Router) Escalate(ctx context.Context, workspaceID, issueID, reason string) (Escalation, error) {
	return r.escalate(ctx, workspaceID, issueID, reason, "")
}

// EscalateReview retains the reviewer and moves exactly one rung above the
// original executor. The reviewer may not become the new executor.
func (r *Router) EscalateReview(ctx context.Context, workspaceID, issueID, executorID, reason string) (Escalation, error) {
	return r.escalate(ctx, workspaceID, issueID, reason, executorID)
}
func (r *Router) escalate(ctx context.Context, workspaceID, issueID, reason, reviewExecutor string) (Escalation, error) {
	var esc Escalation
	settings, issue, done, err := r.admit(ctx, workspaceID, issueID)
	if done != nil {
		if err == nil {
			err = ErrNotEnabled
		}
		return esc, err
	}
	if reviewExecutor != "" {
		issue.AssigneeType, issue.AssigneeID = "agent", reviewExecutor
	}
	switch issue.Status {
	case "done", "cancelled":
		return esc, ErrNotEscalatable
	}
	if issue.AssigneeType != "agent" || issue.AssigneeID == "" {
		return esc, ErrNotEscalatable
	}

	ladder := r.Ladder.For(settings)
	scene := ladder.IssueScene(issue)
	roster, err := r.Store.Roster(ctx, workspaceID)
	if err != nil {
		return esc, err
	}
	roster = ForProject(roster, issue.ProjectID)
	var holder Agent
	for _, a := range roster {
		if a.ID == issue.AssigneeID {
			holder = a
			break
		}
	}
	if holder.ID == "" {
		return esc, ErrNotEscalatable
	}
	esc.From = holder.Name
	// The tag a person put on the seat is its rung; ladder.json's name table
	// is only the fallback for an untagged seat. Reading the name first sent
	// a seat tagged 中 but listed as 强 straight to the strongest rung.
	holderTier := agentTierKey(ladder, holder)
	holderRank := tierRank(ladder, holderTier)
	if holderRank < 0 {
		return esc, ErrNotEscalatable
	}
	if holderRank == 0 {
		esc.AtTop = true
		return esc, nil
	}
	if reviewExecutor == "" && issue.ConsultCount == 0 {
		advisors, _, err := r.AdvisorCandidates(ctx, workspaceID, issueID, issue.AssigneeID)
		if err != nil {
			return esc, err
		}
		if len(advisors) > 0 {
			return esc, ErrConsultRequired
		}
	}
	// The request itself is the signal for 从结果里学 (DENE-1722), whether or
	// not a stronger seat exists to take it.
	if err := r.Store.MarkUnderjudged(ctx, workspaceID, issue.ID, SignalEscalated); err != nil {
		r.log().Warn("routing: escalation not recorded as underjudged", "workspace_id", workspaceID, "issue_id", issue.ID, "error", err)
	}

	candidates := ladder.assignmentCandidates(scene, roster)
	eligible, state, err := r.decisionContext(ctx, workspaceID, settings, issue, scene, candidates)
	if err != nil {
		return esc, err
	}
	state.EscalationReason = strings.TrimSpace(reason)
	stronger := make([]Seat, 0, len(eligible))
	for _, s := range eligible {
		if reviewExecutor != "" && (s.ID == issue.Reviewer.ID || tierRank(ladder, s.TierKey) != holderRank-1) {
			continue
		}
		if rank := tierRank(ladder, s.TierKey); rank >= 0 && rank < holderRank {
			stronger = append(stronger, s)
		}
	}
	if len(stronger) == 0 {
		esc.AtTop = true
		if reviewExecutor != "" {
			return esc, fmt.Errorf("no distinct executor one tier higher; original reviewer retained")
		}
		return esc, nil
	}

	target, preferred := ladder.Tiers[holderRank-1].Key, ""
	if reviewExecutor == "" {
		if d, derr := r.decide(ctx, workspaceID, settings, issue, state); derr == nil && d.Verdict.ExecutorConfidence >= settings.Threshold() {
			if rank := tierRank(ladder, d.Verdict.ExecutorTier); rank >= 0 && rank < holderRank {
				target, preferred = d.Verdict.ExecutorTier, d.Verdict.ExecutorID
			}
		}
	}
	selection := r.routeTicket(ladder, settings, ticketRoute{Ticket: issue, TargetTier: target, PreferredID: preferred, ExplicitTier: true, Candidates: stronger, Roster: roster}, PurposeDo)
	if len(selection.Seats) == 0 {
		_, deliverErr := r.deliver(ctx, workspaceID, issue, CommentKind("escalation_unavailable"), selection.Why, false, Outcome{})
		if deliverErr != nil {
			return esc, deliverErr
		}
		return esc, fmt.Errorf("%s", selection.Why)
	}
	seat := selection.Seats[0]

	if err := r.Store.Handoff(ctx, workspaceID, issue.ID, "agent", seat.ID); err != nil {
		return esc, err
	}
	esc.Changed, esc.To = true, seat.Name

	body := escalationComment(holder.Name, seat.Name, reason) + fmt.Sprintf("\n\n已咨询 %d 次。", issue.ConsultCount)
	if reviewExecutor != "" {
		body = "**验收未通过：超过打回次数上限，升一档重做**\n\n" + holder.Name + " → " + seat.Name + "。原验收席继续验收。\n\n" + reason
	}
	out, err := r.deliver(ctx, workspaceID, issue, CommentKind("escalation:"+issue.AssigneeID+":"+seat.ID), body, false, Outcome{})
	esc.Commented = out.Commented
	return esc, err
}

// tierRank is a rung's position in the ladder, 0 being the strongest; -1 for a
// key the ladder does not know.
func tierRank(l Ladder, key string) int {
	for i, t := range l.Tiers {
		if strings.EqualFold(t.Key, key) {
			return i
		}
	}
	return -1
}

func escalationComment(from, to, reason string) string {
	var b strings.Builder
	b.WriteString("**路由：执行人反馈「太难」，已改派**\n\n")
	b.WriteString("原执行人 " + from + " 反馈这张票超出它的能力，路由重新判断后改派给 " + to + "。")
	if reason = strings.TrimSpace(reason); reason != "" {
		b.WriteString("\n\n> 原因：" + reason)
	}
	return b.String()
}
