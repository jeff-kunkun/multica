package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// The alignment skill set is the carrier's prompt, named and versioned. These
// tests pin the properties that make it auditable rather than decorative: the
// draft reports the skills and prompt versions it is actually running, and
// changing them rewrites the carrier's instructions — the only thing that
// changes how the next reply behaves — without touching the draft's content or
// its revision.
//
// Database-backed like the rest of the issue-draft suite: "the carrier really
// got this prompt" is a property of the agent row, and a mocked query layer
// would only assert that the handler called what the test told it to.

// carrierInstructions reads the prompt the daemon would hand the carrier on the
// next claim.
func carrierInstructions(t *testing.T, agentID string) string {
	t.Helper()
	var instructions string
	dbfx.QueryRow(t, `SELECT instructions FROM agent WHERE id = $1`, agentID).Scan(&instructions)
	return instructions
}

// carrierPolicyKey reads the skill record off the draft row itself — the audit
// trail, as opposed to the API's echo of it.
func carrierPolicyKey(t *testing.T, sessionID string) (string, string) {
	t.Helper()
	var key, version string
	dbfx.QueryRow(t, `
		SELECT policy_key, policy_version FROM issue_draft WHERE chat_session_id = $1
	`, sessionID).Scan(&key, &version)
	return key, version
}

// carrierThinkingLevel reads the reasoning effort frozen onto the carrier. NULL
// and "" are the same answer — "let the local CLI decide" — so the column is
// read as a possibly-absent text and flattened.
func carrierThinkingLevel(t *testing.T, agentID string) string {
	t.Helper()
	var level *string
	dbfx.QueryRow(t, `SELECT thinking_level FROM agent WHERE id = $1`, agentID).Scan(&level)
	if level == nil {
		return ""
	}
	return *level
}

func switchPolicyRequest(t *testing.T, sessionID string, skills ...string) *http.Request {
	t.Helper()
	return withURLParam(newRequest(http.MethodPatch, "/api/issue-drafts/"+sessionID+"/policy", map[string]any{
		"skills": skills,
	}), "sessionId", sessionID)
}

// switchLegacyPolicyRequest sends the pre-DENE-512 single-key body, which an
// installed client still sends and the server still has to honour.
func switchLegacyPolicyRequest(t *testing.T, sessionID, policy string) *http.Request {
	t.Helper()
	return withURLParam(newRequest(http.MethodPatch, "/api/issue-drafts/"+sessionID+"/policy", map[string]any{
		"policy": policy,
	}), "sessionId", sessionID)
}

// A create with no skills gets the guided default — the grill skill — and the
// prompt that goes with it. The question block is asserted by name because it is
// the contract `packages/core/issue-drafts/protocol.ts` parses for the answer
// chips: a version bump that dropped it would silently disable them.
func TestIssueDraftDefaultsToTheGrillSkill(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)

	grill, ok := issueDraftSkillRegistry[issueDraftSkillGrill]
	if !ok {
		t.Fatal("the grill skill is not registered")
	}
	if session.Draft.Policy.Key != grill.Key {
		t.Fatalf("skill key = %q, want %q", session.Draft.Policy.Key, grill.Key)
	}
	if session.Draft.Policy.Version != grill.Version {
		t.Fatalf("skill version = %q, want the registered %q", session.Draft.Policy.Version, grill.Version)
	}
	if !session.Draft.Policy.Guided {
		t.Fatal("the default skill set reports guided = false")
	}
	if len(session.Draft.Policy.Skills) != 1 || session.Draft.Policy.Skills[0].Key != issueDraftSkillGrill {
		t.Fatalf("the response lists %+v, want exactly the grill skill", session.Draft.Policy.Skills)
	}

	recordedKey, recordedVersion := carrierPolicyKey(t, session.SessionID)
	if recordedKey != grill.Key || recordedVersion != grill.Version {
		t.Fatalf("draft row recorded %s@%s, want %s@%s", recordedKey, recordedVersion, grill.Key, grill.Version)
	}

	instructions := carrierInstructions(t, session.AgentID)
	if instructions != (issueDraftSkillSet{issueDraftSkillGrill}).Instructions() {
		t.Fatal("carrier was not given the composed grill prompt")
	}
	if !strings.Contains(instructions, "<issue_draft_question>") {
		t.Fatal("the guided prompt does not describe the question block the client parses")
	}
	if !strings.Contains(instructions, "<issue_draft>") {
		t.Fatal("the guided prompt lost the draft block contract")
	}
}

