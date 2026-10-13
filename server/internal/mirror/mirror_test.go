package mirror

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeSource struct {
	versions map[string]map[string]time.Time
	projects map[string]Project
	issues   map[string]Issue
	comments map[string]Comment
	links    map[string]Link
}

func newFakeSource() *fakeSource {
	return &fakeSource{
		versions: map[string]map[string]time.Time{KindProject: {}, KindIssue: {}, KindComment: {}},
		projects: map[string]Project{},
		issues:   map[string]Issue{},
		comments: map[string]Comment{},
		links:    map[string]Link{},
	}
}

var t0 = time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)

func (f *fakeSource) Versions(context.Context) (map[string]map[string]time.Time, error) {
	out := map[string]map[string]time.Time{}
	for k, m := range f.versions {
		out[k] = map[string]time.Time{}
		for id, at := range m {
			out[k][id] = at
		}
	}
	return out, nil
}
func (f *fakeSource) Project(_ context.Context, id string) (Project, error) {
	return f.projects[id], nil
}
func (f *fakeSource) Issue(_ context.Context, id string) (Issue, error) { return f.issues[id], nil }
func (f *fakeSource) Comment(_ context.Context, id string) (Comment, error) {
	return f.comments[id], nil
}
func (f *fakeSource) Links(context.Context) ([]Link, error) {
	var out []Link
	for _, l := range f.links {
		out = append(out, l)
	}
	return out, nil
}
func (f *fakeSource) SaveLink(_ context.Context, l Link) error {
	f.links[l.Kind+l.LocalID] = l
	return nil
}
func (f *fakeSource) DeleteLink(_ context.Context, kind, id string) error {
	delete(f.links, kind+id)
	return nil
}

type call struct {
	Method, Path string
	Body         map[string]any
}

// fakeTarget is the old server's API: it records every write and hands out
// ids. Paths listed in gone answer 404.
type fakeTarget struct {
	mu    sync.Mutex
	calls []call
	n     int
	gone  map[string]bool
}

func (t *fakeTarget) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("X-Workspace-ID") != "ws-old" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var body map[string]any
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	t.calls = append(t.calls, call{r.Method, r.URL.Path, body})
	if t.gone[r.URL.Path] {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Method == http.MethodPost {
		t.n++
		_ = json.NewEncoder(w).Encode(map[string]string{"id": fmt.Sprintf("r%d", t.n)})
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{}"))
}

func (t *fakeTarget) take() []call {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.calls
	t.calls = nil
	return c
}

func setup(t *testing.T) (*fakeSource, *fakeTarget, *Mirror) {
	t.Helper()
	src := newFakeSource()
	target := &fakeTarget{gone: map[string]bool{}}
	srv := httptest.NewServer(target)
	t.Cleanup(srv.Close)
	m := New(src, NewHTTPRemote(srv.URL, "tok", "ws-old"), "kun-old", time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))

	src.projects["p1"] = Project{ID: "p1", Title: "魔改", Status: "in_progress", Priority: "high"}
	src.versions[KindProject]["p1"] = t0
	// The child is listed before its parent's copy exists; ensure must still
	// create the parent first.
	src.issues["i2"] = Issue{ID: "i2", Identifier: "DENE-2", Title: "子票", Status: "todo", Priority: "none", ParentID: "i1", Assignee: "孙悟空"}
	src.issues["i1"] = Issue{ID: "i1", Identifier: "DENE-1", Title: "父票", Description: "看 [@孙悟饭](mention://agent/abc)", Status: "in_progress", Priority: "high", ProjectID: "p1"}
	src.versions[KindIssue]["i1"] = t0
	src.versions[KindIssue]["i2"] = t0
	src.comments["c1"] = Comment{ID: "c1", IssueID: "i1", Author: "kunkunya", Content: "交给 [@孙悟空](mention://agent/x) 看 [DENE-2](mention://issue/i2)", CreatedAt: t0}
	src.comments["c2"] = Comment{ID: "c2", IssueID: "i1", ParentID: "c1", Author: "孙悟空", Content: "收到", CreatedAt: t0}
	src.versions[KindComment]["c1"] = t0
	src.versions[KindComment]["c2"] = t0
	return src, target, m
}

