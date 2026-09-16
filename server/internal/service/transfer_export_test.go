package service

import (
	"context"
	"encoding/json"
	"fmt"
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

// collectGaps mirrors the recorder exportConfigGroups installs, so these unit
// tests observe the same gap entries a real bundle's manifest carries.
func collectGaps(gaps *[]TransferExportGap) func(string, error) {
	return func(group string, err error) { *gaps = append(*gaps, transferReadGap(group, err)) }
}

const testProjectID = "11111111-2222-3333-4444-555555555555"

// The export must carry both shared view visibilities. visibility='project'
// used to be dropped by a `!= "workspace"` filter, which silently lost every
// project board from the bundle.
func TestSourceExportIssueViews_KeepsWorkspaceAndProjectVisibility(t *testing.T) {
	src := &fakeTransferSource{payloads: map[string]any{
		"/api/issue-views?scope_type=workspace": []map[string]any{
			{"id": "v-ws", "name": "Shared board", "scope_type": "workspace", "visibility": "workspace", "definition_version": 1, "query": map[string]any{}, "display": map[string]any{}},
			{"id": "v-private", "name": "My private tabs", "scope_type": "workspace", "visibility": "private", "definition_version": 1, "query": map[string]any{}, "display": map[string]any{}},
		},
		"/api/issue-views?scope_type=project&scope_id=" + testProjectID: []map[string]any{
			{"id": "v-project", "name": "Project board", "scope_type": "project", "scope_id": testProjectID, "visibility": "project", "definition_version": 1, "query": map[string]any{}, "display": map[string]any{}},
			{"id": "v-project-private", "name": "My project draft", "scope_type": "project", "scope_id": testProjectID, "visibility": "private", "definition_version": 1, "query": map[string]any{}, "display": map[string]any{}},
		},
	}}
	bundle := &ConfigBundle{
		Entities: ConfigEntities{Projects: []ConfigProject{{SourceID: testProjectID, Title: "Board project"}}},
		Stats:    map[string]int{},
	}
	var gaps []TransferExportGap
	sourceExportIssueViews(context.Background(), src, bundle, &gaps, collectGaps(&gaps))

	byID := map[string]ConfigIssueView{}
	for _, v := range bundle.Entities.IssueViews {
		byID[v.SourceID] = v
	}
	if len(byID) != 2 {
		t.Fatalf("issue_views=%v, want the workspace- and project-shared views", bundle.Entities.IssueViews)
	}
	ws, ok := byID["v-ws"]
	if !ok || ws.Visibility != "workspace" {
		t.Fatalf("workspace-visibility view missing: %v", bundle.Entities.IssueViews)
	}
	proj, ok := byID["v-project"]
	if !ok {
		t.Fatal("project-visibility view missing from bundle")
	}
	if proj.Visibility != "project" || proj.ScopeType != "project" || proj.ScopeID == nil || *proj.ScopeID != testProjectID {
		t.Fatalf("project view scope lost: %+v", proj)
	}
	if _, ok := byID["v-private"]; ok {
		t.Fatal("private view exported")
	}
	if _, ok := byID["v-project-private"]; ok {
		t.Fatal("private project view exported")
	}
	if bundle.Stats["issue_views"] != 2 {
		t.Fatalf("stats issue_views=%d, want 2", bundle.Stats["issue_views"])
	}
	if len(gaps) != 0 {
		t.Fatalf("unexpected gaps=%v", gaps)
	}
}

// ListIssueViewsForUser ends in LIMIT 200, so a full page cannot be proven
// complete. The rows still ship, but the manifest has to say so.
func TestSourceExportIssueViews_RecordsGapWhenCapReached(t *testing.T) {
	rows := make([]map[string]any, 0, transferListCaps["/api/issue-views"])
	for i := 0; i < transferListCaps["/api/issue-views"]; i++ {
		rows = append(rows, map[string]any{
			"id": fmt.Sprintf("v-%03d", i), "name": fmt.Sprintf("Board %03d", i),
			"scope_type": "workspace", "visibility": "workspace", "definition_version": 1,
			"query": map[string]any{}, "display": map[string]any{},
		})
	}
	src := &fakeTransferSource{payloads: map[string]any{"/api/issue-views?scope_type=workspace": rows}}
	bundle := &ConfigBundle{Entities: ConfigEntities{}, Stats: map[string]int{}}
	var gaps []TransferExportGap
	sourceExportIssueViews(context.Background(), src, bundle, &gaps, collectGaps(&gaps))

	if len(bundle.Entities.IssueViews) != len(rows) {
		t.Fatalf("exported %d views, want all %d rows the server returned", len(bundle.Entities.IssueViews), len(rows))
	}
	if len(gaps) != 1 {
		t.Fatalf("gaps=%v, want one cap gap", gaps)
	}
	if gaps[0].Group != "issue_views" || gaps[0].Reason != TransferGapListCapReached || gaps[0].Limit != 200 {
		t.Fatalf("cap gap=%+v, want group=issue_views reason=%s limit=200", gaps[0], TransferGapListCapReached)
	}
}

func TestGetList_NoTruncationBelowCapOrWithPlainArray(t *testing.T) {
	src := &fakeTransferSource{payloads: map[string]any{
		"/api/labels":        []map[string]any{{"id": "lb-1"}, {"id": "lb-2"}},
		"/api/issue-views":   []map[string]any{{"id": "v-1"}},
		"/api/quick-actions": []map[string]any{{"id": "qa-1"}},
	}}
	for _, path := range []string{"/api/labels", "/api/issue-views", "/api/quick-actions"} {
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
		if trunc == nil || trunc.Reason != TransferGapListHasMore {
			t.Fatalf("%s truncation=%+v, want reason=%s", path, trunc, TransferGapListHasMore)
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
	if missing.Reason != "read_api_missing" || missing.Status != 404 || missing.Limit != 0 {
		t.Fatalf("404 gap=%+v", missing)
	}
	failed := transferReadGap("labels", &TransferHTTPError{Status: 500, Err: fmt.Errorf("boom")})
	if failed.Reason != "read_api_error" || failed.Status != 500 {
		t.Fatalf("500 gap=%+v", failed)
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
		if trunc == nil || trunc.Reason != TransferGapListShapeUnknown {
			t.Fatalf("%s truncation=%+v, want reason=%s", path, trunc, TransferGapListShapeUnknown)
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