// An installed client that predates the skill picker sends the old single key.
// It must keep getting the prompt that key always meant — the guided default is
// `question`, which is exactly the grill skill.
func TestIssueDraftAcceptsTheLegacyPolicyKey(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	var out CreateIssueDraftSessionResponse
	testutil.Call(t, testHandler.CreateIssueDraftSession, newRequest(http.MethodPost, "/api/issue-drafts", map[string]any{
		"runtime_id": testRuntimeID,
		"policy":     "question",
	})).Want(http.StatusCreated).JSON(&out)

	if out.Draft.Policy.Key != issueDraftSkillGrill {
		t.Fatalf("the legacy guided key recorded %q, want %q", out.Draft.Policy.Key, issueDraftSkillGrill)
	}
	if !strings.Contains(carrierInstructions(t, out.AgentID), "<issue_draft_question>") {
		t.Fatal("the legacy guided key did not install the interviewing prompt")
	}

	// The retired `frontend` key is a skill now, and still resolves.
	var retired CreateIssueDraftSessionResponse
	testutil.Call(t, testHandler.CreateIssueDraftSession, newRequest(http.MethodPost, "/api/issue-drafts", map[string]any{
		"runtime_id": testRuntimeID,
		"policy":     issueDraftPolicyFrontend,
	})).Want(http.StatusCreated).JSON(&retired)

	if retired.Draft.Policy.Key != issueDraftSkillFrontend {
		t.Fatalf("the retired frontend key recorded %q, want %q", retired.Draft.Policy.Key, issueDraftSkillFrontend)
	}
	// The look round asks its one question through the same block, so a
	// front-end-only set is still guided — `guided` follows the block, not one
	// named skill.
	if !retired.Draft.Policy.Guided {
		t.Fatal("the front-end-only set reports guided = false although its method emits the question block")
	}
	if got := carrierInstructions(t, retired.AgentID); !strings.Contains(got, "multica attachment upload") {
		t.Fatal("the retired frontend key lost the look-round method")
	}
}

// The whole point of recording a version: it can be read back, and it names a
// prompt that is in the registry — an audit that pointed at nothing would be
// worse than no audit.
func TestIssueDraftSkillVersionIsAuditable(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)

	var out ListIssueDraftsResponse
	testutil.Call(t, testHandler.ListIssueDrafts, newRequest(http.MethodGet, "/api/issue-drafts", nil)).
		Want(http.StatusOK).JSON(&out)

	var listed *IssueDraftSummary
	for i := range out.Drafts {
		if out.Drafts[i].ChatSessionID == session.SessionID {
			listed = &out.Drafts[i]
			break
		}
	}
	if listed == nil {
		t.Fatal("the draft that was just created is missing from the unfinished list")
	}
	grill, ok := issueDraftSkillRegistry[listed.Policy.Key]
	if !ok {
		t.Fatalf("listed draft reports skill %q, which is not registered", listed.Policy.Key)
	}
	if listed.Policy.Version != grill.Version {
		t.Fatalf("listed draft reports %s@%s, want %s@%s",
			listed.Policy.Key, listed.Policy.Version, grill.Key, grill.Version)
	}
	if len(listed.Policy.Skills) != 1 || listed.Policy.Skills[0].Version != grill.Version {
		t.Fatalf("listed draft lists skills %+v, want one entry at version %s", listed.Policy.Skills, grill.Version)
	}
}

