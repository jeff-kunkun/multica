package handler

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Alignment capabilities are the methods an alignment conversation can be
// opened with, chosen per conversation and assembled into the carrier's
// instructions next to the policy.
//
// They are the Multica-native home of four methods that used to live only in
// the agent's own skill library (`kun-agent-mono/skills/_shared/`): wayfinder,
// grill, grilling and grill-frontend-look. They live here because the carrier
// is not an ordinary agent session — it is created per conversation, has no
// workspace skills bound to it, and its whole behaviour is its instructions
// (issue_draft_policy.go says why that is configuration). A method that has to
// be discovered from a skill description cannot be discovered by a carrier
// whose instructions already spell out what to do; a method assembled into
// those instructions can.
//
// Two things make them capabilities rather than policies. A policy answers
// "how does this conversation ask" — interview, plain dialogue, look round —
// and a draft runs exactly one. A capability answers "which methods may the
// carrier reach for", and a draft runs any subset of them. The client picks
// that subset: the create dialog's skill checkboxes are the input, and the
// prompt installed on the carrier is assembled from them.
const (
	issueDraftCapabilityWayfinder         = "wayfinder"
	issueDraftCapabilityGrill             = "grill"
	issueDraftCapabilityGrilling          = "grilling"
	issueDraftCapabilityGrillFrontendLook = "grill-frontend-look"
)

// issueDraftCapability is one built-in method: the condition that makes it
// apply, and the discipline that runs once it does.
//
// A capability carries no `Guided` flag and no version of its own. The first
// is the policy's dimension — a method does not decide whether the carrier
// interviews, and the two ways it can ask (the question block, or a plain
// sentence) are settled by the policy the method runs under. The second is
// `issueDraftCapabilityVersion`'s, one number for the library, because the
// fragments are assembled together and a per-fragment version would have to be
// reconstructed on every read to answer the only question that matters: which
// text was this prompt built from.
type issueDraftCapability struct {
	Key string
	// Requires names the capabilities this one is incoherent without. It is
	// declared rather than validated so the client can offer a short list
	// without having to know the whole graph: asking for `grill` and getting
	// `grilling` as well is the correct answer to what was asked, not a
	// substitution for it.
	Requires []string
	// Fragment is the method itself, written for the carrier: a text-only
	// conversation that may produce one file and may not create anything.
	Fragment string
}

// issueDraftCapabilityOrder is assembly order, and it is the registry's own
// rather than the client's. The prompt is the product here, and a prompt whose
// sections move because a checkbox list was reordered is a prompt that is not
// reproducible from the record.
var issueDraftCapabilityOrder = []string{
	issueDraftCapabilityWayfinder,
	issueDraftCapabilityGrill,
	issueDraftCapabilityGrilling,
	issueDraftCapabilityGrillFrontendLook,
}

// issueDraftDefaultCapabilities is the set a create request that names none
// gets — the same three the create entry point offers as checked boxes, with
// `grilling` pulled in by `grill`.
//
// The default is the built-in set rather than the empty one because that is
// what "built in" means for this product: an alignment opened from a client
// that has no capability picker yet still runs the methods, and the picker's
// job is to let the user turn them OFF. A draft that wants none says so
// explicitly — see resolveIssueDraftCapabilities.
var issueDraftDefaultCapabilities = []string{
	issueDraftCapabilityWayfinder,
	issueDraftCapabilityGrill,
	issueDraftCapabilityGrillFrontendLook,
}

// issueDraftCapabilityVersion is the version of the whole fragment library
// recorded on every draft, next to the keys it was assembled from.
//
// The policy version pins the contract and the policy's own behaviour; it
// cannot pin text that lives here, because the same policy version is recorded
// by drafts that selected different capabilities. Recording the keys alone
// would name the methods without naming their text, so a finished alignment
// could not answer "which prompt produced this issue" once a fragment was
// edited — the one question the version record exists to answer. Bump this
// whenever any fragment below changes; a draft keeps reporting the version it
// ran (issue_draft_policy.go's rule for policy versions, applied to the half
// of the prompt that is not a policy).
const issueDraftCapabilityVersion = "1"

// issueDraftCapabilityPreamble introduces the assembled fragments. It is what
// keeps a method from reading as a second, competing set of rules: the block
// contract above and the task below are the alignment's, and a method is a
// tool it reaches for.
const issueDraftCapabilityPreamble = `The methods below are the alignment capabilities this conversation was opened with. Reach for one when its condition is met, and say which one you are using. They are how you work a request that needs them; they do not change the draft-block contract above, and where a method and your task for this turn disagree about cadence, the task wins.`

