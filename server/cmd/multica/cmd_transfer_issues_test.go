package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// An upstream-shaped read API
// ---------------------------------------------------------------------------
//
// These fixtures answer with the real endpoint semantics the exporter has to
// survive: `GET /api/issues` is a silently-clamped LIMIT/OFFSET list whose sort
// column is `created_at` when asked for, comments page with a strict
// `created_at > since` cursor capped at 2000 rows, and the issue detail is the
// only place issue reactions are served.

type transferIssueAPIFixture struct {
	issuePrefix string
	issues      []map[string]any
	comments    map[string][]map[string]any
	labels      map[string][]map[string]any
	reactions   map[string][]map[string]any
	attachments map[string][]map[string]any
	bodies      map[string][]byte
	statuses    []map[string]any
	properties  []map[string]any
	squads      []map[string]any
	labelsGroup []map[string]any

	mu              sync.Mutex
	issueQueries    []url.Values
	commentRequests int
}

func (f *transferIssueAPIFixture) recordIssueQuery(v url.Values) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issueQueries = append(f.issueQueries, v)
}

func (f *transferIssueAPIFixture) recordedIssueQueries() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]url.Values, len(f.issueQueries))
	copy(out, f.issueQueries)
	return out
}

func (f *transferIssueAPIFixture) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case path == "/api/me":
			writeJSONTest(w, map[string]any{"id": "user-1", "email": "owner@example.com", "name": "Owner"})
		case path == "/api/workspaces" && r.Method == http.MethodGet:
			writeJSONTest(w, []map[string]any{{
				"id": "ws-1", "slug": "src", "name": "Src", "issue_prefix": f.issuePrefix,
			}})
		case path == "/api/workspaces/ws-1":
			writeJSONTest(w, map[string]any{
				"id": "ws-1", "slug": "src", "name": "Src", "issue_prefix": f.issuePrefix,
				"settings": map[string]any{}, "repos": []any{},
			})
		case path == "/api/workspaces/ws-1/members":
			writeJSONTest(w, []map[string]any{{"user_id": "user-1", "email": "owner@example.com", "role": "owner"}})
		case path == "/api/workspaces/ws-1/plugins":
			writeJSONTest(w, map[string]any{"plugins": []any{}})
		case path == "/api/issues":
			f.recordIssueQuery(r.URL.Query())
			writeJSONTest(w, f.issuePage(r.URL.Query()))
		case strings.HasPrefix(path, "/api/issues/"):
			f.serveIssueSubresource(w, r)
		case strings.HasPrefix(path, "/api/attachments/") && strings.HasSuffix(path, "/download"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/api/attachments/"), "/download")
			body, ok := f.bodies[id]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(body)
		case path == "/api/labels":
			writeJSONTest(w, map[string]any{"labels": f.labelsGroup})
		case path == "/api/issue-statuses":
			writeJSONTest(w, map[string]any{"statuses": f.statuses})
		case path == "/api/properties":
			writeJSONTest(w, map[string]any{"properties": f.properties})
		case path == "/api/squads":
			writeJSONTest(w, f.squads)
		default:
			writeJSONTest(w, []any{})
		}
	}))
}

func writeJSONTest(w http.ResponseWriter, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// issuePage is a faithful `LIMIT/OFFSET` slice of the created_at-ascending
// list. The fixture never honours a `limit` above 100, matching the server's
// silent clamp.
func (f *transferIssueAPIFixture) issuePage(q url.Values) map[string]any {
	limit := 100
	if raw := q.Get("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 100 {
		limit = 100
	}
	offset := 0
	if raw := q.Get("offset"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
			offset = v
		}
	}
	if offset > len(f.issues) {
		offset = len(f.issues)
	}
	end := offset + limit
	if end > len(f.issues) {
		end = len(f.issues)
	}
	return map[string]any{"issues": f.issues[offset:end], "total": len(f.issues)}
}

func (f *transferIssueAPIFixture) serveIssueSubresource(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/issues/")
	parts := strings.Split(rest, "/")
	id := parts[0]
	if len(parts) == 1 {
		// Issue detail: the only read that serves issue reactions.
		writeJSONTest(w, map[string]any{"id": id, "reactions": f.reactions[id]})
		return
	}
	switch parts[1] {
	case "comments":
		f.mu.Lock()
		f.commentRequests++
		f.mu.Unlock()
		writeJSONTest(w, f.commentPage(id, r.URL.Query().Get("since")))
	case "attachments":
		writeJSONTest(w, emptyIfNil(f.attachments[id]))
	case "labels":
		writeJSONTest(w, map[string]any{"labels": emptyIfNil(f.labels[id]), "issue_revision": 1})
	default:
		http.NotFound(w, r)
	}
}

func emptyIfNil(rows []map[string]any) []map[string]any {
	if rows == nil {
		return []map[string]any{}
	}
	return rows
}