// Changing the skill set is two writes that must not drift: the row records the
// set and its versions, and the carrier gets the composed prompt. It must also
// leave the draft itself — content and revision — exactly as it was, or a skill
// change would reject the user's next save with a conflict they cannot explain.
func TestSwitchIssueDraftSkillsRewritesCarrierPromptOnly(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", map[string]any{
		"title":       "Keep my draft",
		"description": "Skill changes must not rewrite this.",
	})

	// Dropping the interviewing skill is the observable half: the question
	// block has to leave the prompt.
	var switched issueDraftResponse
	testutil.Call(t, testHandler.SwitchIssueDraftPolicy, switchPolicyRequest(t, session.SessionID, issueDraftSkillFrontend)).
		Want(http.StatusOK).JSON(&switched)

	frontend := issueDraftSkillRegistry[issueDraftSkillFrontend]
	if switched.Policy.Key != frontend.Key || switched.Policy.Version != frontend.Version {
		t.Fatalf("switch reported %s@%s, want %s@%s",
			switched.Policy.Key, switched.Policy.Version, frontend.Key, frontend.Version)
	}
	if !switched.Policy.Guided {
		t.Fatal("the front-end skill emits the question block, so its set must report guided = true")
	}
	if switched.Revision != saved.Revision {
		t.Fatalf("skill switch moved the revision from %d to %d", saved.Revision, switched.Revision)
	}
	if string(switched.Draft) != string(saved.Draft) {
		t.Fatalf("skill switch rewrote the draft: %s -> %s", saved.Draft, switched.Draft)
	}
	if switched.Status != "ready" {
		t.Fatalf("skill switch moved status to %q, want ready", switched.Status)
	}

	recordedKey, recordedVersion := carrierPolicyKey(t, session.SessionID)
	if recordedKey != frontend.Key || recordedVersion != frontend.Version {
		t.Fatalf("draft row recorded %s@%s after the switch, want %s@%s",
			recordedKey, recordedVersion, frontend.Key, frontend.Version)
	}
	got := carrierInstructions(t, session.AgentID)
	if got != (issueDraftSkillSet{issueDraftSkillFrontend}).Instructions() {
		t.Fatal("carrier kept the old prompt after the skill switch")
	}
	if strings.Contains(got, "converge the draft, and ask about what you cannot decide alone") {
		t.Fatal("the front-end-only prompt still carries the requirement interview its skill was dropped from")
	}
	if strings.Contains(got, "## 决策地图") {
		t.Fatal("the front-end-only prompt still carries the map of a skill that was never enabled")
	}

	// And back, so the endpoint is a real toggle rather than a one-way door.
	testutil.Call(t, testHandler.SwitchIssueDraftPolicy, switchPolicyRequest(t, session.SessionID, issueDraftSkillGrill)).
		Want(http.StatusOK)
	if got := carrierInstructions(t, session.AgentID); got != (issueDraftSkillSet{issueDraftSkillGrill}).Instructions() {
		t.Fatal("carrier did not get the grill prompt back after switching back")
	}
}

// The set is what the prompt is composed from, so a two-skill request has to
// produce a prompt carrying BOTH methods, and record both versions — one per
// skill, positionally aligned with the recorded keys.
func TestSwitchIssueDraftSkillsComposesTheWholeSet(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)

	// Deliberately unsorted: the recorded form must not depend on request order.
	var switched issueDraftResponse
	testutil.Call(t, testHandler.SwitchIssueDraftPolicy, switchPolicyRequest(t, session.SessionID, issueDraftSkillGrill, issueDraftSkillFrontend)).
		Want(http.StatusOK).JSON(&switched)

	set := issueDraftSkillSet{issueDraftSkillFrontend, issueDraftSkillGrill}
	if switched.Policy.Key != set.Keys() || switched.Policy.Version != set.Version() {
		t.Fatalf("two skills recorded %s@%s, want %s@%s",
			switched.Policy.Key, switched.Policy.Version, set.Keys(), set.Version())
	}
	if len(switched.Policy.Skills) != 2 {
		t.Fatalf("response lists %d skills, want 2", len(switched.Policy.Skills))
	}
	for i, key := range set {
		if switched.Policy.Skills[i].Key != key {
			t.Fatalf("response skill %d = %q, want %q", i, switched.Policy.Skills[i].Key, key)
		}
		if want := issueDraftSkillRegistry[key].Version; switched.Policy.Skills[i].Version != want {
			t.Fatalf("response skill %q carries version %q, want %q", key, switched.Policy.Skills[i].Version, want)
		}
	}
	if !switched.Policy.Guided {
		t.Fatal("a set containing the grill skill reports guided = false")
	}

	instructions := carrierInstructions(t, session.AgentID)
	if instructions != set.Instructions() {
		t.Fatal("carrier was not given the composed prompt for the whole set")
	}
	for _, want := range []string{"<issue_draft_question>", "multica attachment upload"} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("the composed prompt does not carry %q", want)
		}
	}
}

