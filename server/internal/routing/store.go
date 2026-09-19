package routing

import "context"

// Issue is the slice of an issue routing reads. Everything here is either fed
// to the judge in trimmed form or used by the state table; nothing else about
// an issue reaches this package.
type Issue struct {
	ID    string
	Title string
	// DescriptionSummary is a clipped description. The full body never leaves
	// the deployment.
	DescriptionSummary string
	// Status is the status CATEGORY (todo / in_progress / in_review / done /
	// blocked / backlog / cancelled), already resolved from any custom status.
	// The state table is written against the seven categories, so a workspace
	// that renamed its statuses routes identically to one that did not.
	Status string

	AssigneeType string // "", "member", "agent", "squad"
	AssigneeID   string
	CreatorType  string // "member" or "agent"
	CreatorID    string

	ProjectID   string
	ProjectName string
	Repository  string
	Labels      []string

	ParentExecutor string
	HasChildren    bool

	// Reviewer is the current value of the reviewer property, resolved to its
	// option NAME. Empty means the slot is empty — the only condition under
	// which routing may write it.
	Reviewer string
}

// AssignedToHuman reports the one case routing never touches at all. A ticket
// a person is holding is that person's ticket: no slot is filled, no comment
// is posted, and nobody is notified. This is the single exception that sits
// above the fill-only-empty-slots rule.
func (i Issue) AssignedToHuman() bool { return i.AssigneeType == "member" }

// Member is a notification target.
type Member struct {
	UserID string
	Name   string
}

// ReviewerProperty is the workspace's reviewer slot definition.
type ReviewerProperty struct {
	ID string
	// Options maps option name to option id. Routing addresses options by
	// name — seat names plus the two fixed non-seat values — so ids stay an
	// implementation detail of the property system.
	Options map[string]string
}

// Fixed non-seat option names on the reviewer property.
const (
	// OptionNoReview is written when the judge decides this ticket needs no
	// separate acceptance pass. It is a value rather than an empty slot so
	// the slot stops being re-judged on every later status change.
	OptionNoReview = "不需要验收"
	// OptionHuman is written when acceptance needs a person. Which person is
	// not stored here: it is the same target the @ rule resolves.
	OptionHuman = "交给人"
)

// CommentKind identifies a routing comment. It is also the de-duplication key:
// one comment of each kind per issue, which is what keeps repeated status
// flips from re-notifying and re-explaining.
type CommentKind string

const (
	// KindAssignment — the todo-row decision comment.
	KindAssignment CommentKind = "assignment"
	// KindHandoff — the in-review-row handoff comment.
	KindHandoff CommentKind = "handoff"
	// KindAdvice — the blocked-row advice comment. Writes no values.
	KindAdvice CommentKind = "advice"
	// KindUnavailable — the model could not be reached on a workspace that is
	// configured. Posted once, then the breaker keeps the issue quiet.
	KindUnavailable CommentKind = "unavailable"
)

// Store is everything Route needs from the rest of the server. Every write on
// it is either conditional (the ...IfUnset pair) or explicitly a handoff, so
// the fill-only-empty-slots rule is enforced in SQL rather than by reading
// first and writing after — two nearly simultaneous Route calls on the same
// new issue would both read an empty slot and both write it.
type Store interface {
	Settings(ctx context.Context, workspaceID string) (Settings, error)
	Issue(ctx context.Context, workspaceID, issueID string) (Issue, error)
	// Roster maps agent name to agent for the whole workspace.
	Roster(ctx context.Context, workspaceID string) (map[string]Agent, error)
	// Reviewer returns the reviewer property definition. ok=false means the
	// workspace has no reviewer slot; routing then fills the executor slot
	// only and says so, rather than failing the whole call.
	Reviewer(ctx context.Context, workspaceID string) (prop ReviewerProperty, ok bool, err error)

	// AssignAgentIfUnassigned fills the executor slot only while it is still
	// empty. Reports whether THIS call wrote it. Starting the seat's run is
	// the store's job, because assignment is what wakes an agent.
	AssignAgentIfUnassigned(ctx context.Context, workspaceID, issueID string, seat Seat) (written bool, err error)
	// SetReviewerIfUnset fills the reviewer slot only while it is still empty.
	SetReviewerIfUnset(ctx context.Context, workspaceID, issueID, propertyID, optionID string) (written bool, err error)
	// Handoff reassigns an issue that already has an assignee. Unlike the two
	// above this is not a fill: the in-review row hands the ticket from the
	// seat that did the work to the seat or person that accepts it.
	Handoff(ctx context.Context, workspaceID, issueID, assigneeType, assigneeID string) error

	HasComment(ctx context.Context, workspaceID, issueID string, kind CommentKind) (bool, error)
	PostComment(ctx context.Context, workspaceID, issueID string, kind CommentKind, body string) error
	// Subscribe adds a user to the issue's subscribers. A mention alone very
	// often does not notify; notification follows subscription, so every @
	// this package writes is paired with one of these.
	Subscribe(ctx context.Context, workspaceID, issueID, userID string) error
	// NotifyTarget resolves who to @ for this issue: the creator, or the
	// workspace owner when the creator is an agent. One rule, no setting.
	NotifyTarget(ctx context.Context, workspaceID string, issue Issue) (Member, error)
}
