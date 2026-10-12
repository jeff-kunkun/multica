package routing

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/llm"
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
	// ActionAssigned — the todo row wrote at least one slot. It reports a
	// write, never an attempt: callers batch on this value, and an "assigned"
	// that wrote nothing is a dispatch that failed silently.
	ActionAssigned Action = "assigned"
	// ActionDeclined — the todo row ran, the judge answered, and no slot was
	// written. Reason says why, slot by slot. Distinct from ActionNoop, which
	// means there was nothing to decide.
	ActionDeclined Action = "declined"
	// ActionHandedOff — the in-review row ran.
	ActionHandedOff Action = "handed_off"
	// ActionAdvised — the blocked row ran and only spoke. When it also seated
	// an empty executor slot the action is ActionAssigned.
	ActionAdvised Action = "advised"
	// ActionUnavailable — the model could not be reached.
	ActionUnavailable Action = "unavailable"
	// ActionWoken — the stale-review row put a stalled ticket back in front
	// of whoever accepts it. Writes no status.
	ActionWoken Action = "woken"
	// ActionCompleted — the stale-review row moved a ticket to done because
	// the reviewer had already passed it on the ticket. The only action in
	// this package that changes a status.
	ActionCompleted Action = "completed"
)

// Outcome reports what happened, so the CLI entry point and the hook entry
// point can be shown to produce the same result on the same ticket.
type Outcome struct {
	State  State
	Action Action
	// Reason is a short note for logs, scripts, and agents, not user copy. It
	// is never empty when Action reports that nothing was written.
	Reason string
	// ExecutorWritten is the seat this call put in the executor slot, if any.
	ExecutorWritten *Seat
	// ExecutorReason and ExecutorDetail are the closed word and the sentence
	// the assignment comment prints for the executor written on this call
	// (DENE-1834): a receipt can say "why him" without reading the comment.
	ExecutorReason PickReason
	ExecutorDetail string
	// ReviewerWritten is the reviewer this call wrote, if any. Empty Kind
	// means this call did not write the slot.
	ReviewerWritten ReviewerRef
	// Mentioned reports whether this call notified a person.
	Mentioned bool
	// Commented reports whether this call posted a routing comment.
	Commented bool
	// Tier is the executor tier this call's decision named; JudgedTier is
	// the model's own tier when the rule floor raised it (DENE-1648), empty
	// when it stood.
	Tier       string
	JudgedTier string
	// Trace is the decision log of the todo row (DENE-1677): the questions,
	// the matched rule, the judge's part, and the seats written. Nil when no
	// decision was made on this call.
	Trace *Trace
	// StrongestGate is what the 最强档门槛 did on this call (DENE-1834).
	StrongestGate StrongestGate
	// Dispatch is what the 分工规则 did on this call (DENE-1834): category,
	// matched sentence, series filter or its fallback.
	Dispatch DispatchMatch
}

// Router is the module both entry points call. The HTTP hooks call Route
// directly as a function; the CLI subcommand reaches the same Route through a
// small endpoint. There is no second implementation and no subprocess: routing
// a ticket must not cost a process start.
type Router struct {
	Store Store
	Judge Judge
	// Analyst is the analysis role. Nil means this build cannot run it, and a
	// workspace that switched it on gets the unavailable path.
	Analyst Analyst
	Breaker *Breaker
	Ladder  Ladder
	// Rules is the rule table; empty means DefaultRules.
	Rules            RuleTable
	Log              *slog.Logger
	analysisFailures sync.Map // content key -> time.Time
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

// Active reports whether routing will act on this workspace's tickets. A
// caller deciding whether an agent's executor pick can be dropped (DENE-1033)
// asks this first: with routing off nobody would fill the slot afterwards, so
// the pick stays as it always was. An unreadable setting counts as off.
// ReasonProjectRoutingOff is the skip reason for a ticket whose project
// turned routing off.
const ReasonProjectRoutingOff = "routing is off for this project"

func (r *Router) Active(ctx context.Context, workspaceID string) bool {
	settings, err := r.Store.Settings(ctx, workspaceID)
	return err == nil && settings.State().Active()
}

// Route answers "given this status, who should be holding this ticket", and
// applies the answer. It is called on issue creation and on every status
// change, and it is the only entry point: adding behaviour for a status is a
// row in the table below, not another hook.
//
// Routing follows three gates before it may fill a slot: a downstream consumer
// must read the value, the value must be deterministic or verifiable (otherwise
// it stays empty), and the existing fill-only/conditional-write/one-comment
// rules remain in force.
//
// Route never writes status, stage, description, labels, or children, never
// merges and never closes. The four things it may touch are the assignee, the
// reviewer property, its own comments, and the subscriber list — and the last
// one only so a mention actually notifies.
func (r *Router) Route(ctx context.Context, workspaceID, issueID string) (Outcome, error) {
	settings, issue, done, err := r.admit(ctx, workspaceID, issueID)
	if done != nil {
		return *done, err
	}

	// A child issue is execution-only. It may still receive an executor at
	// `todo`, but acceptance is a single parent-level handoff. Keep this guard
	// before the status switch so a manually assigned reviewer, a stale child
	// row, or a direct `issue route` call cannot start an independent review
	// chain.
	if issue.ParentIssueID != "" && issue.Status == "in_review" {
		return Outcome{State: settings.State(), Action: ActionNoop, Reason: "sub-issue has no acceptance route"}, nil
	}

	switch issue.Status {
	case "todo":
		return r.routeTodo(ctx, workspaceID, settings, issue, fillStarts)
	case "in_review":
		return r.routeInReview(ctx, workspaceID, settings, issue)
	case "blocked":
		return r.routeBlocked(ctx, workspaceID, settings, issue)
	case "in_progress", "done", "cancelled", "backlog":
		// in_progress: somebody is working, do not interrupt.
		// backlog: nobody intends to work on it yet.
		// done / cancelled: over.
		return Outcome{State: settings.State(), Action: ActionNoop, Reason: "status has no routing behaviour"}, nil
	default:
		// Unknown category. Fail closed: an unrecognised status is not a
		// licence to guess who should hold the ticket.
		return Outcome{State: settings.State(), Action: ActionNoop, Reason: "unknown status category " + issue.Status}, nil
	}
}

// RouteGroupNode is the create-time pass for an issue an alignment confirm
// just wrote as part of a group (DENE-812). Route alone cannot seat such a
// group: the root is created in_progress so it coordinates instead of running,
// and every sub-issue past stage 1 is created in backlog so it waits — two
// statuses the state table deliberately leaves alone. Without this pass a
// confirmed group kept whatever seats the preview happened to hold, and the
// rows the preview left empty stayed empty for good: the stage barrier woke
// nobody on an unheld root, and a promoted child reached todo only if
// somebody promoted it.
//
// It is the same todo row — same ladder, same judge, same fill-only-empty
// slots, same one decision comment per issue — with one difference per node
// kind, carried by the fill mode:
//
//   - a todo sub-issue (stage 1) is seated and its run starts, exactly as
//     Route would do it;
//   - a backlog sub-issue (a later stage) is seated and parked: nothing runs
//     from backlog, so the seat starts when its stage is promoted to todo;
//   - the root of a group is seated as the coordinator, reviewer slot
//     included, and its run is NOT started — the stage barrier and the
//     sub-issues' completions are what wake it.
//
// Anything else — a node somebody already moved on, a root without
// sub-issues — gets exactly what Route gives it.
func (r *Router) RouteGroupNode(ctx context.Context, workspaceID, issueID string) (Outcome, error) {
	settings, issue, done, err := r.admit(ctx, workspaceID, issueID)
	if done != nil {
		return *done, err
	}
	switch {
	case issue.ParentIssueID != "" && issue.Status == "backlog":
		return r.routeTodo(ctx, workspaceID, settings, issue, fillParked)
	case issue.ParentIssueID == "" && issue.HasChildren && issue.Status == "in_progress":
		return r.routeTodo(ctx, workspaceID, settings, issue, fillCoordinator)
	}
	return r.Route(ctx, workspaceID, issueID)
}

// admit is the prelude every entry point shares: the switch, the breaker, the
// issue itself, and the one ticket routing never touches. A non-nil Outcome
// is the answer and the caller returns it with the error as is.
func (r *Router) admit(ctx context.Context, workspaceID, issueID string) (Settings, Issue, *Outcome, error) {
	settings, err := r.Store.Settings(ctx, workspaceID)
	if err != nil {
		return settings, Issue{}, &Outcome{State: StateOff, Action: ActionSkipped, Reason: "settings unreadable"}, err
	}
	state := settings.State()
	if !state.Active() {
		// Off and incomplete are the pre-existing code path, to the letter:
		// no request, no write, no comment, and no mention.
		return settings, Issue{}, &Outcome{State: state, Action: ActionSkipped, Reason: "routing not enabled"}, nil
	}

	// The breaker is checked before the issue is even loaded. While it is
	// cooling down the workspace is "ineffective": the reason belongs in the
	// settings section, and a ticket must not be told about it again.
	if open, _, reason := r.Breaker.Open(workspaceID); open {
		return settings, Issue{}, &Outcome{State: StateIneffective, Action: ActionSkipped, Reason: reason}, nil
	}

	issue, err := r.Store.Issue(ctx, workspaceID, issueID)
	if err != nil {
		return settings, Issue{}, &Outcome{State: state, Action: ActionSkipped, Reason: "issue unreadable"}, err
	}

	if issue.ProjectRoutingOff {
		// The project turned routing off (DENE-1966): the same silence as a
		// workspace that never turned it on — no pick, no swap, no comment.
		return settings, issue, &Outcome{State: state, Action: ActionSkipped, Reason: ReasonProjectRoutingOff}, nil
	}

	if issue.AssignedToHuman() && !humanHeldIsRoutable(issue) {
		// A person's ticket is a person's ticket. Nothing is filled, nothing
		// is said, nobody is pinged.
		return settings, issue, &Outcome{State: state, Action: ActionSkipped, Reason: "assignee is a person"}, nil
	}
	return settings, issue, nil, nil
}

// humanHeldIsRoutable carves ONE case out of "a person holds it, do not
// touch it": the ticket is awaiting acceptance and its reviewer slot has
// never been decided.
//
// The blanket rule was written for "a person is DOING this work". It stopped
// meaning that the moment the in-review row started handing tickets to
// people: the handoff makes the assignee a person, and from then on every
// later pass took the early return above — so a ticket that reached review
// before its reviewer slot was ever filled could never have it filled, could
// never be handed to anybody, and could never be swept. Eighteen tickets in
// one workspace were in exactly that state, all answering `skipped / assignee
// is a person` (DENE-712 B1).
//
// The carve-out is as narrow as the bug: in_review only, empty reviewer slot
// only. A person holding a ticket in any other status is untouched, and a
// person holding a ticket whose reviewer slot already holds a value is
// untouched too — there is nothing left to fill, so the fill-only rule has
// already closed and the only thing left to do would be to take the ticket
// away from them.
func humanHeldIsRoutable(issue Issue) bool {
	return issue.Status == "in_review" && issue.Reviewer.Empty()
}

// fillMode is what an executor seat written by routeTodo does next.
type fillMode int

const (
	// fillStarts — the todo row proper: the assignment starts the seat's run.
	fillStarts fillMode = iota
	// fillParked — a later-stage sub-issue: seated now, runs when its stage is
	// promoted to todo. Nothing runs from backlog, so the write starts nothing.
	fillParked
	// fillCoordinator — the root of a group: seated to supervise its
	// sub-issues, and its run is not started by the write.
	fillCoordinator
	// fillBlocked — a blocked ticket with nobody holding it: seated now, and
	// woken by the block patrol when its wait ends (DENE-1255).
	fillBlocked
)

// routeTodo is the only row that fills slots. It is one decision in four
// steps (DENE-1966), each reading what the step before it settled:
//
//  1. candidates — who could take this ticket at all;
//  2. tier — how strong the seat must be (person's label, rule table, judge,
//     分工规则);
//  3. pick — which seat fills the executor slot and which the reviewer slot;
//  4. reason — what was written and why, said once on the ticket.
//
// A step that ends the decision early returns the outcome to deliver.
func (r *Router) routeTodo(ctx context.Context, workspaceID string, settings Settings, issue Issue, mode fillMode) (Outcome, error) {
	p := &todoPass{
		r: r, workspaceID: workspaceID, settings: settings, issue: issue, mode: mode,
		// Declined until a write proves otherwise: the action is derived from
		// what was written, in the reason step.
		out:          Outcome{State: StateEnabled, Action: ActionDeclined},
		needExecutor: issue.AssigneeType == "",
		needReviewer: issue.ParentIssueID == "" && issue.Reviewer.Empty(),
	}
	if !p.needExecutor && !p.needReviewer {
		// Both slots already hold a value, whoever wrote them. Repeated status
		// flips land here: nothing to fill, so nothing is asked, written, or
		// said. This is where idempotence comes from — no bookkeeping, just
		// the emptiness of the slots.
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "no empty slot"}, nil
	}
	for _, step := range []func(context.Context) (*Outcome, error){p.findCandidates, p.setTier, p.pickSeats} {
		if out, err := step(ctx); out != nil || err != nil {
			if out == nil {
				out = &p.out
			}
			return *out, err
		}
	}
	return p.writeReason(ctx)
}

