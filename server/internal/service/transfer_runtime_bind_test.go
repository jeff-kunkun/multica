package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// Canonical matrix for the candidate-matching rule. The DB-backed tests below
// only cover what a real import writes; every attribute combination lives here.
func TestMatchRuntimeCandidates(t *testing.T) {
	local := TransferRuntimeCandidate{ID: "r-local", Name: "MacBook", Provider: "claude", RuntimeMode: "local"}
	cloud := TransferRuntimeCandidate{ID: "r-cloud", Name: "Cloud", Provider: "claude", RuntimeMode: "cloud"}
	other := TransferRuntimeCandidate{ID: "r-codex", Name: "Codex box", Provider: "codex", RuntimeMode: "local"}
	twin := TransferRuntimeCandidate{ID: "r-twin", Name: "MacBook", Provider: "claude", RuntimeMode: "local"}
	all := []TransferRuntimeCandidate{local, cloud, other, twin}

	for _, tt := range []struct {
		name        string
		candidates  []TransferRuntimeCandidate
		provider    string
		mode        string
		profileName string
		want        []string
	}{
		{
			name:       "provider and mode select one",
			candidates: []TransferRuntimeCandidate{local, cloud, other},
			provider:   "claude",
			mode:       "local",
			want:       []string{"r-local"},
		},
		{
			name:       "mode alone still resolves when the target has one of that mode",
			candidates: []TransferRuntimeCandidate{cloud, other},
			mode:       "cloud",
			want:       []string{"r-cloud"},
		},
		{
			name:        "profile name breaks a tie only when it matches",
			candidates:  all,
			provider:    "claude",
			mode:        "local",
			profileName: "MacBook",
			want:        []string{"r-local", "r-twin"},
		},
		{
			name:        "a profile name nothing matches never empties a real match",
			candidates:  all,
			provider:    "claude",
			mode:        "local",
			profileName: "renamed on the target",
			want:        []string{"r-local", "r-twin"},
		},
		{
			name:       "provider with no runtime on the target matches nothing",
			candidates: []TransferRuntimeCandidate{local, cloud},
			provider:   "codex",
			mode:       "local",
			want:       []string{},
		},
		{
			name:       "no attributes at all does not filter",
			candidates: []TransferRuntimeCandidate{local, cloud},
			want:       []string{"r-local", "r-cloud"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := matchRuntimeCandidates(tt.candidates, tt.provider, tt.mode, tt.profileName)
			ids := make([]string, 0, len(got))
			for _, c := range got {
				ids = append(ids, c.ID)
			}
			if len(ids) != len(tt.want) {
				t.Fatalf("candidates = %v, want %v", ids, tt.want)
			}
			for i := range ids {
				if ids[i] != tt.want[i] {
					t.Fatalf("candidates = %v, want %v", ids, tt.want)
				}
			}
		})
	}
}

// runtimeBindBundle builds a V2 request whose single agent points at a source
// runtime described by runtimes_hint, which is the join a cross-instance import
// uses to find the agent a home on the target (DENE-364).
func runtimeBindBundle(agentName, provider, mode, displayName string) TransferConfigRequest {
	agentSourceID := uuid.NewString()
	sourceRuntimeID := uuid.NewString()
	dry := false
	return TransferConfigRequest{
		Config: ConfigBundle{
			Format:        ConfigBundleFormat,
			SchemaVersion: ConfigBundleSchemaVersion,
			BundleID:      uuid.NewString(),
			ExportedAt:    time.Now().UTC(),
			Source: ConfigBundleSource{
				WorkspaceID: uuid.NewString(),
				Slug:        "source",
				Name:        "Source",
				ExportedBy:  uuid.NewString(),
			},
			Entities: ConfigEntities{
				Agents: []ConfigAgent{{
					SourceID:           agentSourceID,
					SourceRuntimeID:    sourceRuntimeID,
					Name:               agentName,
					RuntimeMode:        mode,
					Visibility:         "workspace",
					PermissionMode:     "private",
					MaxConcurrentTasks: 1,
				}},
			},
			Integrations:       []ConfigIntegration{},
			PluginsToReinstall: []ConfigPlugin{},
			SecretsOmitted:     []SecretOmitted{},
			Stats:              map[string]int{},
		},
		RuntimeProfiles: TransferRuntimesFile{
			RuntimesHint: []TransferRuntimeHint{{
				SourceRuntimeID: sourceRuntimeID,
				Provider:        provider,
				RuntimeMode:     mode,
				DisplayName:     displayName,
			}},
		},
		DryRun:     &dry,
		OnConflict: ConflictSkip,
	}
}