// commentPage reproduces `created_at > since ORDER BY created_at ASC, id ASC
// LIMIT 2001`, then the handler's truncation back to the 2000-row hard cap.
// The strict `>` is the whole point of the tie test below.
func (f *transferIssueAPIFixture) commentPage(issueID, since string) []map[string]any {
	rows := append([]map[string]any{}, f.comments[issueID]...)
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := strFieldTest(rows[i], "created_at"), strFieldTest(rows[j], "created_at")
		if a != b {
			return a < b
		}
		return strFieldTest(rows[i], "id") < strFieldTest(rows[j], "id")
	})
	if since != "" {
		filtered := rows[:0:0]
		for _, row := range rows {
			if strFieldTest(row, "created_at") > since {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	if len(rows) > 2001 {
		rows = rows[:2001]
	}
	if len(rows) > 2000 {
		rows = rows[:2000]
	}
	return rows
}

func strFieldTest(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}

// ---------------------------------------------------------------------------
// Fixture helpers
// ---------------------------------------------------------------------------

func transferTestIssue(id string, number int, title string) map[string]any {
	return map[string]any{
		"id": id, "number": number, "identifier": "SRC-" + strconv.Itoa(number),
		"title": title, "description": "body of " + id, "status": "todo", "status_category": "todo",
		"priority": "high", "creator_type": "member", "creator_id": "user-1",
		"position": float64(-number), "created_at": "2026-09-01T00:00:00Z",
		"updated_at": "2026-09-01T00:00:00Z", "last_activity_at": "2026-09-01T00:00:00Z",
		"metadata": map[string]any{}, "properties": map[string]any{},
	}
}

func transferTestComment(id, issueID, createdAt string) map[string]any {
	return map[string]any{
		"id": id, "issue_id": issueID, "author_type": "member", "author_id": "user-1",
		"content": "comment " + id, "type": "comment", "parent_id": nil,
		"created_at": createdAt, "updated_at": createdAt,
		"reactions": []any{}, "attachments": []any{},
	}
}

func transferTestIssueCmd(t *testing.T, srv *httptest.Server, flags map[string]string) (*cobraCommandResult, error) {
	t.Helper()
	cmd := newTransferExportTestCmd()
	_ = cmd.Flags().Set("server-url", srv.URL)
	_ = cmd.Flags().Set("workspace-id", "ws-1")
	_ = cmd.Flags().Set("workspace", "src")
	for name, value := range flags {
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatalf("set --%s: %v", name, err)
		}
	}
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	err := cmd.Execute()
	return &cobraCommandResult{Stdout: out.String(), Stderr: errBuf.String()}, err
}

type cobraCommandResult struct {
	Stdout string
	Stderr string
}

func readTransferZipEntries(t *testing.T, path string) map[string][]byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = b
	}
	return out
}

func decodeZipJSONL[T any](t *testing.T, entries map[string][]byte, name string) []T {
	t.Helper()
	raw, ok := entries[name]
	if !ok {
		t.Fatalf("bundle is missing %s (have %v)", name, zipEntryNames(entries))
	}
	var out []T
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		var row T
		if err := dec.Decode(&row); err != nil {
			break
		}
		out = append(out, row)
	}
	return out
}

