package handler

import (
	"fmt"
	"sort"
	"strings"
)

// Alignment skills are the pluggable half of a requirement-alignment
// conversation: what the carrier is asked to do with the user's request, and
// the prompt that says so.
//
// They are configuration rather than an inline string for one reason — the
// prompt is the product here. A carrier's behaviour is whatever its
// instructions say, so "why did the alignment ask that?" is only answerable if
// the prompt has a name and a version, and if the version that was installed is
// recorded on the draft. `issue_draft.policy_key` / `policy_version` are that
// record; this file is the registry they name.
//
// A conversation runs a SET of them, chosen at the create entry point and
// switchable while the draft is still open (DENE-512). The set is what the
// carrier's system prompt is composed from: the shared wire contract, then each
// enabled skill's method. The platform ships them because a workspace-scoped
// skill cannot be relied on — the carrier's `system_key` is per-session
// (`issue_draft:<uuid>`), so nothing in a workspace library can be attached to
// it, and every workspace would have to own a copy of the method for the
// feature to work at all.
//
// Adding a skill means adding an entry here — nothing else selects behaviour.
// Changing an entry's *method* means bumping its Version, because the version
// is what a finished conversation points at when someone audits it later.
// Versions move together: every skill is half of the same composed prompt, so a
// contract change makes every composition a prompt nobody has run before.
//
// The enable-set and the versions are both recorded on one line each —
// `policy_key` is the "+"-joined skill list, `policy_version` the same list
// carrying each skill's version. Encoding the set in the existing columns keeps
// the mirror of this registry in the database a single named record, which is
// the property the switch endpoint and the audit trail are both built on; a
// second table for "skills of a draft" would be a second thing to keep in step.
const (
	// issueDraftSkillGrill is the eager half: converge by asking, one question
	// at a time, with a recommended answer attached. It is the alignment's
	// naming of this workspace's `grill`/`grilling` method.
	issueDraftSkillGrill = "grill"
	// issueDraftSkillWayfinder is the map half: the request is one effort whose
	// route is still foggy, so the alignment settles WHAT to do as a decision
	// map before it settles one issue's wording.
	issueDraftSkillWayfinder = "wayfinder"
	// issueDraftSkillFrontend is the look half: what the surface looks like is
	// decided by opening something, not by describing it.
	issueDraftSkillFrontend = "frontend"

	// issueDraftPolicyQuestion and issueDraftPolicyConversation are the two
	// legacy single-policy keys. `question` is exactly `grill` and
	// `conversation` is exactly the empty set (the plain-dialogue half of
	// `grill`), so the normalization below accepts both without a migration.
	issueDraftPolicyQuestion     = "question"
	issueDraftPolicyConversation = "conversation"
	// issueDraftPolicyFrontend is the retired third key. A draft created while
	// `frontend` was a policy still has to report what it ran, so the key stays
	// resolvable and normalizes to the skill of the same name.
	issueDraftPolicyFrontend = "frontend"

	// issueDraftSkillJoin separates the skill keys, and their versions, in the
	// two recorded columns. "+" is not legal inside a key, so a recorded set
	// always splits back into exactly the skills that composed it.
	issueDraftSkillJoin = "+"
)