// todoPass carries one routeTodo decision between its steps.
type todoPass struct {
	r            *Router
	workspaceID  string
	settings     Settings
	issue        Issue
	mode         fillMode
	out          Outcome
	needExecutor bool
	needReviewer bool

	// candidates
	ladder     Ladder
	scene      Scene
	roster     map[string]Agent
	candidates []Seat
	eligible   []Seat
	state      JudgeState

	// tier
	requestedTier string
	labelled      bool
	labelSeat     Seat
	labelSeatOK   bool
	ignored       []string
	dec           decision
	verdict       Verdict
	threshold     float64
	notes         []string
	fresh         []Seat
	snaps         map[string]SeatSnapshot
	dispatch      DispatchMatch
	pickRoster    map[string]Agent

	// pick
	executor         *Seat
	executorSource   string
	seriesNote       string
	cont             ContinuationPick
	load             LoadPick
	learn            LearnPick
	learnApplied     bool
	reviewer         ReviewerRef
	reviewerFallback bool
	fallbackWhy      string
	humanSignoff     bool
}

func (p *todoPass) skipped(reason string, err error) (*Outcome, error) {
	return &Outcome{State: StateEnabled, Action: ActionSkipped, Reason: reason}, err
}

func (p *todoPass) deliver(ctx context.Context, body string, out Outcome) (*Outcome, error) {
	o, err := p.r.deliver(ctx, p.workspaceID, p.issue, KindAssignment, body, false, out)
	return &o, err
}

// findCandidates is step 1: the seats this ticket's direction and project allow,
// and which of them can take work now.
func (p *todoPass) findCandidates(ctx context.Context) (*Outcome, error) {
	r, issue := p.r, p.issue
	p.ladder = r.Ladder.For(p.settings)
	p.scene = p.ladder.IssueScene(issue)
	roster, err := r.Store.Roster(ctx, p.workspaceID)
	if err != nil {
		return p.skipped("roster unreadable", err)
	}
	p.roster = ForProject(roster, issue.ProjectID, issue.ProjectSeats)
	p.candidates = p.ladder.assignmentCandidates(p.scene, p.roster)
	// Coordinator requests in the body become Bot mentions in PostComment.
	// The deliver mention flag targets a human creator/owner, not Grok.
	if len(p.candidates) == 0 {
		return p.deliver(ctx, "没有可自动选派的席位"+projectSeatsNote(issue)+"；未派出，请 Grok 协调。", Outcome{State: StateEnabled, Action: ActionAdvised, Reason: "no routable seat"})
	}
	p.eligible, p.state, err = r.decisionContext(ctx, p.workspaceID, p.settings, issue, p.scene, p.candidates)
	if err != nil {
		return p.skipped("routing context incomplete", err)
	}
	if len(p.eligible) == 0 {
		return p.deliver(ctx, "候选均不在线或没有额度"+projectSeatsNote(issue)+"；未派出，请 Grok 协调。", Outcome{State: StateEnabled, Action: ActionAdvised, Reason: "no eligible seat"})
	}
	return nil, nil
}

// setTier is step 2: how strong the seat must be.
func (p *todoPass) setTier(ctx context.Context) (*Outcome, error) {
	r, issue, settings := p.r, p.issue, p.settings
	// A tier label on the ticket is an answer, not a hint: when it names a
	// rung that exists AND that seat can take work, the executor slot is
	// filled from it and the judge is never asked about strength. A dead
	// labelled seat is not an answer — the judge chooses among the seats
	// that are still eligible.
	//
	// Only a person's label counts. A tier label an agent attached is the same
	// guess as an agent naming a seat, and is set aside for the same reason
	// (DENE-1033).
	p.requestedTier, p.labelled = r.Ladder.RequestedTier(issue.HumanLabels())
	if issue.IgnoredAgentPick() {
		p.ignored = append(p.ignored, NoticeAgentPickIgnored)
	}
	if !p.labelled && len(issue.AgentLabels) > 0 {
		if _, agentTier := r.Ladder.RequestedTier(issue.AgentLabels); agentTier {
			p.ignored = append(p.ignored, NoticeAgentTierLabelIgnored)
		}
	}
	if p.labelled {
		p.labelSeat, p.labelSeatOK = SeatByTier(p.eligible, p.requestedTier)
	}
	executorFromLabel := p.needExecutor && p.labelSeatOK

	p.state.CandidateSeries = p.ladder.candidateSeries(p.roster)
	p.state.EligibleSeats = reasoningSeats(p.ladder, p.eligible, p.roster)
	p.dec = decision{Mode: settings.Mode(), Decider: DeciderNone}
	if p.needReviewer || !executorFromLabel {
		d, err := r.decide(ctx, p.workspaceID, settings, issue, p.state)
		if err != nil {
			o, err := r.reportUnavailable(ctx, p.workspaceID, issue, err)
			return &o, err
		}
		if d.Decider != DeciderNone {
			r.Breaker.Succeed(p.workspaceID)
		}
		p.dec, p.verdict = d, d.Verdict
		p.out.Tier, p.out.JudgedTier = d.Verdict.ExecutorTier, d.RaisedFrom
		p.out.Trace = d.Trace
	}

	p.threshold = settings.Threshold()

	// The observation can go stale between the question and the write. Read
	// it again and only then pick. A seat that died in that gap is not
	// written; another eligible seat is, when one remains.
	var err error
	p.fresh, p.snaps, err = r.eligibleNowWithSnapshots(ctx, p.workspaceID, settings, p.candidates)
	if err != nil {
		return p.skipped("routing context incomplete", err)
	}
	// 分工规则 (DENE-1834): the category the analysis read picks a row, the
	// row filters the seats by series and may cap the tier. A person's tier
	// label still wins the rung; the series filter applies either way.
	p.pickRoster = p.roster
	if p.needExecutor {
		p.fresh, p.pickRoster, p.verdict, p.dispatch = p.ladder.dispatch(settings.EffectiveDispatchPolicy(), p.dec.Facts, p.fresh, p.roster, p.verdict)
		p.out.Dispatch = p.dispatch
		if p.dispatch.Capped != "" {
			p.out.Tier = p.verdict.ExecutorTier
		}
		if p.labelSeatOK && !seatIn(p.fresh, p.labelSeat) && p.dispatch.Filtered {
			p.labelSeatOK = false
			p.notes = append(p.notes, "labelled tier "+p.requestedTier+" has no seat in series "+p.dispatch.Series)
		}
	}
	if p.labelled && !p.labelSeatOK && !p.dispatch.Filtered {
		p.notes = append(p.notes, "labelled tier "+p.requestedTier+" was not eligible")
	}
	if line := p.dispatch.Line(p.ladder); line != "" {
		p.notes = append(p.notes, line)
	}
	if j := judgeNote(p.dec.Trace); j != "" {
		p.notes = append(p.notes, j)
	}
	return nil, nil
}

