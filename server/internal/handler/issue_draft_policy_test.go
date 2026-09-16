package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// The alignment policy is the carrier's prompt, named and versioned. These
// tests pin the two properties that make it auditable rather than decorative:
// the draft reports the policy and prompt version it is actually running, and
// switching it rewrites the carrier's instructions — the only thing that
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

// carrierPolicyKey reads the policy recorded on the draft row itself — the
// audit trail, as opposed to the API's echo of it.
func carrierPolicyKey(t *testing.T, sessionID string) (string, string) {
	t.Helper()
	var key, version string
	dbfx.QueryRow(t, `
		SELECT policy_key, policy_version FROM issue_draft WHERE chat_session_id = $1
	`, sessionID).Scan(&key, &version)
	return key, version
}

func switchPolicyRequest(t *testing.T, sessionID, policy string) *http.Request {
	t.Helper()
	return withURLParam(newRequest(http.MethodPatch, "/api/issue-drafts/"+sessionID+"/policy", map[string]any{
		"policy": policy,
	}), "sessionId", sessionID)
}

// A create with no policy gets the guided default, and the prompt that goes
// with it. The question block is asserted by name because it is the contract
// `packages/core/issue-drafts/protocol.ts` parses for the answer chips: a
// version bump that dropped it would silently disable them.
func TestIssueDraftDefaultsToGuidedQuestionPolicy(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)

	questionPolicy, ok := issueDraftPolicyByKey(issueDraftPolicyQuestion)
	if !ok {
		t.Fatal("the guided question policy is not registered")
	}
	if session.Draft.Policy.Key != issueDraftPolicyQuestion {
		t.Fatalf("policy key = %q, want %q", session.Draft.Policy.Key, issueDraftPolicyQuestion)
	}
	if session.Draft.Policy.Version != questionPolicy.Version {
		t.Fatalf("policy version = %q, want the registered %q", session.Draft.Policy.Version, questionPolicy.Version)
	}
	if !session.Draft.Policy.Guided {
		t.Fatal("the guided policy reports guided = false")
	}

	recordedKey, recordedVersion := carrierPolicyKey(t, session.SessionID)
	if recordedKey != questionPolicy.Key || recordedVersion != questionPolicy.Version {
		t.Fatalf("draft row recorded %s@%s, want %s@%s", recordedKey, recordedVersion, questionPolicy.Key, questionPolicy.Version)
	}

	instructions := carrierInstructions(t, session.AgentID)
	if instructions != questionPolicy.Instructions() {
		t.Fatal("carrier was not given the registered question prompt")
	}
	if !strings.Contains(instructions, "<issue_draft_question>") {
		t.Fatal("the guided prompt does not describe the question block the client parses")
	}
	if !strings.Contains(instructions, "<issue_draft>") {
		t.Fatal("the guided prompt lost the draft block contract")
	}
}

// The whole point of recording a version: it can be read back, and it names a
// prompt that is in the registry — an audit that pointed at nothing would be
// worse than no audit.
func TestIssueDraftPolicyVersionIsAuditable(t *testing.T) {
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
	policy, ok := issueDraftPolicyByKey(listed.Policy.Key)
	if !ok {
		t.Fatalf("listed draft reports policy %q, which is not registered", listed.Policy.Key)
	}
	if listed.Policy.Version != policy.Version {
		t.Fatalf("listed draft reports %s@%s, want %s@%s",
			listed.Policy.Key, listed.Policy.Version, policy.Key, policy.Version)
	}
}

// Switching policy is two writes that must not drift: the row records the
// version, and the carrier gets the prompt. It must also leave the draft itself
// — content and revision — exactly as it was, or a policy change would reject
// the user's next save with a conflict they cannot explain.
func TestSwitchIssueDraftPolicyRewritesCarrierPromptOnly(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", map[string]any{
		"title":       "Keep my draft",
		"description": "Policy changes must not rewrite this.",
	})

	var switched issueDraftResponse
	testutil.Call(t, testHandler.SwitchIssueDraftPolicy, switchPolicyRequest(t, session.SessionID, issueDraftPolicyConversation)).
		Want(http.StatusOK).JSON(&switched)

	conversationPolicy, ok := issueDraftPolicyByKey(issueDraftPolicyConversation)
	if !ok {
		t.Fatal("the conversation policy is not registered")
	}
	if switched.Policy.Key != issueDraftPolicyConversation || switched.Policy.Version != conversationPolicy.Version {
		t.Fatalf("switch reported %s@%s, want %s@%s",
			switched.Policy.Key, switched.Policy.Version, conversationPolicy.Key, conversationPolicy.Version)
	}
	if switched.Policy.Guided {
		t.Fatal("the unguided policy reports guided = true")
	}
	if switched.Revision != saved.Revision {
		t.Fatalf("policy switch moved the revision from %d to %d", saved.Revision, switched.Revision)
	}
	if string(switched.Draft) != string(saved.Draft) {
		t.Fatalf("policy switch rewrote the draft: %s -> %s", saved.Draft, switched.Draft)
	}
	if switched.Status != "ready" {
		t.Fatalf("policy switch moved status to %q, want ready", switched.Status)
	}

	recordedKey, recordedVersion := carrierPolicyKey(t, session.SessionID)
	if recordedKey != conversationPolicy.Key || recordedVersion != conversationPolicy.Version {
		t.Fatalf("draft row recorded %s@%s after the switch, want %s@%s",
			recordedKey, recordedVersion, conversationPolicy.Key, conversationPolicy.Version)
	}
	if got := carrierInstructions(t, session.AgentID); got != conversationPolicy.Instructions() {
		t.Fatal("carrier kept the old prompt after the policy switch")
	}

	// And back, so the endpoint is a real toggle rather than a one-way door.
	questionPolicy, _ := issueDraftPolicyByKey(issueDraftPolicyQuestion)
	testutil.Call(t, testHandler.SwitchIssueDraftPolicy, switchPolicyRequest(t, session.SessionID, issueDraftPolicyQuestion)).
		Want(http.StatusOK)
	if got := carrierInstructions(t, session.AgentID); got != questionPolicy.Instructions() {
		t.Fatal("carrier did not get the question prompt back after switching back")
	}
}