func zipEntryNames(entries map[string][]byte) []string {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func decodeZipJSON(t *testing.T, entries map[string][]byte, name string, dest any) {
	t.Helper()
	raw, ok := entries[name]
	if !ok {
		t.Fatalf("bundle is missing %s", name)
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
}

// transferManifestRefsTest is the minimum shape the manifest refs decoder
// needs for the assertions below.
type transferManifestRefsTest struct {
	Format        string `json:"format"`
	SchemaVersion int    `json:"schema_version"`
	Options       struct {
		Include []string `json:"include"`
	} `json:"options"`
	Refs struct {
		Agents          map[string]any `json:"agents"`
		SystemAgents    map[string]any `json:"system_agents"`
		Projects        map[string]any `json:"projects"`
		Members         map[string]any `json:"members"`
		Squads          map[string]any `json:"squads"`
		Issues          map[string]any `json:"issues"`
		IssueStatuses   map[string]any `json:"issue_statuses"`
		IssueProperties map[string]any `json:"issue_properties"`
	} `json:"refs"`
	Stats map[string]int `json:"stats"`
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// The issue list has no cursor and its default sort is the drag position, so
// the walk has to page by created_at ascending, dedupe by id, and end on the
// page size. 250 issues force three pages.
func TestTransferExportIssues_PaginatesCreatedAtAscendingWithoutLoss(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	fixture := &transferIssueAPIFixture{
		issuePrefix: "SRC",
		comments:    map[string][]map[string]any{},
		labels:      map[string][]map[string]any{},
		reactions:   map[string][]map[string]any{},
		attachments: map[string][]map[string]any{},
		bodies:      map[string][]byte{},
	}
	for i := 0; i < 250; i++ {
		id := fmt.Sprintf("issue-%03d", i)
		fixture.issues = append(fixture.issues, transferTestIssue(id, i+1, "task "+id))
		fixture.comments[id] = []map[string]any{
			transferTestComment("c-"+id, id, "2026-09-02T00:00:00Z"),
		}
	}
	srv := fixture.server()
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "bundle.zip")
	res, err := transferTestIssueCmd(t, srv, map[string]string{
		"out": out, "include": "config,issues",
	})
	if err != nil {
		t.Fatalf("export: %v (stderr %s)", err, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "no issues at all") {
		t.Fatalf("export must warn that the target has to be empty, stderr=%q", res.Stderr)
	}

	entries := readTransferZipEntries(t, out)
	issues := decodeZipJSONL[map[string]any](t, entries, "issues/issues-0001.jsonl")
	if len(issues) != 250 {
		t.Fatalf("exported %d issues, want 250 across pages", len(issues))
	}
	seen := map[string]bool{}
	for _, row := range issues {
		id := strFieldTest(row, "source_id")
		if id == "" {
			t.Fatalf("issue row without a source_id: %v", row)
		}
		if seen[id] {
			t.Fatalf("issue %s appeared twice", id)
		}
		seen[id] = true
	}
	if len(seen) != 250 {
		t.Fatalf("distinct ids=%d, want 250", len(seen))
	}

	for _, q := range fixture.recordedIssueQueries() {
		if q.Get("sort") != "created_at" || q.Get("direction") != "asc" {
			t.Fatalf("issue list paged with sort=%q direction=%q; the default sort is the drag position and reorders under the walk",
				q.Get("sort"), q.Get("direction"))
		}
		if q.Get("limit") != "100" {
			t.Fatalf("limit=%q, want the server's 100 cap", q.Get("limit"))
		}
	}

	// The shard files are paired by index, so nothing in comments-000N may
	// belong to an issue outside issues-000N.
	comments := decodeZipJSONL[map[string]any](t, entries, "issues/comments-0001.jsonl")
	if len(comments) != 250 {
		t.Fatalf("comments-0001 holds %d rows, want one per exported issue", len(comments))
	}
	for _, row := range comments {
		if !seen[strFieldTest(row, "issue_id")] {
			t.Fatalf("comments-0001 holds a comment of %s, which issues-0001 does not declare", strFieldTest(row, "issue_id"))
		}
	}

	var manifest transferManifestRefsTest
	decodeZipJSON(t, entries, "manifest.json", &manifest)
	if manifest.SchemaVersion != 2 {
		t.Fatalf("schema_version=%d, want 2 for a bundle that carries issues", manifest.SchemaVersion)
	}
	if len(manifest.Refs.Issues) != 250 {
		t.Fatalf("refs.issues holds %d entries, want the whole package's 250", len(manifest.Refs.Issues))
	}
	if manifest.Stats["issues"] != 250 {
		t.Fatalf("stats.issues=%d", manifest.Stats["issues"])
	}
}

// Without the issues group the bundle stays schema_version 1: an un-upgraded
// target can still read it.
func TestTransferExportIssues_SchemaVersionFollowsContent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	fixture := &transferIssueAPIFixture{
		issuePrefix: "SRC",
		comments:    map[string][]map[string]any{},
		labels:      map[string][]map[string]any{},
		reactions:   map[string][]map[string]any{},
		attachments: map[string][]map[string]any{},
		bodies:      map[string][]byte{},
	}
	srv := fixture.server()
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "bundle.zip")
	if _, err := transferTestIssueCmd(t, srv, map[string]string{"out": out, "include": "config,conversations,attachments"}); err != nil {
		t.Fatalf("export: %v", err)
	}
	entries := readTransferZipEntries(t, out)
	var manifest transferManifestRefsTest
	decodeZipJSON(t, entries, "manifest.json", &manifest)
	if manifest.SchemaVersion != 1 {
		t.Fatalf("schema_version=%d, want 1 when issues are not included", manifest.SchemaVersion)
	}
	for name := range entries {
		if strings.HasPrefix(name, "issues/") {
			t.Fatalf("bundle wrote %s without the issues group", name)
		}
	}
}

// Two comments sharing a created_at that straddle the 2000-row page boundary:
// the cursor is the last row's timestamp minus one microsecond, so the next
// page re-reads the tie group and the seen-id set drops the duplicates. Using
// the timestamp verbatim would skip the tie row for good.
func TestTransferExportIssues_ReReadsSameMicrosecondTieInsteadOfSkippingIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	base := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	tieAt := base.Add(1998 * time.Second).Format(time.RFC3339Nano)
	comments := []map[string]any{}
	for i := 0; i < 1998; i++ {
		comments = append(comments, transferTestComment(
			fmt.Sprintf("c-%04d", i), "issue-1", base.Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano)))
	}
	for i := 1998; i < 2002; i++ {
		comments = append(comments, transferTestComment(fmt.Sprintf("c-%04d", i), "issue-1", tieAt))
	}
	for i := 2002; i < 2005; i++ {
		comments = append(comments, transferTestComment(
			fmt.Sprintf("c-%04d", i), "issue-1", base.Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano)))
	}

	fixture := &transferIssueAPIFixture{
		issuePrefix: "SRC",
		issues:      []map[string]any{transferTestIssue("issue-1", 1, "long thread")},
		comments:    map[string][]map[string]any{"issue-1": comments},
		labels:      map[string][]map[string]any{},
		reactions:   map[string][]map[string]any{},
		attachments: map[string][]map[string]any{},
		bodies:      map[string][]byte{},
	}
	srv := fixture.server()
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "bundle.zip")
	if _, err := transferTestIssueCmd(t, srv, map[string]string{"out": out, "include": "config,issues"}); err != nil {
		t.Fatalf("export: %v", err)
	}
	entries := readTransferZipEntries(t, out)
	rows := decodeZipJSONL[map[string]any](t, entries, "issues/comments-0001.jsonl")
	if len(rows) != len(comments) {
		t.Fatalf("exported %d comments, want all %d — a tie row was skipped at the page boundary", len(rows), len(comments))
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[strFieldTest(row, "source_id")] = true
	}
	for _, id := range []string{"c-1998", "c-1999", "c-2000", "c-2001"} {
		if !seen[id] {
			t.Fatalf("comment %s (a same-microsecond tie) was dropped", id)
		}
	}
	if fixture.commentRequests < 2 {
		t.Fatalf("comments were read %d time(s); a 2005-row thread cannot fit one 2000-row page", fixture.commentRequests)
	}
}