func TestSyncCopiesTreeOnce(t *testing.T) {
	src, target, m := setup(t)
	ctx := context.Background()
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	calls := target.take()
	if len(calls) != 7 {
		t.Fatalf("want 5 creates and 2 issue updates, got %d: %+v", len(calls), calls)
	}
	remote := map[string]string{}
	for _, l := range src.links {
		remote[l.LocalID] = l.RemoteID
	}
	byPath := map[string][]call{}
	for i, c := range calls {
		if c.Method == http.MethodPut {
			// Each issue is created in backlog and set right after.
			prev := calls[i-1]
			if prev.Method != http.MethodPost || prev.Path != "/api/issues" || prev.Body["status"] != "backlog" {
				t.Fatalf("update not right after a backlog create: %+v then %+v", prev, c)
			}
			if prev.Body["allow_duplicate"] != true {
				t.Fatalf("issue create must allow duplicates: %v", prev.Body)
			}
		}
		byPath[c.Method+" "+c.Path] = append(byPath[c.Method+" "+c.Path], c)
	}
	var parent, child map[string]any
	for _, c := range calls {
		if c.Method != http.MethodPut {
			continue
		}
		switch c.Path {
		case "/api/issues/" + remote["i1"]:
			parent = c.Body
		case "/api/issues/" + remote["i2"]:
			child = c.Body
		}
	}
	if child["parent_issue_id"] != remote["i1"] || parent["project_id"] != remote["p1"] {
		t.Fatalf("relations not mapped: parent=%v child=%v remote=%v", parent, child, remote)
	}
	if parent["parent_issue_id"] != nil || child["project_id"] != nil {
		t.Fatalf("empty relations must be explicit null: parent=%v child=%v", parent, child)
	}
	for _, b := range []map[string]any{parent, child} {
		if b["assignee_type"] != "member" || b["assignee_id"] != "kun-old" || b["reviewer_type"] != "none" || b["reviewer_id"] != nil {
			t.Fatalf("copy must be held by the target member with no reviewer: %v", b)
		}
		if _, ok := b["reviewer_id"]; !ok {
			t.Fatalf("reviewer_id must be sent as null: %v", b)
		}
	}
	if parent["status"] != "in_progress" || child["status"] != "todo" {
		t.Fatalf("real status not set: parent=%v child=%v", parent["status"], child["status"])
	}
	if d := child["description"]; d != "" {
		t.Fatalf("child description = %q", d)
	}
	if d := parent["description"]; d != "看 @孙悟饭" {
		t.Fatalf("parent description = %q", d)
	}
	comments := byPath["POST /api/issues/"+remote["i1"]+"/comments"]
	if len(comments) != 2 {
		t.Fatalf("want 2 comments on the parent copy, got %+v", byPath)
	}
	for _, c := range comments {
		content := c.Body["content"].(string)
		if strings.Contains(content, "mention://") {
			t.Fatalf("mention leaked: %q", content)
		}
		if strings.Contains(content, "收到") {
			if c.Body["parent_id"] != remote["c1"] {
				t.Fatalf("reply not threaded: %v", c.Body)
			}
		} else if content != "**kunkunya**：交给 @孙悟空 看 DENE-2" {
			t.Fatalf("comment content = %q", content)
		}
	}

	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if calls := target.take(); len(calls) != 0 {
		t.Fatalf("unchanged pass wrote %+v", calls)
	}

	// A restart reads the links back and still writes nothing.
	m2 := New(src, m.remote, m.assignee, time.Second, m.log)
	if err := m2.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if calls := target.take(); len(calls) != 0 {
		t.Fatalf("restart pass wrote %+v", calls)
	}
}

