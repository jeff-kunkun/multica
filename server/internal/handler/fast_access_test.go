package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func fastCall(t *testing.T, req fastRequest) (fastResponse, *httptest.ResponseRecorder) {
	t.Helper()
	r := withChatTestWorkspaceCtx(t, newRequest("POST", "/api/fast-access/v1/read", req))
	w := httptest.NewRecorder()
	testHandler.ReadFastAccess(w, r)
	var resp fastResponse
	if w.Code == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
	}
	return resp, w
}
func resetFastQuota() {
	store := testHandler.fastStore()
	store.Lock()
	store.Quotas = map[string]fastQuota{}
	store.Unlock()
}
func TestFastAccessSnapshot(t *testing.T) {
	// Two workspaces this test owns: the shared "Handler Tests" workspace also
	// holds active runs left by earlier tests, which would change the exact
	// active_runs count asserted below.
	ws1 := dbfx.Workspace(t, "Handler fixture", "fast-"+strings.ToLower(randomID()), testutil.Cols{"issue_prefix": "FA"})
	dbfx.Member(t, ws1, testUserID, "owner")
	fx := testutil.New(testPool, ws1, testUserID)
	runtime := fx.Runtime(t, "fast-runtime")
	agent := fx.Agent(t, "fast-agent", runtime)
	ws2 := dbfx.Workspace(t, "Production fixture", "fast-"+strings.ToLower(randomID()), testutil.Cols{"issue_prefix": "FX"})
	dbfx.Member(t, ws2, testUserID, "owner")
	runtime2 := dbfx.Runtime(t, "fast-second", testutil.Cols{"workspace_id": ws2})
	agent2 := dbfx.Agent(t, "fast-second-agent", runtime2, testutil.Cols{"workspace_id": ws2})
	chat := fx.ChatSession(t, agent, testutil.Cols{"title": strings.Repeat("中文🙂", 50), "explicitly_created_at": testutil.Raw("now()"), "last_read_at": testutil.Raw("now()-interval '1 hour'")})
	fx.Insert(t, "chat_message", testutil.Cols{"chat_session_id": chat, "role": "assistant", "content": strings.Repeat("🙂界<", 90)})
	old := fx.Issue(t, "old root issue", testutil.Cols{"created_at": testutil.Raw("now()-interval '2 hours'"), "updated_at": testutil.Raw("now()-interval '2 hours'")})
	root := fx.Comment(t, old, "old root", testutil.Cols{"created_at": testutil.Raw("now()-interval '2 hours'"), "updated_at": testutil.Raw("now()-interval '2 hours'")})
	fx.Comment(t, old, "new reply", testutil.Cols{"parent_id": root})
	for i := 0; i < 17; i++ {
		a, rt := agent, runtime
		if i >= 9 {
			a, rt = agent2, runtime2
		}
		cols := testutil.Cols{"runtime_id": rt, "status": "running"}
		if i == 0 {
			cols["chat_session_id"] = chat
		}
		if i == 1 {
			cols["status"] = "waiting_local_directory"
		}
		if i == 2 {
			cols["status"] = "deferred"
			cols["context"] = []byte(`{"wakeup_id":"11111111-1111-1111-1111-111111111111"}`)
		}
		fx.Task(t, a, cols)
	}
	stranger := dbfx.User(t, "other", "fast-private-"+randomID()+"@example.test")
	dbfx.Member(t, ws1, stranger, "member")
	hidden := fx.Issue(t, "HIDDEN-ISSUE", testutil.Cols{"creator_id": stranger, "visibility": "private"})
	fx.Task(t, agent, testutil.Cols{"runtime_id": runtime, "status": "running", "issue_id": hidden})
	privateChat := fx.ChatSession(t, agent, testutil.Cols{"creator_id": stranger, "visibility": "private", "title": "HIDDEN-CHAT", "explicitly_created_at": testutil.Raw("now()")})
	fx.Task(t, agent, testutil.Cols{"runtime_id": runtime, "status": "running", "chat_session_id": privateChat})
	since := time.Now().Add(-30 * time.Minute)
	req := fastRequest{Scope: &fastScope{Mode: "workspace_ids", WorkspaceIDs: []string{ws1, ws2}}, Limit: 200}
	resetFastQuota()
	resp, w := fastCall(t, req)
	if w.Code != 200 {
		t.Fatalf("read %d: %s", w.Code, w.Body.String())
	}
	if !resp.Complete || !resp.ActiveRunsComplete || len(resp.ActiveRuns) != 17 || len(w.Body.Bytes()) > 50000 || resp.Checkpoint == nil {
		t.Fatalf("invalid envelope: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), hidden) || strings.Contains(w.Body.String(), privateChat) || strings.Contains(w.Body.String(), "HIDDEN") {
		t.Fatal("private data leaked")
	}
	foundChat, foundIssue := false, false
	for _, item := range resp.Changes {
		if item.ID == chat {
			foundChat = true
			if item.UnreadCount == nil || *item.UnreadCount != 1 || !item.ContentTruncated || !utf8.ValidString(item.Preview) || len(item.Preview) > 480 {
				t.Fatalf("chat projection %#v", item)
			}
		}
		if item.ID == old {
			foundIssue = true
			if item.NewCommentCount == nil || *item.NewCommentCount != 2 {
				t.Fatalf("old root new reply missed: count=%v item=%#v", *item.NewCommentCount, item)
			}
		}
	}
	if !foundChat || !foundIssue {
		t.Fatalf("missing chat or old issue")
	}
	var unread time.Time
	if err := testPool.QueryRow(context.Background(), "SELECT last_read_at FROM chat_session WHERE id=$1", chat).Scan(&unread); err != nil {
		t.Fatal(err)
	}
	if time.Since(unread) < 50*time.Minute {
		t.Fatal("read cursor advanced")
	}
	roots, err := testHandler.Queries.ListRootCommentsSinceForIssue(context.Background(), db.ListRootCommentsSinceForIssueParams{IssueID: parseUUID(old), WorkspaceID: parseUUID(ws1), Since: pgtype.Timestamptz{Time: since, Valid: true}, RowLimit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || uuidToString(roots[0].ID) != root {
		t.Fatalf("old root missing: %+v", roots)
	}
	// Stable page replay, byte cap, and checkpoint only at end.
	req.Limit = 5
	req.MaxBytes = 3000
	resetFastQuota()
	page, w := fastCall(t, req)
	if w.Code != 200 || page.Complete || page.NextCursor == nil || page.Checkpoint != nil || page.ActiveRunsComplete {
		t.Fatalf("first page: %d %s", w.Code, w.Body.String())
	}
	frozen := page.AsOf
	seen := map[string]bool{}
	for _, v := range append(page.ActiveRuns, page.Changes...) {
		seen[fastKey(v)] = true
	}
	pages := 1
	fx.Task(t, agent, testutil.Cols{"runtime_id": runtime, "status": "running"}) // must stay outside frozen batch
	for page.NextCursor != nil {
		resetFastQuota()
		page, w = fastCall(t, fastRequest{Cursor: *page.NextCursor, Limit: 5, MaxBytes: 3000})
		if w.Code != 200 {
			t.Fatalf("continuation: %d %s", w.Code, w.Body.String())
		}
		if len(w.Body.Bytes()) > 3000 || !page.AsOf.Equal(frozen) {
			t.Fatal("unstable batch")
		}
		for _, v := range append(page.ActiveRuns, page.Changes...) {
			if seen[fastKey(v)] {
				t.Fatal("duplicate row")
			}
			seen[fastKey(v)] = true
		}
		pages++
	}
	if len(seen) != len(resp.Changes)+len(resp.ActiveRuns) || !page.Complete || page.Checkpoint == nil {
		t.Fatalf("lost items: %d", len(seen))
	}
	// Benchmark the actual database + projection + serialization path, with the
	// quota reset between samples so this measures reads rather than 429 latency.
	durations := []time.Duration{}
	req.Limit = 200
	req.MaxBytes = 50000
	for i := 0; i < 30; i++ {
		resetFastQuota()
		start := time.Now()
		sample, rw := fastCall(t, req)
		durations = append(durations, time.Since(start))
		if rw.Code != 200 || !sample.Complete || len(rw.Body.Bytes()) > 50000 {
			t.Fatalf("sample %d failed %d", i, rw.Code)
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	t.Logf("two-workspace fixture: initial 17 active runs, %d changes, %d bytes; overflow %d pages; 30 samples p50=%s p95=%s (local, not production)", len(resp.Changes), len(mustJSON(resp)), pages, durations[14], durations[28])
}
func TestFastAccessRevocationAndOriginator(t *testing.T) {
	f := newChatDirectoryFixture(t)
	resetFastQuota()
	body := `{"include":["chats"],"limit":1}`
	r := f.taskRequest(t, "/api/fast-access/v1/read")
	r.Method = "POST"
	var callingAgent string
	if err := testPool.QueryRow(context.Background(), "SELECT agent_id::text FROM agent_task_queue WHERE id=$1", f.task).Scan(&callingAgent); err != nil {
		t.Fatal(err)
	}
	r.Header.Set("X-Agent-ID", callingAgent)
	r.Body = ioBody(body)
	w := httptest.NewRecorder()
	testHandler.ReadFastAccess(w, r)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var page fastResponse
	json.Unmarshal(w.Body.Bytes(), &page)
	if strings.Contains(w.Body.String(), f.ownerPrivate) || strings.Contains(w.Body.String(), f.otherPrivate) {
		t.Fatal("runtime owner permissions leaked")
	}
	if page.NextCursor == nil {
		t.Fatal("need paged fixture")
	}
	dbfx.Exec(t, "UPDATE chat_session SET visibility='private' WHERE id=$1", f.workspaceChat)
	resetFastQuota()
	r = f.taskRequest(t, "/api/fast-access/v1/read")
	r.Method = "POST"
	r.Header.Set("X-Agent-ID", callingAgent)
	r.Body = ioBody(fmt.Sprintf(`{"cursor":%q}`, *page.NextCursor))
	w = httptest.NewRecorder()
	testHandler.ReadFastAccess(w, r)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "scope_reset") {
		t.Fatalf("revocation: %d %s", w.Code, w.Body.String())
	}
}
func ioBody(s string) io.ReadCloser { return io.NopCloser(strings.NewReader(s)) }
func TestFastAccessBudgets(t *testing.T) {
	resetFastQuota()

	store := testHandler.fastStore()
	store.Lock()
	store.Quotas[testUserID] = fastQuota{Tokens: 0, At: time.Now()}
	store.Unlock()
	_, limited := fastCall(t, fastRequest{Include: []string{"active_runs"}})
	if limited.Code != 429 || limited.Header().Get("Retry-After") == "" {
		t.Fatalf("exhausted quota: %d", limited.Code)
	}

	for _, req := range []fastRequest{{Limit: 201}, {MaxBytes: 50001}, {Cursor: "x", Since: ptrTime(time.Now())}, {Include: []string{"prompt"}}} {
		_, w := fastCall(t, req)
		if w.Code != 400 {
			t.Fatalf("invalid input accepted: req=%s status=%d body=%s", mustJSON(req), w.Code, w.Body.String())
		}
	}
	resetFastQuota()
	_, w := fastCall(t, fastRequest{Cursor: "unknown"})
	if w.Code != 410 {
		t.Fatal("unknown cursor not rejected")
	}
	resetFastQuota()
	_, w = fastCall(t, fastRequest{Scope: &fastScope{Mode: "workspace_ids", WorkspaceIDs: []string{"11111111-1111-1111-1111-111111111111"}}})
	if w.Code != 403 {
		t.Fatalf("inaccessible workspace: %d", w.Code)
	}
	b := &fastBatch{AsOf: time.Now(), ScopeRevision: "v", Items: []fastItem{{ID: "a", Preview: strings.Repeat("<🙂", 160)}}, ActiveCount: 1}
	_, payload, end, err := fastPage(b, 0, 200, 50000, "next", "check")
	if err != nil || end != 1 || len(payload) > 50000 {
		t.Fatal("JSON byte sizing")
	}
	_, _, _, err = fastPage(b, 0, 200, 100, "next", "check")
	if err == nil {
		t.Fatal("byte budget ignored")
	}
}
func ptrTime(t time.Time) *time.Time { return &t }

func TestFastAccessLinkedProjectBoundary(t *testing.T) {
	source := dbfx.Workspace(t, "Linked fixture", "linked-fast-"+randomID())
	project := dbfx.Project(t, "Shared project", testutil.Cols{"workspace_id": source, "visibility": "workspace"})
	other := dbfx.Project(t, "Not shared", testutil.Cols{"workspace_id": source, "visibility": "workspace"})
	link := dbfx.Insert(t, "workspace_link", testutil.Cols{"source_workspace_id": source, "target_workspace_id": testWorkspaceID, "created_by": testUserID, "status": "active"})
	dbfx.Exec(t, "INSERT INTO workspace_link_project(link_id,project_id) VALUES($1,$2)", link, project)
	allowed := dbfx.Issue(t, "Linked allowed", testutil.Cols{"workspace_id": source, "project_id": project, "visibility": "workspace"})
	dbfx.Issue(t, "LINK-PRIVATE", testutil.Cols{"workspace_id": source, "project_id": project, "visibility": "private"})
	dbfx.Issue(t, "LINK-OTHER", testutil.Cols{"workspace_id": source, "project_id": other, "visibility": "workspace"})
	rt := dbfx.Runtime(t, "linked-runtime", testutil.Cols{"workspace_id": source})
	agent := dbfx.Agent(t, "linked-agent", rt, testutil.Cols{"workspace_id": source})
	dbfx.Task(t, agent, testutil.Cols{"runtime_id": rt, "status": "running", "issue_id": allowed})
	resetFastQuota()
	resp, w := fastCall(t, fastRequest{Scope: &fastScope{Mode: "workspace_ids", WorkspaceIDs: []string{source}}})
	if w.Code != 200 {
		t.Fatalf("linked %d %s", w.Code, w.Body.String())
	}
	if len(resp.Changes) != 1 || resp.Changes[0].ID != allowed || len(resp.ActiveRuns) != 0 || resp.Changes[0].NewCommentCount != nil {
		t.Fatalf("link grants too much: %s", w.Body.String())
	}
	dbfx.Exec(t, "UPDATE project SET visibility='private' WHERE id=$1", project)
	resetFastQuota()
	resp, w = fastCall(t, fastRequest{Scope: &fastScope{Mode: "workspace_ids", WorkspaceIDs: []string{source}}})
	if w.Code != 200 || len(resp.Changes) != 0 {
		t.Fatalf("private project: %d %s", w.Code, w.Body.String())
	}
}

func TestFastAccessAutopilotAndRunEndpoint(t *testing.T) {
	rt := dbfx.Runtime(t, "autopilot-fast")
	agent := dbfx.Agent(t, "autopilot-fast", rt)
	ap := dbfx.Insert(t, "autopilot", testutil.Cols{"workspace_id": testWorkspaceID, "title": "Compact autopilot", "assignee_id": agent, "assignee_type": "agent", "created_by_type": "member", "created_by_id": testUserID, "execution_mode": "run_only", "status": "active"})
	ar := dbfx.Insert(t, "autopilot_run", testutil.Cols{"autopilot_id": ap, "source": "manual", "status": "running"})
	task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": rt, "status": "running", "autopilot_run_id": ar})
	stranger := dbfx.User(t, "private-auto", "private-auto-"+randomID()+"@example.test")
	hiddenIssue := dbfx.Issue(t, "PRIVATE-AUTO-SOURCE", testutil.Cols{"creator_id": stranger, "visibility": "private"})
	hiddenExecution := dbfx.Insert(t, "autopilot_run", testutil.Cols{"autopilot_id": ap, "source": "manual", "status": "running", "issue_id": hiddenIssue})
	hiddenTask := dbfx.Task(t, agent, testutil.Cols{"runtime_id": rt, "status": "running", "autopilot_run_id": hiddenExecution})
	resetFastQuota()
	r := withChatTestWorkspaceCtx(t, newRequest("GET", "/api/fast-access/v1/runs?active=true", nil))
	w := httptest.NewRecorder()
	testHandler.ListFastAccessRuns(w, r)
	if w.Code != 200 {
		t.Fatalf("GET runs %d %s", w.Code, w.Body.String())
	}
	var result fastResponse
	json.Unmarshal(w.Body.Bytes(), &result)
	kinds := map[string]string{}
	for _, item := range result.ActiveRuns {
		kinds[item.ID] = item.Kind
	}
	if kinds[hiddenExecution] != "" || kinds[hiddenTask] != "" {
		t.Fatal("autopilot private issue source leaked")
	}
	if kinds[ar] != "autopilot_run" || kinds[task] != "run" {
		t.Fatal("autopilot execution conflated with task")
	}
	if len(result.Changes) != 0 || !result.Complete {
		t.Fatal("runs endpoint should select active only")
	}
}