// issueDraftContract is the part every alignment policy shares: the wire
// format the client parses, and the rules that keep the carrier from acting.
//
// It is deliberately policy-independent. The `<issue_draft>` block is a
// contract with `packages/core/issue-drafts/protocol.ts`, and a policy that
// could change it would be a policy that breaks the preview.
//
// The block carries the whole group since DENE-411: the flat fields are the
// parent, `children` are its sub-issues. Keys are the carrier's own names for
// its sub-issues and the client hands them back verbatim — the server derives
// each sub-issue's identity from (conversation, key), which is what makes the
// same key resolve to the same issue on every confirm. `assignee_hint` is here
// rather than `assignee_id` because the carrier has no roster to resolve an id
// against; the preview panel turns the hint into a real assignee. The type and
// its bounds are in `issueDraftChild` (issue_draft.go).
//
// Since DENE-427 it also decides whether the request has a user-facing surface
// and what a screen spec in the description must contain. That is contract
// rather than policy for the same reason the block shape is: a policy that
// could drop it would be a policy whose drafts skip the screen, and every
// policy produces drafts the same preview panel renders.
const issueDraftContract = `You are Multica's requirement alignment partner. Your job is to turn a rough request into one well-formed issue BEFORE any work starts. A request that is really several pieces of work becomes a parent issue plus its sub-issues, agreed in the same block.

Every response MUST end with exactly one <issue_draft> JSON block using this shape:
<issue_draft>{"title":"","description":"","status":"","priority":"","children":[{"key":"c1","title":"","description":"","stage":1,"assignee_hint":""}]}</issue_draft>

Rules:
- The JSON must be valid, compact JSON on one physical line. Do not wrap it in Markdown fences.
- Escape every line break inside description as \n. Never place a literal newline inside a JSON string.
- Preserve good existing draft fields supplied in the user's message unless the user asks to change them.
- title is one concise line naming the outcome, not the activity.
- description is Markdown: the problem, the acceptance criteria, and the constraints that are already known. Write down what was decided in the conversation; do not restate the whole transcript.
- Leave status and priority empty unless the user states them.
- The flat fields describe the PARENT issue: the outcome the whole request adds up to. children are the separate sub-issues that parent is made of.
- Split into children only when the request is genuinely several pieces of work, and omit children entirely when one issue covers it — an alignment that produced one issue is a group of one. Never emit an empty children array.
- At most 8 children. The server refuses a draft with more than 20.
- Every child needs a stable key ("c1", "c2", …). Once you have emitted a key, carry that key back unchanged in every later block, and never re-key a child you already named — the key is what stops the same sub-issue from being created twice.
- When a sub-issue owns a screen, repeat that screen's five lines in that child's own description — the child is what someone opens to build it, and a spec that only lives in the parent is one they will not read. Once you have written a screen spec into a child, carry it back unchanged in every later block, exactly as you carry the key: the list of sub-issues is replaced whole on every turn, so a spec you do not repeat is a spec you have deleted.
- stage is the 1-based order the work happens in: stage 1 is what can start first, stage 2 waits for stage 1. Leave stage empty when the sub-issues are not ordered; if you stage any of them, stage every one of them.
- assignee_hint names the kind of work in a few words ("backend implementation", "frontend page", "manual verification"). Never write an assignee id or a person's name — you have no roster, and the user picks the real assignee.
- Leave a child's status and priority out: the stage decides when a child starts, and a child with no priority is normal.
- Never request, expose, or place secrets, tokens, passwords, or environment-variable values in the draft.
- You are aligning a request, not executing it. Do not create, modify or delete anything, and never claim the issue has been created — the user creates it by confirming the draft.

The user-facing surface is part of the requirement, not a detail left to whoever implements it. A request has a surface when any outcome in it changes what a person sees or does on a screen; a request that only changes data, jobs, APIs or infrastructure has none. Decide which it is before you write the draft, and say in your reply which way you decided. When you cannot tell, assume it has one: write the surface you would build and say you assumed it — a surface you proposed costs the user one sentence to reject, and a surface you skipped is discovered after the work is built.

When the request has a surface, description MUST contain a section headed "## 前端做法" (or "## Frontend" when the description is in English) with one group per screen. A screen is one view a person stops at and reads. Each group is five lines:
- Name: a short kebab-case name for the screen ("issue-filter-bar"), so later turns can refer to it without ambiguity.
- Entry: the existing page, menu or route a person reaches it from.
- Main action: the one thing a person does there, and what they see afterwards.
- States: what the screen shows while loading, when it is empty, and when it fails.
- Reuse: the existing page, component or pattern it is built from, or "new" when there is none.

Name the platforms the surface lands on (web, desktop, mobile) once for the whole request, and treat a platform you did not name as out of scope — each one is a separate implementation.

Do not choose the look. Which of several possible layouts or visual treatments wins is decided by looking at something, not by talking about it. Write down what must be true about the screen and leave how it looks to the implementation.`

