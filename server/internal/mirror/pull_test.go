package mirror

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// pullSetup copies the setup tree out, then turns the pull on against a
// fake local server. The first pull only records the target's position.
func pullSetup(t *testing.T) (*fakeSource, *fakeTarget, *fakeTarget, *Mirror) {
	t.Helper()
	src, target, m := setup(t)
	local := &fakeTarget{gone: map[string]bool{}, ids: "l", gets: map[string]any{
		"/api/agents": []map[string]string{{"id": "agent-new", "name": "孙悟空"}},
		"/api/me":     map[string]string{"id": "kun-new"},
	}}
	srv := httptest.NewServer(local)
	t.Cleanup(srv.Close)
	m.EnablePull(NewHTTPRemote(srv.URL, "tok", "ws-old"), "ws-old")
	target.gets = map[string]any{
		"/api/issues": map[string]any{"issues": []map[string]any{{"id": "old", "updated_at": ts(t0)}}},
		"/api/workspaces/ws-old/members": []map[string]string{
			{"user_id": "allen", "name": "Allen"}, {"user_id": "kun-old", "name": "kunkunya"},
		},
		"/api/agents": []map[string]string{{"id": "agent-old", "name": "孙悟空"}},
	}
	if err := m.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	target.take()
	if m.cursor == nil || !m.cursor.SourceUpdatedAt.Equal(t0) || src.links[kindCursor+cursorID].SourceUpdatedAt != t0 {
		t.Fatalf("first pull must record the target position, cursor=%v", m.cursor)
	}
	if calls := local.take(); len(calls) != 0 {
		t.Fatalf("first pull wrote locally: %+v", calls)
	}
	return src, target, local, m
}

func TestPullBringsBackCommentsIssuesAndStatus(t *testing.T) {
	src, target, local, m := pullSetup(t)
	ctx := context.Background()
	remote := func(kind, id string) string { return src.links[kind+id].RemoteID }
	i1, c1, c2 := remote(KindIssue, "i1"), remote(KindComment, "c1"), remote(KindComment, "c2")
	t1 := t0.Add(2 * time.Minute)

	// On the target: Allen replies on the copy of i1 and moves it to
	// blocked; Kun's own copies and Kun's own note are there too. Allen also
	// opens a new issue for 孙悟空.
	target.gets["/api/issues"] = map[string]any{"issues": []map[string]any{
		{"id": "new1", "status": "todo", "creator_type": "member", "creator_id": "allen", "created_at": ts(t1), "updated_at": ts(t1)},
		{"id": i1, "status": "blocked", "creator_type": "member", "creator_id": "kun-old", "created_at": ts(t0), "updated_at": ts(t1.Add(-time.Second))},
		{"id": "legacy", "status": "todo", "creator_type": "member", "creator_id": "kun-old", "assignee_type": "agent", "assignee_id": "agent-old", "created_at": ts(t0.Add(-time.Hour)), "updated_at": ts(t1)},
		{"id": "old", "updated_at": ts(t0.Add(-2 * time.Minute))},
	}}
	target.gets["/api/issues/"+i1+"/comments"] = []map[string]any{
		{"id": c1, "type": "comment", "author_type": "member", "author_id": "kun-old", "content": "kunkunya：x", "created_at": ts(t0)},
		{"id": c2, "type": "comment", "author_type": "member", "author_id": "kun-old", "content": "Codex：收到", "created_at": ts(t0), "parent_id": c1},
		{"id": "a1", "type": "comment", "author_type": "member", "author_id": "allen", "content": "这里再看下 [@孙悟空](mention://agent/agent-old)", "created_at": ts(t1), "parent_id": c2},
		{"id": "k1", "type": "comment", "author_type": "member", "author_id": "kun-old", "content": "我自己在旧侧写的", "created_at": ts(t1)},
		{"id": "s1", "type": "status_change", "author_type": "member", "author_id": "allen", "content": "blocked", "created_at": ts(t1)},
	}
	target.gets["/api/issues/new1"] = map[string]any{"id": "new1", "title": "帮我看个报表", "description": "急 [@Bob](mention://member/bob)", "status": "todo", "priority": "high", "assignee_type": "agent", "assignee_id": "agent-old", "creator_type": "member", "creator_id": "allen", "created_at": ts(t1), "updated_at": ts(t1)}
	target.gets["/api/issues/new1/comments"] = []map[string]any{
		{"id": "a2", "type": "comment", "author_type": "member", "author_id": "allen", "content": "周五前", "created_at": ts(t1)},
	}

	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if calls := target.take(); len(calls) != 0 {
		t.Fatalf("pull wrote to the target: %+v", calls)
	}
	got := map[string]call{}
	for _, c := range local.take() {
		got[c.Method+" "+c.Path] = c
	}
	if len(got) != 4 {
		t.Fatalf("want comment, status, new issue and its comment; got %+v", got)
	}
	reply := got["POST /api/issues/i1/comments"].Body
	if reply["content"] != "Allen：这里再看下 [@孙悟空](mention://agent/agent-new)" || reply["parent_id"] != "c2" {
		t.Fatalf("pulled comment = %v", reply)
	}
	if got["PUT /api/issues/i1"].Body["status"] != "blocked" {
		t.Fatalf("status not pulled: %+v", got)
	}
	issue := got["POST /api/issues"].Body
	if issue["title"] != "帮我看个报表" || issue["description"] != "急 @Bob" || issue["assignee_type"] != "agent" || issue["assignee_id"] != "agent-new" {
		t.Fatalf("pulled issue = %v", issue)
	}
	var newLocal string
	for _, l := range src.links {
		if l.RemoteID == "new1" {
			newLocal = l.LocalID
		}
	}
	if newLocal == "" || got["POST /api/issues/"+newLocal+"/comments"].Body["content"] != "Allen：周五前" {
		t.Fatalf("new issue's comment not pulled: %+v", got)
	}
	if !m.cursor.SourceUpdatedAt.Equal(t1) {
		t.Fatalf("cursor = %v, want %v", m.cursor.SourceUpdatedAt, t1)
	}

	// Next pass: the pulled objects now exist locally. The pulled comment
	// is never copied back; the pulled issue is written over its original,
	// which hands it to Kun on the target. Re-reading the same window pulls
	// nothing twice.
	var a1Local string
	for _, l := range src.links {
		if l.RemoteID == "a1" {
			a1Local = l.LocalID
			if !l.Inbound {
				t.Fatal("pulled comment must be inbound")
			}
		}
	}
	src.versions[KindComment][a1Local] = t1
	src.comments[a1Local] = Comment{ID: a1Local, IssueID: "i1", Author: "kunkunya", Content: "Allen：这里再看下"}
	src.versions[KindIssue][newLocal] = t1
	src.issues[newLocal] = Issue{ID: newLocal, Title: "帮我看个报表", Status: "in_progress", Priority: "high"}
	is := src.issues["i1"]
	is.Status = "blocked"
	src.issues["i1"] = is
	src.versions[KindIssue]["i1"] = t1
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, c := range target.take() {
		paths = append(paths, c.Method+" "+c.Path)
		if c.Method != http.MethodPut || (c.Path != "/api/issues/new1" && c.Path != "/api/issues/"+i1) {
			t.Fatalf("unexpected target write %+v", c)
		}
		if c.Path == "/api/issues/new1" && (c.Body["assignee_id"] != "kun-old" || c.Body["status"] != "in_progress") {
			t.Fatalf("pulled issue not handed to Kun on the target: %v", c.Body)
		}
	}
	if len(paths) != 2 {
		t.Fatalf("target writes = %v", paths)
	}
	if calls := local.take(); len(calls) != 0 {
		t.Fatalf("second pull duplicated: %+v", calls)
	}

	// The target now shows what the forward pass wrote.
	target.gets["/api/issues"].(map[string]any)["issues"].([]map[string]any)[0]["status"] = "in_progress"

	// Deleting the pulled comment here leaves Allen's original alone and does
	// not pull it again.
	delete(src.versions[KindComment], a1Local)
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	for _, c := range target.take() {
		if c.Method == http.MethodDelete {
			t.Fatalf("retired an inbound original: %+v", c)
		}
	}
	if calls := local.take(); len(calls) != 0 {
		t.Fatalf("deleted pulled comment came back: %+v", calls)
	}
	if _, ok := src.links[KindComment+a1Local]; !ok {
		t.Fatal("inbound link must stay so the original is not pulled again")
	}
}

