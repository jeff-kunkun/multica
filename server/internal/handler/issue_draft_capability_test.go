package handler

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// Alignment capabilities are the built-in methods a conversation is opened
// with: chosen per conversation by the create request, resolved into one set,
// assembled into the carrier's instructions and recorded on the draft row.
//
// These tests pin the three properties that make that more than a string
// concatenation. The resolution rules decide what a client's list means — the
// default is not the empty set, a policy's requirements cannot be turned off,
// and a capability that needs another brings it. The assembly rules decide what
// the prompt is — one copy of every method, in the registry's order rather than
// the request's. And the record decides what an audit can say — the keys that
// ran and the version of their text, on the row, in the API, and on the agent.
//
// The unit half runs without a database; the half that talks about what the
// carrier was handed is database-backed like the rest of the suite, because
// "the agent row really holds this prompt" is not a property of a mock.

// capabilityKeysOf is the resolved set as keys, for comparing against a wire
// list without depending on struct shape.
func capabilityKeysOf(t *testing.T, keys []string) []string {
	t.Helper()
	capabilities, err := resolveIssueDraftCapabilities(keys)
	if err != nil {
		t.Fatalf("resolveIssueDraftCapabilities(%v): %v", keys, err)
	}
	return issueDraftCapabilityKeys(capabilities)
}

// An absent list is the built-in default; an empty one is the client saying
// none. The difference is the whole reason the field is not simply "the list
// of methods to run": a client with no picker must still open a capable
// alignment, and a picker with every box unchecked must be able to say so.
func TestIssueDraftCapabilitiesTellAbsentFromEmpty(t *testing.T) {
	byDefault := capabilityKeysOf(t, nil)
	if !slices.Equal(byDefault, issueDraftCapabilityOrder) {
		t.Fatalf("the default capability set resolved to %v, want the whole library %v", byDefault, issueDraftCapabilityOrder)
	}
	for _, offered := range issueDraftDefaultCapabilities {
		if !slices.Contains(byDefault, offered) {
			t.Fatalf("the default set %v does not contain the offered %q", byDefault, offered)
		}
	}

	none := capabilityKeysOf(t, []string{})
	if len(none) != 0 {
		t.Fatalf("an explicit empty list resolved to %v, want no capabilities", none)
	}
}

// A capability that needs another brings it. `grill` without `grilling` is a
// prompt that offers to open a round and never says how to run one, and the
// client should not have to know the pair to ask for the entry.
func TestIssueDraftCapabilitiesExpandRequirements(t *testing.T) {
	resolved := capabilityKeysOf(t, []string{issueDraftCapabilityGrill})
	if !slices.Contains(resolved, issueDraftCapabilityGrilling) {
		t.Fatalf("asking for %q resolved to %v, which lacks its required %q",
			issueDraftCapabilityGrill, resolved, issueDraftCapabilityGrilling)
	}
	if slices.Contains(resolved, issueDraftCapabilityWayfinder) {
		t.Fatalf("asking for %q also pulled in %v; requires is not a synonym for everything",
			issueDraftCapabilityGrill, issueDraftCapabilityWayfinder)
	}
}

// An unknown key is refused at the boundary rather than dropped. A prompt
// assembled without a method the user turned on looks like it worked, and the
// user has no way to see that it did not.
func TestIssueDraftCapabilitiesRefuseUnknownKeys(t *testing.T) {
	if _, err := resolveIssueDraftCapabilities([]string{"grill-frontend-looks"}); err == nil {
		t.Fatal("a misspelled capability key was accepted")
	}
	if _, err := resolveIssueDraftCapabilities([]string{issueDraftCapabilityWayfinder, ""}); err == nil {
		t.Fatal("an empty capability key was accepted")
	}
}

// Assembly order is the registry's, not the request's: two clients that asked
// for the same set in different orders must produce the same prompt, or the
// text is not reproducible from the record.
func TestIssueDraftCapabilityAssemblyOrderIsTheRegistrys(t *testing.T) {
	forward := capabilityKeysOf(t, []string{
		issueDraftCapabilityWayfinder,
		issueDraftCapabilityGrill,
		issueDraftCapabilityGrillFrontendLook,
	})
	backward := capabilityKeysOf(t, []string{
		issueDraftCapabilityGrillFrontendLook,
		issueDraftCapabilityGrill,
		issueDraftCapabilityWayfinder,
	})
	if !slices.Equal(forward, backward) {
		t.Fatalf("the same set assembled in two orders: %v vs %v", forward, backward)
	}
	for i := 1; i < len(forward); i++ {
		if slices.Index(issueDraftCapabilityOrder, forward[i-1]) > slices.Index(issueDraftCapabilityOrder, forward[i]) {
			t.Fatalf("%v is not in registry order %v", forward, issueDraftCapabilityOrder)
		}
	}
}