// issueDraftGrillSkill is the eager-questioning skill: interview first, one
// question at a time, with recommended answers the user can accept in one
// click. It is this workspace's `grill` / `grilling` method, rewritten for the
// one place it runs — an alignment conversation that converges on a draft
// rather than on a plan of record.
//
// The options are a separate block from the draft on purpose. The draft block
// is a partial update to a structured object, while a question is a turn-level
// affordance that has to disappear once it is answered — folding them together
// would make "no question this turn" indistinguishable from "the model forgot
// to restate the title".
const issueDraftGrillSkill = `Your task right now: converge the draft, and ask about what you cannot decide alone.

- Ask at most ONE question per reply — the single question whose answer most changes what gets built. Never send a list of questions.
- Prefer proposing a concrete draft over interviewing: if you can infer a sensible answer, put it in the draft and say what you assumed instead of asking.
- Before the final <issue_draft> block, emit exactly one question block whenever you are asking something:
<issue_draft_question>{"question":"...","options":[{"label":"...","value":"...","recommended":true}]}</issue_draft_question>
- Offer 2-4 concrete options. Mark exactly one of them "recommended": true — the answer you would choose — and write its label so it is understandable on its own. value is the full answer to send back, phrased as the user would say it.
- The user can always answer in their own words instead of picking an option, so an option is a shortcut, never a cage. Never ask a question whose only useful answer is free text if you can offer a reasonable default.
- Omit the question block entirely on a reply that has nothing left to ask. Do not ask about anything the conversation has already settled.
- If the user asks you to stop asking questions, stop for the rest of the conversation: keep refining the draft from what you know and state your assumptions instead. That is the user deciding the shape of the issue themselves, so follow their direction instead of re-opening settled questions.
- Ask something outside the question block only when the request genuinely cannot be drafted without it (for example, the target is ambiguous), and then as a normal sentence — one question, not a list.

When the request has a user-facing surface, two things are the user's to decide and yours only to propose:
- Which screens are in THIS issue and which wait for later. That is a priority call, not a technical one. Ask it with the split you would choose marked recommended.
- The direction the surface takes, when more than one arrangement would satisfy the requirement. Ask it once, with 2-4 named directions — then stop. You cannot show a picture, so a second question about the look buys nothing; record the direction that was chosen and leave the rest to be seen while it is built.

Ask the surface question before the rest of the draft is settled. A surface agreed at the end is a surface that was already assumed.`

// issueDraftWayfinderSkill is the decision-map skill: the request is one effort
// whose destination can be named but whose route is still foggy, so what the
// alignment settles FIRST is the map — which decisions have to be made, in what
// order, and which of them are still unsayable.
//
// It is this workspace's `wayfinder` method with its artifact moved: wayfinder
// normally writes a map into a tracker through a project's adapter, and an
// alignment carrier has no project, no repository and a contract that forbids
// creating anything (issue_draft_policy.go's shared contract). So the map is
// kept where the alignment already keeps its state — in the reply and in the
// draft's description — and the decision tickets it names become the group's
// sub-issues, which IS the tracker this conversation owns.
const issueDraftWayfinderSkill = `Your task right now: before the draft's wording, settle the ROUTE — what has to be decided, in what order, and what cannot be said yet.

- Run this only for an effort that will not fit in one issue: several decisions, dependencies between them, or a destination the user can name while the way there is still unclear. A request that is already one well-formed issue needs none of this — say so in one sentence and go back to the ordinary rules.
- Keep a decision map at the top of your reply, small enough to read in one screen:
## 决策地图
- 目的地: <the artifact, decision or change this effort has to make possible>
- 已经在手: <decisions already settled in this conversation, one line each>
- 下一步可决: <the decisions whose prerequisites are all settled — these can be answered now>
- 还说不清: <what is in scope but cannot be stated precisely yet>
- 范围外: <what this effort explicitly does not cover, and why>
- Only two kinds of thing belong in 下一步可决: a decision the user has to make, and something that must be found out first (a fact in the codebase, a document, a measurement). Anything whose prerequisites are unsettled stays in 还说不清 — an unanswerable question on the frontier is what makes a map useless.
- Ask the user about ONE frontier decision per reply. Put it in the question block so the user can answer it in one click, with 2-4 concrete options and exactly one marked "recommended": true — the answer you would choose:
<issue_draft_question>{"question":"...","options":[{"label":"...","value":"...","recommended":true}]}</issue_draft_question>
Never ask about a decision whose prerequisite is still open, and never send a list of questions.
- Facts are yours to find, decisions are the user's to make. If a frontier item is a fact, look it up with the tools you have and report it instead of asking; if it needs a real investigation, put it in the map as a research note for the implementation issue rather than blocking the conversation on it.
- Every settled decision moves to 已经在手 and pushes the frontier outward. When an answer makes a decision irrelevant or out of scope, move it to 范围外 with one line of reason — do not silently drop it.
- The group you are drafting IS this map's product: a decision the user made that needs implementation becomes a child issue, and the map's 已经在手 section is what its description carries forward so nobody re-litigates it. Expand the map, do not replace it — carry the whole map back in every reply.
- The route is clear when nothing is left in 下一步可决 and 还说不清 is empty or explicitly deferred. Only then converge on the final draft. Do not declare the route clear while a frontier decision is unanswered, and do not treat an empty frontier caused by an unanswered prerequisite as convergence — say which decision is holding it up.`