// A switch that names nothing is refused rather than defaulted: defaulting would
// silently reset a conversation's skills on an empty body, which is the drift
// the recorded set exists to prevent.
func TestSwitchIssueDraftSkillsRejectsEmptyBody(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	before := carrierInstructions(t, session.AgentID)

	for name, body := range map[string]map[string]any{
		"no fields":    {},
		"empty list":   {"skills": []string{}},
		"blank policy": {"policy": "   "},
	} {
		refused := testutil.Call(t, testHandler.SwitchIssueDraftPolicy, withURLParam(
			newRequest(http.MethodPatch, "/api/issue-drafts/"+session.SessionID+"/policy", body),
			"sessionId", session.SessionID,
		)).Want(http.StatusBadRequest).Map()
		if refused["error"] == "" {
			t.Fatalf("%s: refusal carries no message", name)
		}
	}
	if got := carrierInstructions(t, session.AgentID); got != before {
		t.Fatal("a refused skill switch still rewrote the carrier prompt")
	}
	if key, _ := carrierPolicyKey(t, session.SessionID); key != issueDraftSkillGrill {
		t.Fatalf("a refused skill switch recorded %q", key)
	}
}

// An unknown key is refused, not defaulted: a session that looks switched while
// running the old prompt is exactly the drift the recorded version exists to
// prevent. The refusal also has to name the keys, because it is the only place a
// client author is told what they are.
func TestIssueDraftSkillsRejectUnknownKey(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	const wantMessage = "skills must be among: frontend, grill, wayfinder"

	// On create.
	refused := testutil.Call(t, testHandler.CreateIssueDraftSession, newRequest(http.MethodPost, "/api/issue-drafts", map[string]any{
		"runtime_id": testRuntimeID,
		"skills":     []string{"interrogation"},
	})).Want(http.StatusBadRequest).Map()
	if refused["error"] != wantMessage {
		t.Fatalf("create refused an unknown skill with %v, want %q", refused["error"], wantMessage)
	}

	// A legacy client sending an unknown policy gets a rejection too, and it
	// still names the skills rather than the retired keys.
	legacy := testutil.Call(t, testHandler.CreateIssueDraftSession, newRequest(http.MethodPost, "/api/issue-drafts", map[string]any{
		"runtime_id": testRuntimeID,
		"policy":     "interrogation",
	})).Want(http.StatusBadRequest).Map()
	if legacy["error"] != wantMessage {
		t.Fatalf("create refused an unknown legacy policy with %v, want %q", legacy["error"], wantMessage)
	}

	// And on switch, leaving the carrier's prompt alone.
	session := startIssueDraftSession(t)
	before := carrierInstructions(t, session.AgentID)

	switched := testutil.Call(t, testHandler.SwitchIssueDraftPolicy, switchPolicyRequest(t, session.SessionID, "interrogation")).
		Want(http.StatusBadRequest).Map()
	if switched["error"] != wantMessage {
		t.Fatalf("switch refused an unknown skill with %v, want %q", switched["error"], wantMessage)
	}

	if got := carrierInstructions(t, session.AgentID); got != before {
		t.Fatal("a refused skill switch still rewrote the carrier prompt")
	}
	if key, _ := carrierPolicyKey(t, session.SessionID); key != issueDraftSkillGrill {
		t.Fatalf("a refused skill switch recorded skill %q", key)
	}
}

// Every key the registry lists has to survive the endpoints, not just the
// registry: create and switch both resolve through the same normalization, and a
// key the picker offers but an endpoint refuses is a switch that cannot land.
// The version recorded on the row is the audit half, so it is asserted here for
// every entry rather than only for the default.
func TestIssueDraftSkillKeysAreSwitchable(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	for _, key := range issueDraftSkillKeys() {
		skill, ok := issueDraftSkillRegistry[key]
		if !ok {
			t.Fatalf("issueDraftSkillKeys listed %q, which the registry does not hold", key)
		}

		session := startIssueDraftSession(t)
		var switched issueDraftResponse
		testutil.Call(t, testHandler.SwitchIssueDraftPolicy, switchPolicyRequest(t, session.SessionID, key)).
			Want(http.StatusOK).JSON(&switched)

		if switched.Policy.Key != key || switched.Policy.Version != skill.Version || switched.Policy.Guided != skill.Guided {
			t.Fatalf("switching to %q reported %+v, want %s@%s guided=%t",
				key, switched.Policy, skill.Key, skill.Version, skill.Guided)
		}
		if recorded, version := carrierPolicyKey(t, session.SessionID); recorded != key || version != skill.Version {
			t.Fatalf("switching to %q recorded %s@%s on the draft row, want %s@%s",
				key, recorded, version, skill.Key, skill.Version)
		}
		if got := carrierInstructions(t, session.AgentID); got != (issueDraftSkillSet{key}).Instructions() {
			t.Fatalf("switching to %q did not install that skill's composed prompt", key)
		}
	}
}

