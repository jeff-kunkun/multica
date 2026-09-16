package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type transferOptionsFixture struct {
	fx        *testutil.Fixture
	env       TransferImportEnv
	workspace string
}

// newTransferOptionsFixture creates a throwaway target workspace plus the
// member who imports into it. Rows the import writes itself are not built by a
// fixture builder, so they are torn down explicitly before the workspace goes.
func newTransferOptionsFixture(t *testing.T, workspaceCols testutil.Cols) transferOptionsFixture {
	t.Helper()

	pool := newResolveOriginatorPool(t)
	bootstrap := testutil.New(pool, "", "")
	suffix := time.Now().UnixNano()
	importer := bootstrap.User(t,
		fmt.Sprintf("xfer-opt-importer-%d", suffix),
		fmt.Sprintf("xfer-opt-importer-%d@example.com", suffix),
	)
	slug := fmt.Sprintf("xfer-opt-%d", suffix)
	ws := bootstrap.Workspace(t, slug, slug, workspaceCols)
	fx := testutil.New(pool, ws, importer)
	fx.Member(t, ws, importer, "owner")
	fx.Cleanup(t, `DELETE FROM agent WHERE workspace_id = $1`, ws)
	fx.Cleanup(t, `DELETE FROM autopilot WHERE workspace_id = $1`, ws)
	fx.Cleanup(t, `DELETE FROM autopilot_rule_version WHERE workspace_id = $1`, ws)

	wsID, err := util.ParseUUID(ws)
	if err != nil {
		t.Fatalf("parse workspace id: %v", err)
	}
	importerID, err := util.ParseUUID(importer)
	if err != nil {
		t.Fatalf("parse importer id: %v", err)
	}

	return transferOptionsFixture{
		fx:        fx,
		workspace: ws,
		env: TransferImportEnv{
			Queries:    db.New(pool),
			TxStarter:  pool,
			TargetID:   wsID,
			TargetSlug: slug,
			ImporterID: importerID,
		},
	}
}

// transferOptionsBundle builds a minimal V2 config request carrying the three
// entities the option switches act on: the workspace, one agent to assign the
// autopilot to, and one active autopilot.
func transferOptionsBundle(agentName, autopilotTitle string, workspace *ConfigWorkspace) ConfigBundle {
	agentSourceID := uuid.NewString()
	autopilotSourceID := uuid.NewString()
	bundle := ConfigBundle{
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
			Workspace: workspace,
			Agents: []ConfigAgent{{
				SourceID:           agentSourceID,
				Name:               agentName,
				RuntimeMode:        "local",
				Visibility:         "workspace",
				PermissionMode:     "private",
				MaxConcurrentTasks: 1,
			}},
			Autopilots: []ConfigAutopilot{{
				SourceID:      autopilotSourceID,
				Title:         autopilotTitle,
				Assignee:      &ConfigPolymorphicRef{Type: "agent", ID: agentSourceID},
				ExecutionMode: "create_issue",
				Status:        "active",
			}},
		},
		Integrations:       []ConfigIntegration{},
		PluginsToReinstall: []ConfigPlugin{},
		SecretsOmitted:     []SecretOmitted{},
		Stats:              map[string]int{},
	}
	return bundle
}

func transferOptionsRequest(bundle ConfigBundle, options ConfigImportOptions) TransferConfigRequest {
	dry := false
	return TransferConfigRequest{
		Config:     bundle,
		DryRun:     &dry,
		OnConflict: ConflictSkip,
		Options:    options,
	}
}

// DENE-363: ImportTransferConfig built its ConfigImportRequest without the
// caller's options, so ActivateAutopilots was always false and every migrated
// automation landed paused — reading as "the automations never came across".
func TestTransferImportAppliesAutopilotActivationOption(t *testing.T) {
	ctx := context.Background()

	for _, tt := range []struct {
		name     string
		activate bool
		want     string
	}{
		{name: "activation keeps the source status", activate: true, want: "active"},
		{name: "without activation every automation lands paused", activate: false, want: "paused"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newTransferOptionsFixture(t, nil)
			applySettings := true
			suffix := time.Now().UnixNano()
			title := fmt.Sprintf("xfer-ap-%d", suffix)
			bundle := transferOptionsBundle(fmt.Sprintf("xfer-agent-%d", suffix), title, nil)

			report, err := ImportTransferConfig(ctx, fixture.env, transferOptionsRequest(bundle, ConfigImportOptions{
				ActivateAutopilots:     tt.activate,
				ApplyWorkspaceSettings: &applySettings,
			}))
			if err != nil {
				t.Fatalf("import: %v", err)
			}

			var status string
			fixture.fx.QueryRow(t,
				`SELECT status FROM autopilot WHERE workspace_id = $1 AND title = $2`,
				fixture.workspace, title,
			).Scan(&status)
			if status != tt.want {
				t.Fatalf("autopilot status = %q, want %q (report batches: %v)", status, tt.want, report.ConfigReport.Batches)
			}

			paused := 0
			for _, w := range report.ConfigReport.Warnings {
				if w.Code == "autopilots_imported_paused" {
					paused += w.Count
				}
			}
			if tt.activate && paused != 0 {
				t.Fatalf("activation reported a paused count: %v", report.ConfigReport.Warnings)
			}
			if !tt.activate && paused != 1 {
				t.Fatalf("paused warning count = %d, want 1 (warnings: %v)", paused, report.ConfigReport.Warnings)
			}
		})
	}
}

