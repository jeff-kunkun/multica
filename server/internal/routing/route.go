package routing

import (
	"context"
	"errors"
	"log/slog"
	"strings"
)

// Action is what a Route call did, for logging, the CLI, and tests.
type Action string

const (
	// ActionSkipped — routing is not active for this workspace, or the ticket
	// is one it never touches. No request, no write, no comment, no mention.
	ActionSkipped Action = "skipped"
	// ActionNoop — this status has no routing behaviour (in_progress, done,
	// cancelled, backlog), or there was nothing left to fill.
	ActionNoop Action = "noop"
	// ActionAssigned — the todo row ran.
	ActionAssigned Action = "assigned"
	// ActionHandedOff — the in-review row ran.
	ActionHandedOff Action = "handed_off"
	// ActionAdvised — the blocked row ran. Writes nothing.
	ActionAdvised Action = "advised"
	// ActionUnavailable — the model could not be reached.
	ActionUnavailable Action = "unavailable"
)

// Outcome reports what happened, so the CLI entry point and the hook entry
// point can be shown to produce the same result on the same ticket.
type Outcome struct {
	State  State
	Action Action
	// Reason is a short machine-readable note for logs, not user copy.
	Reason string
	// ExecutorWritten is the seat this call put in the executor slot, if any.
	ExecutorWritten *Seat
	// ReviewerWritten is the reviewer option name this call wrote, if any.
	ReviewerWritten string
	// Mentioned reports whether this call notified a person.
	Mentioned bool
	// Commented reports whether this call posted a routing comment.
	Commented bool
}

// Router is the module both entry points call. The HTTP hooks call Route
// directly as a function; the CLI subcommand reaches the same Route through a
// small endpoint. There is no second implementation and no subprocess: routing
// a ticket must not cost a process start.
type Router struct {
	Store   Store
	Judge   Judge
	Breaker *Breaker
	Ladder  Ladder
	Log     *slog.Logger
}

// New builds a Router with the shipped ladder and breaker.
func New(store Store, judge Judge) *Router {
	return &Router{Store: store, Judge: judge, Breaker: NewBreaker(), Ladder: DefaultLadder}
}

func (r *Router) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// Route answers "given this status, who should be holding this ticket", and
// applies the answer. It is called on issue creation and on every status
// change, and it is the only entry point: adding behaviour for a status is a
// row in the table below, not another hook.
//
// Route never writes status, stage, description, labels, or children, never
// merges and never closes. The four things it may touch are the assignee, the
// reviewer property, its own comments, and the subscriber list — and the last
// one only so a mention actually notifies.
func (r *Router) Route(ctx context.Context, workspaceID, issueID string) (Outcome, error) {
	settings, err := r.Store.Settings(ctx, workspaceID)
	if err != nil {
		return Outcome{State: StateOff, Action: ActionSkipped, Reason: "settings unreadable"}, err
	}
	state := settings.State()
	if !state.Active() {
		// Off and incomplete are the pre-existing code path, to the letter:
		// no request, no write, no comment, and no mention.
		return Outcome{State: state, Action: ActionSkipped, Reason: "routing not enabled"}, nil
	}

	// The breaker is checked before the issue is even loaded. While it is
	// cooling down the workspace is "ineffective": the reason belongs in the
	// settings section, and a ticket must not be told about it again.
	if open, _, reason := r.Breaker.Open(workspaceID); open {
		return Outcome{State: StateIneffective, Action: ActionSkipped, Reason: reason}, nil
	}

	issue, err := r.Store.Issue(ctx, workspaceID, issueID)
	if err != nil {
		return Outcome{State: state, Action: ActionSkipped, Reason: "issue unreadable"}, err
	}

	if issue.AssignedToHuman() {
		// A person's ticket is a person's ticket. Nothing is filled, nothing
		// is said, nobody is pinged.
		return Outcome{State: state, Action: ActionSkipped, Reason: "assignee is a person"}, nil
	}

	switch issue.Status {
	case "todo":
		return r.routeTodo(ctx, workspaceID, settings, issue)
	case "in_review":
		return r.routeInReview(ctx, workspaceID, issue)
	case "blocked":
		return r.routeBlocked(ctx, workspaceID, settings, issue)
	case "in_progress", "done", "cancelled", "backlog":
		// in_progress: somebody is working, do not interrupt.
		// backlog: nobody intends to work on it yet.
		// done / cancelled: over.
		return Outcome{State: state, Action: ActionNoop, Reason: "status has no routing behaviour"}, nil
	default:
		// Unknown category. Fail closed: an unrecognised status is not a
		// licence to guess who should hold the ticket.
		return Outcome{State: state, Action: ActionNoop, Reason: "unknown status category " + issue.Status}, nil
	}
}