func boolPtr(v bool) *bool { return &v }

// DENE-364: V2 only reported runtimes_to_bind, so every migrated agent landed
// with a NULL runtime_id — "the agents are there but nothing runs".
func TestTransferImportBindsRuntime(t *testing.T) {
	ctx := context.Background()

	for _, tt := range []struct {
		name       string
		options    ConfigImportOptions
		setup      func(t *testing.T, fx transferOptionsFixture) (wantRuntime string)
		wantStatus string
		wantReason string
		wantBound  bool
	}{
		{
			name: "a public runtime owned by someone else is still a candidate",
			setup: func(t *testing.T, fx transferOptionsFixture) string {
				suffix := time.Now().UnixNano()
				stranger := fx.fx.User(t,
					fmt.Sprintf("xfer-bind-public-%d", suffix),
					fmt.Sprintf("xfer-bind-public-%d@example.com", suffix),
				)
				return fx.fx.Runtime(t, "Shared box", testutil.Cols{
					"provider": "claude", "runtime_mode": "local",
					"visibility": "public", "owner_id": stranger,
				})
			},
			wantStatus: RuntimeBindBound,
			wantBound:  true,
		},
		{
			name: "a single matching runtime is bound",
			setup: func(t *testing.T, fx transferOptionsFixture) string {
				return fx.fx.Runtime(t, "MacBook", testutil.Cols{"provider": "claude", "runtime_mode": "local"})
			},
			wantStatus: RuntimeBindBound,
			wantBound:  true,
		},
		{
			name: "several matching runtimes are handed back, not guessed",
			setup: func(t *testing.T, fx transferOptionsFixture) string {
				// Neither carries the source runtime's display name, so the
				// profile tie-break cannot narrow them either.
				fx.fx.Runtime(t, "Mac A", testutil.Cols{"provider": "claude", "runtime_mode": "local"})
				fx.fx.Runtime(t, "Mac B", testutil.Cols{"provider": "claude", "runtime_mode": "local"})
				return ""
			},
			wantStatus: RuntimeBindChoose,
		},
		{
			name: "no runtime at all reports a reason instead of skipping silently",
			setup: func(t *testing.T, fx transferOptionsFixture) string {
				return ""
			},
			wantStatus: RuntimeBindNone,
			wantReason: bindReasonNoVisibleRuntime,
		},
		{
			name: "a runtime of another provider reports no match",
			setup: func(t *testing.T, fx transferOptionsFixture) string {
				fx.fx.Runtime(t, "Codex box", testutil.Cols{"provider": "codex", "runtime_mode": "local"})
				return ""
			},
			wantStatus: RuntimeBindNone,
			wantReason: bindReasonNoProviderMatch,
		},
		{
			// An auto-bind must never move an agent onto a machine and account
			// that are not the importer's. The importer's own runtime here is
			// a different provider, so the only way this could come back bound
			// is by treating the stranger's private runtime as a candidate.
			name: "another member's private runtime is not a candidate",
			setup: func(t *testing.T, fx transferOptionsFixture) string {
				suffix := time.Now().UnixNano()
				stranger := fx.fx.User(t,
					fmt.Sprintf("xfer-bind-stranger-%d", suffix),
					fmt.Sprintf("xfer-bind-stranger-%d@example.com", suffix),
				)
				fx.fx.Runtime(t, "Someone else's Mac", testutil.Cols{
					"provider": "claude", "runtime_mode": "local",
					"visibility": "private", "owner_id": stranger,
				})
				fx.fx.Runtime(t, "My Codex box", testutil.Cols{"provider": "codex", "runtime_mode": "local"})
				return ""
			},
			wantStatus: RuntimeBindNone,
			wantReason: bindReasonNoProviderMatch,
		},
		{
			name:    "switching auto-bind off leaves the unique match unbound",
			options: ConfigImportOptions{AutoBindRuntimes: boolPtr(false)},
			setup: func(t *testing.T, fx transferOptionsFixture) string {
				fx.fx.Runtime(t, "MacBook", testutil.Cols{"provider": "claude", "runtime_mode": "local"})
				return ""
			},
			wantStatus: RuntimeBindChoose,
			wantReason: bindReasonAutoBindDisabled,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fx := newTransferOptionsFixture(t, nil)
			wantRuntime := tt.setup(t, fx)

			req := runtimeBindBundle("Builder", "claude", "local", "MacBook")
			req.Options = tt.options

			report, err := ImportTransferConfig(ctx, fx.env, req)
			if err != nil {
				t.Fatalf("import: %v", err)
			}
			if len(report.RuntimesToBind) != 1 {
				t.Fatalf("runtimes_to_bind = %d rows, want 1", len(report.RuntimesToBind))
			}
			bind := report.RuntimesToBind[0]
			if bind.Status != tt.wantStatus {
				t.Fatalf("status = %q (reason %q), want %q", bind.Status, bind.Reason, tt.wantStatus)
			}
			if tt.wantReason != "" && bind.Reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", bind.Reason, tt.wantReason)
			}
			if tt.wantBound && bind.BoundRuntimeID != wantRuntime {
				t.Fatalf("bound_runtime_id = %q, want %q", bind.BoundRuntimeID, wantRuntime)
			}

			// The report is only a claim; the agent row is the delivery.
			var runtimeID *string
			fx.fx.QueryRow(t, `SELECT runtime_id::text FROM agent WHERE id = $1`, bind.AgentTargetID).Scan(&runtimeID)
			switch {
			case tt.wantBound && (runtimeID == nil || *runtimeID != wantRuntime):
				t.Fatalf("agent.runtime_id = %v, want %q", runtimeID, wantRuntime)
			case !tt.wantBound && runtimeID != nil:
				t.Fatalf("agent.runtime_id = %q, want no binding", *runtimeID)
			}
		})
	}
}

// A dry run must plan the bind without writing it: the migration card shows the
// preview before the operator confirms.
func TestTransferImportDryRunPlansBindWithoutWriting(t *testing.T) {
	ctx := context.Background()
	fx := newTransferOptionsFixture(t, nil)
	runtimeID := fx.fx.Runtime(t, "MacBook", testutil.Cols{"provider": "claude", "runtime_mode": "local"})

	req := runtimeBindBundle("Builder", "claude", "local", "MacBook")
	dry := true
	req.DryRun = &dry

	report, err := ImportTransferConfig(ctx, fx.env, req)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(report.RuntimesToBind) != 1 {
		t.Fatalf("runtimes_to_bind = %d rows, want 1", len(report.RuntimesToBind))
	}
	if got := report.RuntimesToBind[0]; got.Status != RuntimeBindBound || got.BoundRuntimeID != runtimeID {
		t.Fatalf("plan = %+v, want bound to %s", got, runtimeID)
	}
	if n := fx.fx.Count(t, `SELECT count(*) FROM agent WHERE workspace_id = $1`, fx.workspace); n != 0 {
		t.Fatalf("dry run wrote %d agents, want 0", n)
	}
}