// issueDraftCapabilityWayfinderMethod is the decision-map method: what to do
// with a request whose destination is nameable and whose route is not.
//
// Rewritten for the carrier rather than copied from the user's skill. The
// original maintains a map, claims decision tickets and wires blocking edges
// through a tracker adapter in a repository; an alignment has no repository —
// its whole structured output is the issue group it is agreeing on. So the
// group IS the map: the parent is the destination, the children are the
// decisions that can be settled now, and `stage` is the blocking order. The
// judgement the method keeps is the one that makes a map worth drawing: a
// question you cannot phrase precisely yet is not a ticket.
const issueDraftCapabilityWayfinderMethod = `Wayfinder — a request whose destination can be named but whose route is still in fog.

- Reach for it when the conversation keeps producing "it depends" instead of a piece of work, and nobody can yet say what the next decisions are.
- An alignment charting a map produces the map, not the destination. Nothing in the draft is implementation, and it must not read as if the work were already understood.
- The group is the map. The parent is the destination. Each child is one decision that can be settled in a single sitting — titled as the question it settles, not as the work it implies — and staged by what blocks what: stage 1 holds the decisions that can be answered now, stage 2 the ones waiting on them.
- The parent's description carries the map itself under a fixed "## 决策地图" heading ("## Decision map" when the description is in English): the decisions already settled, one line each; "Not yet specified" for what is in scope but cannot be stated precisely yet; "Out of scope" with the reason each excluded direction was excluded. A settled decision that turned into work still gets its line — the map is what the issues were derived from.
- One question keeps a map honest: can this be stated precisely now? If it can, it is a child even when it is blocked and its answer is unknown. If it cannot, it stays under "Not yet specified" and graduates when a settled decision makes it statable. Never open a child for a question you cannot phrase.
- A request that is plainly one piece of work is not a map. Say so and align it normally.`

// issueDraftCapabilityGrillMethod is the entry to a grilling round, and
// deliberately nothing more — the discipline is the next capability's.
//
// The split is the user's skill library's, and it is load-bearing: an
// alignment can hold the tree without spending its turns on it (the request
// needs a decision, not an interview), and a grilling round can be reached
// from a request that never needed a map.
const issueDraftCapabilityGrillMethod = `Grill — opening a grilling round.

- Reach for it when the dependencies are already visible enough that the remaining decisions can be converged inside this one conversation. The method is the grilling capability's; this one only decides when to open it.
- Opening it is a sentence, not a ceremony: name the decision tree you are about to work through and start its first round. Do not open a round the user did not ask for and the request does not need — a request that can be drafted is drafted.
- When the dependencies are not visible yet, the request is a map or a research question, not a grill. Do not interview your way towards a tree you cannot draw.`

// issueDraftCapabilityGrillingMethod is the interview discipline: how a round
// is shaped, what an answer does to the tree, and where the gate sits.
//
// It is written against the alignment's medium rather than the original's. The
// original asks the whole frontier in one turn; the alignment asks one
// question per reply and puts it in a block the client turns into answer
// chips, so the frontier is the carrier's working state and the single
// question is the ask. And the original can send its agent off to check facts
// in a repository; this carrier has no filesystem, so a fact that is not
// already in the conversation is a question — never an assumption the draft
// quietly carries.
const issueDraftCapabilityGrillingMethod = `Grilling — converging a plan by interviewing, round by round.

- Draw the decision tree before asking anything: each judgement unlocks the ones hanging behind it. A round is every decision whose prerequisites are already settled, so it can be answered without guessing.
- Name the round in your reply — which decisions it holds, which are already settled and how — and then ask the one question whose answer most changes what gets built, in whichever form your task for this turn allows. The frontier is your working state, not a questionnaire: a reply that lists every open decision as a question is a list this medium cannot carry.
- Every question comes with the answer you would choose. Never ask bare. The recommendation is what the user is correcting when they disagree, and it is what makes a round answerable in one line.
- Answers reshape the tree. Mark each one settled or not: a vague answer, a skipped one, and two answers that contradict each other are all still unsettled prerequisites. Do not re-ask in the same words — put the trade-off a different way — and when the user parks a decision, record what it is waiting on and keep everything behind it out of the round until it moves.
- Facts are yours, decisions are theirs. Anything already in this conversation or in the draft is a fact: use it and say so. This conversation has no filesystem and no repository, so a fact that is not here is a question for the user, not an investigation you can run — and never turn a fact you could not check into an assumption the draft carries silently.
- Nothing is executed before agreement. You create nothing here; the draft is a proposal and the user is the one who confirms it. A round that ends with work started has skipped the gate it exists to hold.`