// Five ref maps and the relations file: labels and reactions all have to
// travel, because the import rebuilds them from these rows alone.
func TestTransferExportIssues_CarriesFiveRefsMapsAndRelations(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	fixture := &transferIssueAPIFixture{
		issuePrefix: "SRC",
		issues:      []map[string]any{transferTestIssue("issue-1", 7, "labelled")},
		comments: map[string][]map[string]any{
			"issue-1": {func() map[string]any {
				row := transferTestComment("c-1", "issue-1", "2026-09-02T00:00:00Z")
				row["reactions"] = []map[string]any{{"id": "r-2", "comment_id": "c-1", "actor_type": "member", "actor_id": "user-1", "emoji": "✅", "created_at": "2026-09-02T00:00:01Z"}}
				return row
			}()},
		},
		labels: map[string][]map[string]any{
			"issue-1": {{"id": "lb-1", "resource_type": "issue", "name": "bug"}},
		},
		reactions: map[string][]map[string]any{
			"issue-1": {{"id": "r-1", "issue_id": "issue-1", "actor_type": "member", "actor_id": "user-1", "emoji": "👍", "created_at": "2026-09-01T00:00:01Z"}},
		},
		attachments: map[string][]map[string]any{},
		bodies:      map[string][]byte{},
		labelsGroup: []map[string]any{{"id": "lb-1", "resource_type": "issue", "name": "bug"}},
		statuses:    []map[string]any{{"id": "st-1", "key": "todo", "name": "Todo", "category": "todo"}},
		properties:  []map[string]any{{"id": "prop-1", "name": "Deploy target", "type": "text"}},
		squads:      []map[string]any{{"id": "sq-1", "name": "Stage Crew"}},
	}
	srv := fixture.server()
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "bundle.zip")
	if _, err := transferTestIssueCmd(t, srv, map[string]string{"out": out, "include": "config,issues"}); err != nil {
		t.Fatalf("export: %v", err)
	}
	entries := readTransferZipEntries(t, out)
	var manifest transferManifestRefsTest
	decodeZipJSON(t, entries, "manifest.json", &manifest)
	for name, got := range map[string]int{
		"members":          len(manifest.Refs.Members),
		"squads":           len(manifest.Refs.Squads),
		"issues":           len(manifest.Refs.Issues),
		"issue_statuses":   len(manifest.Refs.IssueStatuses),
		"issue_properties": len(manifest.Refs.IssueProperties),
	} {
		if got == 0 {
			t.Fatalf("manifest.refs.%s is empty; the import cannot remap from an empty map", name)
		}
	}
	if manifest.Refs.IssueProperties["prop-1"] == nil {
		t.Fatal("refs.issue_properties does not carry the source property definition id")
	}

	relations := decodeZipJSONL[map[string]any](t, entries, "issues/relations.jsonl")
	kinds := map[string]int{}
	for _, row := range relations {
		kinds[strFieldTest(row, "kind")]++
	}
	for _, kind := range []string{"issue_label", "issue_reaction", "comment_reaction"} {
		if kinds[kind] == 0 {
			t.Fatalf("relations.jsonl has no %s row: %v", kind, kinds)
		}
	}
}