// The switch that keeps the target's own configuration is the one a dropped
// options struct inverts: absent means "apply", so only a forwarded false keeps
// the target intact.
func TestTransferImportAppliesWorkspaceSettingsOption(t *testing.T) {
	ctx := context.Background()

	for _, tt := range []struct {
		name          string
		applySettings bool
		wantContext   string
		wantPrefix    string
	}{
		{name: "applying writes the bundle settings", applySettings: true, wantContext: "source-context", wantPrefix: "TGT"},
		{name: "declining keeps the target settings", applySettings: false, wantContext: "target-context", wantPrefix: "TGT"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newTransferOptionsFixture(t, testutil.Cols{
				"context":      "target-context",
				"issue_prefix": "TGT",
			})
			suffix := time.Now().UnixNano()
			bundle := transferOptionsBundle(
				fmt.Sprintf("xfer-agent-%d", suffix),
				fmt.Sprintf("xfer-ap-%d", suffix),
				&ConfigWorkspace{
					Settings:    json.RawMessage(`{}`),
					Repos:       json.RawMessage(`[]`),
					Context:     "source-context",
					IssuePrefix: "SRC",
				},
			)

			applySettings := tt.applySettings
			if _, err := ImportTransferConfig(ctx, fixture.env, transferOptionsRequest(bundle, ConfigImportOptions{
				ApplyWorkspaceSettings: &applySettings,
			})); err != nil {
				t.Fatalf("import: %v", err)
			}

			var context pgtype.Text
			var prefix string
			fixture.fx.QueryRow(t,
				`SELECT context, issue_prefix FROM workspace WHERE id = $1`, fixture.workspace,
			).Scan(&context, &prefix)
			if context.String != tt.wantContext || prefix != tt.wantPrefix {
				t.Fatalf("workspace context=%q prefix=%q, want context=%q prefix=%q",
					context.String, prefix, tt.wantContext, tt.wantPrefix)
			}
		})
	}
}

// The issue prefix has its own switch because adopting it rewrites every later
// issue key; it must not ride along on the workspace-settings switch.
func TestTransferImportAppliesIssuePrefixOption(t *testing.T) {
	ctx := context.Background()

	for _, tt := range []struct {
		name        string
		applyPrefix bool
		wantPrefix  string
	}{
		{name: "adopting the prefix rewrites the workspace prefix", applyPrefix: true, wantPrefix: "SRC"},
		{name: "leaving it alone keeps the target prefix", applyPrefix: false, wantPrefix: "TGT"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newTransferOptionsFixture(t, testutil.Cols{"issue_prefix": "TGT"})
			suffix := time.Now().UnixNano()
			bundle := transferOptionsBundle(
				fmt.Sprintf("xfer-agent-%d", suffix),
				fmt.Sprintf("xfer-ap-%d", suffix),
				&ConfigWorkspace{
					Settings:    json.RawMessage(`{}`),
					Repos:       json.RawMessage(`[]`),
					IssuePrefix: "SRC",
				},
			)

			applySettings := true
			if _, err := ImportTransferConfig(ctx, fixture.env, transferOptionsRequest(bundle, ConfigImportOptions{
				ApplyWorkspaceSettings: &applySettings,
				ApplyIssuePrefix:       tt.applyPrefix,
			})); err != nil {
				t.Fatalf("import: %v", err)
			}

			var prefix string
			fixture.fx.QueryRow(t, `SELECT issue_prefix FROM workspace WHERE id = $1`, fixture.workspace).Scan(&prefix)
			if prefix != tt.wantPrefix {
				t.Fatalf("issue_prefix = %q, want %q", prefix, tt.wantPrefix)
			}
		})
	}
}
