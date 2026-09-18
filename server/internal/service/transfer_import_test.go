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

// DENE-403 end to end: the export half must hand the import half a real
// autopilot body, not the GET /api/autopilots/{id} envelope. With the envelope
// read as the autopilot, the bundle carried an all-empty row, the import
// skipped it as assignee_unmapped, and the target workspace ended up with no
// automation at all. This drives export → import → the target's autopilot row.
func TestTransferAutopilotRoundTripKeepsSourceStatus(t *testing.T) {
	ctx := context.Background()
	fixture := newTransferOptionsFixture(t, nil)

	suffix := time.Now().UnixNano()
	title := fmt.Sprintf("xfer-roundtrip-ap-%d", suffix)
	agentSourceID := uuid.NewString()
	autopilotID := uuid.NewString()

	src := &fakeTransferSource{payloads: map[string]any{
		"/api/me":              map[string]any{"id": uuid.NewString(), "email": "owner@example.com"},
		"/api/workspaces":      []map[string]any{{"id": "ws-1", "slug": "src", "name": "Src"}},
		"/api/workspaces/ws-1": map[string]any{"id": "ws-1", "slug": "src", "name": "Src"},
		"/api/agents": []map[string]any{{
			"id": agentSourceID, "name": fmt.Sprintf("xfer-agent-%d", suffix), "instructions": "do work",
			"runtime_mode": "local", "visibility": "workspace", "permission_mode": "private",
			"max_concurrent_tasks": 1, "runtime_config": map[string]any{},
			"conversation_starters": []any{}, "disabled_runtime_skills": []any{},
		}},
		"/api/autopilots": map[string]any{"autopilots": []map[string]any{{"id": autopilotID, "title": title}}, "total": 1},
		"/api/autopilots/" + autopilotID: map[string]any{
			// The envelope server/internal/handler/autopilot.go writes.
			"autopilot": map[string]any{
				"id": autopilotID, "title": title, "description": "每个工作日早晨跑一遍",
				"status": "active", "execution_mode": "create_issue",
				"assignee_type": "agent", "assignee_id": agentSourceID,
			},
			"triggers": []map[string]any{{
				"kind": "schedule", "enabled": true,
				"cron_expression": "0 9 * * *", "timezone": "Asia/Shanghai",
			}},
			"collaborators": []map[string]any{},
		},
	}}

	files, err := ExportFromSource(ctx, src, TransferExportOpts{Include: []string{"config"}, WorkspaceRef: "src"})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(files.Config.Entities.Autopilots) != 1 {
		t.Fatalf("exported autopilots=%v", files.Config.Entities.Autopilots)
	}
	exported := files.Config.Entities.Autopilots[0]
	if exported.Title != title || exported.Status != "active" || exported.ExecutionMode != "create_issue" {
		t.Fatalf("exported autopilot = %+v, want the source body", exported)
	}
	if exported.Assignee == nil || exported.Assignee.Type != "agent" || exported.Assignee.ID != agentSourceID {
		t.Fatalf("exported assignee = %+v, want %s", exported.Assignee, agentSourceID)
	}
	if files.Config.Stats["autopilots"] != 1 {
		t.Fatalf("export stats=%v", files.Config.Stats)
	}
	if len(files.Manifest.ExportGaps) != 0 {
		t.Fatalf("export gaps=%v", files.Manifest.ExportGaps)
	}

	applySettings := true
	report, err := ImportTransferConfig(ctx, fixture.env, transferOptionsRequest(files.Config, ConfigImportOptions{
		ActivateAutopilots:     true,
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
	if status != "active" {
		t.Fatalf("target autopilot status = %q, want active (batches: %v)", status, report.ConfigReport.Batches)
	}

	var triggerCount int
	fixture.fx.QueryRow(t,
		`SELECT count(*) FROM autopilot_trigger tr
		   JOIN autopilot a ON a.id = tr.autopilot_id
		  WHERE a.workspace_id = $1 AND a.title = $2 AND tr.kind = 'schedule'`,
		fixture.workspace, title,
	).Scan(&triggerCount)
	if triggerCount != 1 {
		t.Fatalf("target schedule triggers = %d, want 1 (batches: %v)", triggerCount, report.ConfigReport.Batches)
	}
}