// An unknown key is refused, not defaulted: a session that looks switched while
// running the old prompt is exactly the drift the recorded version exists to
// prevent.
func TestIssueDraftPolicyRejectsUnknownKey(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	// On create.
	testutil.Call(t, testHandler.CreateIssueDraftSession, newRequest(http.MethodPost, "/api/issue-drafts", map[string]any{
		"runtime_id": testRuntimeID,
		"policy":     "interrogation",
	})).Want(http.StatusBadRequest)

	// And on switch, leaving the carrier's prompt alone.
	session := startIssueDraftSession(t)
	questionPolicy, _ := issueDraftPolicyByKey(issueDraftPolicyQuestion)
	before := carrierInstructions(t, session.AgentID)

	testutil.Call(t, testHandler.SwitchIssueDraftPolicy, switchPolicyRequest(t, session.SessionID, "interrogation")).
		Want(http.StatusBadRequest)

	if got := carrierInstructions(t, session.AgentID); got != before {
		t.Fatal("a refused policy switch still rewrote the carrier prompt")
	}
	if key, _ := carrierPolicyKey(t, session.SessionID); key != questionPolicy.Key {
		t.Fatalf("a refused policy switch recorded policy %q", key)
	}
}

// Switching is a write on someone else's conversation if ownership is not
// checked, and the only thing it changes is what their carrier will be told.
func TestSwitchIssueDraftPolicyRequiresOwnership(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	otherUser := dbfx.User(t, "Issue Draft Policy Outsider", "issue-draft-policy-outsider@multica.ai")
	dbfx.Member(t, testWorkspaceID, otherUser, "member")

	testutil.Call(t, testHandler.SwitchIssueDraftPolicy, testutil.WithHeaders(
		switchPolicyRequest(t, session.SessionID, issueDraftPolicyConversation),
		"X-User-ID", otherUser,
	)).Want(http.StatusForbidden)

	questionPolicy, _ := issueDraftPolicyByKey(issueDraftPolicyQuestion)
	if got := carrierInstructions(t, session.AgentID); got != questionPolicy.Instructions() {
		t.Fatal("a rejected policy switch rewrote the carrier prompt")
	}
}

// A reply already in flight was claimed with the previous prompt, so switching
// under it would make the switch look like it did not take. Same gate as the
// runtime switch.
func TestSwitchIssueDraftPolicyRefusesWhileTurnIsPending(t *testing.T) {
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

	testutil.Call(t, testHandler.SwitchIssueDraftPolicy, switchPolicyRequest(t, session.SessionID, issueDraftPolicyConversation)).
		Want(http.StatusConflict)

	questionPolicy, _ := issueDraftPolicyByKey(issueDraftPolicyQuestion)
	if got := carrierInstructions(t, session.AgentID); got != questionPolicy.Instructions() {
		t.Fatal("a refused policy switch rewrote the carrier prompt")
	}
}

// The registry is configuration, and configuration rots quietly: every entry
// must carry a prompt and a version, keys must match their entry, and the
// guided policy must be the one that explains the question block.
func TestIssueDraftPolicyRegistryIsWellFormed(t *testing.T) {
	if len(issueDraftPolicyRegistry) == 0 {
		t.Fatal("no alignment policies are registered")
	}
	for key, policy := range issueDraftPolicyRegistry {
		if policy.Key != key {
			t.Fatalf("registry entry %q carries key %q", key, policy.Key)
		}
		if strings.TrimSpace(policy.Version) == "" {
			t.Fatalf("policy %q has no version", key)
		}
		if strings.TrimSpace(policy.Behaviour) == "" {
			t.Fatalf("policy %q has no prompt", key)
		}
		if !strings.Contains(policy.Instructions(), "<issue_draft>") {
			t.Fatalf("policy %q does not carry the shared draft-block contract", key)
		}
	}
	guided, ok := issueDraftPolicyByKey(issueDraftPolicyQuestion)
	if !ok || !guided.Guided {
		t.Fatal("the guided default is not registered as guided")
	}
	if !strings.Contains(guided.Instructions(), "<issue_draft_question>") {
		t.Fatal("the guided policy does not describe the question block")
	}
	plain, ok := issueDraftPolicyByKey(issueDraftPolicyConversation)
	if !ok || plain.Guided {
		t.Fatal("the conversation policy is missing or reports itself as guided")
	}
	if strings.Contains(plain.Instructions(), "<issue_draft_question>") {
		t.Fatal("the unguided policy still teaches the question block")
	}
}

// Finalize is the structured direct-write path this whole flow exists to
// protect: a policy switch must not become a second way to create an issue, and
// it must not be required before confirming.
func TestPolicySwitchDoesNotCreateAnIssue(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	testutil.Call(t, testHandler.SwitchIssueDraftPolicy, switchPolicyRequest(t, session.SessionID, issueDraftPolicyConversation)).
		Want(http.StatusOK)

	if got := dbfx.Count(t, `
		SELECT COUNT(*) FROM issue WHERE workspace_id = $1 AND origin_type = 'issue_draft'
	`, testWorkspaceID); got != 0 {
		t.Fatalf("switching policy created %d issues", got)
	}
}
