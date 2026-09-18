package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// DENE-364: runtimes_to_bind used to be a read-only report built by matching an
// agent's *name* against a runtime's display name — a key that almost never
// matched, so migrated agents arrived with no runtime and "could not be
// clicked". The plan now matches on the three facts a cross-environment
// migration is supposed to reproduce (provider, runtime mode, custom profile
// name) and applies the three-tier rule: a single candidate may be auto-bound,
// several are left for a human, none records an actionable reason.

func transferRuntimeFixture(t *testing.T) transferOptionsFixture {
	t.Helper()
	fixture := newTransferOptionsFixture(t, nil)
	// agent_runtime and runtime_profile carry plain workspace UUIDs with no DB
	// FK, so the workspace teardown does not collect them.
	fixture.fx.Cleanup(t, `DELETE FROM agent_runtime WHERE workspace_id = $1`, fixture.workspace)
	fixture.fx.Cleanup(t, `DELETE FROM runtime_profile WHERE workspace_id = $1`, fixture.workspace)
	return fixture
}

// transferRuntimeRow inserts one target runtime. It defaults to a visible
// built-in runtime owned by the importer, so a test only states what it is
// actually varying.
func transferRuntimeRow(t *testing.T, fixture transferOptionsFixture, name string, over testutil.Cols) string {
	t.Helper()
	cols := testutil.Cols{
		"workspace_id": fixture.workspace,
		"name":         name,
		"runtime_mode": "cloud",
		"provider":     "xfer-provider",
		"status":       "online",
		"device_info":  "",
		"metadata":     testutil.Raw("'{}'::jsonb"),
		"last_seen_at": testutil.Raw("now()"),
		"visibility":   "public",
		"owner_id":     uuidString(fixture.env.ImporterID),
	}
	for k, v := range over {
		cols[k] = v
	}
	return fixture.fx.Insert(t, "agent_runtime", cols)
}

// transferRuntimeHintRequest builds a one-agent transfer whose runtime hint is
// stated explicitly, so each case reads as "source agent ran on X".
func transferRuntimeHintRequest(agentName string, hint *TransferAgentRuntimeHint) TransferConfigRequest {
	agentSourceID := uuid.NewString()
	bundle := transferOptionsBundle(agentName, "xfer-rt-autopilot-"+uuid.NewString()[:6], nil)
	bundle.Entities.Agents[0].SourceID = agentSourceID
	runtimes := TransferRuntimesFile{}
	if hint != nil {
		h := *hint
		h.SourceAgentID = agentSourceID
		runtimes.AgentHints = []TransferAgentRuntimeHint{h}
	}
	dry := true
	return TransferConfigRequest{
		Config:          bundle,
		RuntimeProfiles: runtimes,
		DryRun:          &dry,
		OnConflict:      ConflictSkip,
	}
}

// planFor runs the transfer and returns the single binding row it planned.
func planFor(t *testing.T, fixture transferOptionsFixture, req TransferConfigRequest) TransferRuntimeBind {
	t.Helper()
	report, err := ImportTransferConfig(context.Background(), fixture.env, req)
	if err != nil {
		t.Fatalf("ImportTransferConfig: %v", err)
	}
	if len(report.RuntimesToBind) != 1 {
		t.Fatalf("want exactly 1 binding row, got %d (%+v)", len(report.RuntimesToBind), report.RuntimesToBind)
	}
	return report.RuntimesToBind[0]
}

func candidateNames(bind TransferRuntimeBind) []string {
	names := make([]string, 0, len(bind.Candidates))
	for _, c := range bind.Candidates {
		names = append(names, c.Name)
	}
	return names
}

func wantCandidateNames(t *testing.T, bind TransferRuntimeBind, want ...string) {
	t.Helper()
	got := candidateNames(bind)
	if len(got) != len(want) {
		t.Fatalf("candidates = %v, want %v (status=%s reason=%s)", got, want, bind.Status, bind.Reason)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidates = %v, want %v", got, want)
		}
	}
	if len(bind.CandidateIDs) != len(bind.Candidates) {
		t.Fatalf("candidate_ids = %v does not mirror candidates %v", bind.CandidateIDs, bind.Candidates)
	}
}

