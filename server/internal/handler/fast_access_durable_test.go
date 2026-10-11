package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func fastMustExec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func fastSnapshot(t *testing.T, include ...string) fastResponse {
	t.Helper()
	resetFastQuota()
	b, w := fastCall(t, fastRequest{Scope: &fastScope{Mode: "workspace_ids", WorkspaceIDs: []string{testWorkspaceID}}, Include: include, Limit: 200})
	if w.Code != 200 || !b.Complete || b.Checkpoint == nil {
		t.Fatalf("snapshot: %d %s", w.Code, w.Body.String())
	}
	return b
}
func fastDelta(t *testing.T, cp string) fastResponse {
	t.Helper()
	resetFastQuota()
	b, w := fastCall(t, fastRequest{Cursor: cp, Limit: 200})
	if w.Code != 200 {
		t.Fatalf("delta: %d %s", w.Code, w.Body.String())
	}
	return b
}
func TestFastDurableCommitOrderAndMultiStatement(t *testing.T) {
	ctx := context.Background()
	a := dbfx.Issue(t, "order A")
	b := dbfx.Issue(t, "order B")
	tx1, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx1.Rollback(ctx)
	if _, err = tx1.Exec(ctx, `UPDATE issue SET title='A pending' WHERE id=$1`, parseUUID(a)); err != nil {
		t.Fatal(err)
	}
	// Later transaction commits first: it must own the lower committed seq.
	fastMustExec(t, `UPDATE issue SET title='B committed' WHERE id=$1`, parseUUID(b))
	cp := fastSnapshot(t, "issues")
	if err = tx1.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	delta := fastDelta(t, *cp.Checkpoint)
	found := false
	for _, i := range delta.Changes {
		if i.ID == a && i.Title == "A pending" {
			found = true
		}
	}
	if !found {
		t.Fatal("late commit skipped by baseline watermark")
	}
	duplicate := fastDelta(t, *cp.Checkpoint)
	x, _ := json.Marshal(delta.Changes)
	y, _ := json.Marshal(duplicate.Changes)
	if string(x) != string(y) {
		t.Fatal("replay is not stable")
	}
	// Fresh Handler simulates another process with no in-memory cursor store.
	clone := *testHandler
	r := withChatTestWorkspaceCtx(t, newRequest("POST", "/api/fast-access/v1/read", fastRequest{Cursor: *delta.Checkpoint}))
	w := httptest.NewRecorder()
	clone.ReadFastAccess(w, r)
	if w.Code != 200 {
		t.Fatalf("replica %d %s", w.Code, w.Body.String())
	}
	ws2 := dbfx.Workspace(t, "lock order second", "lock-"+randomID())
	dbfx.Member(t, ws2, testUserID, "owner")
	x1 := dbfx.Issue(t, "lock x1")
	x2 := dbfx.Issue(t, "lock x2", testutil.Cols{"workspace_id": ws2})
	y1 := dbfx.Issue(t, "lock y1")
	y2 := dbfx.Issue(t, "lock y2", testutil.Cols{"workspace_id": ws2})
	left, _ := testPool.Begin(ctx)
	defer left.Rollback(ctx)
	right, _ := testPool.Begin(ctx)
	defer right.Rollback(ctx)
	for _, v := range []struct {
		tx pgx.Tx
		id string
	}{{left, x1}, {left, x2}, {right, y2}, {right, y1}} {
		if _, err = v.tx.Exec(ctx, `UPDATE issue SET title=title||' changed' WHERE id=$1`, parseUUID(v.id)); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	done := make(chan error, 2)
	for _, tx := range []pgx.Tx{left, right} {
		go func(tx pgx.Tx) { <-start; done <- tx.Commit(ctx) }(tx)
	}
	close(start)
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("cross-workspace commit deadlocked")
		}
	}
	var pending int
	if err = testPool.QueryRow(ctx, "SELECT count(*) FROM fast_access_pending").Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("pending=%d err=%v", pending, err)
	}
	t.Log("out-of-order commits, multi-statement reversed workspaces, restart/replica replay and duplicate IDs passed")
}
func TestFastDurableTransitionsAndNoLeaseEvents(t *testing.T) {
	runtime := dbfx.Runtime(t, "journal runtime")
	agent := dbfx.Agent(t, "journal agent", runtime)
	task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": runtime, "status": "queued"})
	cp := fastSnapshot(t, "run_changes")
	fastMustExec(t, `UPDATE agent_task_queue SET status='dispatched' WHERE id=$1`, parseUUID(task))
	fastMustExec(t, `UPDATE agent_task_queue SET prepare_lease_expires_at=now()+interval '1 minute' WHERE id=$1`, parseUUID(task))
	fastMustExec(t, `UPDATE agent_task_queue SET status='running' WHERE id=$1`, parseUUID(task))
	fastMustExec(t, `UPDATE agent_task_queue SET status='completed' WHERE id=$1`, parseUUID(task))
	d := fastDelta(t, *cp.Checkpoint)
	var statuses []string
	var last string
	for _, i := range d.Changes {
		if i.ID == task {
			statuses = append(statuses, i.Status)
			if i.EventID == "" || i.Revision == last {
				t.Fatal("missing stable identity/revision")
			}
			last = i.Revision
		}
	}
	if strings.Join(statuses, ",") != "dispatched,running,completed" {
		t.Fatalf("transitions %v", statuses)
	}
}
func TestFastDurablePaginationResetAndEmptyFilter(t *testing.T) {
	cp := fastSnapshot(t, "chats")
	dbfx.Issue(t, "filtered issue")
	empty := fastDelta(t, *cp.Checkpoint)
	if len(empty.Changes) != 0 || empty.Checkpoint == nil || !empty.Complete {
		t.Fatal("empty filtered page did not advance")
	}
	cp = fastSnapshot(t, "issues")
	for i := 0; i < 205; i++ {
		dbfx.Issue(t, fmt.Sprintf("page %03d", i))
	}
	resetFastQuota()
	first, w := fastCall(t, fastRequest{Cursor: *cp.Checkpoint, Limit: 100})
	if w.Code != 200 || first.Complete || first.Checkpoint != nil || first.NextCursor == nil {
		t.Fatalf("first page %d %s", w.Code, w.Body.String())
	}
	late := dbfx.Issue(t, "after upper bound")
	ids := map[string]bool{}
	for _, i := range first.Changes {
		ids[i.ID] = true
	}
	current := first
	for !current.Complete {
		resetFastQuota()
		current, w = fastCall(t, fastRequest{Cursor: *current.NextCursor, Limit: 100})
		if w.Code != 200 {
			t.Fatalf("page %d %s", w.Code, w.Body.String())
		}
		for _, i := range current.Changes {
			if ids[i.ID] {
				t.Fatal("duplicate page object")
			}
			ids[i.ID] = true
		}
	}
	if len(ids) != 205 || ids[late] {
		t.Fatalf("moving upper bound: %d late=%v", len(ids), ids[late])
	}
	next := fastDelta(t, *current.Checkpoint)
	if len(next.Changes) != 1 || next.Changes[0].ID != late {
		t.Fatal("gap after pagination")
	}
	// Deletion safely resets without returning a hidden/deleted object ID.
	fastMustExec(t, `DELETE FROM issue WHERE id=$1`, parseUUID(late))
	resetFastQuota()
	_, w = fastCall(t, fastRequest{Cursor: *next.Checkpoint})
	if w.Code != 409 || strings.Contains(w.Body.String(), late) {
		t.Fatalf("unsafe deletion %d %s", w.Code, w.Body.String())
	}
	before := time.Now().Add(-8 * 24 * time.Hour)
	resetFastQuota()
	_, w = fastCall(t, fastRequest{Since: &before})
	if w.Code != 410 {
		t.Fatalf("old history %d", w.Code)
	}
	fresh := fastSnapshot(t, "chats")
	fastMustExec(t, `UPDATE fast_access_cursor SET expires_at=now()-interval '1 second' WHERE token=$1`, *fresh.Checkpoint)
	resetFastQuota()
	_, w = fastCall(t, fastRequest{Cursor: *fresh.Checkpoint})
	if w.Code != 410 {
		t.Fatal("expired checkpoint accepted")
	}
}
func TestFastDurableSSEReplayAndScopeReset(t *testing.T) {

	rt := dbfx.Runtime(t, "SSE old history runtime")
	agent := dbfx.Agent(t, "SSE old history agent", rt)
	fastMustExec(t, `INSERT INTO agent_task_queue(agent_id,runtime_id,status,created_at,completed_at) SELECT $1::uuid,$2::uuid,'completed',now()-interval '30 days',now()-interval '30 days' FROM generate_series(1,10001)`, agent, rt)
	fastMustExec(t, `UPDATE fast_access_meta SET coverage_from=now()-interval '1 hour',permission_changed_at=now()-interval '1 hour'`)
	fastMustExec(t, `UPDATE fast_access_journal SET changed_at=now()-interval '30 days' WHERE kind='run' AND workspace_id=$1`, testWorkspaceID)
	issue := dbfx.Issue(t, "SSE issue", testutil.Cols{"visibility": "workspace"})
	cp := fastSnapshot(t, "issues", "run_changes", "active_runs")
	fastMustExec(t, `UPDATE issue SET title='between baseline and subscribe' WHERE id=$1`, parseUUID(issue))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authenticated := withChatTestWorkspaceCtx(t, newRequest("GET", r.URL.String(), nil)).WithContext(r.Context())
		// Keep the fixture context (user and workspace), but propagate cancel.
		base := withChatTestWorkspaceCtx(t, newRequest("GET", r.URL.String(), nil))
		ctx, cancel := context.WithCancel(base.Context())
		defer cancel()
		go func() {
			select {
			case <-r.Context().Done():
				cancel()
			case <-ctx.Done():
			}
		}()
		authenticated = base.WithContext(ctx)
		authenticated.Header.Set("Last-Event-ID", r.Header.Get("Last-Event-ID"))
		testHandler.FastAccessEvents(w, authenticated)
	}))
	defer server.Close()
	read := func(token string) (string, string) {
		t.Helper()
		request, _ := http.NewRequest("GET", server.URL+"?cursor=ignored", nil)
		request.Header.Set("Last-Event-ID", token)
		client := &http.Client{Timeout: 5 * time.Second}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("SSE %d", response.StatusCode)
		}
		scan := bufio.NewScanner(response.Body)
		payload, next := "", ""
		for scan.Scan() {
			line := scan.Text()
			payload += line + "\n"
			if strings.HasPrefix(line, "id: ") {
				next = strings.TrimPrefix(line, "id: ")
				break
			}
		}
		if next == "" {
			t.Fatalf("SSE missing checkpoint %s err=%v", payload, scan.Err())
		}
		return payload, next
	}
	first, next := read(*cp.Checkpoint)
	again, _ := read(*cp.Checkpoint)
	eventID := func(s string) string {
		for _, line := range strings.Split(s, "\n") {
			if strings.HasPrefix(line, "data: ") {
				var e map[string]any
				_ = json.Unmarshal([]byte(line[6:]), &e)
				if id, ok := e["event_id"].(string); ok {
					return id
				}
			}
		}
		return ""
	}
	if !strings.Contains(first, issue) || eventID(first) == "" || eventID(first) != eventID(again) {
		t.Fatal("SSE gap/dedup failure")
	}
	fastMustExec(t, `UPDATE issue SET visibility='private' WHERE id=$1`, parseUUID(issue))
	req, _ := http.NewRequest("GET", server.URL+"?cursor="+next, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("SSE permission reset %d", resp.StatusCode)
	}
}