// Every entry must carry a method and appear in the assembly order exactly
// once: an entry with no fragment renders an empty section, and one missing
// from the order is a capability that can be selected, recorded, and never
// reach the prompt.
func TestIssueDraftCapabilityRegistryIsWellFormed(t *testing.T) {
	if len(issueDraftCapabilityRegistry) == 0 {
		t.Fatal("no alignment capabilities are registered")
	}
	if len(issueDraftCapabilityOrder) != len(issueDraftCapabilityRegistry) {
		t.Fatalf("assembly order holds %d keys and the registry %d; every entry must appear once",
			len(issueDraftCapabilityOrder), len(issueDraftCapabilityRegistry))
	}
	for _, key := range issueDraftCapabilityOrder {
		capability, ok := issueDraftCapabilityRegistry[key]
		if !ok {
			t.Fatalf("assembly order names %q, which the registry does not hold", key)
		}
		if capability.Key != key {
			t.Fatalf("registry entry %q carries key %q", key, capability.Key)
		}
		if strings.TrimSpace(capability.Fragment) == "" {
			t.Fatalf("capability %q has no method", key)
		}
		for _, required := range capability.Requires {
			if _, ok := issueDraftCapabilityRegistry[required]; !ok {
				t.Fatalf("capability %q requires %q, which is not registered", key, required)
			}
			if required == key {
				t.Fatalf("capability %q requires itself", key)
			}
		}
	}
	if strings.TrimSpace(issueDraftCapabilityVersion) == "" {
		t.Fatal("the capability library has no version")
	}
	for _, key := range issueDraftDefaultCapabilities {
		if _, ok := issueDraftCapabilityRegistry[key]; !ok {
			t.Fatalf("the default set names %q, which is not registered", key)
		}
	}
}

// The methods are the four the user's own skill library used to carry, and the
// judgement each one exists for has to survive the rewrite into the carrier's
// medium: a decision map whose fog stays out of the children, a grilling round
// whose frontier is working state rather than a questionnaire, and a look round
// that is not finished until there is something to open.
func TestIssueDraftCapabilityMethodsCarryTheirJudgements(t *testing.T) {
	for key, want := range map[string][]string{
		issueDraftCapabilityWayfinder: {
			// The tell, and the boundary: the map is not the destination.
			"it depends",
			"produces the map, not the destination",
			// The group IS the map, and stage is the blocking order.
			"The group is the map",
			"staged by what blocks what",
			// The ticket-versus-fog judgement, which is the whole method.
			"can this be stated precisely now",
			`"## 决策地图"`,
			`"## Decision map"`,
			"Never open a child for a question you cannot phrase",
		},
		issueDraftCapabilityGrill: {
			"opening a grilling round",
			"the grilling capability's",
			"a map or a research question, not a grill",
		},
		issueDraftCapabilityGrilling: {
			// The round is the frontier; the ask is one question.
			"prerequisites are already settled",
			"The frontier is your working state, not a questionnaire",
			// No bare questions, ever.
			"Every question comes with the answer you would choose",
			// Unsettled is a state, not a retry.
			"still unsettled prerequisites",
			// The carrier has no repository, so a fact it cannot check is a
			// question — never a silent assumption.
			"no filesystem and no repository",
			// The gate.
			"Nothing is executed before agreement",
		},
		issueDraftCapabilityGrillFrontendLook: {
			// Offered, unless it is already the round.
			"Offer it before starting it",
			"it is the round and it is not offered again",
			// The two ways to run it, and the tie-break between directions.
			"One screen at a time is the default",
			"Five structural directions",
			"are not different directions",
			// The artifact, and the judgement.
			"multica attachment upload",
			"A round is not finished until the user has something to open",
			// Widths and states, the two-screen ceiling, and the boundary that
			// keeps a look round from becoming an implementation.
			"desktop AND phone width",
			"loading / empty / error",
			"At most two screens in one alignment",
			"This is a visual reference, not production code",
		},
	} {
		capability, ok := issueDraftCapabilityRegistry[key]
		if !ok {
			t.Fatalf("capability %q is not registered", key)
		}
		for _, fragment := range want {
			if !strings.Contains(capability.Fragment, fragment) {
				t.Fatalf("the %q method does not carry %q", key, fragment)
			}
		}
	}

	// A method that reached for a repository would be unusable in the carrier:
	// it has no project and writes nothing the user owns. The look round's one
	// file is scratch in its own working directory, and that is the only write
	// any of them may make.
	for key, capability := range issueDraftCapabilityRegistry {
		if strings.Contains(capability.Fragment, "docs/design/") {
			t.Fatalf("capability %q writes into a repository the carrier does not have", key)
		}
	}
}