func TestTransferPlanRuntimeBindingsUniqueCandidate(t *testing.T) {
	fixture := transferRuntimeFixture(t)
	runtimeName := "same-machine-" + uuid.NewString()[:6]
	want := transferRuntimeRow(t, fixture, runtimeName, testutil.Cols{
		"provider": "claude", "runtime_mode": "local",
	})
	// A runtime on another provider must never widen the candidate set: binding
	// an agent to a machine that cannot run its CLI is exactly the mistake the
	// three-tier rule exists to prevent.
	transferRuntimeRow(t, fixture, "other-provider-"+uuid.NewString()[:6], testutil.Cols{
		"provider": "codex", "runtime_mode": "local",
	})

	bind := planFor(t, fixture, transferRuntimeHintRequest("xfer-unique-"+uuid.NewString()[:6], &TransferAgentRuntimeHint{
		Provider: "claude", RuntimeMode: "local",
	}))

	if bind.Status != RuntimeBindPending {
		t.Fatalf("status = %s, want %s (reason=%s)", bind.Status, RuntimeBindPending, bind.Reason)
	}
	wantCandidateNames(t, bind, runtimeName)
	if len(bind.CandidateIDs) != 1 || bind.CandidateIDs[0] != want {
		t.Fatalf("candidate_ids = %v, want [%s]", bind.CandidateIDs, want)
	}
	if bind.Provider != "claude" || bind.RuntimeMode != "local" {
		t.Fatalf("plan lost the source facts: %+v", bind)
	}
}

func TestTransferPlanRuntimeBindingsLeavesSeveralCandidatesAlone(t *testing.T) {
	fixture := transferRuntimeFixture(t)
	first := "rt-a-" + uuid.NewString()[:6]
	second := "rt-b-" + uuid.NewString()[:6]
	transferRuntimeRow(t, fixture, first, testutil.Cols{"provider": "claude", "runtime_mode": "local"})
	transferRuntimeRow(t, fixture, second, testutil.Cols{"provider": "claude", "runtime_mode": "local"})

	bind := planFor(t, fixture, transferRuntimeHintRequest("xfer-multi-"+uuid.NewString()[:6], &TransferAgentRuntimeHint{
		Provider: "claude", RuntimeMode: "local",
	}))

	if bind.Status != RuntimeBindPending {
		t.Fatalf("status = %s, want %s", bind.Status, RuntimeBindPending)
	}
	if bind.BoundRuntimeID != "" {
		t.Fatalf("a multi-candidate row must not carry a bound runtime, got %s", bind.BoundRuntimeID)
	}
	wantCandidateNames(t, bind, first, second)
}

func TestTransferPlanRuntimeBindingsRecordsZeroCandidateReason(t *testing.T) {
	fixture := transferRuntimeFixture(t)
	transferRuntimeRow(t, fixture, "codex-only-"+uuid.NewString()[:6], testutil.Cols{"provider": "codex"})

	bind := planFor(t, fixture, transferRuntimeHintRequest("xfer-zero-"+uuid.NewString()[:6], &TransferAgentRuntimeHint{
		Provider: "claude", RuntimeMode: "cloud",
	}))

	if bind.Status != RuntimeBindNoCandidate {
		t.Fatalf("status = %s, want %s", bind.Status, RuntimeBindNoCandidate)
	}
	if bind.ReasonCode != RuntimeBindReasonNoRuntime {
		t.Fatalf("reason_code = %q, want %q", bind.ReasonCode, RuntimeBindReasonNoRuntime)
	}
	// The reason has to name what is missing, otherwise the card cannot tell
	// the user which machine to connect.
	if !strings.Contains(bind.Reason, "provider=claude") {
		t.Fatalf("reason does not name the provider: %q", bind.Reason)
	}
	wantCandidateNames(t, bind)
}

func TestTransferPlanRuntimeBindingsRequiresMatchingRuntimeMode(t *testing.T) {
	fixture := transferRuntimeFixture(t)
	transferRuntimeRow(t, fixture, "cloud-claude-"+uuid.NewString()[:6], testutil.Cols{"provider": "claude", "runtime_mode": "cloud"})

	bind := planFor(t, fixture, transferRuntimeHintRequest("xfer-mode-"+uuid.NewString()[:6], &TransferAgentRuntimeHint{
		Provider: "claude", RuntimeMode: "local",
	}))

	if bind.Status != RuntimeBindNoCandidate || bind.ReasonCode != RuntimeBindReasonNoRuntime {
		t.Fatalf("status/reason = %s/%s, want %s/%s", bind.Status, bind.ReasonCode, RuntimeBindNoCandidate, RuntimeBindReasonNoRuntime)
	}
	wantCandidateNames(t, bind)
}