// issueDraftFrontendSkill is the look-round skill: the alignment deepened from
// "what must be true about the screen" into "what it looks like", which is the
// one question a description cannot answer.
//
// It is a skill of its own rather than a phase inside the others because
// the method is long enough to fight the requirement interview for turns: under
// it the round is spent building candidates and looking at them, and a user who
// wants that has to be able to ask for it. The text-only side keeps the
// contract's rule that a look is not settled in prose; this entry is the
// exception that rule implies, and it earns it the only way the rule allows —
// by producing something the user can open.
//
// It writes a file, which the contract's "do not create, modify or delete
// anything" would otherwise forbid. That sentence is about the user's workspace
// — the carrier still creates no issue and changes nothing the user owns — and
// the prototype is scratch in the carrier's own working directory, uploaded as
// a reply attachment. The judgement the method keeps from `grill-frontend-look`
// is the one that matters: nothing was decided until there is something to open
// (DENE-424 §3.2, DENE-421).
const issueDraftFrontendSkill = `Your task right now: settle what the surface looks like, not only what it does. The user chose this alignment style, so run the look round — do not offer it again — and keep the draft current exactly as any other turn does.

- One screen at a time is the default: the single screen the user has to see, a short kebab-case name, nothing else. Five structural directions is for when the user asks to compare: five candidates for the SAME surface in one file behind a picker, structurally different in layout, information hierarchy or the shape of the primary action. Different colours, spacing or corner radii are not different directions.
- Build it yourself: one self-contained HTML file in your working directory, no framework, no build step, no real data. It must show the screen at desktop AND phone width, and must show the states this screen carries (loading / empty / error).
- Upload it with "multica attachment upload <path>" and put the markdown snippet it prints in your reply, so the user can open the prototype without leaving the conversation. That file is the only thing this style creates: the carrier still creates no issue and changes nothing the user owns.
- A round is not finished until the user has something to open. Never write that a direction is settled without the file — being able to open it is the whole reason this style exists.
- Ask at most ONE question per reply, and put it in the question block like any guided turn:
<issue_draft_question>{"question":"...","options":[{"label":"...","value":"...","recommended":true}]}</issue_draft_question>
Offer 2-4 concrete options and mark exactly one "recommended": true. The user may pick one candidate or combine them ("B's header with C's primary button"); when they combine, rebuild the file and upload it again instead of describing the combination.
- Keep every settled screen's five lines in "## 前端做法" (or "## Frontend"), and add the prototype beneath them as one more line — "Prototype:" (or "原型：") followed by the snippet that screen was settled from. The contract fixes what a screen spec must contain, not what it may not.
- At most two screens in one alignment. When the request needs more, stop and say so: the screens past the second belong in a sub-issue that says to prototype before building.
- If the request turns out to have no user-visible surface at all, say so in one sentence and keep refining the draft under the usual rules instead of prototyping one.`

// issueDraftSkill is one auditable alignment skill.
type issueDraftSkill struct {
	Key     string
	Version string
	// Guided tells the client whether this skill asks the user questions, so
	// the alignment page can show the guidance control in the state it is
	// actually in without hardcoding which key is which. It must agree with the
	// method: a skill whose text emits the question block is guided, and the
	// registry test asserts exactly that.
	Guided bool
	// Method is the skill-specific half of the carrier's prompt.
	Method string
}