// Switching is a write on someone else's conversation if ownership is not
// checked, and the only thing it changes is what their carrier will be told.
func TestSwitchIssueDraftSkillsRequiresOwnership(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	otherUser := dbfx.User(t, "Issue Draft Policy Outsider", "issue-draft-policy-outsider@multica.ai")
	dbfx.Member(t, testWorkspaceID, otherUser, "member")

	testutil.Call(t, testHandler.SwitchIssueDraftPolicy, testutil.WithHeaders(
		switchPolicyRequest(t, session.SessionID, issueDraftSkillFrontend),
		"X-User-ID", otherUser,
	)).Want(http.StatusForbidden)

	if got := carrierInstructions(t, session.AgentID); got != (issueDraftSkillSet{issueDraftSkillGrill}).Instructions() {
		t.Fatal("a rejected skill switch rewrote the carrier prompt")
	}
}

// A reply already in flight was claimed with the previous prompt, so switching
// under it would make the switch look like it did not take. Same gate as the
// runtime switch.
func TestSwitchIssueDraftSkillsRefusesWhileTurnIsPending(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	dbfx.Task(t, session.AgentID, testutil.Cols{
		"chat_session_id": session.SessionID,
		"runtime_id":      testRuntimeID,
		"status":          "queued",
	})

	testutil.Call(t, testHandler.SwitchIssueDraftPolicy, switchPolicyRequest(t, session.SessionID, issueDraftSkillFrontend)).
		Want(http.StatusConflict)

	if got := carrierInstructions(t, session.AgentID); got != (issueDraftSkillSet{issueDraftSkillGrill}).Instructions() {
		t.Fatal("a refused skill switch rewrote the carrier prompt")
	}
}

// The registry is configuration, and configuration rots quietly: every entry
// must carry a method and a version, keys must match their entry, and `guided`
// must agree with whether the method actually teaches the question block.
func TestIssueDraftSkillRegistryIsWellFormed(t *testing.T) {
	if len(issueDraftSkillRegistry) == 0 {
		t.Fatal("no alignment skills are registered")
	}
	for key, skill := range issueDraftSkillRegistry {
		if skill.Key != key {
			t.Fatalf("registry entry %q carries key %q", key, skill.Key)
		}
		if strings.TrimSpace(skill.Version) == "" {
			t.Fatalf("skill %q has no version", key)
		}
		if strings.TrimSpace(skill.Method) == "" {
			t.Fatalf("skill %q has no method", key)
		}
		composed := (issueDraftSkillSet{key}).Instructions()
		if !strings.Contains(composed, "<issue_draft>") {
			t.Fatalf("skill %q does not carry the shared draft-block contract", key)
		}
	}
	if !(issueDraftSkillSet{issueDraftSkillGrill}).Guided() {
		t.Fatal("the grill skill is not registered as guided")
	}
	if !strings.Contains((issueDraftSkillSet{issueDraftSkillGrill}).Instructions(), "<issue_draft_question>") {
		t.Fatal("the grill skill does not describe the question block")
	}
	if (issueDraftSkillSet{}).Guided() {
		t.Fatal("the empty skill set reports itself guided")
	}

	// `guided` is what the client renders answer chips from, so a guided entry
	// that never describes the block asks questions nobody can click, and an
	// unguided one that does interviews after the user turned that skill off.
	// The registry grows, so this is a loop rather than three named checks.
	for key, skill := range issueDraftSkillRegistry {
		teaches := strings.Contains(issueDraftSkillSet{key}.Instructions(), "<issue_draft_question>")
		if skill.Guided && !teaches {
			t.Fatalf("skill %q reports itself guided but never describes the question block", key)
		}
		if !skill.Guided && teaches {
			t.Fatalf("skill %q reports itself unguided but still teaches the question block", key)
		}
	}
}