// §10.3 #11-13: nothing canary-shaped survives into the unzipped bundle, the
// `***` mask never appears, every redaction is recorded, and a secret-bearing
// .env attachment ships metadata only.
func TestTransferExportIssues_SecretCanariesAbsentFromBundle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	const (
		canaryDesc   = "ghp_CANARYDESC1234567890"
		canaryPriv   = "-----BEGIN OPENSSH PRIVATE KEY-----\nfakekey\n-----END OPENSSH PRIVATE KEY-----"
		canaryAssign = "CANARY_ISSUE_9c0d"
		canaryMeta   = "CANARY_META_1e2f"
		canaryAttach = "CANARY_ICOMMENT_3a4b"
	)
	envBody := []byte("SECRET=" + canaryAttach + "\n")

	issue := transferTestIssue("issue-1", 1, "deploy notes")
	issue["description"] = "token " + canaryDesc
	issue["metadata"] = map[string]any{"deploy_token": canaryMeta}
	comment := transferTestComment("c-1", "issue-1", "2026-09-02T00:00:00Z")
	comment["content"] = "key API_KEY=" + canaryAssign + "\n" + canaryPriv
	comment["attachments"] = []map[string]any{{
		"id": "att-1", "filename": "prod.env", "content_type": "text/plain",
		"size_bytes": len(envBody), "created_at": "2026-09-02T00:00:00Z",
	}}

	fixture := &transferIssueAPIFixture{
		issuePrefix: "SRC",
		issues:      []map[string]any{issue},
		comments:    map[string][]map[string]any{"issue-1": {comment}},
		labels:      map[string][]map[string]any{},
		reactions:   map[string][]map[string]any{},
		attachments: map[string][]map[string]any{},
		bodies:      map[string][]byte{"att-1": envBody},
	}
	srv := fixture.server()
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "bundle.zip")
	if _, err := transferTestIssueCmd(t, srv, map[string]string{"out": out, "include": "config,issues,attachments"}); err != nil {
		t.Fatalf("export: %v", err)
	}
	entries := readTransferZipEntries(t, out)
	var all bytes.Buffer
	for _, name := range zipEntryNames(entries) {
		all.Write(entries[name])
		all.WriteByte('\n')
	}
	body := all.String()
	for _, needle := range []string{canaryDesc, canaryPriv, canaryAssign, canaryMeta, canaryAttach, "***", "BEGIN OPENSSH PRIVATE KEY"} {
		if strings.Contains(body, needle) {
			t.Fatalf("bundle leaked %q", needle)
		}
	}
	for name := range entries {
		if strings.HasPrefix(name, "attachments/blobs/") {
			t.Fatalf("a secret-bearing .env attachment shipped a body at %s", name)
		}
	}

	var secrets []map[string]any
	decodeZipJSON(t, entries, "secrets_omitted.json", &secrets)
	fields := map[string]bool{}
	for _, row := range secrets {
		if field, ok := row["field"].(string); ok {
			fields[field] = true
		}
	}
	for _, want := range []string{"description", "metadata.deploy_token", "content", "body"} {
		found := false
		for field := range fields {
			if strings.HasSuffix(field, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("secrets_omitted.json has no record for %s: %v", want, fields)
		}
	}
	if !strings.Contains(body, "[REDACTED:github_token]") || !strings.Contains(body, "[REDACTED:private_key]") {
		t.Fatal("expected the redaction placeholders to be what replaced the canaries")
	}
}