func TestPullKeepsLocalChangeOverTargetStatus(t *testing.T) {
	src, target, local, m := pullSetup(t)
	ctx := context.Background()
	i1 := src.links[KindIssue+"i1"].RemoteID
	t1 := t0.Add(2 * time.Minute)
	target.gets["/api/issues"] = map[string]any{"issues": []map[string]any{
		{"id": i1, "status": "blocked", "created_at": ts(t0), "updated_at": ts(t1)},
	}}
	target.gets["/api/issues/"+i1+"/comments"] = []map[string]any{}
	// The forward write fails this pass, so the target still shows a status
	// that is older than the local one.
	is := src.issues["i1"]
	is.Status = "done"
	src.issues["i1"] = is
	src.versions[KindIssue]["i1"] = t1
	target.gone["/api/issues/"+i1] = true
	target.gone["/api/issues"] = true
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	for _, c := range local.take() {
		if c.Method == http.MethodPut {
			t.Fatalf("stale target status pulled over a local change: %+v", c)
		}
	}
}

func TestPullRetriesUnreachableLocalNextPass(t *testing.T) {
	src, target, local, m := pullSetup(t)
	ctx := context.Background()
	i1 := src.links[KindIssue+"i1"].RemoteID
	t1 := t0.Add(2 * time.Minute)
	target.gets["/api/issues"] = map[string]any{"issues": []map[string]any{
		{"id": i1, "status": "in_progress", "created_at": ts(t0), "updated_at": ts(t1)},
	}}
	target.gets["/api/issues/"+i1+"/comments"] = []map[string]any{
		{"id": "a1", "type": "comment", "author_type": "member", "author_id": "allen", "content": "在吗", "created_at": ts(t1)},
	}
	local.gone["/api/issues/i1/comments"] = true // 404 is not a refusal
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if !m.cursor.SourceUpdatedAt.Equal(t0) {
		t.Fatalf("cursor moved past a failed pull: %v", m.cursor.SourceUpdatedAt)
	}
	local.take()
	delete(local.gone, "/api/issues/i1/comments")
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	calls := local.take()
	if len(calls) != 1 || !strings.HasPrefix(calls[0].Body["content"].(string), "Allen：") {
		t.Fatalf("retry = %+v", calls)
	}
}
