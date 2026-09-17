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
	bodies   map[string][]byte
	// optionalCalls records the paths read through GetOptionalJSON, so a test
	// can pin which reads the export treats as an optional subsystem.
	optionalCalls []string
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

func (f *fakeTransferSource) GetOptionalJSON(ctx context.Context, path string, out any) error {
	f.optionalCalls = append(f.optionalCalls, path)
	return f.GetJSON(ctx, path, out)
}

func (f *fakeTransferSource) GetBytes(_ context.Context, path string) ([]byte, error) {
	if body, ok := f.bodies[path]; ok {
		return body, nil
	}
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

// The plugins endpoint powers an optional subsystem, so the export has to ask
// for the optional-read policy: the client refuses to spend its retry ladder on
// a subsystem the source does not have, which is what made a disabled plugin
// feature look like a three-minute hang (DENE-406).
func TestSourceExportSkills_ReadsPluginsThroughTheOptionalClientPath(t *testing.T) {
	src := &fakeTransferSource{payloads: map[string]any{
		"/api/skills":                  []map[string]any{{"id": "sk-1", "name": "notes"}},
		"/api/workspaces/ws-1/plugins": map[string]any{"plugins": []any{}},
	}}
	bundle := &ConfigBundle{Entities: ConfigEntities{}, Stats: map[string]int{}}
	var gaps []TransferExportGap
	sourceExportSkills(context.Background(), src, "ws-1", bundle, &gaps, collectGaps(&gaps))
	if len(src.optionalCalls) != 1 || src.optionalCalls[0] != "/api/workspaces/ws-1/plugins" {
		t.Fatalf("optional reads = %v, want the plugins read and only it", src.optionalCalls)
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

// DENE-403: GET /api/autopilots/{id} answers with the autopilot body nested
// under "autopilot", plus triggers and collaborators as top-level siblings.
// Reading the envelope itself as the autopilot exported a row whose every field
// was empty, which the import then skipped as assignee_unmapped — so no
// automation ever reached the target.
func TestSourceExportAutopilots_UnwrapsDetailEnvelope(t *testing.T) {
	src := &fakeTransferSource{payloads: map[string]any{
		"/api/autopilots": map[string]any{
			"autopilots": []map[string]any{{"id": "ap-1", "title": "验收自动驾驶"}},
			"total":      1,
		},
		// The envelope shape server/internal/handler/autopilot.go writes.
		"/api/autopilots/ap-1": map[string]any{
			"autopilot": map[string]any{
				"id":                   "ap-1",
				"title":                "验收自动驾驶",
				"description":          "每个工作日早晨跑一遍",
				"status":               "active",
				"execution_mode":       "create_issue",
				"assignee_type":        "agent",
				"assignee_id":          "22222222-2222-4222-8222-000000000001",
				"project_id":           "proj-1",
				"issue_title_template": "daily {{date}}",
				"subscribers":          []map[string]any{{"user_type": "member", "user_id": "user-sub"}},
			},
			"triggers": []map[string]any{{
				"kind": "schedule", "enabled": true,
				"cron_expression": "0 9 * * *", "timezone": "Asia/Shanghai",
			}},
			"collaborators": []map[string]any{{"user_type": "member", "user_id": "user-col"}},
		},
	}}
	bundle := &ConfigBundle{Entities: ConfigEntities{}, Stats: map[string]int{}}
	var gaps []TransferExportGap
	sourceExportAutopilots(context.Background(), src, bundle, &gaps, collectGaps(&gaps))

	if len(gaps) != 0 {
		t.Fatalf("unexpected gaps=%v", gaps)
	}
	if len(bundle.Entities.Autopilots) != 1 {
		t.Fatalf("autopilots=%v", bundle.Entities.Autopilots)
	}
	ap := bundle.Entities.Autopilots[0]
	if ap.Title != "验收自动驾驶" || ap.Status != "active" || ap.ExecutionMode != "create_issue" {
		t.Fatalf("autopilot body not unwrapped: %+v", ap)
	}
	if ap.Description != "每个工作日早晨跑一遍" || ap.IssueTitleTemplate == nil || *ap.IssueTitleTemplate != "daily {{date}}" {
		t.Fatalf("autopilot text fields = %+v", ap)
	}
	if ap.Assignee == nil || ap.Assignee.Type != "agent" || ap.Assignee.ID != "22222222-2222-4222-8222-000000000001" {
		t.Fatalf("assignee = %+v, want the agent from the nested body", ap.Assignee)
	}
	if ap.ProjectID == nil || *ap.ProjectID != "proj-1" {
		t.Fatalf("project_id = %v", ap.ProjectID)
	}
	// The unwrap must not take triggers/collaborators with it: they live on the
	// envelope, one level above the body.
	if len(ap.Triggers) != 1 || ap.Triggers[0].Kind != "schedule" ||
		ap.Triggers[0].CronExpression == nil || *ap.Triggers[0].CronExpression != "0 9 * * *" {
		t.Fatalf("triggers = %+v, want the schedule trigger from the envelope top level", ap.Triggers)
	}
	if len(ap.Collaborators) != 1 || ap.Collaborators[0].UserID != "user-col" {
		t.Fatalf("collaborators = %+v", ap.Collaborators)
	}
	if len(ap.Subscribers) != 1 || ap.Subscribers[0].UserID != "user-sub" {
		t.Fatalf("subscribers = %+v, want the nested body's subscribers", ap.Subscribers)
	}
	if bundle.Stats["autopilots"] != 1 {
		t.Fatalf("stats=%v", bundle.Stats)
	}
}

// The list row is a complete autopilot body, so a detail read that fails (or an
// envelope key this exporter does not know) must still export the row.
func TestSourceExportAutopilots_FallsBackToTheListRow(t *testing.T) {
	src := &fakeTransferSource{
		payloads: map[string]any{
			"/api/autopilots": map[string]any{
				"autopilots": []map[string]any{{
					"id": "ap-1", "title": "夜跑", "status": "paused",
					"execution_mode": "run_only", "assignee_type": "agent", "assignee_id": "ag-1",
				}},
				"total": 1,
			},
		},
		errors: map[string]error{
			"/api/autopilots/ap-1": &TransferHTTPError{Status: 500, Err: fmt.Errorf("boom")},
		},
	}
	bundle := &ConfigBundle{Entities: ConfigEntities{}, Stats: map[string]int{}}
	var gaps []TransferExportGap
	sourceExportAutopilots(context.Background(), src, bundle, &gaps, collectGaps(&gaps))

	if len(bundle.Entities.Autopilots) != 1 {
		t.Fatalf("autopilots=%v", bundle.Entities.Autopilots)
	}
	ap := bundle.Entities.Autopilots[0]
	if ap.Title != "夜跑" || ap.Status != "paused" || ap.Assignee == nil || ap.Assignee.ID != "ag-1" {
		t.Fatalf("autopilot=%+v, want the list row when the detail read fails", ap)
	}
}

// A body this exporter cannot read must not be counted as an exported
// automation: it reaches the target as assignee_unmapped and is skipped there.
func TestSourceExportAutopilots_NamesUnreadableBodyInsteadOfCountingIt(t *testing.T) {
	src := &fakeTransferSource{payloads: map[string]any{
		"/api/autopilots": map[string]any{
			"autopilots": []map[string]any{{"id": "ap-1"}},
			"total":      1,
		},
		"/api/autopilots/ap-1": map[string]any{
			"autopilot":     map[string]any{"id": "ap-1"},
			"triggers":      []map[string]any{},
			"collaborators": []map[string]any{},
		},
	}}
	bundle := &ConfigBundle{Entities: ConfigEntities{}, Stats: map[string]int{}}
	var gaps []TransferExportGap
	sourceExportAutopilots(context.Background(), src, bundle, &gaps, collectGaps(&gaps))

	if len(bundle.Entities.Autopilots) != 0 {
		t.Fatalf("autopilots=%v, want the unreadable row dropped", bundle.Entities.Autopilots)
	}
	if bundle.Stats["autopilots"] != 0 {
		t.Fatalf("stats=%v, want no exported-automation count for a row that cannot arrive", bundle.Stats)
	}
	if len(gaps) != 1 || gaps[0].Group != "autopilots" || gaps[0].Reason != gapReasonAutopilotFieldsUnreadable {
		t.Fatalf("gaps=%v, want one %s entry", gaps, gapReasonAutopilotFieldsUnreadable)
	}
	if !warningGapReason(gaps[0].Reason) {
		t.Fatalf("reason %q must be a warning: the rest of the group still ships", gaps[0].Reason)
	}
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
		gapReasonAutopilotFieldsUnreadable,
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

// A workspace export runs for minutes. Without progress samples the Desktop
// card sat on "0 / 26 sessions" the whole time, which is what made users think
// it had hung and click Export again (DENE-318). Pin what the callback hears:
// the session walk and every attachment body fetched.
func TestExportFromSource_ReportsSessionAndAttachmentProgress(t *testing.T) {
	pngBody := []byte("PNGDATA")
	otherBody := []byte("PNGDATA-2")
	src := &fakeTransferSource{
		payloads: map[string]any{
			"/api/me":              map[string]any{"id": "user-1", "email": "owner@example.com"},
			"/api/workspaces":      []map[string]any{{"id": "ws-1", "slug": "src", "name": "Src"}},
			"/api/workspaces/ws-1": map[string]any{"id": "ws-1", "slug": "src", "name": "Src"},
			"/api/chat/sessions?status=all": []map[string]any{
				{"id": "sess-1", "agent_id": "ag-1", "title": "Deploy", "status": "active", "created_at": "2026-09-01T00:00:00Z", "updated_at": "2026-09-01T00:00:00Z"},
				{"id": "sess-2", "agent_id": "ag-1", "title": "Review", "status": "active", "created_at": "2026-09-02T00:00:00Z", "updated_at": "2026-09-02T00:00:00Z"},
			},
			"/api/chat/sessions/sess-1/pending-task": map[string]any{},
			"/api/chat/sessions/sess-2/pending-task": map[string]any{},
			"/api/chat/sessions/sess-1/messages/page?limit=100": map[string]any{
				"messages": []map[string]any{{
					"id": "msg-1", "role": "user", "message_kind": "message", "content": "hi",
					"created_at": "2026-09-01T00:00:01Z",
					"attachments": []map[string]any{{
						"id": "att-1", "filename": "shot-1.png", "content_type": "image/png",
						"size_bytes": len(pngBody), "created_at": "2026-09-01T00:00:01Z",
					}},
				}},
				"has_more": false,
			},
			"/api/chat/sessions/sess-2/messages/page?limit=100": map[string]any{
				"messages": []map[string]any{{
					"id": "msg-2", "role": "user", "message_kind": "message", "content": "yo",
					"created_at": "2026-09-02T00:00:01Z",
					"attachments": []map[string]any{{
						"id": "att-2", "filename": "shot-2.png", "content_type": "image/png",
						"size_bytes": len(otherBody), "created_at": "2026-09-02T00:00:01Z",
					}},
				}},
				"has_more": false,
			},
		},
		bodies: map[string][]byte{
			"/api/attachments/att-1/download": pngBody,
			"/api/attachments/att-2/download": otherBody,
		},
	}

	var samples []TransferExportProgress
	files, err := ExportFromSource(context.Background(), src, TransferExportOpts{
		Include:      []string{"conversations", "attachments"},
		WorkspaceRef: "src",
		Progress:     func(p TransferExportProgress) { samples = append(samples, p) },
	})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(files.Attachments) != 2 || len(files.Blobs) != 2 {
		t.Fatalf("attachments=%d blobs=%d, want both exported so the counters mean something",
			len(files.Attachments), len(files.Blobs))
	}
	if len(samples) == 0 {
		t.Fatal("no progress samples")
	}

	if first := samples[0]; first.SessionsTotal != 2 || first.SessionIndex != 0 {
		t.Errorf("first sample = %+v, want the session total before any session is walked", first)
	}
	if last := samples[len(samples)-1]; last.SessionIndex != 2 || last.SessionsTotal != 2 ||
		last.SessionTitle != "" || last.AttachmentsDownloaded != 2 {
		t.Errorf("last sample = %+v, want a finished walk with both attachment bodies", last)
	}

	sessionTitles := map[int]string{}
	downloaded := []int{}
	for _, s := range samples {
		if s.SessionTitle != "" {
			sessionTitles[s.SessionIndex] = s.SessionTitle
		}
		downloaded = append(downloaded, s.AttachmentsDownloaded)
	}
	if sessionTitles[1] != "Deploy" || sessionTitles[2] != "Review" {
		t.Errorf("session titles = %v, want each session named as it starts", sessionTitles)
	}
	for i := 1; i < len(downloaded); i++ {
		if downloaded[i] < downloaded[i-1] {
			t.Fatalf("attachment count went backwards: %v", downloaded)
		}
	}
	if downloaded[len(downloaded)-1] != 2 {
		t.Errorf("attachment counter ended at %d, want 2", downloaded[len(downloaded)-1])
	}
}
