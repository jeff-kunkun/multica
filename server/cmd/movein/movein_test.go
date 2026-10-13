package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/mirror"
)

// fakeOld is the old server: a fixed workspace, answering GETs only.
type fakeOld struct {
	mu      sync.Mutex
	gets    map[string]any
	writes  []string
	issueIn []map[string]any
}

func (f *fakeOld) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method != http.MethodGet {
		f.writes = append(f.writes, r.Method+" "+r.URL.Path)
		http.Error(w, "read only", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Authorization") != "Bearer tok" {
		http.Error(w, "no token", http.StatusUnauthorized)
		return
	}
	var body any
	switch {
	case r.URL.Path == "/api/issues":
		page := []map[string]any{}
		if r.URL.Query().Get("offset") == "0" {
			page = f.issueIn
		}
		body = map[string]any{"issues": page, "total": len(f.issueIn)}
	default:
		v, ok := f.gets[r.URL.Path]
		if !ok {
			v = []any{}
		}
		body = v
	}
	_ = json.NewEncoder(w).Encode(body)
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Skipf("database: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Skipf("database not reachable: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

const testSlug = "movein-test"

// dropWorkspace removes everything a move into the test workspace wrote.
func dropWorkspace(ctx context.Context, pool *pgxpool.Pool, target string) {
	var ws string
	if pool.QueryRow(ctx, `SELECT id::text FROM workspace WHERE slug = $1`, testSlug).Scan(&ws) != nil {
		return
	}
	for _, q := range []string{
		`DELETE FROM chat_message WHERE chat_session_id IN (SELECT id FROM chat_session WHERE workspace_id = $1)`,
		`DELETE FROM chat_session WHERE workspace_id = $1`,
		`UPDATE comment SET parent_id = NULL WHERE workspace_id = $1`,
		`DELETE FROM comment WHERE workspace_id = $1`,
		`UPDATE issue SET parent_issue_id = NULL WHERE workspace_id = $1`,
		`DELETE FROM issue WHERE workspace_id = $1`,
		`DELETE FROM project_resource WHERE workspace_id = $1`,
		`DELETE FROM project WHERE workspace_id = $1`,
		`DELETE FROM agent WHERE workspace_id = $1`,
		`DELETE FROM agent_runtime WHERE workspace_id = $1`,
		`DELETE FROM member WHERE workspace_id = $1`,
		`DELETE FROM workspace WHERE id = $1`,
	} {
		_, _ = pool.Exec(ctx, q, ws)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM mirror_link WHERE target = $1`, target)
	_, _ = pool.Exec(ctx, `DELETE FROM "user" WHERE email = 'movein-test@local.test'`)
}

func TestMoveInIsCompleteQuietAndIdempotent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	old := &fakeOld{}
	srv := httptest.NewServer(old)
	t.Cleanup(srv.Close)
	target := mirror.TargetKey(srv.URL, "ws-old")
	dropWorkspace(ctx, pool, target)
	t.Cleanup(func() { dropWorkspace(ctx, pool, target) })

	// The office: Kun, a Claude and a Codex member, and one issue already
	// holding number 2.
	var userID, wsID string
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("setup %q: %v", q, err)
		}
	}
	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ('Kun', 'movein-test@local.test') RETURNING id::text`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO workspace (name, slug, issue_prefix, issue_counter) VALUES ('Office', $1, 'DENE', 2) RETURNING id::text`, testSlug).Scan(&wsID); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`, wsID, userID)
	var codexAgent string
	for _, provider := range []string{"claude", "codex"} {
		var rt, agent string
		if err := pool.QueryRow(ctx, `
			INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, device_info, metadata, visibility, owner_id)
			VALUES ($1, $2, 'cloud', $2, 'online', '', '{}', 'private', $3) RETURNING id::text`, wsID, provider, userID).Scan(&rt); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `
			INSERT INTO agent (workspace_id, name, description, runtime_mode, runtime_config, visibility, permission_mode,
			                   max_concurrent_tasks, owner_id, runtime_id)
			VALUES ($1, $2, '', 'cloud', '{}', 'private', 'private', 1, $3, $4) RETURNING id::text`,
			wsID, provider+" member", userID, rt).Scan(&agent); err != nil {
			t.Fatal(err)
		}
		codexAgent = agent
	}
	exec(`INSERT INTO issue (workspace_id, number, title, status, priority, creator_type, creator_id, position)
	      VALUES ($1, 2, 'already here', 'todo', 'none', 'member', $2, 0)`, wsID, userID)

	// The old workspace: one project, a parent issue listed after its child,
	// a cancelled issue, a reply older than the comment it answers, and a
	// chat whose last answer is stamped ahead of now.
	t0 := "2026-01-01T00:00:00Z"
	later := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	old.gets = map[string]any{
		"/api/workspaces": []map[string]any{{"id": "ws-old", "slug": "kunkunya"}},
		"/api/me":         map[string]any{"id": "kun-old"},
		"/api/workspaces/ws-old/members": []map[string]any{
			{"user_id": "kun-old", "name": "kunkunya"}, {"user_id": "allen", "name": "Allen"},
		},
		"/api/agents":   []map[string]any{{"id": "agent-old", "name": "孙悟空", "runtime_id": "rt-old"}},
		"/api/runtimes": []map[string]any{{"id": "rt-old", "provider": "codex"}},
		"/api/projects": map[string]any{"projects": []map[string]any{{
			"id": "p1", "title": "Office", "description": "see [DENE-1](mention://issue/i1)",
			"status": "in_progress", "priority": "high", "created_at": t0, "updated_at": t0,
		}}},
		"/api/projects/p1/resources": map[string]any{"resources": []map[string]any{{
			"id": "r1", "resource_type": "github_repo", "resource_ref": map[string]any{"url": "https://example.com/r"},
			"position": 0, "created_at": t0,
		}}},
		"/api/issues/i1/comments": []map[string]any{
			{"id": "c2", "type": "comment", "author_type": "agent", "author_id": "agent-old", "content": "收到", "parent_id": "c1", "created_at": "2026-01-01T00:00:01Z"},
			{"id": "c1", "type": "comment", "author_type": "member", "author_id": "allen", "content": "ping [@孙悟空](mention://agent/agent-old)", "created_at": "2026-01-01T00:00:02Z"},
			{"id": "c3", "type": "comment", "author_type": "member", "author_id": "kun-old", "content": "mine", "created_at": "2026-01-01T00:00:03Z"},
			{"id": "c4", "type": "comment", "author_type": "member", "author_id": "kun-old", "content": "gone", "created_at": "2026-01-01T00:00:04Z", "deleted_at": t0},
		},
		"/api/chat/sessions": map[string]any{"sessions": []map[string]any{{
			"id": "s1", "agent_id": "agent-old", "title": "hi", "status": "active", "project_id": "p1", "created_at": t0, "updated_at": t0,
		}}},
		"/api/chat/sessions/s1/messages": []map[string]any{
			{"id": "m1", "role": "user", "content": "hello", "created_at": t0},
			{"id": "m2", "role": "assistant", "content": "hi", "created_at": later},
		},
	}
	old.issueIn = []map[string]any{
		{"id": "i2", "number": 2, "identifier": "DENE-2", "title": "child", "status": "in_progress", "priority": "low",
			"assignee_type": "agent", "assignee_id": "agent-old", "parent_issue_id": "i1", "project_id": "p1", "created_at": t0, "updated_at": t0},
		{"id": "i1", "number": 1, "identifier": "DENE-1", "title": "parent", "status": "todo", "priority": "urgent",
			"description": "child is [DENE-2](mention://issue/i2)", "project_id": "p1", "created_at": t0, "updated_at": t0},
		{"id": "i3", "number": 5, "identifier": "DENE-5", "title": "dropped", "status": "cancelled", "priority": "none",
			"assignee_type": "member", "assignee_id": "allen", "created_at": t0, "updated_at": t0},
	}

	move := func() stats {
		t.Helper()
		m := &mover{old: newOldServer(srv.URL, "tok"), pool: pool, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
		if err := m.prepare(ctx, srv.URL, "kunkunya", testSlug, ""); err != nil {
			t.Fatal(err)
		}
		if err := m.run(ctx); err != nil {
			t.Fatal(err)
		}
		return m.stats
	}
	first := move()
	if first.ProjectsNew != 1 || first.Resources != 1 || first.IssuesNew != 3 || first.OldIssueTotal != 3 ||
		first.Comments != 3 || first.ChatsNew != 1 || first.Messages != 2 {
		t.Fatalf("first move = %+v", first)
	}

	ids := &mover{space: spaceFor(wsID, target)}
	issue := func(oldID string) (number int, status, description string, assignee, parent, project *string) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT number, status, COALESCE(description, ''), assignee_id::text, parent_issue_id::text, project_id::text
			FROM issue WHERE id = $1`, ids.id(mirror.KindIssue, oldID)).Scan(&number, &status, &description, &assignee, &parent, &project); err != nil {
			t.Fatalf("issue %s: %v", oldID, err)
		}
		return
	}
	n1, s1, d1, a1, p1, proj1 := issue("i1")
	n2, s2, d2, a2, p2, _ := issue("i2")
	n3, s3, d3, _, _, _ := issue("i3")
	if n1 != 1 || n3 != 5 || n2 != 6 {
		t.Fatalf("numbers = %d %d %d; want 1 and 5 kept, 2 taken so 6", n1, n2, n3)
	}
	if s1 != "done" || s2 != "done" || s3 != "cancelled" {
		t.Fatalf("statuses = %s %s %s", s1, s2, s3)
	}
	if a1 != nil || a2 != nil || p1 != nil || p2 == nil || *p2 != ids.id(mirror.KindIssue, "i1") ||
		proj1 == nil || *proj1 != ids.id(mirror.KindProject, "p1") {
		t.Fatalf("links wrong: assignees %v %v parent %v project %v", a1, a2, p2, proj1)
	}
	if !strings.Contains(d1, "(mention://issue/"+ids.id(mirror.KindIssue, "i2")+")") ||
		!strings.HasSuffix(d1, "搬家记录：原编号 DENE-1 · 原执行人 无 · 原状态 待办") ||
		!strings.HasSuffix(d2, "原编号 DENE-2 · 原执行人 孙悟空 · 原状态 进行中") ||
		!strings.HasSuffix(d3, "原执行人 Allen · 原状态 已取消") {
		t.Fatalf("descriptions:\n%s\n%s\n%s", d1, d2, d3)
	}

	rows, err := pool.Query(ctx, `SELECT content, COALESCE(parent_id::text, '') FROM comment WHERE issue_id = $1 ORDER BY created_at`,
		ids.id(mirror.KindIssue, "i1"))
	if err != nil {
		t.Fatal(err)
	}
	var comments []string
	for rows.Next() {
		var content, parent string
		_ = rows.Scan(&content, &parent)
		comments = append(comments, content+"|"+parent)
	}
	rows.Close()
	want := []string{
		"孙悟空：收到|" + ids.id(mirror.KindComment, "c1"),
		"Allen：ping @孙悟空|",
		"mine|",
	}
	if strings.Join(comments, "\n") != strings.Join(want, "\n") {
		t.Fatalf("comments:\n%s\nwant:\n%s", strings.Join(comments, "\n"), strings.Join(want, "\n"))
	}

	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	if got := count(`SELECT count(*) FROM project WHERE workspace_id = $1 AND status = 'in_progress' AND description LIKE 'see [DENE-1](mention://issue/%'`, wsID); got != 1 {
		t.Fatalf("project not moved as it was")
	}
	var chatAgent string
	if err := pool.QueryRow(ctx, `SELECT agent_id::text FROM chat_session WHERE workspace_id = $1`, wsID).Scan(&chatAgent); err != nil || chatAgent != codexAgent {
		t.Fatalf("chat went to %s, want the Codex member %s (%v)", chatAgent, codexAgent, err)
	}
	if got := count(`SELECT count(*) FROM chat_message m JOIN chat_session s ON s.id = m.chat_session_id
		WHERE s.workspace_id = $1 AND m.role = 'assistant' AND m.created_at > s.last_read_at`, wsID); got != 0 {
		t.Fatalf("%d unread messages", got)
	}
	if got := count(`SELECT count(*) FROM chat_session WHERE workspace_id = $1 AND unread_since IS NOT NULL`, wsID); got != 0 {
		t.Fatalf("%d chats flagged unread", got)
	}
	if got := count(`SELECT count(*) FROM inbox_item WHERE workspace_id = $1`, wsID); got != 0 {
		t.Fatalf("%d inbox items", got)
	}
	if got := count(`SELECT count(*) FROM agent_task_queue t JOIN agent a ON a.id = t.agent_id WHERE a.workspace_id = $1`, wsID); got != 0 {
		t.Fatalf("%d runs queued", got)
	}
	if got := count(`SELECT issue_counter FROM workspace WHERE id = $1`, wsID); got != 6 {
		t.Fatalf("issue_counter = %d, want 6", got)
	}
	links := count(`SELECT count(*) FROM mirror_link WHERE target = $1`, target)
	if links != 1+3+3 || count(`SELECT count(*) FROM mirror_link WHERE target = $1 AND NOT inbound`, target) != 0 {
		t.Fatalf("mirror links = %d, want 7 all inbound", links)
	}
	if len(old.writes) != 0 {
		t.Fatalf("wrote to the old server: %v", old.writes)
	}

	// Again: everything is found and nothing changes.
	snapshot := func() string {
		var s string
		if err := pool.QueryRow(ctx, `SELECT concat_ws('/',
			(SELECT count(*) FROM issue WHERE workspace_id = $1), (SELECT count(*) FROM comment WHERE workspace_id = $1),
			(SELECT count(*) FROM project WHERE workspace_id = $1), (SELECT count(*) FROM project_resource WHERE workspace_id = $1),
			(SELECT count(*) FROM chat_session WHERE workspace_id = $1),
			(SELECT count(*) FROM chat_message m JOIN chat_session c ON c.id = m.chat_session_id WHERE c.workspace_id = $1),
			(SELECT max(updated_at) FROM issue WHERE workspace_id = $1), (SELECT issue_counter FROM workspace WHERE id = $1),
			(SELECT count(*) FROM mirror_link WHERE target = $2))`, wsID, target).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := snapshot()
	second := move()
	if second.ProjectsNew != 0 || second.IssuesNew != 0 || second.ChatsNew != 0 || second.Issues != 3 || second.Chats != 1 {
		t.Fatalf("second move = %+v", second)
	}
	if after := snapshot(); after != before {
		t.Fatalf("second move changed the office: %s -> %s", before, after)
	}
}

func TestParentsFirstPlacesRepliesAfterTheirParent(t *testing.T) {
	p := func(s string) *string { return &s }
	got := parentsFirst([]oldComment{
		{ID: "b", ParentID: p("a")}, {ID: "a"}, {ID: "x", ParentID: p("missing")},
		{ID: "l1", ParentID: p("l2")}, {ID: "l2", ParentID: p("l1")},
	})
	var order []string
	for _, c := range got {
		order = append(order, c.ID)
	}
	if strings.Join(order, ",") != "a,x,b,l1,l2" {
		t.Fatalf("order = %v", order)
	}
}