// pickSeats is step 3: the seat on the settled tier for each empty slot.
func (p *todoPass) pickSeats(ctx context.Context) (*Outcome, error) {
	if p.needExecutor {
		if out, err := p.pickExecutor(ctx); out != nil || err != nil {
			return out, err
		}
	}
	if p.needReviewer {
		return p.pickReviewer(ctx)
	}
	return nil, nil
}

// pickExecutor fills the executor slot.
//
// A verdict under the threshold is a weak answer, not a missing one: it
// lands on the ladder's fallback rung — the generic strong seat — instead
// of leaving the ticket in todo for somebody to notice. A seat the
// snapshot says cannot take work is not a fallback. The confidence stays
// visible in the decision comment.
func (p *todoPass) pickExecutor(ctx context.Context) (*Outcome, error) {
	r, issue, settings, ladder := p.r, p.issue, p.settings, p.ladder
	if len(p.fresh) == 0 {
		p.notes = append(p.notes, "executor not filled: no eligible seat")
		return nil, nil
	}
	verdict, dispatch := p.verdict, p.dispatch
	labelStill := p.labelSeatOK && seatIn(p.fresh, p.labelSeat)
	seat, source, why := r.pickConfiguredExecutor(settings, issue.ProjectID, ladder, p.fresh, p.pickRoster, p.labelSeat, labelStill, verdict, p.threshold, issue)
	if seat.ID == "" {
		return p.deliver(ctx, "## 自动选派\n\n**为什么是他**：兜底 —— "+why+"\n\n未派出；请调整兜底席或人工指派。", Outcome{State: StateEnabled, Action: ActionSkipped, Reason: why})
	}
	var class *OutcomeClass
	// 从结果里学 only revisits the table's own tier: a label is a
	// person's call and a fallback is no judgement to learn from.
	if source == pickJudge && p.dec.Decider == DeciderRule && p.dec.Facts != nil {
		c := ClassOf(p.scene, verdict.ExecutorTier, *p.dec.Facts)
		class = &c
		p.learn = r.learned(ctx, p.workspaceID, settings, ladder, c)
		if p.learn.Raise && settings.LearnFromOutcomes {
			if up, ok := SeatByTier(p.fresh, p.learn.To); ok {
				seat, source, p.learnApplied = up, pickLearned, true
				seat.Learned = p.learn.Detail()
			}
		}
	}
	// 最强档门槛: the top rung only when nobody on the middle rungs
	// can take it and its seat is not tight. A person's label is
	// their call and is not gated.
	// A learned raise to the top is evidence that the strong rung
	// was not suitable for this class, which is the gate's own
	// test, so it is not gated.
	if !labelStill && source != pickLearned && !verdict.Reasoned && settings.StrongestGateOn() {
		var gate StrongestGate
		seat, gate = ladder.gateStrongest(seat, p.candidates, p.fresh, p.pickRoster, p.snaps)
		p.out.StrongestGate = gate
		if gate.Moved && class != nil {
			// The outcome class names the judged rung; the seat is
			// now a rung below, so nothing is recorded to learn from.
			class = nil
		}
		if seat.ID == "" {
			return p.deliver(ctx, "## 自动选派\n\n**为什么是他**：兜底 —— "+gate.Why+"；未派出，请人工指派或调整可用席位。", Outcome{State: StateEnabled, Action: ActionSkipped, Reason: gate.Why})
		}
		if line := gate.Line(ladder); line != "" {
			p.notes = append(p.notes, line)
		}
	}
	tierSeat, tierSource, tierWhy := seat, source, why
	// 接着做 ranks above the ladder's pick but not above a person's
	// tier label: the label is a person's instruction about strength.
	if !labelStill && source != pickFallback && !verdict.Reasoned {
		cont, err := r.continuation(ctx, p.workspaceID, settings, ladder, p.pickRoster, p.scene, issue, seat)
		p.cont = cont
		if err != nil {
			return p.skipped("routing context incomplete", err)
		}
		if cont.OK && settings.PreferContinuation {
			seat, source, why = cont.Seat, pickContinuation, ""
			seat.Continues = cont.Detail()
		} else {
			// 负载 only reorders the ladder's own cell, so it runs
			// after 接着做 has declined (DENE-1203).
			load, err := r.load(ctx, p.workspaceID, settings, ladder, p.pickRoster, seat)
			p.load = load
			if err != nil {
				return p.skipped("routing context incomplete", err)
			}
			if load.Moved && settings.PreferIdle {
				seat, source = load.Seat, pickLoad
				seat.Balanced = load.Detail()
			}
		}
	}
	if !labelStill && source != pickFallback && verdict.PreferredSeries != "" && !dispatch.Filtered {
		preferred, matched, preferenceErr := r.availableSeriesSeat(ctx, p.workspaceID, settings, ladder, p.scene, p.roster, seat, verdict.PreferredSeries)
		if preferenceErr != nil {
			return p.skipped("routing context incomplete", preferenceErr)
		}
		if matched {
			if preferred.ID != seat.ID {
				// Series changes the seat within the chosen tier. Restore
				// the tier provenance, not the displaced seat's reason.
				source, why = tierSource, tierWhy
				preferred.Learned = tierSeat.Learned
				p.cont, p.load = ContinuationPick{}, LoadPick{}
			}
			seat = preferred
			p.seriesNote = "系列偏好：" + verdict.PreferredSeries + "；同档可用席位 " + seat.Name
		} else {
			p.seriesNote = "系列偏好：" + verdict.PreferredSeries + "；同档没有可用席位，沿用原选择"
		}
		p.notes = append(p.notes, p.seriesNote)
	}
	written, err := r.Store.AssignAgentIfUnassigned(ctx, p.workspaceID, issue.ID, seat, p.mode == fillStarts)
	if err != nil {
		return &p.out, err
	}
	if !written {
		p.notes = append(p.notes, "executor not filled: the slot was taken before this call wrote it")
		return nil, nil
	}
	p.executor, p.executorSource = &seat, source
	p.out.ExecutorWritten = &seat
	if class != nil {
		r.recordOutcome(ctx, p.workspaceID, issue.ID, *class)
	}
	if why != "" {
		p.notes = append(p.notes, "executor fell back to "+seat.Name+": "+why)
	}
	return nil, nil
}

// pickReviewer fills the reviewer slot.
//
// Same rule for the second slot: an automatic dispatch fills both. An
// unusable verdict stays on the executor's rung and picks another model
// family, or one rung down when that rung has no second family. It never
// steps up, so the strongest rung appears only when the judge names it
// with confidence. The slot never names a person: see decideReviewer.
func (p *todoPass) pickReviewer(ctx context.Context) (*Outcome, error) {
	r, issue, ladder, verdict, executor := p.r, p.issue, p.ladder, p.verdict, p.executor
	// The judge asking for a person is kept as a note on the ticket rather
	// than as the slot's value. The seat does the mechanical check and pings
	// the person for the call it cannot make.
	p.humanSignoff = verdict.Reviewer == ReviewerHuman
	ref, ok := r.decideReviewer(verdict, ladder, p.scene, p.roster, p.fresh, executor, issue)
	if ok && (verdict.ReviewerTier == "" || (executor != nil && (executor.TierKey == "medium" || executor.TierKey == "weak"))) {
		// The table's check landed where the ladder put it; say why.
		_, p.fallbackWhy = r.fallbackReviewer(ladder, p.scene, p.roster, p.fresh, executor, issue)
	}
	why := ""
	switch {
	case !ok && p.humanSignoff:
		why = "judge asked for a person, and the reviewer slot never names one"
	case !ok && p.dec.Decider == DeciderNone:
		why = "no model was asked"
	case !ok:
		why = "judge named tier \"" + verdict.ReviewerTier + "\", which has no seat here"
	case verdict.ReviewerConfidence < p.threshold:
		ok = false
		why = "confidence " + pct(verdict.ReviewerConfidence) + " < threshold " + pct(p.threshold)
	}
	if !ok {
		ref, p.fallbackWhy = r.fallbackReviewer(ladder, p.scene, p.roster, p.fresh, executor, issue)
		p.reviewerFallback = true
		p.notes = append(p.notes, "reviewer fell back to "+ref.Label()+": "+why)
	}
	if ref.Kind == ReviewerAgent && !seatIn(p.fresh, Seat{ID: ref.ID}) {
		eligible, err := r.seatStillEligible(ctx, p.workspaceID, p.settings, ref.ID)
		if err != nil {
			return &p.out, err
		}
		if !eligible {
			p.notes = append(p.notes, "reviewer not filled: seat became ineligible")
			ref = ReviewerRef{}
		}
	}
	if ref.Empty() {
		return nil, nil
	}
	written, err := r.Store.SetReviewerIfUnset(ctx, p.workspaceID, issue.ID, ref)
	if err != nil {
		return &p.out, err
	}
	if written {
		p.reviewer = ref
		p.out.ReviewerWritten = ref
	} else {
		p.notes = append(p.notes, "reviewer not filled: the slot was taken before this call wrote it")
	}
	return nil, nil
}