func TestFastDurableNewGrantInvalidatesAndPrivateEventsStayHidden(t *testing.T) {
	stranger := dbfx.User(t, "journal stranger", "journal-"+randomID()+"@example.test")
	dbfx.Member(t, testWorkspaceID, stranger, "member")
	hidden := dbfx.Issue(t, "hidden journal", testutil.Cols{"visibility": "private", "creator_id": stranger})
	cp := fastSnapshot(t, "issues")
	fastMustExec(t, `UPDATE issue SET title='still hidden' WHERE id=$1`, parseUUID(hidden))
	d := fastDelta(t, *cp.Checkpoint)
	for _, i := range d.Changes {
		if i.ID == hidden {
			t.Fatal("hidden identity replayed")
		}
	}
	fastMustExec(t, `UPDATE issue SET visibility='workspace' WHERE id=$1`, parseUUID(hidden))
	resetFastQuota()
	_, w := fastCall(t, fastRequest{Cursor: *d.Checkpoint})
	if w.Code != 409 {
		t.Fatalf("new grant did not require rebuild: %d", w.Code)
	}
	rebuilt := fastSnapshot(t, "issues")
	found := false
	for _, i := range rebuilt.Changes {
		if i.ID == hidden {
			found = true
		}
	}
	if !found {
		t.Fatal("rebuild did not include newly granted object")
	}
}