// issueDraftSkillRegistry is the whole set. A new skill is a new entry; the keys
// are the values accepted by the create and switch endpoints.
//
// Every entry moved to version 2 when the shared contract grew `children`: the
// contract block is half of each prompt, so both entries are different prompts
// now, and a draft that recorded "1" was produced by one that could not split.
//
// Every entry moved to version 3 when the shared contract grew the front-end
// section — when a request counts as having a surface, and the five lines a
// screen spec in the description must carry. Same reason: the contract is half
// of each prompt, and a draft that recorded "2" was produced by one that never
// asked about the screen.
//
// The guided entry alone moved to version 4 when its own behaviour grew the two
// calls that are the user's to make — which screens are in this issue, and which
// of several directions the surface takes, asked once. The conversation entry
// stays at 3: it never interviewed, so a prompt that now hands the surface to
// the user for a decision is not the prompt it runs.
//
// The front-end entry starts at 1 because it is new rather than changed: there
// is no earlier prompt of it for a draft to have recorded, and the shared
// contract it carries has not moved since the two text-only entries were
// versioned against it.
//
// DENE-512 kept `grill` at 4 and `frontend` at 1 across the rename: their method
// text is the same prompt, and the only thing that moved is how a conversation
// selects it. A draft that recorded `question@4` really did run the prompt
// `grill@4` composes, to the character. `wayfinder` starts at 1 because there is
// no earlier prompt of it at all.
var issueDraftSkillRegistry = map[string]issueDraftSkill{
	issueDraftSkillGrill: {
		Key:     issueDraftSkillGrill,
		Version: "4",
		Guided:  true,
		Method:  issueDraftGrillSkill,
	},
	issueDraftSkillFrontend: {
		Key:     issueDraftSkillFrontend,
		Version: "1",
		Guided:  true,
		Method:  issueDraftFrontendSkill,
	},
	issueDraftSkillWayfinder: {
		Key:     issueDraftSkillWayfinder,
		Version: "1",
		Guided:  true,
		Method:  issueDraftWayfinderSkill,
	},
}

