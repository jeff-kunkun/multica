package handler

import (
	"fmt"
	"sort"
	"strings"
)

// Alignment policies are the pluggable half of a requirement-alignment
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
// Adding a policy means adding an entry here — nothing else selects behaviour.
// Changing an entry's *prompt* means bumping its Version, because the version
// is what a finished conversation points at when someone audits it later.
const (
	issueDraftPolicyQuestion     = "question"
	issueDraftPolicyConversation = "conversation"
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
- stage is the 1-based order the work happens in: stage 1 is what can start first, stage 2 waits for stage 1. Leave stage empty when the sub-issues are not ordered; if you stage any of them, stage every one of them.
- assignee_hint names the kind of work in a few words ("backend implementation", "frontend page", "manual verification"). Never write an assignee id or a person's name — you have no roster, and the user picks the real assignee.
- Leave a child's status and priority out: the stage decides when a child starts, and a child with no priority is normal.
- Never request, expose, or place secrets, tokens, passwords, or environment-variable values in the draft.
- You are aligning a request, not executing it. Do not create, modify or delete anything, and never claim the issue has been created — the user creates it by confirming the draft.`

// issueDraftQuestionPolicy is the guided policy: interview first, one question
// at a time, with recommended answers the user can accept in one click.
//
// The options are a separate block from the draft on purpose. The draft block
// is a partial update to a structured object, while a question is a turn-level
// affordance that has to disappear once it is answered — folding them together
// would make "no question this turn" indistinguishable from "the model forgot
// to restate the title".
const issueDraftQuestionPolicy = `Your task right now: converge the draft, and ask about what you cannot decide alone.

- Ask at most ONE question per reply — the single question whose answer most changes what gets built. Never send a list of questions.
- Prefer proposing a concrete draft over interviewing: if you can infer a sensible answer, put it in the draft and say what you assumed instead of asking.
- Before the final <issue_draft> block, emit exactly one question block whenever you are asking something:
<issue_draft_question>{"question":"...","options":[{"label":"...","value":"...","recommended":true}]}</issue_draft_question>
- Offer 2-4 concrete options. Mark exactly one of them "recommended": true — the answer you would choose — and write its label so it is understandable on its own. value is the full answer to send back, phrased as the user would say it.
- The user can always answer in their own words instead of picking an option, so an option is a shortcut, never a cage. Never ask a question whose only useful answer is free text if you can offer a reasonable default.
- Omit the question block entirely on a reply that has nothing left to ask. Do not ask about anything the conversation has already settled.
- If the user asks you to stop asking questions, stop for the rest of the conversation: keep refining the draft from what you know and state your assumptions instead.`

// issueDraftConversationPolicy is the unguided policy: plain dialogue, no
// interview. The user drives; the carrier answers and keeps the draft current.
const issueDraftConversationPolicy = `Your task right now: hold an ordinary conversation about the request and keep the draft current.

- Do not interview the user and do not emit question blocks. Answer what was asked, propose the draft you would write, and name any assumption you had to make.
- Ask something only when the request genuinely cannot be drafted without it (for example, the target is ambiguous), and then ask it as a normal sentence — one question, not a list.
- The user may close the guidance on purpose: they are deciding the shape of the issue themselves, so follow their direction instead of re-opening settled questions.`

// issueDraftPolicy is one auditable alignment policy.
type issueDraftPolicy struct {
	Key     string
	Version string
	// Guided tells the client whether this policy asks the user questions, so
	// the alignment page can show the guidance control in the state it is
	// actually in without hardcoding which key is which.
	Guided bool
	// Behaviour is the policy-specific half of the carrier's prompt.
	Behaviour string
}

// Instructions is the full system prompt installed on the carrier: the shared
// wire contract plus this policy's behaviour.
func (p issueDraftPolicy) Instructions() string {
	return issueDraftContract + "\n\n" + p.Behaviour
}

// issueDraftPolicyRegistry is the whole set. A new policy is a new entry; the
// keys are the values accepted by the create and switch endpoints.
//
// Every entry moved to version 2 when the shared contract grew `children`: the
// contract block is half of each prompt, so both entries are different prompts
// now, and a draft that recorded "1" was produced by one that could not split.
var issueDraftPolicyRegistry = map[string]issueDraftPolicy{
	issueDraftPolicyQuestion: {
		Key:       issueDraftPolicyQuestion,
		Version:   "2",
		Guided:    true,
		Behaviour: issueDraftQuestionPolicy,
	},
	issueDraftPolicyConversation: {
		Key:       issueDraftPolicyConversation,
		Version:   "2",
		Guided:    false,
		Behaviour: issueDraftConversationPolicy,
	},
}

// issueDraftPolicyKeys is the registry's key list, sorted so an error message
// does not depend on map iteration order.
func issueDraftPolicyKeys() []string {
	keys := make([]string, 0, len(issueDraftPolicyRegistry))
	for key := range issueDraftPolicyRegistry {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// issueDraftPolicyByKey resolves a client-supplied policy key. The caller
// decides what an unknown key means; this only answers whether it exists.
func issueDraftPolicyByKey(key string) (issueDraftPolicy, bool) {
	policy, ok := issueDraftPolicyRegistry[key]
	return policy, ok
}

// issueDraftPolicyUnknownMessage is the one wording every rejection of an
// unknown key uses, so the create and switch endpoints cannot drift apart.
func issueDraftPolicyUnknownMessage() string {
	return fmt.Sprintf("policy must be one of: %s", strings.Join(issueDraftPolicyKeys(), ", "))
}

// issueDraftPolicyResponse is the auditable half of a draft's wire shape: which
// policy is running, which version of its prompt the carrier was given, and
// whether that policy asks questions.
//
// Version is read from the draft row, never from the registry — a conversation
// keeps reporting the prompt it actually ran after the registry moves on. A key
// that is no longer registered still reports its recorded version; only
// `guided` falls back, because the client needs a definite answer for how to
// draw the control.
type issueDraftPolicyResponse struct {
	Key     string `json:"key"`
	Version string `json:"version"`
	Guided  bool   `json:"guided"`
}

func issueDraftPolicyResponseFromRow(key, version string) issueDraftPolicyResponse {
	guided := false
	if policy, ok := issueDraftPolicyByKey(key); ok {
		guided = policy.Guided
	}
	return issueDraftPolicyResponse{Key: key, Version: version, Guided: guided}
}