func TestSyncUpdatesAndRetires(t *testing.T) {
	src, target, m := setup(t)
	ctx := context.Background()
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	target.take()
	remote := func(kind, id string) string { return src.links[kind+id].RemoteID }

	is := src.issues["i2"]
	is.Status = "done"
	src.issues["i2"] = is
	src.versions[KindIssue]["i2"] = t0.Add(time.Minute)
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	calls := target.take()
	if len(calls) != 1 || calls[0].Method != http.MethodPut || calls[0].Path != "/api/issues/"+remote(KindIssue, "i2") || calls[0].Body["status"] != "done" {
		t.Fatalf("status change: %+v", calls)
	}

	// The parent issue is deleted locally, taking its comments with it.
	issueRemote, c1, c2 := remote(KindIssue, "i1"), remote(KindComment, "c1"), remote(KindComment, "c2")
	delete(src.versions[KindIssue], "i1")
	delete(src.versions[KindComment], "c1")
	delete(src.versions[KindComment], "c2")
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range target.take() {
		got[c.Method+" "+c.Path+fmt.Sprint(c.Body["status"])] = true
	}
	want := []string{
		"DELETE /api/comments/" + c1 + "<nil>",
		"DELETE /api/comments/" + c2 + "<nil>",
		"PUT /api/issues/" + issueRemote + "cancelled",
	}
	for _, w := range want {
		if !got[w] {
			t.Fatalf("missing %q in %v", w, got)
		}
	}
	if len(src.links) != 2 {
		t.Fatalf("retired links not dropped: %v", src.links)
	}
}

func TestSyncRecreatesRemoteCopyThatIsGone(t *testing.T) {
	src, target, m := setup(t)
	ctx := context.Background()
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	target.take()
	old := src.links[KindProject+"p1"].RemoteID
	target.gone["/api/projects/"+old] = true
	src.versions[KindProject]["p1"] = t0.Add(time.Minute)
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	calls := target.take()
	if len(calls) != 2 || calls[1].Method != http.MethodPost || calls[1].Path != "/api/projects" {
		t.Fatalf("want PUT then POST, got %+v", calls)
	}
	if src.links[KindProject+"p1"].RemoteID == old {
		t.Fatal("link still points at the gone copy")
	}
}

func TestSyncRetriesFailedWriteNextPass(t *testing.T) {
	src, target, m := setup(t)
	ctx := context.Background()
	target.gone["/api/projects"] = true // creates fail
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := src.links[KindProject+"p1"]; ok {
		t.Fatal("failed create must not be recorded")
	}
	if _, ok := src.links[KindIssue+"i1"]; ok {
		t.Fatal("issue in an uncopied project must wait for it")
	}
	if _, ok := src.links[KindIssue+"i2"]; ok {
		t.Fatal("child of an uncopied issue must wait for it")
	}
	target.take()
	delete(target.gone, "/api/projects")
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if len(src.links) != 5 {
		t.Fatalf("second pass should copy everything, links=%v", src.links)
	}
}

func TestConfigFromEnv(t *testing.T) {
	for _, k := range []string{"MULTICA_MIRROR_WORKSPACE", "MULTICA_MIRROR_TARGET_URL", "MULTICA_MIRROR_TARGET_TOKEN", "MULTICA_MIRROR_TARGET_WORKSPACE", "MULTICA_MIRROR_TARGET_ASSIGNEE"} {
		t.Setenv(k, "")
	}
	if _, ok := ConfigFromEnv(); ok {
		t.Fatal("empty env must leave the mirror off")
	}
	t.Setenv("MULTICA_MIRROR_WORKSPACE", "kun")
	t.Setenv("MULTICA_MIRROR_TARGET_URL", "https://ai.example/")
	t.Setenv("MULTICA_MIRROR_TARGET_TOKEN", "mul_x")
	if _, ok := ConfigFromEnv(); ok {
		t.Fatal("missing target workspace must leave the mirror off")
	}
	t.Setenv("MULTICA_MIRROR_TARGET_WORKSPACE", "ws")
	if _, ok := ConfigFromEnv(); ok {
		t.Fatal("missing target assignee must leave the mirror off")
	}
	t.Setenv("MULTICA_MIRROR_TARGET_ASSIGNEE", "kun-old")
	c, ok := ConfigFromEnv()
	if !ok || c.TargetAssignee != "kun-old" || c.TargetURL != "https://ai.example" || c.Interval != defaultInterval {
		t.Fatalf("config = %+v ok=%v", c, ok)
	}
}
