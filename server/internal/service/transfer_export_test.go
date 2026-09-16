package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type fakeTransferSource struct {
	payloads map[string]any
	errors   map[string]error
}

func (f *fakeTransferSource) GetJSON(_ context.Context, path string, out any) error {
	if err, ok := f.errors[path]; ok {
		return err
	}
	v, ok := f.payloads[path]
	if !ok {
		return json.Unmarshal([]byte("[]"), out)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (f *fakeTransferSource) GetBytes(context.Context, string) ([]byte, error) {
	return nil, fmt.Errorf("not implemented")
}

func TestExportAgentsGroup_SystemKeyWithoutKind(t *testing.T) {
	src := &fakeTransferSource{payloads: map[string]any{
		"/api/agents": []map[string]any{
			{"id": "sys-1", "system_key": "mika", "instructions": "hi", "name": "Mika"},
			{"id": "usr-1", "name": "Bot", "instructions": "do work"},
			{"id": "bldr-1", "system_key": "agent_builder:hidden", "name": "Builder"},
		},
	}}
	bundle := &ConfigBundle{Entities: ConfigEntities{}, Stats: map[string]int{}}
	var gaps []TransferExportGap
	exportAgentsGroup(context.Background(), src, bundle, &gaps, func(string, error) {})

	if len(bundle.Entities.SystemAgents) != 1 || bundle.Entities.SystemAgents[0].SystemKey != "mika" {
		t.Fatalf("system_agents=%v", bundle.Entities.SystemAgents)
	}
	if len(bundle.Entities.Agents) != 1 || bundle.Entities.Agents[0].Name != "Bot" {
		t.Fatalf("agents=%v", bundle.Entities.Agents)
	}
	if bundle.Stats["system_agents"] != 1 || bundle.Stats["agents"] != 1 {
		t.Fatalf("stats=%v", bundle.Stats)
	}
}

func TestSourceExportSkills_ExcludesPluginResources(t *testing.T) {
	wsID := "ws-1"
	src := &fakeTransferSource{payloads: map[string]any{
		"/api/workspaces/ws-1/plugins": map[string]any{
			"plugins": []map[string]any{{
				"plugin_key": "demo",
				"resources": []map[string]any{
					{"type": "skill", "key": "pr-review"},
					{"type": "surface", "key": "not-a-skill"},
				},
			}},
		},
		"/api/skills": []map[string]any{
			{"id": "sk-1", "name": "pr-review", "content": "plugin skill"},
			{"id": "sk-2", "name": "my-notes", "content": "user skill"},
		},
	}}
	bundle := &ConfigBundle{Entities: ConfigEntities{}, Stats: map[string]int{}}
	var gaps []TransferExportGap
	sourceExportSkills(context.Background(), src, wsID, bundle, &gaps, func(string, error) {})
	if len(gaps) != 0 {
		t.Fatalf("unexpected gaps=%v", gaps)
	}
	if len(bundle.Entities.Skills) != 1 || bundle.Entities.Skills[0].Name != "my-notes" {
		t.Fatalf("skills=%v", bundle.Entities.Skills)
	}
}

func TestSourceExportSkills_PluginsUnavailableExportsAllWithGap(t *testing.T) {
	wsID := "ws-1"
	src := &fakeTransferSource{
		payloads: map[string]any{
			"/api/skills": []map[string]any{
				{"id": "sk-1", "name": "pr-review", "content": "plugin skill"},
				{"id": "sk-2", "name": "my-notes", "content": "user skill"},
			},
		},
		errors: map[string]error{
			"/api/workspaces/ws-1/plugins": &TransferHTTPError{Status: 503, Err: fmt.Errorf("Plugin management is not enabled")},
		},
	}
	bundle := &ConfigBundle{Entities: ConfigEntities{}, Stats: map[string]int{}}
	var gaps []TransferExportGap
	sourceExportSkills(context.Background(), src, wsID, bundle, &gaps, func(string, error) {})
	if len(bundle.Entities.Skills) != 2 {
		t.Fatalf("want all skills exported, got %d", len(bundle.Entities.Skills))
	}
	if len(gaps) != 1 || gaps[0].Reason != "plugin_skills_unfiltered" || gaps[0].Group != "skills" || gaps[0].Status != 503 {
		t.Fatalf("gaps=%v", gaps)
	}
}

func TestSourceExportIssueViews_KeepsProjectSharedViews(t *testing.T) {
	src := &fakeTransferSource{payloads: map[string]any{
		"/api/issue-views?scope_type=workspace": []map[string]any{
			{"id": "v-ws", "name": "Shared", "scope_type": "workspace", "visibility": "workspace"},
			{"id": "v-ws-private", "name": "Mine", "scope_type": "workspace", "visibility": "private"},
		},
		"/api/issue-views?scope_type=project&scope_id=p-1": []map[string]any{
			{"id": "v-proj", "name": "Project shared", "scope_type": "project", "scope_id": "p-1", "visibility": "project"},
			{"id": "v-proj-private", "name": "Mine too", "scope_type": "project", "scope_id": "p-1", "visibility": "private"},
		},
	}}
	bundle := &ConfigBundle{
		Entities: ConfigEntities{Projects: []ConfigProject{{SourceID: "p-1"}}},
		Stats:    map[string]int{},
	}
	var gaps []TransferExportGap
	sourceExportIssueViews(context.Background(), src, bundle, &gaps, func(group string, err error) {
		t.Fatalf("unexpected gap %s: %v", group, err)
	})

	got := []string{}
	for _, v := range bundle.Entities.IssueViews {
		got = append(got, v.SourceID)
	}
	if len(got) != 2 || got[0] != "v-ws" || got[1] != "v-proj" {
		t.Fatalf("exported views = %v, want [v-ws v-proj] (project-shared views must not be dropped)", got)
	}
	if bundle.Stats["issue_views"] != 2 {
		t.Fatalf("stats=%v", bundle.Stats)
	}
	if len(gaps) != 0 {
		t.Fatalf("gaps=%v", gaps)
	}
}

func TestSourceExportIssueViews_FlagsScopeCap(t *testing.T) {
	rows := make([]map[string]any, issueViewsScopeCap)
	for i := range rows {
		rows[i] = map[string]any{"id": fmt.Sprintf("v-%d", i), "scope_type": "workspace", "visibility": "workspace"}
	}
	src := &fakeTransferSource{payloads: map[string]any{
		"/api/issue-views?scope_type=workspace": rows,
	}}
	bundle := &ConfigBundle{Entities: ConfigEntities{}, Stats: map[string]int{}}
	var gaps []TransferExportGap
	sourceExportIssueViews(context.Background(), src, bundle, &gaps, func(group string, err error) {
		t.Fatalf("unexpected gap %s: %v", group, err)
	})
	if len(gaps) != 1 || gaps[0].Reason != gapReasonIssueViewsCapped || gaps[0].Group != "issue_views" {
		t.Fatalf("gaps=%v, want one %s warning", gaps, gapReasonIssueViewsCapped)
	}
	if len(bundle.Entities.IssueViews) != issueViewsScopeCap {
		t.Fatalf("views=%d", len(bundle.Entities.IssueViews))
	}
}

func TestWarningGapReason_ClassifiesReadFailuresAsFatal(t *testing.T) {
	for _, reason := range []string{gapReasonReadAPIError, gapReasonReadAPIMissing, "something_else"} {
		if warningGapReason(reason) {
			t.Errorf("reason %q must stay fatal", reason)
		}
	}
	for _, reason := range []string{gapReasonPluginUnfiltered, gapReasonIssueViewsCapped} {
		if !warningGapReason(reason) {
			t.Errorf("reason %q must be a warning", reason)
		}
	}
}

// exportFromSourceWithGaps drives ExportFromSource against a minimal fake
// source so the fail-fast policy is exercised end to end.
func exportFromSourceWithGaps(t *testing.T, errors map[string]error) (*TransferExportFiles, error) {
	t.Helper()
	payloads := map[string]any{
		"/api/me":                               map[string]any{"id": "user-1", "email": "owner@example.com"},
		"/api/workspaces":                       []map[string]any{{"id": "ws-1", "slug": "src", "name": "Src"}},
		"/api/workspaces/ws-1":                  map[string]any{"id": "ws-1", "slug": "src", "name": "Src"},
		"/api/skills":                           []map[string]any{{"id": "sk-1", "name": "notes", "content": "x"}},
		"/api/agents":                           []map[string]any{{"id": "ag-1", "name": "Bot"}},
		"/api/labels":                           []map[string]any{{"id": "lb-1", "name": "bug"}},
		"/api/issue-views?scope_type=workspace": []map[string]any{},
	}
	src := &fakeTransferSource{payloads: payloads, errors: errors}
	return ExportFromSource(context.Background(), src, TransferExportOpts{
		Include:      []string{"config"},
		WorkspaceRef: "src",
	})
}

func TestExportFromSource_FailsOnCoreReadError(t *testing.T) {
	_, err := exportFromSourceWithGaps(t, map[string]error{
		"/api/labels": &TransferHTTPError{Status: 400, Err: fmt.Errorf("workspace_id or workspace_slug is required")},
	})
	if err == nil || !strings.Contains(err.Error(), "labels") {
		t.Fatalf("err = %v, want a fatal labels gap", err)
	}
}

func TestExportFromSource_KeepsDocumentedWarnings(t *testing.T) {
	files, err := exportFromSourceWithGaps(t, map[string]error{
		"/api/workspaces/ws-1/plugins": &TransferHTTPError{Status: 503, Err: fmt.Errorf("Plugin management is not enabled")},
	})
	if err != nil {
		t.Fatalf("documented degradation must not abort the export: %v", err)
	}
	if len(files.Manifest.ExportGaps) != 1 || files.Manifest.ExportGaps[0].Reason != gapReasonPluginUnfiltered {
		t.Fatalf("gaps=%v", files.Manifest.ExportGaps)
	}
	if len(files.Config.Entities.Skills) != 1 {
		t.Fatalf("skills group was dropped: %v", files.Config.Entities.Skills)
	}
}

func TestExportFromSource_KeepsMissingEndpointAsCompatibilityWarning(t *testing.T) {
	files, err := exportFromSourceWithGaps(t, map[string]error{
		"/api/labels": &TransferHTTPError{Status: 404, Err: fmt.Errorf("not found")},
	})
	if err != nil {
		t.Fatalf("404 must stay a compatibility downgrade: %v", err)
	}
	if len(files.Manifest.ExportGaps) != 1 || files.Manifest.ExportGaps[0].Reason != gapReasonReadAPIMissing {
		t.Fatalf("gaps=%v", files.Manifest.ExportGaps)
	}
}