// writeReason is step 4: the outcome and the one assignment comment.
func (p *todoPass) writeReason(ctx context.Context) (Outcome, error) {
	r, issue, settings, ladder, out, executor := p.r, p.issue, p.settings, p.ladder, p.out, p.executor
	// The action reports the writes, not the fact that this row ran. A partial
	// fill is still "assigned", and Reason carries what was not written and
	// every slot that took a fallback.
	if out.ExecutorWritten != nil || !out.ReviewerWritten.Empty() {
		out.Action = ActionAssigned
	}
	out.Reason = strings.Join(p.notes, "; ")
	if out.ExecutorWritten != nil {
		out.ExecutorReason, out.ExecutorDetail = executorPickReason(issue, p.needExecutor, executor, p.executorSource, p.dec.RaisedFrom != "")
	}
	if out.Trace != nil {
		if executor != nil {
			out.Trace.Seat = executor.Name
		}
		if !p.reviewer.Empty() {
			out.Trace.Reviewer = p.reviewer.Label()
		}
	}

	// The one condition that earns an @: the ticket is in a state where
	// nobody will move it. Here that means the executor slot is still empty,
	// so the ticket sits in todo until a person notices.
	// A parked sub-issue is the exception: it is not sitting in todo, and its
	// stage's promotion routes it again, so an empty slot there is not yet a
	// ticket nobody will move.
	// A blocked ticket's empty slot is said by the blocked row's advice, which
	// names the fact and the action, so this comment does not @ for it too.
	stillUnassigned := p.needExecutor && executor == nil
	notify := stillUnassigned && p.mode != fillParked && p.mode != fillBlocked
	body := r.assignmentComment(issue, p.scene, p.candidates, p.verdict, p.threshold,
		executor, p.executorSource, p.reviewer, p.reviewerFallback, p.fallbackWhy, p.humanSignoff,
		p.needExecutor, p.needReviewer, notify, p.mode, p.dec, settings, p.ignored,
		ContinuationLine(p.cont, settings.PreferContinuation, executor),
		LoadLine(p.load, settings.PreferIdle, executor),
		learnLine(p.learn, settings.LearnFromOutcomes, p.learnApplied, executor, p.executorSource))
	if p.seriesNote != "" {
		body += "\n\n" + p.seriesNote
	}
	if line := p.dispatch.Line(ladder); line != "" {
		body += "\n\n" + line
	}
	if line := out.StrongestGate.Line(ladder); line != "" {
		body += "\n\n" + line
	}
	if note := DemotionFootnote(ladder, p.roster, executor); note != "" {
		body += "\n\n" + note
	}
	if note := UpshiftFootnote(executor); note != "" {
		body += "\n\n" + note
	}

	return r.deliver(ctx, p.workspaceID, issue, KindAssignment, body, notify, out)
}

// learnLine is LearnLine for the seat this call wrote; with nothing written
// there is no pick to talk about.
func learnLine(p LearnPick, enabled, applied bool, written *Seat, source string) string {
	if written == nil {
		return ""
	}
	return LearnLine(p, enabled, applied, source == pickLearned)
}

// judgeNote names a judge answer the rule table did not take, so a hook or
// CLI reading Reason sees the drift without opening the comment.
func judgeNote(t *Trace) string {
	if t == nil || t.Judge == nil || t.Judge.Effect != JudgeIgnored {
		return ""
	}
	return "judge answer \"" + t.Judge.Tier + "\" not used: " + t.Judge.Why
}

// Where the executor seat came from, for the decision comment.
const (
	pickLabel    = "label"
	pickJudge    = "judge"
	pickFallback = "fallback"
	// pickContinuation — the 接着做 rule placed the seat (DENE-1202).
	pickContinuation = "continuation"
	// pickLoad — the 负载 rule placed the seat (DENE-1203).
	pickLoad = "load"
	// pickLearned — 从结果里学 raised the table's tier one rung (DENE-1722).
	pickLearned = "learned"
)

// pickExecutor resolves the executor seat, and always resolves one: candidates
// is non-empty by the time it is called, and every branch ends on a seat. The
// tier label on the ticket wins, then the judge's rung when it is confident
// and exists in this workspace, and the ladder's fallback rung otherwise. The
// third return value is why the fallback happened, empty when it did not.
func (r *Router) pickExecutor(candidates []Seat, labelSeat Seat, labelled bool, v Verdict, threshold float64) (Seat, string, string) {
	if labelled {
		return labelSeat, pickLabel, ""
	}
	seat, ok := SeatByTier(candidates, v.ExecutorTier)
	why := ""
	switch {
	case ok && v.ExecutorConfidence >= threshold:
		return seat, pickJudge, ""
	case v.ExecutorTier == "":
		why = "no model was asked"
	case ok:
		why = "confidence " + pct(v.ExecutorConfidence) + " < threshold " + pct(threshold)
	default:
		why = "judge named tier \"" + v.ExecutorTier + "\", which has no seat here"
	}
	fallback, fallbackOK := r.Ladder.FallbackSeat(candidates)
	if !fallbackOK {
		return Seat{}, pickFallback, why + "; no safe fallback seat"
	}
	return fallback, pickFallback, why
}

// continuation assembles the 接着做 snapshot for this ticket and applies the
// rule. base is the seat the ladder picked; its rung is the floor. A ticket
// with no related executor costs nothing: no read, empty answer.
func (r *Router) continuation(ctx context.Context, workspaceID string, settings Settings, ladder Ladder, roster map[string]Agent, scene Scene, issue Issue, base Seat) (ContinuationPick, error) {
	ids := make([]string, 0, len(issue.Related))
	for _, t := range issue.Related {
		if t.ExecutorID != "" {
			ids = append(ids, t.ExecutorID)
		}
	}
	if len(ids) == 0 {
		return ContinuationPick{}, nil
	}
	facts, err := r.Store.RoutingFacts(ctx, workspaceID, ids, settings.ProviderKeys())
	if err != nil {
		return ContinuationPick{}, err
	}
	seats := make(map[string]ContinuationSeat, len(ids))
	for _, id := range ids {
		agent, onRoster := agentByID(roster, id)
		_, unpickable := SeatSelectable(agent.selectState(), agent.selectContext())
		seats[id] = ContinuationSeat{
			Seat:         seatFromRoster(ladder, roster, id),
			OnRoster:     onRoster,
			Unpickable:   unpickable,
			Availability: facts.Seats[id].Availability,
		}
	}
	return PickContinuation(ContinuationSnapshot{
		Related:      issue.Related,
		Seats:        seats,
		Scene:        scene,
		RequiredTier: base.TierKey,
		TierOrder:    ladder.TierKeys(),
	}), nil
}

// load assembles the 负载 snapshot for the ladder's cell and applies the rule.
// Busyness moves all the time, so it is read here once, together with the
// cell's availability, and the rule never reads again. A cell with one seat
// costs nothing: no read, the ladder's pick.
func (r *Router) load(ctx context.Context, workspaceID string, settings Settings, ladder Ladder, roster map[string]Agent, base Seat) (LoadPick, error) {
	if base.Upshifted {
		return PickLoad(LoadSnapshot{Base: base}), nil
	}
	var cell []LoadSeat
	for _, agent := range roster {
		rs := relaySeat(ladder, agent)
		if !rs.Eligible || rs.Tier != base.TierKey || rs.Direction != base.Direction {
			continue
		}
		cell = append(cell, LoadSeat{
			Seat:      Seat{ID: agent.ID, Name: agent.Name, TierKey: rs.Tier, TierLabel: base.TierLabel, Direction: rs.Direction},
			Demoted:   rs.Demoted,
			UsageRank: rs.UsageRank,
		})
	}
	if len(cell) < 2 {
		return PickLoad(LoadSnapshot{Base: base}), nil
	}
	ids := make([]string, 0, len(cell))
	for _, ls := range cell {
		ids = append(ids, ls.Seat.ID)
	}
	facts, err := r.Store.RoutingFacts(ctx, workspaceID, ids, settings.ProviderKeys())
	if err != nil {
		return LoadPick{}, err
	}
	for i := range cell {
		cell[i].Availability = facts.Seats[cell[i].Seat.ID].Availability
		cell[i].Running = facts.Running[cell[i].Seat.ID]
	}
	return PickLoad(LoadSnapshot{Base: base, Seats: cell}), nil
}