// The front-end section is the shared contract's, not a skill's, so it reaches
// the carrier whichever skill set is running: a draft that skipped the screen
// must not be indistinguishable from one whose request genuinely has no surface.
// Version 3 is the record of the contract that started asking about the screen
// — a draft that recorded "2" was produced by one that never did.
func TestIssueDraftContractAsksForTheFrontendSection(t *testing.T) {
	for _, want := range []string{
		// When a request counts as having a surface, and the tie-break:
		// unsure means "has one", because the two mistakes are not symmetric.
		"A request has a surface when any outcome in it changes what a person sees or does on a screen",
		"When you cannot tell, assume it has one",
		// The fixed heading, in the language the description is written in.
		`"## 前端做法"`,
		`"## Frontend"`,
		// The five lines one screen group is made of.
		"- Name:",
		"- Entry:",
		"- Main action:",
		"- States:",
		"- Reuse:",
		// A child's spec comes back with its key, or the group rewrite
		// silently deletes it.
		"carry it back unchanged in every later block",
	} {
		if !strings.Contains(issueDraftContract, want) {
			t.Fatalf("the shared contract does not carry %q", want)
		}
	}

	for key := range issueDraftSkillRegistry {
		if !strings.Contains(issueDraftSkillSet{key}.Instructions(), "## 前端做法") {
			t.Fatalf("skill %q does not carry the front-end section", key)
		}
	}
}

// The two calls the carrier must not make for the user: which screens are in
// this issue, and which direction the surface takes. The grill skill asks both
// — the second one exactly once, because it cannot show a picture. The
// front-end skill runs the look round instead, and never hands the direction
// back to the user as prose. Version 4 is the record of the interviewing prompt
// that started asking; the front-end skill stays at 1.
func TestIssueDraftGrillSkillHandsScopeAndDirectionToTheUser(t *testing.T) {
	grill, ok := issueDraftSkillRegistry[issueDraftSkillGrill]
	if !ok || !grill.Guided {
		t.Fatal("the grill skill is not registered as guided")
	}
	if grill.Version != "4" {
		t.Fatalf("the grill skill is at version %q, want 4", grill.Version)
	}
	guided := (issueDraftSkillSet{issueDraftSkillGrill}).Instructions()
	for _, want := range []string{
		// Scope: the priority call is the user's, and the carrier proposes.
		"two things are the user's to decide and yours only to propose",
		"Which screens are in THIS issue and which wait for later",
		"Ask it with the split you would choose marked recommended",
		// Direction: asked once, with named options, then stopped.
		"The direction the surface takes",
		"Ask it once, with 2-4 named directions — then stop",
		"a second question about the look buys nothing",
		// Timing: the surface is settled before the rest of the draft.
		"Ask the surface question before the rest of the draft is settled",
	} {
		if !strings.Contains(guided, want) {
			t.Fatalf("the grill prompt does not carry %q", want)
		}
	}
}

// The front-end skill is the only one that runs the look round. The other skills
// keep the contract's rule that a look is not settled in prose; this one earns
// the exception the only way that rule allows — by producing a file the user can
// open. Both halves are pinned here, because the failure modes are opposite: a
// skill that lost the upload step only talks about prototypes, and a text-only
// skill that grew one is the requirement interview suddenly spending its turns
// drawing.
//
// Version 1 is the record of the entry as it shipped, under the policy key that
// has since become this skill's key.
func TestIssueDraftFrontendSkillRunsTheLookRound(t *testing.T) {
	frontend, ok := issueDraftSkillRegistry[issueDraftSkillFrontend]
	if !ok {
		t.Fatal("the front-end skill is not registered")
	}
	if !frontend.Guided {
		t.Fatal("the front-end skill reports itself unguided, so its one question at a time would never render answer chips")
	}
	if frontend.Version != "1" {
		t.Fatalf("the front-end skill is at version %q, want 1", frontend.Version)
	}
	for _, want := range []string{
		// The user picked this style, so the round starts instead of being
		// offered again — the offer belongs to the skills that do not have it.
		"run the look round — do not offer it again",
		// The default unit, and the comparison mode with its own tie-break.
		"One screen at a time is the default",
		"Five structural directions",
		"one file behind a picker",
		// The artifact, and the judgement that makes the style worth having.
		"multica attachment upload",
		"A round is not finished until the user has something to open",
		// Widths and states: what makes it openable rather than a mock.
		"desktop AND phone width",
		"loading / empty / error",
		// The round stops at two screens; past that it becomes an implementation
		// issue rather than a longer alignment.
		"At most two screens in one alignment",
		// The settled prototype has to survive into the draft, or whoever opens
		// the issue cannot see what was agreed.
		`"Prototype:"`,
		`"原型："`,
	} {
		if !strings.Contains(issueDraftSkillSet{issueDraftSkillFrontend}.Instructions(), want) {
			t.Fatalf("the front-end prompt does not carry %q", want)
		}
	}

	for _, key := range []string{issueDraftSkillGrill, issueDraftSkillWayfinder} {
		if strings.Contains(issueDraftSkillSet{key}.Instructions(), "multica attachment upload") {
			t.Fatalf("skill %q builds prototypes; the look round belongs to %q", key, issueDraftSkillFrontend)
		}
	}
}