// issueDraftCapabilityFrontendLookMethod is the look round: the part of an
// alignment that settles what a surface looks like rather than what it does.
//
// It used to be the whole of the `frontend` policy's behaviour. It moved here
// so there is one copy of it: the method applies whenever the user has asked
// for it, and the `frontend` policy — which is the alignment opened AS the
// look round — now declares it as a requirement instead of restating it. The
// rewrite from the user's skill is the one DENE-424 measured: an alignment
// carrier has no repository, so the artifact is an attachment on the reply
// rather than `docs/design/prototypes/<screen>.html`, and "收成一条线" is not
// reachable here at all — a flow belongs to the implementation that owns the
// repository.
const issueDraftCapabilityFrontendLookMethod = `Frontend look — settling what a surface looks like, not only what it does.

- Offer it before starting it, as the one question of that turn, when the request changes something a person will look at and the shape of the screen changes how many issues this becomes. Start it when the user says yes — or straight away when they already asked for it, or when this alignment was opened as the look round, because then it is the round and it is not offered again.
- One screen at a time is the default: the single screen the user has to see, a short kebab-case name, nothing else. Five structural directions is for when the user asks to compare: five candidates for the SAME surface in one file behind a picker, structurally different in layout, information hierarchy or the shape of the primary action. Different colours, spacing or corner radii are not different directions.
- Build it yourself: one self-contained HTML file in your working directory, no framework, no build step, no real data. It must show the screen at desktop AND phone width, and must show the states this screen carries (loading / empty / error).
- Upload it with "multica attachment upload <path>" and put the markdown snippet it prints in your reply, so the user can open the prototype without leaving the conversation. That file is scratch in your own working directory, and it is the only thing this creates: no issue is created, and nothing the user owns is changed.
- A round is not finished until the user has something to open. Never write that a direction is settled without the file — being able to open it is the whole reason this exists.
- The user may pick one candidate or combine them ("B's header with C's primary button"); when they combine, rebuild the file and upload it again instead of describing the combination.
- Keep every settled screen's five lines in "## 前端做法" (or "## Frontend"), and add the prototype beneath them as one more line — "Prototype:" (or "原型：") followed by the snippet that screen was settled from. The contract fixes what a screen spec must contain, not what it may not.
- At most two screens in one alignment. When the request needs more, stop and say so: the screens past the second belong in a sub-issue that says to prototype before building.
- This is a visual reference, not production code.
- If the request turns out to have no user-visible surface at all, say so in one sentence and keep refining the draft under the usual rules instead of prototyping one.`

// issueDraftCapabilityRegistry is the whole library. A new capability is a new
// entry plus its place in issueDraftCapabilityOrder.
var issueDraftCapabilityRegistry = map[string]issueDraftCapability{
	issueDraftCapabilityWayfinder: {
		Key:      issueDraftCapabilityWayfinder,
		Fragment: issueDraftCapabilityWayfinderMethod,
	},
	issueDraftCapabilityGrill: {
		Key:      issueDraftCapabilityGrill,
		Requires: []string{issueDraftCapabilityGrilling},
		Fragment: issueDraftCapabilityGrillMethod,
	},
	issueDraftCapabilityGrilling: {
		Key:      issueDraftCapabilityGrilling,
		Fragment: issueDraftCapabilityGrillingMethod,
	},
	issueDraftCapabilityGrillFrontendLook: {
		Key:      issueDraftCapabilityGrillFrontendLook,
		Fragment: issueDraftCapabilityFrontendLookMethod,
	},
}