func TestTransferPlanRuntimeBindingsMatchesCustomProfileByName(t *testing.T) {
	fixture := transferRuntimeFixture(t)
	profileName := "XferProfile-" + uuid.NewString()[:6]
	profileID := fixture.fx.Insert(t, "runtime_profile", testutil.Cols{
		"workspace_id":    fixture.workspace,
		"display_name":    profileName,
		"protocol_family": "claude",
		"command_name":    "claude",
		"fixed_args":      testutil.Raw("'[]'::jsonb"),
		"visibility":      "workspace",
		"created_by":      uuidString(fixture.env.ImporterID),
		"enabled":         true,
	})
	profiled := "profiled-" + uuid.NewString()[:6]
	transferRuntimeRow(t, fixture, profiled, testutil.Cols{
		"provider": "claude", "runtime_mode": "local", "profile_id": profileID,
	})
	// Same provider and mode, but a built-in runtime: the source agent used a
	// custom profile, so this must not be a candidate.
	transferRuntimeRow(t, fixture, "builtin-"+uuid.NewString()[:6], testutil.Cols{
		"provider": "claude", "runtime_mode": "local",
	})

	bind := planFor(t, fixture, transferRuntimeHintRequest("xfer-profile-"+uuid.NewString()[:6], &TransferAgentRuntimeHint{
		Provider: "claude", RuntimeMode: "local", ProfileName: profileName,
	}))

	if bind.Status != RuntimeBindPending {
		t.Fatalf("status = %s, want %s (reason=%s)", bind.Status, RuntimeBindPending, bind.Reason)
	}
	wantCandidateNames(t, bind, profiled)
	if bind.Candidates[0].ProfileName != profileName {
		t.Fatalf("candidate lost the profile name: %+v", bind.Candidates[0])
	}
}

func TestTransferPlanRuntimeBindingsBuiltinSourceSkipsProfiledRuntime(t *testing.T) {
	fixture := transferRuntimeFixture(t)
	profileID := fixture.fx.Insert(t, "runtime_profile", testutil.Cols{
		"workspace_id":    fixture.workspace,
		"display_name":    "Unused-" + uuid.NewString()[:6],
		"protocol_family": "claude",
		"command_name":    "claude",
		"fixed_args":      testutil.Raw("'[]'::jsonb"),
		"visibility":      "workspace",
		"created_by":      uuidString(fixture.env.ImporterID),
		"enabled":         true,
	})
	transferRuntimeRow(t, fixture, "profiled-only-"+uuid.NewString()[:6], testutil.Cols{
		"provider": "claude", "runtime_mode": "local", "profile_id": profileID,
	})

	bind := planFor(t, fixture, transferRuntimeHintRequest("xfer-builtin-"+uuid.NewString()[:6], &TransferAgentRuntimeHint{
		Provider: "claude", RuntimeMode: "local",
	}))

	if bind.Status != RuntimeBindNoCandidate {
		t.Fatalf("status = %s, want %s", bind.Status, RuntimeBindNoCandidate)
	}
	wantCandidateNames(t, bind)
}

func TestTransferPlanRuntimeBindingsWithoutHintExplainsItself(t *testing.T) {
	fixture := transferRuntimeFixture(t)
	transferRuntimeRow(t, fixture, "available-"+uuid.NewString()[:6], testutil.Cols{"provider": "claude"})

	// A bundle exported before the per-agent runtime hint existed: there is no
	// way to tell which provider the agent expects, so nothing is bound.
	bind := planFor(t, fixture, transferRuntimeHintRequest("xfer-nohint-"+uuid.NewString()[:6], nil))

	if bind.Status != RuntimeBindNoCandidate || bind.ReasonCode != RuntimeBindReasonProviderUnknown {
		t.Fatalf("status/reason = %s/%s, want %s/%s", bind.Status, bind.ReasonCode, RuntimeBindNoCandidate, RuntimeBindReasonProviderUnknown)
	}
	wantCandidateNames(t, bind)
}

func TestTransferPlanRuntimeBindingsSkipsInvisibleRuntimes(t *testing.T) {
	fixture := transferRuntimeFixture(t)
	// Another member's private machine: the importer cannot bind an agent onto
	// it, so it must not be offered as a candidate either.
	other := fixture.fx.User(t, fmt.Sprintf("xfer-other-%d", time.Now().UnixNano()), fmt.Sprintf("xfer-other-%d@example.com", time.Now().UnixNano()))
	fixture.fx.Member(t, fixture.workspace, other, "member")
	transferRuntimeRow(t, fixture, "foreign-private-"+uuid.NewString()[:6], testutil.Cols{
		"provider": "claude", "runtime_mode": "local", "visibility": "private", "owner_id": other,
	})

	bind := planFor(t, fixture, transferRuntimeHintRequest("xfer-private-"+uuid.NewString()[:6], &TransferAgentRuntimeHint{
		Provider: "claude", RuntimeMode: "local",
	}))

	if bind.Status != RuntimeBindNoCandidate {
		t.Fatalf("status = %s, want %s", bind.Status, RuntimeBindNoCandidate)
	}
	wantCandidateNames(t, bind)
}