// The wayfinder skill is the route half: it settles WHAT has to be decided
// before it settles one issue's wording, and its map is carried in the reply
// rather than written into a tracker the carrier has no access to. Both halves
// matter: a map that is never carried back is a map the next turn forgets, and a
// map that demands a tracker makes the skill unusable in an alignment.
func TestIssueDraftWayfinderSkillDrawsTheDecisionMap(t *testing.T) {
	wayfinder, ok := issueDraftSkillRegistry[issueDraftSkillWayfinder]
	if !ok {
		t.Fatal("the wayfinder skill is not registered")
	}
	if wayfinder.Version != "1" {
		t.Fatalf("the wayfinder skill is at version %q, want 1", wayfinder.Version)
	}
	wayfinderPrompt := (issueDraftSkillSet{issueDraftSkillWayfinder}).Instructions()
	for _, want := range []string{
		// The map, with the sections that make it a map rather than a summary.
		"## 决策地图",
		"目的地",
		"已经在手",
		"下一步可决",
		"还说不清",
		"范围外",
		// The frontier rule: only decisions whose prerequisites are settled are
		// answerable, and an empty frontier is not convergence.
		"the decisions whose prerequisites are all settled",
		"an unanswerable question on the frontier is what makes a map useless",
		"do not treat an empty frontier caused by an unanswered prerequisite as convergence",
		// Facts are looked up, decisions are asked.
		"Facts are yours to find, decisions are the user's to make",
		// The map is carried, not replaced, and its decisions become the group.
		"carry the whole map back in every reply",
		"a decision the user made that needs implementation becomes a child issue",
	} {
		if !strings.Contains(wayfinderPrompt, want) {
			t.Fatalf("the wayfinder prompt does not carry %q", want)
		}
	}

	// It asks about the frontier through the same block every guided skill uses,
	// because a frontier decision with no clickable answer is a map that stalls.
	if !wayfinder.Guided {
		t.Fatal("the wayfinder skill reports itself unguided although its method asks about frontier decisions")
	}
	if !strings.Contains(wayfinderPrompt, "<issue_draft_question>") {
		t.Fatal("the wayfinder method asks questions without the block the client renders answer chips from")
	}
}

// The recorded set is read back as a set. A row written before DENE-512 carries
// a bare key and a bare version, and both have to keep decoding to exactly the
// one skill they named — that is what makes the audit trail survive the change
// instead of needing a migration.
func TestIssueDraftSkillRecordDecodesLegacyRows(t *testing.T) {
	legacy := issueDraftPolicyResponseFromRow("conversation", "3")
	if legacy.Key != "conversation" || legacy.Version != "3" {
		t.Fatalf("legacy row reported %s@%s, want conversation@3", legacy.Key, legacy.Version)
	}
	if len(legacy.Skills) != 1 || legacy.Skills[0].Key != "conversation" || legacy.Skills[0].Version != "3" {
		t.Fatalf("legacy row listed %+v, want one entry for conversation@3", legacy.Skills)
	}
	if legacy.Guided {
		t.Fatal("a key that is no longer registered must fall back to guided = false")
	}

	composed := issueDraftPolicyResponseFromRow("frontend+grill", "1+4")
	if len(composed.Skills) != 2 {
		t.Fatalf("composed row listed %d skills, want 2", len(composed.Skills))
	}
	if composed.Skills[0].Key != "frontend" || composed.Skills[0].Version != "1" {
		t.Fatalf("first skill = %+v, want frontend@1", composed.Skills[0])
	}
	if composed.Skills[1].Key != "grill" || composed.Skills[1].Version != "4" {
		t.Fatalf("second skill = %+v, want grill@4", composed.Skills[1])
	}
	if !composed.Guided {
		t.Fatal("a set containing grill must report guided = true")
	}
}