// issueDraftCapabilityUnknownMessage is the one wording every rejection of an
// unknown key uses, so create and switch cannot drift apart.
func issueDraftCapabilityUnknownMessage() string {
	keys := make([]string, 0, len(issueDraftCapabilityRegistry))
	for key := range issueDraftCapabilityRegistry {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return fmt.Sprintf("capabilities must be one of: %s", strings.Join(keys, ", "))
}

// resolveIssueDraftCapabilities turns a client's key list into the capabilities
// the carrier will actually be given.
//
// Three rules, each of which the record depends on:
//
//   - `nil` means the request did not name any, and gets the built-in default.
//     An empty list is not the same request: it is a client saying "none", and
//     it is how the picker's "uncheck everything" state reaches the server.
//   - An unknown key is refused rather than ignored. A prompt assembled from a
//     key that silently vanished looks like it worked and behaves like a
//     method the user cannot see is missing.
//   - `Requires` is expanded here, once, so what is recorded on the draft is
//     what ran — not what was asked for.
//
// The result is in registry order and free of duplicates, so two clients that
// asked for the same set in different orders assemble the same prompt.
func resolveIssueDraftCapabilities(keys []string) ([]issueDraftCapability, error) {
	if keys == nil {
		keys = issueDraftDefaultCapabilities
	}
	selected := make(map[string]struct{}, len(keys))
	var add func(key string) error
	add = func(key string) error {
		key = strings.TrimSpace(key)
		if key == "" {
			return errors.New(issueDraftCapabilityUnknownMessage())
		}
		capability, ok := issueDraftCapabilityRegistry[key]
		if !ok {
			return errors.New(issueDraftCapabilityUnknownMessage())
		}
		if _, seen := selected[key]; seen {
			return nil
		}
		selected[key] = struct{}{}
		for _, required := range capability.Requires {
			if err := add(required); err != nil {
				return err
			}
		}
		return nil
	}
	for _, key := range keys {
		if err := add(key); err != nil {
			return nil, err
		}
	}

	resolved := make([]issueDraftCapability, 0, len(selected))
	for _, key := range issueDraftCapabilityOrder {
		if _, ok := selected[key]; !ok {
			continue
		}
		resolved = append(resolved, issueDraftCapabilityRegistry[key])
	}
	return resolved, nil
}

// issueDraftCapabilityKeys is the resolved set as keys, in assembly order —
// the shape the draft row and the API both record.
func issueDraftCapabilityKeys(capabilities []issueDraftCapability) []string {
	keys := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		keys = append(keys, capability.Key)
	}
	return keys
}

// issueDraftMergeCapabilities unions resolved sets into one, in registry order.
//
// It is how a policy's own requirements join the client's selection: the
// `frontend` policy requires the look-round method whether or not the picker
// left it checked, and the union — not the client's list — is what is both
// assembled and recorded. Every input is already resolved (requires expanded,
// ordered, deduplicated), so this only merges: the caller cannot accidentally
// re-expand a recorded key list into something a later registry would resolve
// differently.
func issueDraftMergeCapabilities(sets ...[]issueDraftCapability) []issueDraftCapability {
	present := make(map[string]struct{})
	for _, set := range sets {
		for _, capability := range set {
			present[capability.Key] = struct{}{}
		}
	}
	merged := make([]issueDraftCapability, 0, len(present))
	for _, key := range issueDraftCapabilityOrder {
		if _, ok := present[key]; !ok {
			continue
		}
		merged = append(merged, issueDraftCapabilityRegistry[key])
	}
	return merged
}

// issueDraftKnownCapabilityKeys filters a recorded key list down to the keys
// this build still registers, in registry order.
//
// It exists for the one read that cannot fail: a draft row may name a
// capability a later build retired, and rebuilding its prompt must not turn a
// conversation that is merely old into a 500. An unknown key is dropped from
// the prompt and stays on the row — the row is the record of what ran, and
// rewriting it would be the one edit an audit trail must not make.
func issueDraftKnownCapabilityKeys(keys []string) []string {
	known := make([]string, 0, len(keys))
	for _, key := range issueDraftCapabilityOrder {
		for _, recorded := range keys {
			if recorded == key {
				known = append(known, key)
				break
			}
		}
	}
	return known
}

// issueDraftCapabilitiesFromKeys resolves keys that are already recorded on a
// draft, where an unknown key is dropped rather than refused.
func issueDraftCapabilitiesFromKeys(keys []string) []issueDraftCapability {
	resolved := make([]issueDraftCapability, 0, len(keys))
	for _, key := range issueDraftKnownCapabilityKeys(keys) {
		resolved = append(resolved, issueDraftCapabilityRegistry[key])
	}
	return resolved
}

// issueDraftCapabilityResponse is the auditable half of a draft's capability
// record: which methods this alignment run carries, and which version of their
// text the carrier was given.
//
// Keys is the recorded list, not the registry's idea of it. A capability a
// later build retired stays in the answer — the row is the record of what ran,
// and a read that quietly dropped it would make an old alignment claim a prompt
// it never had. The client intersects the list with the keys it knows, which is
// the same shape the policy response already uses for a key it no longer
// recognizes.
type issueDraftCapabilityResponse struct {
	Keys    []string `json:"keys"`
	Version string   `json:"version"`
}

// issueDraftCapabilityResponseFromRow reads the record off a draft row. Keys is
// never nil, so the client parses an empty list rather than a missing field.
func issueDraftCapabilityResponseFromRow(keys []string, version string) issueDraftCapabilityResponse {
	out := issueDraftCapabilityResponse{Keys: make([]string, 0, len(keys)), Version: version}
	out.Keys = append(out.Keys, keys...)
	return out
}
