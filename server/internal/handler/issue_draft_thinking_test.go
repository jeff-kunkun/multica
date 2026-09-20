package handler

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// The execution configuration a create request carries — the model, the
// reasoning effort — is frozen onto the carrier agent at creation rather than
// stored on the draft, because the daemon reads both off the claimed agent row.
// A value written after the session exists is a value the first turn never ran
// with, which is why the round trip these tests assert is "the agent row really
// holds it", not "the API echoed the request".
//
// Database-backed like the rest of the issue-draft suite: the answer lives on
// the agent row, and a mocked query layer would only assert that the handler
// called what the test told it to.

// carrierExecutionConfig reads the model and reasoning effort frozen onto the
// carrier. Both are nullable columns and both are read as text: NULL and "" are
// the same answer — "whatever the local CLI is configured with" — so the query
// flattens rather than making every caller tell them apart.
func carrierExecutionConfig(t *testing.T, agentID string) (model, thinkingLevel string) {
	t.Helper()
	var level *string
	dbfx.QueryRow(t, `SELECT COALESCE(model, ''), thinking_level FROM agent WHERE id = $1`, agentID).
		Scan(&model, &level)
	if level == nil {
		return model, ""
	}
	return model, *level
}

// The three choices the composite picker makes in one place — which model,
// which effort, which capabilities — all reach the carrier exactly as chosen,
// and the draft reports the capability set it actually runs. This is the
// create-time half of the picker's contract; the panel's half is that it sends
// what it is showing (packages/views/modals/align-create-issue.test.tsx).
func TestCreateIssueDraftSessionFreezesModelAndThinkingLevel(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	// The shared test runtime is an unknown provider with no reasoning dial, so
	// the effort needs a runtime whose provider owns one.
	runtimeID := newThinkingTestRuntime(t, "Issue Draft Thinking Runtime", "claude")

	var created CreateIssueDraftSessionResponse
	testutil.Call(t, testHandler.CreateIssueDraftSession, newRequest(http.MethodPost, "/api/issue-drafts", map[string]any{
		"runtime_id":     runtimeID,
		"model":          "claude-opus-4-6",
		"thinking_level": "high",
		"capabilities":   []string{issueDraftCapabilityGrillFrontendLook},
	})).Want(http.StatusCreated).JSON(&created)

	model, level := carrierExecutionConfig(t, created.AgentID)
	if model != "claude-opus-4-6" {
		t.Fatalf("carrier model = %q, want claude-opus-4-6", model)
	}
	if level != "high" {
		t.Fatalf("carrier thinking_level = %q, want high", level)
	}
	// The response and the row report what ran, which is the requested set —
	// `grill-frontend-look` requires nothing, so nothing is added here. The
	// union with a policy's own requirements is covered by the capability
	// suite; what this asserts is that a create carrying all three choices
	// still records its capabilities.
	if want := []string{issueDraftCapabilityGrillFrontendLook}; !slices.Equal(created.Draft.Capabilities.Keys, want) {
		t.Fatalf("the created draft reports capabilities %v, want %v", created.Draft.Capabilities.Keys, want)
	}
	recorded, _ := carrierCapabilityKeys(t, created.SessionID)
	if !slices.Equal(recorded, created.Draft.Capabilities.Keys) {
		t.Fatalf("the draft row recorded capabilities %v, want the response's %v",
			recorded, created.Draft.Capabilities.Keys)
	}
}

// An omitted effort is "let the local CLI decide", stored as NULL rather than
// as a word the daemon would pass through — and it is not the same request as an
// unusable one.
func TestCreateIssueDraftSessionWithoutThinkingLevelLeavesItUnset(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	runtimeID := newThinkingTestRuntime(t, "Issue Draft No Effort Runtime", "claude")

	var created CreateIssueDraftSessionResponse
	testutil.Call(t, testHandler.CreateIssueDraftSession, newRequest(http.MethodPost, "/api/issue-drafts", map[string]any{
		"runtime_id": runtimeID,
	})).Want(http.StatusCreated).JSON(&created)

	if _, level := carrierExecutionConfig(t, created.AgentID); level != "" {
		t.Fatalf("carrier thinking_level = %q, want empty", level)
	}
}

// An effort the target runtime cannot take is refused at the door. Persisting it
// would leave the picker showing a level the daemon drops, which is the failure
// the shared validation exists to prevent — and the refusal has to name what was
// wrong, because "invalid request" gives the user nothing to act on.
func TestCreateIssueDraftSessionRefusesUnusableThinkingLevel(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	runtimeID := newThinkingTestRuntime(t, "Issue Draft Strict Effort Runtime", "claude")

	// A token this provider does not know.
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