// The reasoning effort is frozen onto the carrier at creation, like the model,
// because the daemon reads both off the claimed agent: a level written after the
// first turn is a level that turn never ran at. And a level the runtime cannot
// take is refused at the door rather than persisted and dropped.
func TestIssueDraftSessionFreezesThinkingLevel(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	// The shared test runtime is deliberately an unknown provider with no
	// reasoning dial, so this needs a runtime whose provider owns one — the
	// level is only storable where it means something.
	runtimeID := newThinkingTestRuntime(t, "Issue Draft Thinking Runtime", "claude")

	var withLevel CreateIssueDraftSessionResponse
	testutil.Call(t, testHandler.CreateIssueDraftSession, newRequest(http.MethodPost, "/api/issue-drafts", map[string]any{
		"runtime_id":     runtimeID,
		"thinking_level": "high",
	})).Want(http.StatusCreated).JSON(&withLevel)
	if got := carrierThinkingLevel(t, withLevel.AgentID); got != "high" {
		t.Fatalf("carrier thinking_level = %q, want high", got)
	}

	// Absent means "let the local CLI decide", which is stored as empty rather
	// than as a word the daemon would pass through.
	var withoutLevel CreateIssueDraftSessionResponse
	testutil.Call(t, testHandler.CreateIssueDraftSession, newRequest(http.MethodPost, "/api/issue-drafts", map[string]any{
		"runtime_id": runtimeID,
	})).Want(http.StatusCreated).JSON(&withoutLevel)
	if got := carrierThinkingLevel(t, withoutLevel.AgentID); got != "" {
		t.Fatalf("carrier thinking_level = %q, want empty", got)
	}

	// A token this provider does not know is a 400 naming the value, not a
	// stored level the daemon silently drops.
	refused := testutil.Call(t, testHandler.CreateIssueDraftSession, newRequest(http.MethodPost, "/api/issue-drafts", map[string]any{
		"runtime_id":     runtimeID,
		"thinking_level": "extremely-high",
	})).Want(http.StatusBadRequest).Map()
	if message, _ := refused["error"].(string); !strings.Contains(message, "extremely-high") {
		t.Fatalf("refusal %q does not name the rejected level", message)
	}

	// And a runtime with no dialect at all says so, rather than implying the
	// token was misspelled.
	noDial := testutil.Call(t, testHandler.CreateIssueDraftSession, newRequest(http.MethodPost, "/api/issue-drafts", map[string]any{
		"runtime_id":     testRuntimeID,
		"thinking_level": "high",
	})).Want(http.StatusBadRequest).Map()
	if message, _ := noDial["error"].(string); !strings.Contains(message, "does not support a per-agent reasoning effort") {
		t.Fatalf("refusal %q does not name the missing capability", message)
	}
}

// Finalize is the structured direct-write path this whole flow exists to
// protect: a skill switch must not become a second way to create an issue, and
// it must not be required before confirming.
func TestSkillSwitchDoesNotCreateAnIssue(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	testutil.Call(t, testHandler.SwitchIssueDraftPolicy, switchPolicyRequest(t, session.SessionID, issueDraftSkillFrontend)).
		Want(http.StatusOK)

	if got := dbfx.Count(t, `
		SELECT COUNT(*) FROM issue WHERE workspace_id = $1 AND origin_type = 'issue_draft'
	`, testWorkspaceID); got != 0 {
		t.Fatalf("switching skills created %d issues", got)
	}
}

// newThinkingTestRuntime creates a runtime under a provider that owns a
// reasoning dial. The shared test runtime is an unknown provider on purpose, so
// anything that asserts a thinking level needs its own.
func newThinkingTestRuntime(t *testing.T, name, provider string) string {
	t.Helper()
	var runtimeID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, last_seen_at
		)
		VALUES ($1, NULL, $2, 'cloud', $3, 'online', 'thinking test runtime', '{}'::jsonb, $4, now())
		RETURNING id
	`, testWorkspaceID, name, provider, testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("create %s runtime %q: %v", provider, name, err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
	})
	return runtimeID
}