// routeTodo is the only row that fills slots.
func (r *Router) routeTodo(ctx context.Context, workspaceID string, settings Settings, issue Issue) (Outcome, error) {
	out := Outcome{State: StateEnabled, Action: ActionAssigned}

	needExecutor := issue.AssigneeType == ""
	prop, hasReviewerSlot, err := r.Store.Reviewer(ctx, workspaceID)
	if err != nil {
		return Outcome{State: StateEnabled, Action: ActionSkipped, Reason: "reviewer property unreadable"}, err
	}
	needReviewer := hasReviewerSlot && issue.Reviewer == ""

	if !needExecutor && !needReviewer {
		// Both slots already hold a value, whoever wrote them. Repeated status
		// flips land here: nothing to fill, so nothing is asked, written, or
		// said. This is where idempotence comes from — no bookkeeping, just
		// the emptiness of the slots.
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "no empty slot"}, nil
	}

	direction := r.Ladder.Direction(issue.ProjectName)
	roster, err := r.Store.Roster(ctx, workspaceID)
	if err != nil {
		return Outcome{State: StateEnabled, Action: ActionSkipped, Reason: "roster unreadable"}, err
	}
	candidates := r.Ladder.Candidates(direction, roster)
	if len(candidates) == 0 {
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "ladder has no seat in this workspace"}, nil
	}

	verdict, err := r.Judge.Assign(ctx, settings.Model, r.judgeState(issue, direction, candidates))
	if err != nil {
		return r.reportUnavailable(ctx, workspaceID, issue, err)
	}
	r.Breaker.Succeed(workspaceID)

	threshold := settings.Threshold()

	// --- executor slot ---------------------------------------------------
	var executor *Seat
	executorConfident := verdict.ExecutorConfidence >= threshold
	if needExecutor && executorConfident {
		if seat, ok := SeatByTier(candidates, verdict.ExecutorTier); ok {
			written, err := r.Store.AssignAgentIfUnassigned(ctx, workspaceID, issue.ID, seat)
			if err != nil {
				return out, err
			}
			if written {
				executor = &seat
				out.ExecutorWritten = &seat
			}
		} else {
			// The judge named a rung that is not on the ladder. That is a
			// broken answer, not an unconfident one: treat it as unfilled.
			executorConfident = false
		}
	}

	// --- reviewer slot ---------------------------------------------------
	reviewerName := ""
	reviewerConfident := verdict.ReviewerConfidence >= threshold
	if needReviewer && reviewerConfident {
		name, ok := r.reviewerOptionName(verdict, candidates, executor, issue)
		if ok {
			if optID, exists := prop.Options[name]; exists {
				written, err := r.Store.SetReviewerIfUnset(ctx, workspaceID, issue.ID, prop.ID, optID)
				if err != nil {
					return out, err
				}
				if written {
					reviewerName = name
					out.ReviewerWritten = name
				}
			} else {
				r.log().Warn("routing: reviewer option missing from property",
					"workspace_id", workspaceID, "issue_id", issue.ID, "option", name)
				reviewerConfident = false
			}
		} else {
			reviewerConfident = false
		}
	}

	// --- notify ----------------------------------------------------------
	// The one condition that earns an @: the ticket is in a state where
	// nobody will move it. Here that means the executor slot is still empty,
	// so the ticket sits in todo until a person notices.
	stillUnassigned := needExecutor && executor == nil
	body := r.assignmentComment(issue, direction, candidates, verdict, threshold,
		executor, reviewerName, needExecutor, needReviewer, hasReviewerSlot, stillUnassigned)

	return r.deliver(ctx, workspaceID, issue, KindAssignment, body, stillUnassigned, out)
}