// The prompt the carrier is handed is the three parts in order, and the
// capabilities arrive with the preamble that makes them methods rather than a
// second set of rules. The control is the same assembly with no capabilities:
// a draft that turned them all off is the pre-capability prompt, not a broken
// one.
func TestIssueDraftInstructionsAssembleContractCapabilitiesBehaviour(t *testing.T) {
	policy, ok := issueDraftPolicyByKey(issueDraftPolicyQuestion)
	if !ok {
		t.Fatal("the guided policy is not registered")
	}
	capabilities, err := resolveIssueDraftCapabilities(nil)
	if err != nil {
		t.Fatalf("resolving the default capabilities: %v", err)
	}
	assembled := policy.Instructions(capabilities)

	contract := strings.Index(assembled, issueDraftContract)
	preamble := strings.Index(assembled, issueDraftCapabilityPreamble)
	behaviour := strings.Index(assembled, policy.Behaviour)
	if contract < 0 || preamble < 0 || behaviour < 0 {
		t.Fatal("the assembled prompt is missing one of its three parts")
	}
	if !(contract < preamble && preamble < behaviour) {
		t.Fatalf("the assembled prompt is out of order: contract=%d preamble=%d behaviour=%d", contract, preamble, behaviour)
	}
	for _, capability := range capabilities {
		if !strings.Contains(assembled, capability.Fragment) {
			t.Fatalf("the assembled prompt is missing the %q method it was opened with", capability.Key)
		}
	}

	without := policy.Instructions(nil)
	if strings.Contains(without, issueDraftCapabilityPreamble) {
		t.Fatal("a prompt with no capabilities still introduces them")
	}
	if without != issueDraftContract+"\n\n"+policy.Behaviour {
		t.Fatal("a prompt with no capabilities is not the contract plus the behaviour")
	}
}

// The create request is the input: the keys it names are resolved, merged with
// the policy's requirements, assembled into the agent's instructions and
// recorded on the draft row, and the response reports the set that ran.
func TestCreateIssueDraftSessionInstallsAndRecordsCapabilities(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	var created CreateIssueDraftSessionResponse
	testutil.Call(t, testHandler.CreateIssueDraftSession, newRequest(http.MethodPost, "/api/issue-drafts", map[string]any{
		"runtime_id": testRuntimeID,
		// Deliberately the entry alone: the response and the row must report
		// what ran, which includes the capability `grill` requires.
		"capabilities": []string{issueDraftCapabilityGrill},
	})).Want(http.StatusCreated).JSON(&created)

	want := []string{issueDraftCapabilityGrill, issueDraftCapabilityGrilling}
	if !slices.Equal(created.Draft.Capabilities.Keys, want) {
		t.Fatalf("the created draft reports capabilities %v, want %v", created.Draft.Capabilities.Keys, want)
	}
	if created.Draft.Capabilities.Version != issueDraftCapabilityVersion {
		t.Fatalf("the created draft reports capability version %q, want %q",
			created.Draft.Capabilities.Version, issueDraftCapabilityVersion)
	}

	recorded, version := carrierCapabilityKeys(t, created.SessionID)
	if !slices.Equal(recorded, want) {
		t.Fatalf("the draft row recorded capabilities %v, want %v", recorded, want)
	}
	if version != issueDraftCapabilityVersion {
		t.Fatalf("the draft row recorded capability version %q, want %q", version, issueDraftCapabilityVersion)
	}

	instructions := carrierInstructions(t, created.AgentID)
	if !strings.Contains(instructions, issueDraftCapabilityGrillingMethod) {
		t.Fatal("the carrier was not given the method its entry capability requires")
	}
	if strings.Contains(instructions, issueDraftCapabilityWayfinderMethod) {
		t.Fatal("the carrier was given a method the request did not ask for")
	}
}

// Turning every capability off is a request the client can make, and it has to
// survive the round trip: the agent gets the pre-capability prompt, and the row
// records an empty set rather than the default it was not given.
func TestCreateIssueDraftSessionAcceptsNoCapabilities(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	var created CreateIssueDraftSessionResponse
	testutil.Call(t, testHandler.CreateIssueDraftSession, newRequest(http.MethodPost, "/api/issue-drafts", map[string]any{
		"runtime_id":   testRuntimeID,
		"capabilities": []string{},
	})).Want(http.StatusCreated).JSON(&created)

	if len(created.Draft.Capabilities.Keys) != 0 {
		t.Fatalf("an explicit empty list reported %v", created.Draft.Capabilities.Keys)
	}
	recorded, _ := carrierCapabilityKeys(t, created.SessionID)
	if len(recorded) != 0 {
		t.Fatalf("the draft row recorded %v for a request that asked for none", recorded)
	}
	instructions := carrierInstructions(t, created.AgentID)
	for key, capability := range issueDraftCapabilityRegistry {
		if strings.Contains(instructions, capability.Fragment) {
			t.Fatalf("the carrier was given the %q method after the user turned every capability off", key)
		}
	}
}