// PickAcceptanceSeat chooses a reviewer for an issue that is about to enter
// in_review with an empty reviewer slot. It does not call the judge: the
// rule is the ladder's, same rung and a different model family, or one rung
// down when that rung has only one family. The seat that did the work is
// never returned. ok is false when nothing else can check the work.
func (r *Router) PickAcceptanceSeat(ctx context.Context, workspaceID string, issue Issue) (ReviewerRef, string, bool) {
	if r == nil || r.Store == nil {
		return ReviewerRef{}, "没有验收席名册", false
	}
	settings, err := r.Store.Settings(ctx, workspaceID)
	if err != nil {
		return ReviewerRef{}, "读不到工作区的席位设置", false
	}
	ladder := r.Ladder
	if len(ladder.Tiers) == 0 {
		ladder = DefaultLadder
	}
	ladder = ladder.For(settings)
	scene := ladder.IssueScene(issue)
	roster, err := r.Store.Roster(ctx, workspaceID)
	if err != nil {
		return ReviewerRef{}, "读不到席位名册", false
	}
	roster = ForProject(roster, issue.ProjectID, issue.ProjectSeats)
	candidates := ladder.assignmentCandidates(scene, roster)
	candidates, err = r.eligibleNow(ctx, workspaceID, settings, candidates)
	if err != nil {
		return ReviewerRef{}, "读不到席位可用状态", false
	}
	ref, why := r.fallbackReviewer(ladder, scene, roster, candidates, nil, issue)
	if ref.Kind != ReviewerAgent || ref.ID == "" || ref.ID == issue.AssigneeID {
		if why == "" {
			why = "选不出和执行席不同的验收席"
		}
		return ReviewerRef{}, why, false
	}
	return ref, why, true
}

// fallbackReviewer adapts review to the common selector: one tier above the
// executor, adjacent-tier fallback, and never self-review.
func (r *Router) fallbackReviewer(ladder Ladder, scene Scene, roster map[string]Agent, candidates []Seat, executor *Seat, issue Issue) (ReviewerRef, string) {
	if executor != nil {
		issue.AssigneeType, issue.AssigneeID = "agent", executor.ID
	}
	selection := r.routeTicket(ladder, Settings{}, ticketRoute{Ticket: issue, Candidates: candidates, Roster: roster}, PurposeReview)
	if len(selection.Seats) == 0 {
		return ReviewerRef{}, selection.Why
	}
	return seatReviewer(selection.Seats[0]), selection.Why
}

func seatFromRoster(ladder Ladder, roster map[string]Agent, id string) Seat {
	agent, ok := agentByID(roster, id)
	if !ok {
		return Seat{ID: id}
	}
	seat := Seat{ID: agent.ID, Name: agent.Name, Direction: ladder.agentDirection(agent)}
	key := ""
	if tagged, ok := ladder.NormalizeTier(agent.Tier); ok && tagged != "" {
		key = tagged
	} else if named, ok := ladder.TierOf(agent.Name); ok {
		key = named
	}
	seat.TierKey = key
	if tier, ok := ladder.TierByKey(key); ok {
		seat.TierLabel = tier.Label
	}
	return seat
}

func seatReviewer(s Seat) ReviewerRef {
	return ReviewerRef{Kind: ReviewerAgent, ID: s.ID, Name: s.Name}
}

// decideReviewer keeps the old call boundary while all policy lives in routeTicket.
func (r *Router) decideReviewer(v Verdict, ladder Ladder, scene Scene, roster map[string]Agent, candidates []Seat, executor *Seat, issue Issue) (ReviewerRef, bool) {
	ref, _ := r.fallbackReviewer(ladder, scene, roster, candidates, executor, issue)
	return ref, !ref.Empty()
}

// unsureReviewer reports a reviewer verdict that 按判断配验收 reads as "no
// confident call for a check": the model answered, did not ask for a person,
// and either did not want a check or wanted one below the threshold. A
// confident seat answer whose tier has no seat here still asked for a check,
// and no answer at all is nobody's judgement, so both keep the fallback seat.
func unsureReviewer(v Verdict, dec decision, threshold float64) bool {
	if dec.Decider == DeciderNone || v.Reviewer == ReviewerHuman {
		return false
	}
	return v.Reviewer == ReviewerNone || v.ReviewerConfidence < threshold
}

// routeInReview hands the ticket to whoever accepts it. This row does not fill
// a slot, it moves the ticket, so it is not governed by the fill-only rule.
//
// It runs once per stay in review, not once per issue. A handoff comment from
// an earlier stay used to be treated as "the reviewer has already been woken",
// which is how a ticket could enter in_review again — reviewer already
// decided, no run active — and sit there until a person typed an @ (DENE-617,
// DENE-772). The comment is still posted at most once; the wake is not.
//
// A stay that still has a run in flight is left alone. The executor sets
// in_review from inside the run that is about to finish, and starting the
// reviewer in that window races the delivery. The completion callback calls
// Route again once that run is gone.
func (r *Router) routeInReview(ctx context.Context, workspaceID string, settings Settings, issue Issue) (Outcome, error) {
	if issue.ParentIssueID != "" {
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "sub-issue has no acceptance route"}, nil
	}
	out := Outcome{State: StateEnabled, Action: ActionHandedOff}
	decidedHere := false
	if issue.Reviewer.Empty() {
		// Nothing was ever decided for this slot: the ticket was dispatched by
		// hand, or it predates routing. Giving up here is what leaves a queue
		// of tickets sitting in review that nobody was ever told to check, so
		// the reviewer is decided now, by the same judge and the same ladder
		// fallback the todo row uses. This is still a fill, not an overwrite —
		// the write is conditional on the slot being empty.
		ref, outcome, err := r.decideReviewerNow(ctx, workspaceID, settings, issue)
		if err != nil || ref.Empty() {
			return outcome, err
		}
		issue.Reviewer = ref
		out.ReviewerWritten = ref
		decidedHere = true
	}
	if issue.Reviewer.Kind == ReviewerNoReview {
		return Outcome{
			State:           StateEnabled,
			Action:          ActionNoop,
			Reason:          "issue needs no acceptance pass",
			ReviewerWritten: out.ReviewerWritten,
		}, nil
	}

	state, err := r.Store.Acceptance(ctx, workspaceID, issue)
	if err != nil {
		return out, err
	}
	if state.ActiveRun {
		// The run that is still open — almost always the executor finishing
		// the status write — owns the ticket until it ends. Completion calls
		// this row again.
		return Outcome{
			State:           StateEnabled,
			Action:          ActionNoop,
			Reason:          "active run in progress",
			ReviewerWritten: out.ReviewerWritten,
		}, nil
	}

	if issue.Reviewer.Kind == ReviewerMember {
		if state.MemberNotified {
			return Outcome{
				State:           StateEnabled,
				Action:          ActionNoop,
				Reason:          "already notified this round",
				ReviewerWritten: out.ReviewerWritten,
			}, nil
		}
		// A person is named in the slot — by hand, or by a routing version
		// that still wrote people there. The ticket is NOT handed over: an
		// issue a person holds is one routing never touches again, so
		// reassigning here is what leaves the ticket frozen, unable to be
		// moved on by any seat. The person is notified instead; whoever is
		// doing the work keeps the ticket until a seat or a human moves it.
		target := Member{UserID: issue.Reviewer.ID, Name: issue.Reviewer.Name}
		out.Action = ActionAdvised
		out.Reason = "reviewer slot names a person: notified, ticket not reassigned"
		// The inbox row is the notice, and NotifyMember is its only writer.
		// deliverTo would insert another one on a different connection from
		// the stay check, so a status-change hook and the completion callback
		// could each leave a routing_needs_you. The comment is still once per
		// issue; a later stay finds it already posted and only refreshes the
		// notice.
		body := r.reviewerIsPersonComment(issue, target, decidedHere)
		if target.UserID != "" {
			body += "\n\n" + mentionLink(target)
		}
		written, err := r.Store.PostComment(ctx, workspaceID, issue.ID, KindHandoff, body)
		if err != nil {
			return out, err
		}
		if written {
			out.Commented = true
		}
		wrote, err := r.Store.NotifyMember(ctx, workspaceID, issue.ID, target)
		if err != nil {
			return out, err
		}
		if !wrote {
			return Outcome{
				State:           StateEnabled,
				Action:          ActionNoop,
				Reason:          "already notified this round",
				ReviewerWritten: out.ReviewerWritten,
				Commented:       out.Commented,
			}, nil
		}
		out.Mentioned = true
		return out, nil
	}

	if state.AgentEngaged {
		// This stay already put the ticket on the reviewer and started a run.
		// A second status flip, the completion callback, and a manual re-route
		// all land here. The stale sweep is the one path that may wake them
		// again, and it does not come through this check.
		return Outcome{
			State:           StateEnabled,
			Action:          ActionNoop,
			Reason:          "already handed off this round",
			ReviewerWritten: out.ReviewerWritten,
		}, nil
	}

	// An agent reviewer. The reference is resolved against the roster on the
	// spot: a seat that has since been archived is no longer a reviewer, and
	// saying so beats handing the ticket to a seat that cannot run. A seat
	// that is only switched off is still a reviewer — the wake moves to
	// another family instead of vanishing.
	roster, err := r.Store.Roster(ctx, workspaceID)
	if err != nil {
		return out, err
	}
	roster = ForProject(roster, issue.ProjectID, issue.ProjectSeats)
	seatAgent, ok := agentByID(roster, issue.Reviewer.ID)
	if !ok {
		card, found, err := r.Store.OffRosterSeat(ctx, workspaceID, issue.Reviewer.ID)
		if err != nil {
			return out, err
		}
		if !found {
			return Outcome{
				State:           StateEnabled,
				Action:          ActionNoop,
				Reason:          "reviewer seat not in roster",
				ReviewerWritten: out.ReviewerWritten,
			}, nil
		}
		return r.handOffToSubstitute(ctx, workspaceID, settings, issue, roster, card, out, coverDisabled)
	}
	// A reviewer chosen before the project named its executors is not woken
	// once it is off the list: the wake is a new automatic pick.
	if !ProjectAdmits(issue.ProjectSeats, seatAgent.ID) {
		return r.handOffToSubstitute(ctx, workspaceID, settings, issue, roster, seatAgent, out, coverOffList)
	}
	facts, err := r.Store.RoutingFacts(ctx, workspaceID, []string{seatAgent.ID}, settings.ProviderKeys())
	if err != nil {
		return Outcome{State: StateEnabled, Action: ActionSkipped, Reason: "routing context incomplete"}, err
	}
	if snap, ok := facts.Seats[seatAgent.ID]; ok && Unselectable(snap.Availability) {
		if snap.Availability == AvailabilityDisabled {
			return r.handOffToSubstitute(ctx, workspaceID, settings, issue, roster, seatAgent, out, coverDisabled)
		}
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "reviewer seat is not eligible"}, nil
	}
	if err := r.Store.Handoff(ctx, workspaceID, issue.ID, "agent", seatAgent.ID); err != nil {
		return out, err
	}
	// No mention: assignment itself starts the seat's run, so an @ here would
	// only be noise to somebody who is not needed. The comment is once per
	// issue; a later stay still hands off above, it just does not repeat the
	// explanation.
	body := r.handoffComment(issue, seatAgent.Name, decidedHere)
	return r.deliver(ctx, workspaceID, issue, KindHandoff, body, false, out)
}