// reviewerOptionName turns a verdict branch into the option name to write.
// A reviewer may never be the seat that did the work — checking your own
// output is not a check — so a collision is promoted one rung up the ladder,
// and falls back to a person when the ladder has no rung above.
func (r *Router) reviewerOptionName(v Verdict, candidates []Seat, executor *Seat, issue Issue) (string, bool) {
	switch v.Reviewer {
	case ReviewerNone:
		return OptionNoReview, true
	case ReviewerHuman:
		return OptionHuman, true
	case ReviewerSeat:
		seat, ok := SeatByTier(candidates, v.ReviewerTier)
		if !ok {
			return "", false
		}
		if executor != nil && seat.ID == executor.ID {
			stronger, ok := StrongerThan(candidates, seat)
			if !ok {
				return OptionHuman, true
			}
			seat = stronger
		} else if executor == nil && issue.AssigneeType == "agent" && seat.ID == issue.AssigneeID {
			stronger, ok := StrongerThan(candidates, seat)
			if !ok {
				return OptionHuman, true
			}
			seat = stronger
		}
		return seat.Name, true
	}
	return "", false
}

// routeInReview hands the ticket to whoever accepts it. This row does not fill
// a slot, it moves the ticket, so it is not governed by the fill-only rule —
// but it still runs at most once per issue, because the handoff comment is
// posted at most once per issue.
func (r *Router) routeInReview(ctx context.Context, workspaceID string, issue Issue) (Outcome, error) {
	out := Outcome{State: StateEnabled, Action: ActionHandedOff}
	switch issue.Reviewer {
	case "":
		// Nothing was ever decided for this slot — most likely the executor
		// went out under the threshold and the ticket was picked up by hand.
		// There is nobody to hand to, and the todo row already notified.
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "reviewer slot is empty"}, nil
	case OptionNoReview:
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "issue needs no acceptance pass"}, nil
	}

	done, err := r.Store.HasComment(ctx, workspaceID, issue.ID, KindHandoff)
	if err != nil {
		return out, err
	}
	if done {
		// Already handed off once. Flipping the status back and forth must not
		// reassign again or say the same thing twice.
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "already handed off"}, nil
	}

	if issue.Reviewer == OptionHuman {
		target, err := r.Store.NotifyTarget(ctx, workspaceID, issue)
		if err != nil {
			return out, err
		}
		if target.UserID == "" {
			return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "no notification target"}, nil
		}
		if err := r.Store.Handoff(ctx, workspaceID, issue.ID, "member", target.UserID); err != nil {
			return out, err
		}
		body := r.handoffComment(issue, target.Name, true, target)
		return r.deliver(ctx, workspaceID, issue, KindHandoff, body, true, out)
	}

	roster, err := r.Store.Roster(ctx, workspaceID)
	if err != nil {
		return out, err
	}
	seatAgent, ok := roster[issue.Reviewer]
	if !ok {
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "reviewer seat not in roster"}, nil
	}
	if issue.AssigneeType == "agent" && issue.AssigneeID == seatAgent.ID {
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "reviewer already holds the issue"}, nil
	}
	if err := r.Store.Handoff(ctx, workspaceID, issue.ID, "agent", seatAgent.ID); err != nil {
		return out, err
	}
	// No mention: assignment itself starts the seat's run, so an @ here would
	// only be noise to somebody who is not needed.
	body := r.handoffComment(issue, seatAgent.Name, false, Member{})
	return r.deliver(ctx, workspaceID, issue, KindHandoff, body, false, out)
}