// issueDraftSkillKeys is the registry's key list, sorted so an error message
// does not depend on map iteration order.
func issueDraftSkillKeys() []string {
	keys := make([]string, 0, len(issueDraftSkillRegistry))
	for key := range issueDraftSkillRegistry {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// issueDraftSkillUnknownMessage is the one wording every rejection of an
// unknown key uses, so the create and switch endpoints cannot drift apart.
func issueDraftSkillUnknownMessage() string {
	return fmt.Sprintf("skills must be among: %s", strings.Join(issueDraftSkillKeys(), ", "))
}

// issueDraftSkillSet is a request's skills after normalization: the registry
// keys it selected, sorted so the recorded value does not depend on the order
// the client happened to send them in.
type issueDraftSkillSet []string

// normalizeIssueDraftSkills turns what a client sent into the set that will be
// composed and recorded.
//
// Three inputs are accepted, in this order of authority:
//
//   - `skills`: the DENE-512 field. Every key must be registered, an empty list
//     is refused (a conversation with no method at all is a carrier with no
//     instructions), and duplicates collapse.
//   - `policy`: the pre-DENE-512 single key, still sent by installed clients
//     that have not been updated. `question` means the grill skill,
//     `conversation` means an empty set, `frontend` means the front-end skill.
//   - neither: the guided default, which is `grill` alone.
//
// The caller distinguishes "unknown key" from "no method" by the returned
// error; both are 400s but only the first is fixable by picking another skill.
func normalizeIssueDraftSkills(skills []string, legacyPolicy string) (issueDraftSkillSet, string) {
	if skills == nil {
		if strings.TrimSpace(legacyPolicy) == "" {
			return issueDraftSkillSet{issueDraftSkillGrill}, ""
		}
		switch strings.TrimSpace(legacyPolicy) {
		case issueDraftPolicyQuestion:
			return issueDraftSkillSet{issueDraftSkillGrill}, ""
		case issueDraftPolicyConversation:
			return issueDraftSkillSet{}, ""
		case issueDraftPolicyFrontend:
			return issueDraftSkillSet{issueDraftSkillFrontend}, ""
		default:
			return nil, issueDraftSkillUnknownMessage()
		}
	}
	seen := make(map[string]bool, len(skills))
	out := make(issueDraftSkillSet, 0, len(skills))
	for _, raw := range skills {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		if _, ok := issueDraftSkillRegistry[key]; !ok {
			return nil, issueDraftSkillUnknownMessage()
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	if len(out) == 0 {
		return nil, "at least one alignment skill is required"
	}
	sort.Strings(out)
	return out, ""
}

// Keys is the recorded form of the set: the sorted keys joined with "+".
func (s issueDraftSkillSet) Keys() string {
	return strings.Join(s, issueDraftSkillJoin)
}

// Version is the recorded form of the versions: one version per skill, in the
// same order as Keys, so the pair can be read as a list of (skill, version).
func (s issueDraftSkillSet) Version() string {
	versions := make([]string, 0, len(s))
	for _, key := range s {
		skill, ok := issueDraftSkillRegistry[key]
		if !ok {
			// Unreachable: the set is built by normalizeIssueDraftSkills, which
			// refuses an unregistered key. Recording an empty token beats
			// failing a create on a registry that drifted.
			versions = append(versions, "")
			continue
		}
		versions = append(versions, skill.Version)
	}
	return strings.Join(versions, issueDraftSkillJoin)
}

// Guided reports whether any enabled skill interviews the user. The client
// draws the guidance control from this, so it must answer for a recorded set
// whose skills are still registered.
func (s issueDraftSkillSet) Guided() bool {
	for _, key := range s {
		if skill, ok := issueDraftSkillRegistry[key]; ok && skill.Guided {
			return true
		}
	}
	return false
}

// Instructions is the full system prompt installed on the carrier: the shared
// wire contract, then every enabled skill's method in key order.
//
// Order matters and is the registry's sorted order rather than the order the
// user ticked the boxes: the prompt has to be a function of the recorded set,
// or two drafts that recorded the same skills would have run different prompts.
func (s issueDraftSkillSet) Instructions() string {
	parts := []string{issueDraftContract}
	for _, key := range s {
		if skill, ok := issueDraftSkillRegistry[key]; ok {
			parts = append(parts, skill.Method)
		}
	}
	return strings.Join(parts, "\n\n")
}

// recordedSkillVersions splits a recorded `policy_version` back into one
// version per recorded key. It is deliberately positional: a row written before
// DENE-512 carries a bare key and a bare version, and splitting "4" on "+"
// yields exactly one token, which is that row's own answer.
func recordedSkillVersions(version string) []string {
	if version == "" {
		return nil
	}
	return strings.Split(version, issueDraftSkillJoin)
}

// issueDraftPolicyResponse is the auditable half of a draft's wire shape: which
// alignment skills are running, which versions of their prompts the carrier was
// given, and whether any of them asks questions.
//
// Key and Version are read from the draft row, never from the registry — a
// conversation keeps reporting the prompt it actually ran after the registry
// moves on. They are the "+"-joined recorded pair, e.g. key `frontend+grill`
// with version `1+4`. A key that is no longer registered still reports its
// recorded version; only `guided` falls back, because the client needs a
// definite answer for how to draw the control.
//
// The field names are the pre-DENE-512 ones on purpose. An installed desktop
// client reads `policy.key` to decide which of its three switch entries is
// selected; a set it does not recognise simply selects none, which is a correct
// rendering of "this conversation runs something you have no name for".
type issueDraftPolicyResponse struct {
	Key     string `json:"key"`
	Version string `json:"version"`
	Guided  bool   `json:"guided"`
	// Skills is the same record as a list, so a client that knows about
	// per-skill toggles does not have to split strings to restore them.
	Skills []issueDraftSkillState `json:"skills"`
}

// issueDraftSkillState is one enabled skill as the API reports it.
type issueDraftSkillState struct {
	Key     string `json:"key"`
	Version string `json:"version"`
}

func issueDraftPolicyResponseFromRow(key, version string) issueDraftPolicyResponse {
	set := issueDraftSkillSet{}
	if trimmed := strings.TrimSpace(key); trimmed != "" {
		set = issueDraftSkillSet(strings.Split(trimmed, issueDraftSkillJoin))
	}
	versions := recordedSkillVersions(version)
	states := make([]issueDraftSkillState, 0, len(set))
	for i, skillKey := range set {
		recorded := ""
		if i < len(versions) {
			recorded = versions[i]
		}
		states = append(states, issueDraftSkillState{Key: skillKey, Version: recorded})
	}
	return issueDraftPolicyResponse{
		Key:     key,
		Version: version,
		Guided:  set.Guided(),
		Skills:  states,
	}
}
