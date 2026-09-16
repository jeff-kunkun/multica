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
	sourceExportIssueViews(context.Background(), src, bundle, &gaps, collectGaps(&gaps))
	if len(gaps) != 1 || gaps[0].Reason != gapReasonIssueViewsCapped || gaps[0].Group != "issue_views" {
		t.Fatalf("gaps=%v, want one %s warning", gaps, gapReasonIssueViewsCapped)
	}
	if gaps[0].Limit != issueViewsScopeCap {
		t.Fatalf("cap gap=%+v, want limit=%d so the report says where it stopped", gaps[0], issueViewsScopeCap)
	}
	if len(bundle.Entities.IssueViews) != issueViewsScopeCap {
		t.Fatalf("views=%d", len(bundle.Entities.IssueViews))
	}
}

// collectGaps mirrors the recorder exportConfigGroups installs, so these unit
// tests observe the same gap entries a real bundle's manifest carries.
func collectGaps(gaps *[]TransferExportGap) func(string, error) {
	return func(group string, err error) { *gaps = append(*gaps, transferReadGap(group, err)) }
}

func TestWarningGapReason_ClassifiesReadFailuresAsFatal(t *testing.T) {
	for _, reason := range []string{gapReasonReadAPIError, gapReasonReadAPIMissing, "something_else"} {
		if warningGapReason(reason) {
			t.Errorf("reason %q must stay fatal", reason)
		}
	}
	for _, reason := range []string{
		gapReasonPluginUnfiltered, gapReasonIssueViewsCapped,
		gapReasonListCapReached, gapReasonListHasMore, gapReasonListShapeUnknown,
	} {
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

// GET /api/issue-statuses answers {"statuses": [...], "categories": [...]}.
// The envelope key list used to miss "statuses", so the export reported
// issue_statuses=0 for a workspace that has a catalog.
func TestGetList_DecodesIssueStatusesEnvelope(t *testing.T) {
	src := &fakeTransferSource{payloads: map[string]any{
		"/api/issue-statuses": map[string]any{
			"statuses": []map[string]any{
				{"id": "st-1", "key": "todo", "name": "Todo", "category": "todo"},
				{"id": "st-2", "key": "done", "name": "Done", "category": "done"},
			},
			"categories": []string{"todo", "in_progress", "done"},
			"total":      2,
		},
	}}
	var rows []map[string]any
	trunc, err := getList(context.Background(), src, "/api/issue-statuses", &rows)
	if err != nil {
		t.Fatal(err)
	}
	if trunc != nil {
		t.Fatalf("unexpected truncation %+v", trunc)
	}
	if len(rows) != 2 || strField(rows[0], "key") != "todo" {
		t.Fatalf("rows=%v, want the two statuses", rows)
	}
}

// A response shape this exporter cannot read must be named, not exported as an
// empty group.
func TestGetList_ReportsUnknownEnvelopeShape(t *testing.T) {
	src := &fakeTransferSource{payloads: map[string]any{
		"/api/renamed": map[string]any{"rows": []map[string]any{{"id": "x"}}},
		"/api/empty":   map[string]any{},
	}}
	for _, path := range []string{"/api/renamed", "/api/empty"} {
		var rows []map[string]any
		trunc, err := getList(context.Background(), src, path, &rows)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if trunc == nil || trunc.Reason != gapReasonListShapeUnknown {
			t.Fatalf("%s truncation=%+v, want reason=%s", path, trunc, gapReasonListShapeUnknown)
		}
		if len(rows) != 0 {
			t.Fatalf("%s decoded %d rows from an unknown shape", path, len(rows))
		}
	}
}

// Every list endpoint the export reads answers with an array, or with one of
// these envelope keys (audited against the live handlers). A missing key here
// is how the issue-status catalog silently vanished from the bundle.
func TestGetList_RecognizesEveryEndpointEnvelope(t *testing.T) {
	envelopes := map[string][]string{
		"/api/labels":                               {"labels"},
		"/api/issue-statuses":                       {"statuses"},
		"/api/properties":                           {"properties"},
		"/api/workspaces/ws-1/plugins":              {"plugins"},
		"/api/projects":                             {"projects"},
		"/api/projects/p-1/resources":               {"resources"},
		"/api/autopilots":                           {"autopilots"},
		"/api/quick-actions":                        {"quick_actions"},
		"/api/workspaces/ws-1/github/installations": {"installations"},
		"/api/workspaces/ws-1/vcs/connections":      {"connections"},
	}
	for path, keys := range envelopes {
		obj := map[string]any{}
		for _, k := range keys {
			obj[k] = []map[string]any{{"id": "row-1"}}
		}
		src := &fakeTransferSource{payloads: map[string]any{path: obj}}
		var rows []map[string]any
		trunc, err := getList(context.Background(), src, path, &rows)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if trunc != nil {
			t.Fatalf("%s: envelope %v not recognized: %+v", path, keys, trunc)
		}
		if len(rows) != 1 {
			t.Fatalf("%s: decoded %d rows, want 1", path, len(rows))
		}
	}
}

// The issue-view scope cap is the only server-side cap the export can hit
// today; getList reports it with the row count it stopped at.
func TestGetList_ReportsServerCapWithLimit(t *testing.T) {
	rows := make([]map[string]any, issueViewsScopeCap)
	for i := range rows {
		rows[i] = map[string]any{"id": fmt.Sprintf("v-%d", i)}
	}
	src := &fakeTransferSource{payloads: map[string]any{
		"/api/issue-views?scope_type=workspace": rows,
	}}
	var got []map[string]any
	trunc, err := getList(context.Background(), src, "/api/issue-views?scope_type=workspace", &got)
	if err != nil {
		t.Fatal(err)
	}
	if trunc == nil || trunc.Reason != gapReasonIssueViewsCapped || trunc.Limit != issueViewsScopeCap {
		t.Fatalf("truncation=%+v, want %s with limit %d", trunc, gapReasonIssueViewsCapped, issueViewsScopeCap)
	}
	if len(got) != issueViewsScopeCap {
		t.Fatalf("decoded %d rows, want the whole capped page to ship", len(got))
	}
}

func TestGetList_NoTruncationBelowCapOrWithPlainArray(t *testing.T) {
	src := &fakeTransferSource{payloads: map[string]any{
		"/api/labels":      []map[string]any{{"id": "lb-1"}, {"id": "lb-2"}},
		"/api/issue-views": []map[string]any{{"id": "v-1"}},
	}}
	for _, path := range []string{"/api/labels", "/api/issue-views"} {
		var rows []map[string]any
		trunc, err := getList(context.Background(), src, path, &rows)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if trunc != nil {
			t.Fatalf("%s reported truncation %+v for a short list", path, trunc)
		}
		if len(rows) == 0 {
			t.Fatalf("%s decoded no rows", path)
		}
	}
}

// An envelope that advertises another page is not a complete list, and one
// request cannot see past it.
func TestGetList_ReportsEnvelopeHasMore(t *testing.T) {
	src := &fakeTransferSource{payloads: map[string]any{
		"/api/things-page": map[string]any{
			"items":    []map[string]any{{"id": "a"}},
			"has_more": true,
		},
		"/api/things-cursor": map[string]any{
			"items":       []map[string]any{{"id": "a"}},
			"next_cursor": map[string]any{"created_at": "2026-09-01T00:00:00Z", "id": "a"},
		},
		"/api/things-done": map[string]any{
			"items":       []map[string]any{{"id": "a"}},
			"has_more":    false,
			"next_cursor": nil,
		},
	}}
	for _, path := range []string{"/api/things-page", "/api/things-cursor"} {
		var rows []map[string]any
		trunc, err := getList(context.Background(), src, path, &rows)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if trunc == nil || trunc.Reason != gapReasonListHasMore {
			t.Fatalf("%s truncation=%+v, want reason=%s", path, trunc, gapReasonListHasMore)
		}
		if len(rows) != 1 {
			t.Fatalf("%s decoded %d rows, want the page it did read to ship", path, len(rows))
		}
	}
	var rows []map[string]any
	trunc, err := getList(context.Background(), src, "/api/things-done", &rows)
	if err != nil {
		t.Fatal(err)
	}
	if trunc != nil {
		t.Fatalf("exhausted envelope reported truncation %+v", trunc)
	}
}

func TestTransferReadGap_KeepsHTTPErrorVocabulary(t *testing.T) {
	missing := transferReadGap("labels", &TransferHTTPError{Status: 404, Err: fmt.Errorf("nope")})
	if missing.Reason != gapReasonReadAPIMissing || missing.Status != 404 || missing.Limit != 0 {
		t.Fatalf("404 gap=%+v", missing)
	}
	failed := transferReadGap("labels", &TransferHTTPError{Status: 500, Err: fmt.Errorf("boom")})
	if failed.Reason != gapReasonReadAPIError || failed.Status != 500 {
		t.Fatalf("500 gap=%+v", failed)
	}
}