// handOffToSubstitute covers an acceptance wake whose reviewer cannot take
// work. The slot already names them, so they stay the designated reviewer:
// recovery may give the ticket back only before the cover has started.
//
// cause says why the reviewer cannot take it: switched off, its acceptance
// runs keep failing (DENE-1647), or it is off the project's executor list
// (DENE-1959). Only the wording differs, except that an off-list reviewer is
// not the designated one any more, so recovery never hands the ticket back.
func (r *Router) handOffToSubstitute(ctx context.Context, workspaceID string, settings Settings, issue Issue, roster map[string]Agent, disabled Agent, out Outcome, cause reviewerCover) (Outcome, error) {
	ladder := r.Ladder.For(settings)
	scene := ladder.IssueScene(issue)
	holder := seatFromRoster(ladder, map[string]Agent{disabled.Name: disabled}, disabled.ID)
	if holder.Name == "" {
		holder.Name = disabled.Name
	}
	if holder.ID == "" {
		holder.ID = disabled.ID
	}
	var avoid []string
	if issue.AssigneeType == "agent" && issue.AssigneeID != "" && issue.AssigneeID != disabled.ID {
		avoid = append(avoid, issue.AssigneeID)
	}
	for _, id := range issue.Workers {
		if id != disabled.ID {
			avoid = append(avoid, id)
		}
	}
	// The roster only knows who is switched on; a cover that is offline or
	// out of quota cannot take the review either. Unknown is not a reason
	// to drop a seat.
	ids := make([]string, 0, len(roster))
	for _, agent := range roster {
		if agent.ID != "" && agent.ID != disabled.ID {
			ids = append(ids, agent.ID)
		}
	}
	facts, err := r.Store.RoutingFacts(ctx, workspaceID, ids, settings.ProviderKeys())
	if err != nil {
		return Outcome{State: StateEnabled, Action: ActionSkipped, Reason: "routing context incomplete"}, err
	}
	for _, id := range ids {
		if snap, ok := facts.Seats[id]; ok && Unselectable(snap.Availability) {
			avoid = append(avoid, id)
		}
	}
	replacement, steppedDown, ok := SubstituteSeat(ladder, holder, roster, avoid, scene)
	if !ok {
		body := disabledReviewerStuck(disabled.Name)
		kind := CommentKind("reviewer_off:" + disabled.ID)
		switch cause {
		case coverFailed:
			body = failedReviewerStuck(disabled.Name)
			kind = CommentKind("reviewer_failed:" + disabled.ID)
		case coverOffList:
			body = offListReviewerStuck(disabled.Name)
			kind = CommentKind("reviewer_off_list:" + disabled.ID)
		}
		stuck, err := r.deliver(ctx, workspaceID, issue, kind, body, true, out)
		if err != nil {
			return stuck, err
		}
		stuck.Action = ActionAdvised
		stuck.Reason = "reviewer seat is off and no replacement"
		return stuck, nil
	}
	ref := seatReviewer(replacement)
	written, err := r.Store.ReplaceReviewer(ctx, workspaceID, issue.ID, disabled.ID, ref)
	if err != nil {
		return out, err
	}
	if !written {
		return Outcome{
			State:           StateEnabled,
			Action:          ActionNoop,
			Reason:          "reviewer slot changed",
			ReviewerWritten: out.ReviewerWritten,
		}, nil
	}
	if err := r.Store.RememberReviewerRelay(ctx, workspaceID, issue.ID, ReviewerRelay{
		OriginalID:      disabled.ID,
		OriginalName:    disabled.Name,
		ReplacementID:   replacement.ID,
		ReplacementName: replacement.Name,
		Designated:      cause != coverOffList,
		CoveredAt:       time.Now().UTC(),
	}); err != nil {
		return out, err
	}
	issue.Reviewer = ref
	out.ReviewerWritten = ref
	if err := r.Store.Handoff(ctx, workspaceID, issue.ID, "agent", replacement.ID); err != nil {
		return out, err
	}
	note := disabledReviewerNote(disabled.Name, replacement.Name, steppedDown)
	switch cause {
	case coverFailed:
		note = failedReviewerNote(disabled.Name, replacement.Name, steppedDown)
	case coverOffList:
		note = offListReviewerNote(disabled.Name, replacement.Name)
	}
	body := note + "\n\n" + r.handoffComment(issue, replacement.Name, false)
	delivered, err := r.deliver(ctx, workspaceID, issue, KindHandoff, body, false, out)
	if err != nil {
		return delivered, err
	}
	if !delivered.Commented {
		return r.deliver(ctx, workspaceID, issue, CommentKind("reviewer_off:"+disabled.ID+":"+replacement.ID), note, false, delivered)
	}
	return delivered, nil
}

// reviewerCover is why an acceptance wake goes to a substitute.
type reviewerCover int

const (
	coverDisabled reviewerCover = iota
	coverFailed
	coverOffList
)

// agentByID finds a seat in the roster by id. The roster is keyed by name
// because the ladder addresses seats by name; the reviewer slot addresses
// them by id, which is what survives a rename.
func agentByID(roster map[string]Agent, id string) (Agent, bool) {
	if id == "" {
		return Agent{}, false
	}
	for _, a := range roster {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}

// decideReviewerNow fills an empty reviewer slot at the in-review row. It is
// the todo row's reviewer half, reached from the other end: same judge, same
// threshold, same ladder fallback, same conditional write. It returns the
// reference that is now in the slot, or an empty reference plus the Outcome to
// return when there is nothing to decide.
func (r *Router) decideReviewerNow(ctx context.Context, workspaceID string, settings Settings, issue Issue) (ReviewerRef, Outcome, error) {
	noop := func(reason string) Outcome {
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: reason}
	}
	ladder := r.Ladder.For(settings)
	scene := ladder.IssueScene(issue)
	roster, err := r.Store.Roster(ctx, workspaceID)
	if err != nil {
		return ReviewerRef{}, Outcome{State: StateEnabled, Action: ActionSkipped, Reason: "roster unreadable"}, err
	}
	roster = ForProject(roster, issue.ProjectID, issue.ProjectSeats)
	candidates := ladder.assignmentCandidates(scene, roster)
	if len(candidates) == 0 {
		return ReviewerRef{}, noop("ladder has no seat in this workspace"), nil
	}
	eligible, _, err := r.decisionContext(ctx, workspaceID, settings, issue, scene, candidates)
	if err != nil {
		return ReviewerRef{}, Outcome{State: StateEnabled, Action: ActionSkipped, Reason: "routing context incomplete"}, err
	}
	if len(eligible) == 0 {
		return ReviewerRef{}, noop("no eligible seat"), nil
	}

	verdict := Verdict{Reviewer: ReviewerSeat, ReviewerConfidence: 1}

	fresh, err := r.eligibleNow(ctx, workspaceID, settings, candidates)
	if err != nil {
		return ReviewerRef{}, Outcome{State: StateEnabled, Action: ActionSkipped, Reason: "routing context incomplete"}, err
	}

	// executor is nil on purpose: at this row the ticket is already held by
	// whoever did the work, and decideReviewer reads that holder off the
	// issue to keep a seat from reviewing its own output.
	ref, ok := r.decideReviewer(verdict, ladder, scene, roster, fresh, nil, issue)
	if !ok || verdict.ReviewerConfidence < settings.Threshold() {
		ref, _ = r.fallbackReviewer(ladder, scene, roster, fresh, nil, issue)
	}
	if ref.Kind == ReviewerAgent && !seatIn(fresh, Seat{ID: ref.ID}) {
		eligible, err := r.seatStillEligible(ctx, workspaceID, settings, ref.ID)
		if err != nil {
			return ReviewerRef{}, Outcome{State: StateEnabled, Action: ActionSkipped, Reason: "routing context incomplete"}, err
		}
		if !eligible {
			return ReviewerRef{}, noop("reviewer seat is not eligible"), nil
		}
	}
	if ref.Empty() {
		_, why := r.fallbackReviewer(ladder, scene, roster, fresh, nil, issue)
		out, err := r.deliver(ctx, workspaceID, issue, CommentKind("review_unavailable"), "验收未派出："+why, false, Outcome{State: StateEnabled, Action: ActionAdvised, Reason: why})
		return ReviewerRef{}, out, err
	}

	written, err := r.Store.SetReviewerIfUnset(ctx, workspaceID, issue.ID, ref)
	if err != nil {
		return ReviewerRef{}, Outcome{State: StateEnabled, Action: ActionHandedOff}, err
	}
	if !written {
		// Another pass filled it between the read and the write. Whatever it
		// wrote is the answer; re-read rather than hand off to a stale one.
		fresh, err := r.Store.Issue(ctx, workspaceID, issue.ID)
		if err != nil {
			return ReviewerRef{}, Outcome{State: StateEnabled, Action: ActionSkipped, Reason: "issue unreadable"}, err
		}
		if fresh.Reviewer.Empty() {
			return ReviewerRef{}, noop("reviewer slot is empty"), nil
		}
		return fresh.Reviewer, Outcome{}, nil
	}
	return ref, Outcome{}, nil
}