// `--estimate` has to cover the issue group through the same paging code the
// real export uses.
func TestTransferExportIssues_EstimateCoversIssues(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	fixture := &transferIssueAPIFixture{
		issuePrefix: "SRC",
		issues: []map[string]any{
			transferTestIssue("issue-1", 1, "one"),
			transferTestIssue("issue-2", 2, "two"),
		},
		comments: map[string][]map[string]any{
			"issue-1": {transferTestComment("c-1", "issue-1", "2026-09-02T00:00:00Z")},
			"issue-2": {},
		},
		labels:      map[string][]map[string]any{},
		reactions:   map[string][]map[string]any{},
		attachments: map[string][]map[string]any{},
		bodies:      map[string][]byte{},
	}
	srv := fixture.server()
	defer srv.Close()

	res, err := transferTestIssueCmd(t, srv, map[string]string{"include": "config,issues", "estimate": "true"})
	if err != nil {
		t.Fatalf("estimate: %v", err)
	}
	var estimate struct {
		Issues        int   `json:"issues"`
		IssueComments int   `json:"issue_comments"`
		EstimatedByte int64 `json:"estimated_bytes"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &estimate); err != nil {
		t.Fatalf("estimate output %q: %v", res.Stdout, err)
	}
	if estimate.Issues != 2 || estimate.IssueComments != 1 {
		t.Fatalf("estimate=%+v, want 2 issues and 1 sampled comment", estimate)
	}
	if estimate.EstimatedByte <= 0 {
		t.Fatalf("estimate=%+v, want a positive byte guess", estimate)
	}
}

// ---------------------------------------------------------------------------
// Import
// ---------------------------------------------------------------------------

// writeTransferIssuesZip builds the smallest V3 bundle that carries an issues
// group. Shard i of `commentShards` belongs to shard i of `issueShards`, which
// is the pairing the exporter writes.
func writeTransferIssuesZip(t *testing.T, path string, refs map[string]any, issueShards [][]map[string]any, commentShards [][]map[string]any, relations []map[string]any) {
	t.Helper()
	manifest := map[string]any{
		"format": "multica.workspace-transfer", "schema_version": 2, "bundle_id": "b-1",
		"refs": refs,
	}
	config := map[string]any{
		"format": "multica.workspace-config", "schema_version": 1, "bundle_id": "c-1",
		"source":   map[string]any{"workspace_id": "src-ws", "slug": "src", "issue_prefix": "SRC"},
		"entities": map[string]any{},
	}
	writeTransferZipFiles(t, path, func(add func(name string, body any)) {
		add("manifest.json", manifest)
		add("config.json", config)
		for i, shard := range issueShards {
			add(fmt.Sprintf("issues/issues-%04d.jsonl", i+1), shard)
			comments := []map[string]any{}
			if i < len(commentShards) {
				comments = commentShards[i]
			}
			add(fmt.Sprintf("issues/comments-%04d.jsonl", i+1), comments)
		}
		if len(relations) > 0 {
			add("issues/relations.jsonl", relations)
		}
	})
}

func writeTransferZipFiles(t *testing.T, path string, fill func(add func(name string, body any))) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(f)
	fill(func(name string, body any) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip entry %s: %v", name, err)
		}
		var raw []byte
		switch v := body.(type) {
		case []map[string]any:
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			for _, row := range v {
				if err := enc.Encode(row); err != nil {
					t.Fatal(err)
				}
			}
			raw = buf.Bytes()
		case string:
			raw = []byte(v)
		default:
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			raw = b
		}
		if _, err := w.Write(raw); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	})
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}
}

type transferImportRecorder struct {
	mu       sync.Mutex
	requests []map[string]any
	target   []map[string]any
	// counter is the target's `issue_counter`. It is deliberately settable
	// independently of `target`: deleting the top tasks leaves the counter above
	// MAX(number), and `--renumber` has to offset by the counter.
	counter int32
}

func (r *transferImportRecorder) post(body map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, body)
}

func (r *transferImportRecorder) posted() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]map[string]any, len(r.requests))
	copy(out, r.requests)
	return out
}

func (r *transferImportRecorder) server(t *testing.T, targetPrefix string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.URL.Path == "/api/workspaces":
			writeJSONTest(w, []map[string]any{{"id": "ws-1", "slug": "tgt", "name": "Tgt"}})
		case req.URL.Path == "/api/workspaces/ws-1":
			writeJSONTest(w, map[string]any{
				"id": "ws-1", "slug": "tgt", "name": "Tgt",
				"issue_prefix": targetPrefix, "issue_counter": r.counter,
			})
		case req.URL.Path == "/api/issues":
			writeJSONTest(w, map[string]any{"issues": r.target, "total": len(r.target)})
		case strings.HasSuffix(req.URL.Path, "/transfer/issues"):
			var body map[string]any
			data, _ := io.ReadAll(req.Body)
			if err := json.Unmarshal(data, &body); err != nil {
				t.Errorf("decode transfer/issues body: %v", err)
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			r.post(body)
			writeJSONTest(w, map[string]any{"applied": true})
		case strings.HasSuffix(req.URL.Path, "/transfer/config"):
			writeJSONTest(w, map[string]any{"config_report": map[string]any{"stats": map[string]any{}}})
		default:
			writeJSONTest(w, []any{})
		}
	}))
}

func transferIssuesRequestBody(t *testing.T, body map[string]any, key string) []map[string]any {
	t.Helper()
	raw, _ := body[key].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, row := range raw {
		m, _ := row.(map[string]any)
		out = append(out, m)
	}
	return out
}

func transferRefsIssueIDs(t *testing.T, body map[string]any) []string {
	t.Helper()
	refs, _ := body["refs"].(map[string]any)
	issues, _ := refs["issues"].(map[string]any)
	ids := make([]string, 0, len(issues))
	for id := range issues {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// The target's "no foreign issues" gate recognizes the rows this bundle already
// wrote by looking them up in refs.issues, so every shard has to carry the
// WHOLE package index. A shard carrying only its own ids makes shard 2 read
// shard 1's rows as pre-existing tasks and answer 400.
//
// The finalize request is the second, equally silent failure: it is the only
// request that backfills parent pointers, so it must re-send the package's link
// rows. An empty `comments` array there flattens every thread while the report
// still reads clean.
func TestTransferImportIssues_WholePackageRefsAndFinalizeLinkRows(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	refs := map[string]any{
		"members":          map[string]any{"user-1": map[string]any{"email": "owner@example.com"}},
		"squads":           map[string]any{"sq-1": map[string]any{"name": "Stage Crew"}},
		"issues":           map[string]any{"issue-a": map[string]any{"number": 1, "identifier": "SRC-1"}, "issue-b": map[string]any{"number": 2, "identifier": "SRC-2"}},
		"issue_statuses":   map[string]any{"todo": "todo"},
		"issue_properties": map[string]any{"prop-1": map[string]any{"name": "Deploy target"}},
	}
	issueShards := [][]map[string]any{
		{{"source_id": "issue-a", "number": 1, "title": "root", "status": "todo", "priority": "none", "creator_type": "member", "creator_id": "user-1", "created_at": "2026-09-01T00:00:00Z", "updated_at": "2026-09-01T00:00:00Z"}},
		{{"source_id": "issue-b", "number": 2, "title": "child", "status": "todo", "priority": "none", "creator_type": "member", "creator_id": "user-1", "parent_issue_id": "issue-a", "created_at": "2026-09-01T00:00:01Z", "updated_at": "2026-09-01T00:00:01Z"}},
	}
	commentShards := [][]map[string]any{
		{{"source_id": "c-1", "issue_id": "issue-a", "author_type": "member", "author_id": "user-1", "content": "root comment", "type": "comment", "created_at": "2026-09-01T00:00:02Z", "updated_at": "2026-09-01T00:00:02Z"}},
		{{"source_id": "c-2", "issue_id": "issue-b", "author_type": "member", "author_id": "user-1", "content": "reply", "type": "comment", "parent_id": "c-1", "created_at": "2026-09-01T00:00:03Z", "updated_at": "2026-09-01T00:00:03Z"}},
	}

	rec := &transferImportRecorder{}
	srv := rec.server(t, "TGT")
	defer srv.Close()

	inPath := filepath.Join(t.TempDir(), "bundle.zip")
	writeTransferIssuesZip(t, inPath, refs, issueShards, commentShards, nil)

	cmd := newTransferImportTestCmd()
	_ = cmd.Flags().Set("server-url", srv.URL)
	_ = cmd.Flags().Set("workspace", "tgt")
	_ = cmd.Flags().Set("in", inPath)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader(""))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("import: %v", err)
	}

	posted := rec.posted()
	if len(posted) != 3 {
		t.Fatalf("posted %d /transfer/issues requests, want 2 shards + 1 finalize", len(posted))
	}
	wantRefs := []string{"issue-a", "issue-b"}
	for i := 0; i < 2; i++ {
		got := transferRefsIssueIDs(t, posted[i])
		if strings.Join(got, ",") != strings.Join(wantRefs, ",") {
			t.Fatalf("shard %d refs.issues=%v, want the whole package %v", i+1, got, wantRefs)
		}
	}
	first := transferIssuesRequestBody(t, posted[0], "issues")
	if len(first) != 1 || first[0]["source_id"] != "issue-a" {
		t.Fatalf("shard 1 issues=%v", first)
	}

	fin := posted[2]
	if fin["finalize"] != true {
		t.Fatalf("last request is not a finalize: %v", fin)
	}
	if got := transferRefsIssueIDs(t, fin); strings.Join(got, ",") != strings.Join(wantRefs, ",") {
		t.Fatalf("finalize refs.issues=%v, want the whole package", got)
	}
	finComments := transferIssuesRequestBody(t, fin, "comments")
	if len(finComments) != 2 {
		t.Fatalf("finalize sent %d comment link rows, want both comments — an empty array leaves every parent pointer NULL and flattens the threads", len(finComments))
	}
	parents := map[string]any{}
	for _, row := range finComments {
		parents[row["source_id"].(string)] = row["parent_id"]
	}
	if parents["c-1"] != nil {
		t.Fatalf("root comment link row carries parent_id=%v", parents["c-1"])
	}
	if parents["c-2"] != "c-1" {
		t.Fatalf("reply link row parent_id=%v, want c-1", parents["c-2"])
	}
	for _, row := range finComments {
		if body, _ := row["content"].(string); body != "" {
			t.Fatalf("finalize link row carries a body; the whole point is staying under the request cap: %v", row)
		}
	}
	finIssues := transferIssuesRequestBody(t, fin, "issues")
	var childParent any
	for _, row := range finIssues {
		if row["source_id"] == "issue-b" {
			childParent = row["parent_issue_id"]
		}
	}
	if childParent != "issue-a" {
		t.Fatalf("finalize issue link rows do not carry the child's parent: %v", finIssues)
	}
}

// --renumber offsets every number by the target's `issue_counter`, refuses to
// run without a literal `yes`, and lands the mapping table the prompt points
// at.
//
// DENE-400: the offset is the counter, not MAX(number). The fixture answers
// `GET /api/issues` with 5 and 9 (so MAX(number) = 9) while its workspace serves
// `issue_counter = 12`: a regression to the listing watermark offsets by 9 and
// lands 10/11, which the expectations below reject.
func TestTransferImportIssues_RenumberNeedsYesAndWritesNumberMap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	refs := map[string]any{
		"issues": map[string]any{
			"issue-a": map[string]any{"number": 1, "identifier": "SRC-1"},
			"issue-b": map[string]any{"number": 2, "identifier": "SRC-2"},
		},
	}
	issueShards := [][]map[string]any{{
		{"source_id": "issue-a", "number": 1, "title": "one", "status": "todo", "priority": "none", "creator_type": "member", "creator_id": "user-1", "created_at": "2026-09-01T00:00:00Z", "updated_at": "2026-09-01T00:00:00Z"},
		{"source_id": "issue-b", "number": 2, "title": "two", "status": "todo", "priority": "none", "creator_type": "member", "creator_id": "user-1", "created_at": "2026-09-01T00:00:01Z", "updated_at": "2026-09-01T00:00:01Z"},
	}}
	commentShards := [][]map[string]any{{}}

	rec := &transferImportRecorder{
		target: []map[string]any{
			{"id": "t-5", "number": 5, "title": "existing"},
			{"id": "t-9", "number": 9, "title": "existing"},
		},
		counter: 12,
	}
	srv := rec.server(t, "TGT")
	defer srv.Close()

	inPath := filepath.Join(t.TempDir(), "bundle.zip")
	writeTransferIssuesZip(t, inPath, refs, issueShards, commentShards, nil)

	runImport := func(answer string) error {
		cmd := newTransferImportTestCmd()
		_ = cmd.Flags().Set("server-url", srv.URL)
		_ = cmd.Flags().Set("workspace", "tgt")
		_ = cmd.Flags().Set("in", inPath)
		_ = cmd.Flags().Set("renumber", "true")
		var errBuf bytes.Buffer
		cmd.SetOut(io.Discard)
		cmd.SetErr(&errBuf)
		cmd.SetIn(strings.NewReader(answer))
		err := cmd.Execute()
		if err != nil {
			if !strings.Contains(errBuf.String(), "Type `yes` to continue") {
				t.Fatalf("a refused renumber must say why, stderr=%q", errBuf.String())
			}
		}
		return err
	}

	if err := runImport("no\n"); err == nil {
		t.Fatal("renumber ran without the `yes` confirmation")
	}
	if len(rec.posted()) != 0 {
		t.Fatal("a refused renumber still wrote to the target")
	}

	if err := runImport("yes\n"); err != nil {
		t.Fatalf("import with renumber: %v", err)
	}
	posted := rec.posted()
	if len(posted) != 2 {
		t.Fatalf("posted %d issue requests, want 1 shard + 1 finalize", len(posted))
	}
	// Every request goes through the target's empty-workspace gate, so every
	// request has to carry the flag; the finalize pass included.
	for i, body := range posted {
		if body["renumber"] != true {
			t.Fatalf("request %d did not declare renumber, so a non-empty target would 400 it: %v", i, body)
		}
	}
	rows := transferIssuesRequestBody(t, posted[0], "issues")
	numbers := map[string]float64{}
	for _, row := range rows {
		numbers[row["source_id"].(string)] = row["number"].(float64)
	}
	if numbers["issue-a"] != 13 || numbers["issue-b"] != 14 {
		t.Fatalf("renumbered numbers=%v, want the target's issue_counter 12 added to each (13, 14)", numbers)
	}

	mapPath := inPath + ".number-map.csv"
	raw, err := os.ReadFile(mapPath)
	if err != nil {
		t.Fatalf("number map not written: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Fatalf("number map rows=%v, want a header plus one row per imported issue", lines)
	}
	if !strings.Contains(lines[1], "SRC-1") || !strings.Contains(lines[1], "TGT-13") {
		t.Fatalf("number map row=%q, want SRC-1 -> TGT-13", lines[1])
	}
	if !strings.Contains(lines[2], "SRC-2") || !strings.Contains(lines[2], "TGT-14") {
		t.Fatalf("number map row=%q, want SRC-2 -> TGT-14", lines[2])
	}
}

// The compatibility matrix §9.2: a V2 bundle has no issues directory and a
// bare V1 config JSON has no manifest at all. Both must still import.
func TestTransferImport_ReadsV2BundleAndBareV1JSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	for _, tc := range []struct {
		name string
		path func(t *testing.T) string
	}{
		{"v2-zip-without-issues", func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "v2.zip")
			writeTransferZipFiles(t, path, func(add func(string, any)) {
				add("manifest.json", map[string]any{"format": "multica.workspace-transfer", "schema_version": 1, "bundle_id": "b-1"})
				add("config.json", map[string]any{"format": "multica.workspace-config", "schema_version": 1, "bundle_id": "c-1", "entities": map[string]any{}})
			})
			return path
		}},
		{"bare-v1-json", func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "v1.json")
			body, _ := json.Marshal(map[string]any{"format": "multica.workspace-config", "schema_version": 1, "bundle_id": "c-1", "entities": map[string]any{}})
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &transferImportRecorder{}
			srv := rec.server(t, "TGT")
			defer srv.Close()

			cmd := newTransferImportTestCmd()
			_ = cmd.Flags().Set("server-url", srv.URL)
			_ = cmd.Flags().Set("workspace", "tgt")
			_ = cmd.Flags().Set("in", tc.path(t))
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetIn(strings.NewReader(""))
			if err := cmd.Execute(); err != nil {
				t.Fatalf("import: %v", err)
			}
			if n := len(rec.posted()); n != 0 {
				t.Fatalf("a bundle without an issues group posted %d issue shards", n)
			}
		})
	}
}