// routeBlocked writes nothing. A blocked ticket is stuck on something routing
// cannot know; all it can do is say what it thinks and make sure somebody
// sees it.
func (r *Router) routeBlocked(ctx context.Context, workspaceID string, settings Settings, issue Issue) (Outcome, error) {
	out := Outcome{State: StateEnabled, Action: ActionAdvised}
	done, err := r.Store.HasComment(ctx, workspaceID, issue.ID, KindAdvice)
	if err != nil {
		return out, err
	}
	if done {
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "already advised"}, nil
	}

	direction := r.Ladder.Direction(issue.ProjectName)
	roster, err := r.Store.Roster(ctx, workspaceID)
	if err != nil {
		return out, err
	}
	candidates := r.Ladder.Candidates(direction, roster)
	advice, err := r.Judge.Unblock(ctx, settings.Model, r.judgeState(issue, direction, candidates))
	if err != nil {
		return r.reportUnavailable(ctx, workspaceID, issue, err)
	}
	r.Breaker.Succeed(workspaceID)

	body := r.adviceComment(issue, advice, candidates)
	return r.deliver(ctx, workspaceID, issue, KindAdvice, body, true, out)
}

// reportUnavailable records the failure with the breaker and, on the first
// occurrence for this issue, says so on the ticket and notifies. Once the
// breaker opens, Route returns before reaching here at all, which is what
// keeps a dead model from leaving a trail of identical comments.
func (r *Router) reportUnavailable(ctx context.Context, workspaceID string, issue Issue, cause error) (Outcome, error) {
	r.Breaker.Fail(workspaceID, cause)
	r.log().Warn("routing: judge unavailable",
		"workspace_id", workspaceID, "issue_id", issue.ID, "error", cause)
	out := Outcome{State: StateEnabled, Action: ActionUnavailable, Reason: cause.Error()}
	body := r.unavailableComment(issue)
	outcome, err := r.deliver(ctx, workspaceID, issue, KindUnavailable, body, true, out)
	if err != nil {
		return outcome, err
	}
	// The caller sees the judge failure, not a success: a hook that logged
	// "routed" here would hide a broken model behind a green line.
	return outcome, nil
}

// deliver posts a routing comment at most once per issue per kind, and pairs
// every mention with a subscription so the mention actually notifies.
func (r *Router) deliver(ctx context.Context, workspaceID string, issue Issue, kind CommentKind, body string, mention bool, out Outcome) (Outcome, error) {
	already, err := r.Store.HasComment(ctx, workspaceID, issue.ID, kind)
	if err != nil {
		return out, err
	}
	if already {
		return out, nil
	}

	if mention {
		target, err := r.Store.NotifyTarget(ctx, workspaceID, issue)
		if err != nil {
			return out, err
		}
		if target.UserID != "" {
			// Subscribe first. A mention that reaches the issue before the
			// subscription exists is a name in a comment nobody is told about,
			// and there is nothing on the ticket that shows the difference.
			if err := r.Store.Subscribe(ctx, workspaceID, issue.ID, target.UserID); err != nil {
				return out, err
			}
			body = body + "\n\n" + mentionLink(target)
			out.Mentioned = true
		}
	}

	if err := r.Store.PostComment(ctx, workspaceID, issue.ID, kind, body); err != nil {
		return out, err
	}
	out.Commented = true
	return out, nil
}

func mentionLink(m Member) string {
	name := m.Name
	if strings.TrimSpace(name) == "" {
		name = "there"
	}
	return "[@" + name + "](mention://member/" + m.UserID + ")"
}

func (r *Router) judgeState(issue Issue, direction string, candidates []Seat) JudgeState {
	tiers := make([]string, 0, len(candidates))
	for _, c := range candidates {
		tiers = append(tiers, c.TierKey)
	}
	return JudgeState{
		Title:              issue.Title,
		DescriptionSummary: issue.DescriptionSummary,
		Labels:             issue.Labels,
		Project:            issue.ProjectName,
		Repository:         issue.Repository,
		Status:             issue.Status,
		ParentExecutor:     issue.ParentExecutor,
		HasChildren:        issue.HasChildren,
		Direction:          direction,
		Candidates:         tiers,
	}
}

// ErrNotEnabled is returned by the manual entry point when it is asked to
// route a workspace that has routing switched off, so the CLI can say so
// instead of reporting a silent no-op.
var ErrNotEnabled = errors.New("routing: not enabled for this workspace")