// routeBlocked seats an empty executor slot, then says once what is in the
// way. A blocked ticket nobody holds is woken by nobody when its wait ends, so
// the slot is filled with the todo row's ladder and judge — parked: no run
// starts and the status stays blocked (DENE-1255). A held slot is left alone.
//
// The advice reads the wait record before any model: an empty executor slot,
// a block that names nothing to wait on, and a blocker that already ended are
// facts, and a model guessing around them pointed people at the wrong thing.
// A wait on tickets still open is no advice at all: the ticket is waiting,
// not stuck.
func (r *Router) routeBlocked(ctx context.Context, workspaceID string, settings Settings, issue Issue) (Outcome, error) {
	out := Outcome{State: StateEnabled, Action: ActionAdvised}
	fillWhy := ""
	if issue.AssigneeType == "" {
		seated, err := r.routeTodo(ctx, workspaceID, settings, issue, fillBlocked)
		if err != nil || seated.Action == ActionUnavailable || seated.Action == ActionSkipped {
			return seated, err
		}
		out.ExecutorWritten, out.ReviewerWritten, out.Commented = seated.ExecutorWritten, seated.ReviewerWritten, seated.Commented
		if seat := seated.ExecutorWritten; seat != nil {
			issue.AssigneeType, issue.AssigneeID = "agent", seat.ID
		} else {
			fillWhy = seated.Reason
		}
	}
	if out.ExecutorWritten != nil || !out.ReviewerWritten.Empty() {
		out.Action = ActionAssigned
	}

	done, err := r.Store.HasComment(ctx, workspaceID, issue.ID, KindAdvice)
	if err != nil {
		return out, err
	}
	if done {
		if out.Action == ActionAssigned {
			return out, nil
		}
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "already advised"}, nil
	}

	if body, mention, ok := blockedFactComment(issue, fillWhy); ok {
		return r.deliver(ctx, workspaceID, issue, KindAdvice, body, mention, out)
	}
	// A held ticket waiting on open tickets is not stuck: the block patrol
	// wakes its executor when they end. Asking a model here read the wait as
	// "needs a person" and @'d the owner for nothing.
	if pending := issue.Wait.Pending(); issue.AssigneeType != "" && issue.Wait.Registered && len(pending) > 0 {
		if out.Action == ActionAssigned {
			return out, nil
		}
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "waiting on " + strings.Join(pending, ", ")}, nil
	}

	ladder := r.Ladder.For(settings)
	scene := ladder.IssueScene(issue)
	roster, err := r.Store.Roster(ctx, workspaceID)
	if err != nil {
		return out, err
	}
	roster = ForProject(roster, issue.ProjectID, issue.ProjectSeats)
	candidates := ladder.assignmentCandidates(scene, roster)
	_, state, err := r.decisionContext(ctx, workspaceID, settings, issue, scene, candidates)
	if err != nil {
		return Outcome{State: StateEnabled, Action: ActionSkipped, Reason: "routing context incomplete"}, err
	}
	advice, asked, err := r.advise(ctx, workspaceID, settings, issue, state)
	if err != nil {
		return r.reportUnavailable(ctx, workspaceID, issue, err)
	}
	if !asked {
		if out.Action == ActionAssigned {
			return out, nil
		}
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "no model is switched on to advise"}, nil
	}
	r.Breaker.Succeed(workspaceID)

	body := r.adviceComment(issue, advice, candidates)
	return r.deliver(ctx, workspaceID, issue, KindAdvice, body, true, out)
}

// SeatExecutor fills an empty executor slot without starting a run, on a
// ticket that is neither over nor awaiting acceptance. The block patrol asks
// it before waking an owner it does not have (DENE-1255): it then starts the
// seat the usual way, so the wake and the fill stay two steps with one
// meaning each. A held slot answers noop.
func (r *Router) SeatExecutor(ctx context.Context, workspaceID, issueID string) (Outcome, error) {
	settings, issue, done, err := r.admit(ctx, workspaceID, issueID)
	if done != nil {
		return *done, err
	}
	if issue.AssigneeType != "" {
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "executor slot is held"}, nil
	}
	switch issue.Status {
	case "done", "cancelled", "in_review":
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "status has no executor to seat"}, nil
	}
	return r.routeTodo(ctx, workspaceID, settings, issue, fillBlocked)
}

// reportUnavailable records the failure with the breaker and, on the first
// occurrence for this issue, says so on the ticket and notifies. Once the
// breaker opens, Route returns before reaching here at all, which is what
// keeps a dead model from leaving a trail of identical comments.
func (r *Router) reportUnavailable(ctx context.Context, workspaceID string, issue Issue, cause error) (Outcome, error) {
	r.Breaker.Fail(workspaceID, cause)
	if errors.Is(cause, llm.ErrNotConfigured) {
		// "This deployment never configured an internal LLM" is a fact about
		// the deployment, not about this ticket. Commenting and @-ing on
		// every issue would punish a workspace for an admin's omission, and
		// the spec puts diagnosis in one place: the settings section, which
		// reads this through Health.
		r.log().Warn("routing: internal LLM not configured",
			"workspace_id", workspaceID, "issue_id", issue.ID)
		return Outcome{State: StateIneffective, Action: ActionSkipped, Reason: "internal LLM not configured"}, nil
	}
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

	target := Member{}
	if mention {
		target, err = r.Store.NotifyTarget(ctx, workspaceID, issue)
		if err != nil {
			return out, err
		}
		if target.UserID != "" {
			body = body + "\n\n" + mentionLink(target)
		}
	}

	// The comment is written BEFORE anyone is notified, and the notification
	// is conditional on that write having won. HasComment above is a cheap
	// filter, not the guard: two Route calls on the same new issue can both
	// pass it, and only the unique index can decide which one posts. If the
	// loser notified anyway, the person would be pinged about a decision
	// comment that is not theirs and does not exist.
	written, err := r.Store.PostComment(ctx, workspaceID, issue.ID, kind, body)
	if err != nil {
		return out, err
	}
	if !written {
		return out, nil
	}
	out.Commented = true

	if target.UserID != "" {
		if err := r.Store.Subscribe(ctx, workspaceID, issue.ID, target.UserID); err != nil {
			return out, err
		}
		out.Mentioned = true
	}
	return out, nil
}

// deliverTo is deliver with the notification target already decided. The
// in-review handoff to a person mentions THAT person — the one the reviewer
// slot names — not whoever the creator-or-owner rule would resolve today.
func (r *Router) deliverTo(ctx context.Context, workspaceID string, issue Issue, kind CommentKind, body string, target Member, out Outcome) (Outcome, error) {
	already, err := r.Store.HasComment(ctx, workspaceID, issue.ID, kind)
	if err != nil {
		return out, err
	}
	if already {
		return out, nil
	}
	if target.UserID != "" {
		body = body + "\n\n" + mentionLink(target)
	}
	written, err := r.Store.PostComment(ctx, workspaceID, issue.ID, kind, body)
	if err != nil {
		return out, err
	}
	if !written {
		return out, nil
	}
	out.Commented = true
	if target.UserID != "" {
		if err := r.Store.Subscribe(ctx, workspaceID, issue.ID, target.UserID); err != nil {
			return out, err
		}
		out.Mentioned = true
	}
	return out, nil
}

func mentionLink(m Member) string {
	name := m.Name
	if strings.TrimSpace(name) == "" {
		name = "there"
	}
	return "[@" + name + "](mention://member/" + m.UserID + ")"
}