// An unknown key is refused before anything exists — no carrier, no session, no
// draft — because a conversation opened under a prompt nobody assembled is the
// failure this validation exists to prevent.
func TestCreateIssueDraftSessionRefusesUnknownCapability(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	testutil.Call(t, testHandler.CreateIssueDraftSession, newRequest(http.MethodPost, "/api/issue-drafts", map[string]any{
		"runtime_id":   testRuntimeID,
		"capabilities": []string{"grill-frontend-look", "way-finder"},
	})).Want(http.StatusBadRequest)
}

// Switching the policy rewrites the whole installed prompt, so the capabilities
// have to come across it — the user's own selection survives, and a policy that
// requires a method adds it rather than replacing the set with its own.
func TestSwitchIssueDraftPolicyKeepsCapabilitiesAndAddsRequirements(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	var created CreateIssueDraftSessionResponse
	testutil.Call(t, testHandler.CreateIssueDraftSession, newRequest(http.MethodPost, "/api/issue-drafts", map[string]any{
		"runtime_id":   testRuntimeID,
		"capabilities": []string{issueDraftCapabilityWayfinder},
	})).Want(http.StatusCreated).JSON(&created)

	var switched issueDraftResponse
	testutil.Call(t, testHandler.SwitchIssueDraftPolicy, switchPolicyRequest(t, created.SessionID, issueDraftPolicyFrontend)).
		Want(http.StatusOK).JSON(&switched)

	want := []string{issueDraftCapabilityWayfinder, issueDraftCapabilityGrillFrontendLook}
	if !slices.Equal(switched.Capabilities.Keys, want) {
		t.Fatalf("the switched draft reports capabilities %v, want %v", switched.Capabilities.Keys, want)
	}
	recorded, version := carrierCapabilityKeys(t, created.SessionID)
	if !slices.Equal(recorded, want) {
		t.Fatalf("the switch recorded capabilities %v on the row, want %v", recorded, want)
	}
	if version != issueDraftCapabilityVersion {
		t.Fatalf("the switch recorded capability version %q, want %q", version, issueDraftCapabilityVersion)
	}

	instructions := carrierInstructions(t, created.AgentID)
	if !strings.Contains(instructions, issueDraftCapabilityFrontendLookMethod) {
		t.Fatal("the look-round policy installed a prompt without the look-round method")
	}
	if !strings.Contains(instructions, issueDraftCapabilityWayfinderMethod) {
		t.Fatal("the policy switch dropped the capability the user had chosen")
	}
	if strings.Contains(instructions, issueDraftCapabilityGrillMethod) {
		t.Fatal("the switch pulled in the grill entry, which neither the user nor the policy asked for")
	}
}

// A key the running build has retired must not strand a live conversation: the
// switch drops it from the prompt, and the row keeps it — the row is the record
// of what ran, and rewriting it would be the one edit an audit trail must not
// make.
func TestSwitchIssueDraftPolicyToleratesARetiredCapability(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	dbfx.Exec(t, `UPDATE issue_draft SET capability_keys = $2 WHERE chat_session_id = $1`,
		session.SessionID, []string{issueDraftCapabilityWayfinder, "retired-method"})

	testutil.Call(t, testHandler.SwitchIssueDraftPolicy, switchPolicyRequest(t, session.SessionID, issueDraftPolicyConversation)).
		Want(http.StatusOK)

	instructions := carrierInstructions(t, session.AgentID)
	if strings.Contains(instructions, "retired-method") {
		t.Fatal("a retired capability's key leaked into the assembled prompt")
	}
	if !strings.Contains(instructions, issueDraftCapabilityWayfinderMethod) {
		t.Fatal("a retired neighbour cost the conversation the capability that still exists")
	}
}

// carrierCapabilityKeys reads the capability record off the draft row itself —
// the audit trail, as opposed to the API's echo of it.
func carrierCapabilityKeys(t *testing.T, sessionID string) ([]string, string) {
	t.Helper()
	var keys []string
	var version string
	dbfx.QueryRow(t, `
		SELECT capability_keys, capability_version FROM issue_draft WHERE chat_session_id = $1
	`, sessionID).Scan(&keys, &version)
	return keys, version
}