func TestFastDurableStreamLimitAndKeepalive(t *testing.T) {
	cp := fastSnapshot(t, "chats")
	serve := func(w http.ResponseWriter, r *http.Request) {
		base := withChatTestWorkspaceCtx(t, newRequest("GET", r.URL.String(), nil))
		ctx, cancel := context.WithCancel(base.Context())
		defer cancel()
		go func() {
			select {
			case <-r.Context().Done():
				cancel()
			case <-ctx.Done():
			}
		}()
		clone := *testHandler
		clone.FastAccessEvents(w, base.WithContext(ctx))
	}
	s := httptest.NewServer(http.HandlerFunc(serve))
	defer s.Close()
	client := &http.Client{Timeout: 35 * time.Second}
	one, err := client.Get(s.URL + "?cursor=" + *cp.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Body.Close()
	two, err := client.Get(s.URL + "?cursor=" + *cp.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer two.Body.Close()
	three, err := client.Get(s.URL + "?cursor=" + *cp.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer three.Body.Close()
	if one.StatusCode != 200 || two.StatusCode != 200 || three.StatusCode != 429 || three.Header.Get("Retry-After") != "30" {
		t.Fatal("shared replica stream cap failed")
	}
	scan := bufio.NewScanner(one.Body)
	found := false
	start := time.Now()
	for scan.Scan() {
		if scan.Text() == ": keepalive" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("keepalive missing: %v", scan.Err())
	}
	t.Logf("real SSE keepalive after %s; shared two-stream cap returned 429", time.Since(start))
	fastMustExec(t, `UPDATE member SET role=role WHERE workspace_id=$1 AND user_id=$2`, parseUUID(testWorkspaceID), parseUUID(testUserID))
	found = false
	for scan.Scan() {
		if scan.Text() == "event: scope_reset" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("live stream failed to invalidate scope")
	}
}

type fastSlowWriter struct {
	header http.Header
	writes int
}

func (w *fastSlowWriter) Header() http.Header { return w.header }
func (w *fastSlowWriter) WriteHeader(int)     {}
func (w *fastSlowWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes > 1 {
		return 0, fmt.Errorf("slow consumer write deadline exceeded")
	}
	return len(p), nil
}
func (w *fastSlowWriter) FlushError() error                { return nil }
func (w *fastSlowWriter) SetWriteDeadline(time.Time) error { return nil }
func TestFastDurableSlowConsumerCanReplay(t *testing.T) {
	issue := dbfx.Issue(t, "slow consumer")
	cp := fastSnapshot(t, "issues")
	fastMustExec(t, `UPDATE issue SET title='replay after write failure' WHERE id=$1`, parseUUID(issue))
	r := withChatTestWorkspaceCtx(t, newRequest("GET", "/api/fast-access/v1/events?cursor="+*cp.Checkpoint, nil))
	w := &fastSlowWriter{header: http.Header{}}
	testHandler.FastAccessEvents(w, r)
	if w.writes != 2 {
		t.Fatalf("writes=%d", w.writes)
	}
	var leases int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM fast_access_stream WHERE principal=$1`, testUserID).Scan(&leases); err != nil || leases != 0 {
		t.Fatalf("stream lease leaked: %d %v", leases, err)
	}
	d := fastDelta(t, *cp.Checkpoint)
	if len(d.Changes) != 1 || d.Changes[0].ID != issue {
		t.Fatal("slow client could not replay")
	}
}

func TestFastDurableEmptyFilteredPagesAdvanceFixedUpper(t *testing.T) {
	stranger := dbfx.User(t, "filtered stranger", "filtered-"+randomID()+"@example.test")
	hidden := dbfx.Issue(t, "hidden pages", testutil.Cols{"visibility": "private", "creator_id": stranger})
	visible := dbfx.Issue(t, "visible after hidden")
	cp := fastSnapshot(t, "issues")
	for range 401 {
		fastMustExec(t, `UPDATE issue SET title=title WHERE id=$1`, parseUUID(hidden))
	}
	fastMustExec(t, `UPDATE issue SET title='last visible' WHERE id=$1`, parseUUID(visible))
	first := fastDelta(t, *cp.Checkpoint)
	if first.Complete || len(first.Changes) != 0 || first.Checkpoint != nil || first.NextCursor == nil {
		t.Fatal("invisible page exposed or prematurely committed")
	}
	second := fastDelta(t, *first.NextCursor)
	if second.Complete || len(second.Changes) != 0 || second.Checkpoint != nil || second.NextCursor == nil {
		t.Fatal("second invisible page did not advance")
	}
	final := fastDelta(t, *second.NextCursor)
	if !final.Complete || len(final.Changes) != 1 || final.Changes[0].ID != visible || final.Checkpoint == nil {
		t.Fatal("visible event after invisible pages lost")
	}
}

func TestFastDurableHistoryBeyondTenThousandEvents(t *testing.T) {
	issue := dbfx.Issue(t, "long retained history")
	cp := fastSnapshot(t, "issues")
	// One multi-statement transaction, real business-row capture and commit.
	tx, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	for range 10005 {
		if _, err = tx.Exec(context.Background(), `UPDATE issue SET title=title WHERE id=$1`, parseUUID(issue)); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	cursor := *cp.Checkpoint
	pages := 0
	for {
		page := fastDelta(t, cursor)
		pages++
		for _, i := range page.Changes {
			if seen[i.EventID] || i.EventID == "" {
				t.Fatal("duplicate/missing event ID")
			}
			seen[i.EventID] = true
		}
		if page.Complete {
			if page.Checkpoint == nil {
				t.Fatal("missing final checkpoint")
			}
			break
		}
		if page.Checkpoint != nil || page.NextCursor == nil {
			t.Fatal("premature checkpoint")
		}
		cursor = *page.NextCursor
		if pages > 200 {
			t.Fatal("nonadvancing cursor")
		}
	}
	if len(seen) != 10005 {
		t.Fatalf("lost history: %d", len(seen))
	}
	t.Logf("%d historical events delivered in %d bounded pages", len(seen), pages)
}

func TestFastDurablePrunedPrefixRequiresReset(t *testing.T) {
	issue := dbfx.Issue(t, "retained prefix")
	cp := fastSnapshot(t, "issues")
	fastMustExec(t, `UPDATE issue SET title='expires before consumption' WHERE id=$1`, parseUUID(issue))
	fastMustExec(t, `UPDATE fast_access_journal SET changed_at=now()-interval '8 days' WHERE workspace_id=$1`, parseUUID(testWorkspaceID))
	fastMustExec(t, `SELECT fast_access_prune()`)
	resetFastQuota()
	_, w := fastCall(t, fastRequest{Cursor: *cp.Checkpoint})
	if w.Code != 410 {
		t.Fatalf("pruned committed prefix silently skipped: %d %s", w.Code, w.Body.String())
	}
	rebuilt := fastSnapshot(t, "issues")
	d := fastDelta(t, *rebuilt.Checkpoint)
	if !d.Complete || len(d.Changes) != 0 {
		t.Fatal("fresh snapshot could not resume after retention reset")
	}
}

func TestFastDurableLastMessageDeletionResetsDirectory(t *testing.T) {
	runtime := dbfx.Runtime(t, "last message")
	agent := dbfx.Agent(t, "last message", runtime)
	chat := dbfx.ChatSession(t, agent)
	msg := dbfx.Insert(t, "chat_message", testutil.Cols{"chat_session_id": chat, "role": "assistant", "content": "last visible message"})
	cp := fastSnapshot(t, "chats")
	fastMustExec(t, `DELETE FROM chat_message WHERE id=$1`, parseUUID(msg))
	resetFastQuota()
	_, w := fastCall(t, fastRequest{Cursor: *cp.Checkpoint})
	if w.Code != 409 || strings.Contains(w.Body.String(), chat) {
		t.Fatal("disappearing implicit chat did not safely reset")
	}
}