// decisionContext loads the seat and provider summary, drops seats that are
// definitely ineligible, and builds the judge payload. The payload still
// lists the dropped seats so the request shows why a rung is missing.
// candidate_tiers contains only the seats the judge may choose.
func (r *Router) decisionContext(ctx context.Context, workspaceID string, settings Settings, issue Issue, scene Scene, candidates []Seat) ([]Seat, JudgeState, error) {
	facts, err := r.Store.RoutingFacts(ctx, workspaceID, seatIDs(candidates), settings.ProviderKeys())
	if err != nil {
		return nil, JudgeState{}, err
	}
	eligible := EligibleSeats(candidates, facts.Seats)
	state := r.judgeState(issue, scene, eligible)
	state.RoutingPolicy = DefaultRoutingPolicy
	state.PolicyPrompt = settings.EffectivePolicyPrompt()
	state.Seats = OrderSeatViews(candidates, facts.Seats)
	state.EligibleSeats = reasoningSeats(r.ladder(), eligible, nil)
	state.ProviderQuotas = EnsureProviderQuotas(facts.Providers, settings.ProviderKeys())
	return eligible, state, nil
}

// eligibleNow re-reads the summary immediately before a conditional write.
func (r *Router) eligibleNow(ctx context.Context, workspaceID string, settings Settings, candidates []Seat) ([]Seat, error) {
	seats, _, err := r.eligibleNowWithSnapshots(ctx, workspaceID, settings, candidates)
	return seats, err
}

// eligibleNowWithSnapshots is eligibleNow keeping the snapshot it read, for
// a sentence that names why a seat was left out.
func (r *Router) eligibleNowWithSnapshots(ctx context.Context, workspaceID string, settings Settings, candidates []Seat) ([]Seat, map[string]SeatSnapshot, error) {
	facts, err := r.Store.RoutingFacts(ctx, workspaceID, seatIDs(candidates), settings.ProviderKeys())
	if err != nil {
		return nil, nil, err
	}
	return EligibleSeats(candidates, facts.Seats), facts.Seats, nil
}

func seatIDs(candidates []Seat) []string {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ID != "" {
			ids = append(ids, candidate.ID)
		}
	}
	return ids
}

// seatStillEligible re-reads one seat that was not in the one-per-rung
// candidate list. A same-tier alternate is chosen from the roster, so the
// candidate recheck never saw it. Missing data stays eligible: unknown is
// not a reason to drop the seat.
func (r *Router) seatStillEligible(ctx context.Context, workspaceID string, settings Settings, id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	facts, err := r.Store.RoutingFacts(ctx, workspaceID, []string{id}, settings.ProviderKeys())
	if err != nil {
		return false, err
	}
	snap, ok := facts.Seats[id]
	if ok && Unselectable(snap.Availability) {
		return false, nil
	}
	return true, nil
}

func seatIn(seats []Seat, seat Seat) bool {
	if seat.ID == "" {
		return false
	}
	for _, candidate := range seats {
		if candidate.ID == seat.ID {
			return true
		}
	}
	return false
}

func (r *Router) judgeState(issue Issue, scene Scene, candidates []Seat) JudgeState {
	tiers := make([]string, 0, len(candidates))
	seen := map[string]bool{}
	for _, c := range candidates {
		if !seen[c.TierKey] {
			tiers = append(tiers, c.TierKey)
			seen[c.TierKey] = true
		}
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
		Direction:          scene.Primary(),
		Candidates:         tiers,
	}
}

// ErrNotEnabled is returned by the manual entry point when it is asked to
// route a workspace that has routing switched off, so the CLI can say so
// instead of reporting a silent no-op.
var ErrNotEnabled = errors.New("routing: not enabled for this workspace")

// HealthReport is what the settings section renders. It is assembled from the
// stored settings plus live breaker state, and it is the only surface on which
// a routing failure is visible to a person — tickets stay quiet by design.
type HealthReport struct {
	// State is the same four-way classification the settings section shows.
	State State
	// Mode is which roles are on. Model, BaseURL and the key fields below
	// describe the primary one: the judge when it is on, else the analysis
	// model.
	Mode Mode
	// Usable is false exactly when State is not enabled. The client gates the
	// ineffective chip on this rather than re-deriving it.
	Usable bool
	// Reason is human-readable, and empty unless something is wrong.
	Reason string
	// RetryAfterSeconds is how long the cooldown still has to run.
	RetryAfterSeconds int
	// LastSuccessAt / LastFailureAt are unix seconds, zero for never.
	LastSuccessAt int64
	LastFailureAt int64
	Model         string
	Threshold     float64
	// BaseURL is the workspace's own endpoint, empty when it uses the
	// deployment gateway. Reported raw; the handler reduces it to a host
	// before it reaches a client, because a pasted URL can carry userinfo.
	BaseURL string
	// KeySet reports that a workspace key is stored and openable. It is a
	// boolean and never the key: "is one saved?" is the only question the
	// settings section needs answered, and it is the only one that can be
	// answered without putting a credential on the wire.
	//
	// A stored-but-unopenable key (deployment secret rotated, row restored
	// into a different deployment) reports false, which is what makes the
	// settings section say "type it again" instead of showing a green chip
	// over a key nothing can use.
	KeySet bool
	// UsesWorkspaceGateway is true when this workspace's calls go to its own
	// endpoint rather than the deployment's. Derived from the pair, so a
	// half-filled pair reports false and the section can say why.
	UsesWorkspaceGateway bool
	// Roles describes each role on its own — analysis first, then judge —
	// because the two can sit on different endpoints with different keys.
	Roles []RoleHealth
}

// Role names, for RoleHealth.
const (
	RoleAnalysis = "analysis"
	RoleJudge    = "judge"
)

// RoleHealth is one role's switch and endpoint, for the settings section.
type RoleHealth struct {
	Role                 string
	Enabled              bool
	Model                string
	BaseURL              string
	KeySet               bool
	UsesWorkspaceGateway bool
}

// Health answers "is routing actually working for this workspace right now".
//
// It reads; it never dials the model. A settings page that probed on every
// render would turn an open tab into an outbound request loop, and would also
// defeat the breaker it is reporting on. Probe is the explicit re-check.
func (r *Router) Health(ctx context.Context, workspaceID string) (HealthReport, error) {
	settings, err := r.Store.Settings(ctx, workspaceID)
	if err != nil {
		return HealthReport{}, err
	}
	target, called := settings.PrimaryTarget()
	out := HealthReport{
		State:                settings.State(),
		Mode:                 settings.Mode(),
		Model:                target.Model,
		Threshold:            settings.Threshold(),
		BaseURL:              target.BaseURL,
		KeySet:               target.APIKey != "",
		UsesWorkspaceGateway: target.Override(),
	}
	for _, role := range []struct {
		name string
		on   bool
		t    Target
	}{
		{RoleAnalysis, settings.AnalysisOn(), settings.AnalysisTarget()},
		{RoleJudge, settings.JudgeOn(), settings.Target()},
	} {
		out.Roles = append(out.Roles, RoleHealth{
			Role:                 role.name,
			Enabled:              role.on,
			Model:                role.t.Model,
			BaseURL:              role.t.BaseURL,
			KeySet:               role.t.APIKey != "",
			UsesWorkspaceGateway: role.t.Override(),
		})
	}
	h := r.Breaker.Health(workspaceID)
	if !h.LastSuccess.IsZero() {
		out.LastSuccessAt = h.LastSuccess.Unix()
	}
	if !h.LastFailure.IsZero() {
		out.LastFailureAt = h.LastFailure.Unix()
	}
	// Checked before the breaker: a deployment with no internal LLM at all is
	// answerable on the spot, and making the reader wait for a ticket to fail
	// first would leave the settings section green while nothing can work.
	if a, ok := r.Judge.(Availability); ok && called && out.State == StateEnabled && !a.Available(target) {
		out.State = StateIneffective
		out.Reason = NotConfiguredReason
		out.Usable = false
		return out, nil
	}
	if out.State == StateEnabled && h.Open {
		out.State = StateIneffective
		// Reason is set ONLY here. A failure reason shown next to a healthy
		// chip reads as a current fault; the failure timestamp carries "it
		// stumbled once" without claiming routing is down.
		out.Reason = h.Reason
		out.RetryAfterSeconds = int(h.Retry.Seconds())
	}
	out.Usable = out.State == StateEnabled
	return out, nil
}

// Probe dials the routing model once, on purpose, and folds the answer into
// the breaker. It is what the "re-check" button in the settings section runs:
// the only way out of a cooldown that is shorter than waiting for it.
//
// It asks the judge a fixed, trivial question rather than a second kind of
// request, so a probe that passes is evidence about the call routing actually
// makes.
func (r *Router) Probe(ctx context.Context, workspaceID string) (HealthReport, error) {
	settings, err := r.Store.Settings(ctx, workspaceID)
	if err != nil {
		return HealthReport{}, err
	}
	target, called := settings.PrimaryTarget()
	if settings.State() != StateEnabled || !called {
		// Nothing to dial, and dialling anyway would be the one case where
		// a disabled workspace makes an outbound request.
		return r.Health(ctx, workspaceID)
	}
	_, err = r.Judge.Assign(ctx, target, JudgeState{
		Title:              "Routing self-check",
		DescriptionSummary: "Connectivity probe issued from the routing settings section. Answer with any tier.",
		Status:             "todo",
		Candidates:         []string{"strong"},
	})
	if err != nil {
		r.Breaker.Fail(workspaceID, err)
	} else {
		r.Breaker.Succeed(workspaceID)
	}
	return r.Health(ctx, workspaceID)
}
