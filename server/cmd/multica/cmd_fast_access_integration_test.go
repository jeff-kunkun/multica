package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// openPrivateEpochDatabase opens the run's database with a private copy of
// fast_access_meta first on the search_path. Its permission_revision is one
// global epoch that every membership or visibility write bumps, and a resumed
// cursor page whose epoch moved answers scope_reset. Under the full suite the
// other DB-backed packages bump it dozens of times a second in the shared
// database, so the cursor walk below rebuilt until the read quota answered
// 429. The capture/flush triggers and the handler SQL name the table
// unqualified, so through this pool only this test's own writes move its
// epoch; every other table stays shared and is exercised as in production.
func openPrivateEpochDatabase(ctx context.Context, t *testing.T) *pgxpool.Pool {
	t.Helper()
	admin := testutil.OpenTestDatabase(ctx, t)
	schema := fmt.Sprintf("fast_access_cli_%d", time.Now().UnixNano())
	for _, stmt := range []string{
		`CREATE SCHEMA ` + schema,
		`CREATE TABLE ` + schema + `.fast_access_meta (LIKE public.fast_access_meta INCLUDING ALL)`,
		`INSERT INTO ` + schema + `.fast_access_meta SELECT * FROM public.fast_access_meta`,
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("drop private epoch schema: %v", err)
		}
	})
	return testutil.OpenTestDatabaseConfig(ctx, t, func(config *pgxpool.Config) {
		config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	})
}

// This exercises real HTTP handlers, authorization/projection SQL, and cursor
// packing against the runner's disposable database. Identity is injected at the
// authenticated middleware boundary; login/token validation is not under test.
func TestFastAccessCLIHandlerIntegration(t *testing.T) {
	ctx := context.Background()
	pool := openPrivateEpochDatabase(ctx, t)
	f := testutil.New(pool, "", "")
	user := f.User(t, "CLI reader", fmt.Sprintf("cli-%d@example.test", time.Now().UnixNano()))
	f.UserID = user
	ws := f.Workspace(t, "CLI one", fmt.Sprintf("cli-one-%d", time.Now().UnixNano()))
	ws2 := f.Workspace(t, "CLI two", fmt.Sprintf("cli-two-%d", time.Now().UnixNano()))
	f.WorkspaceID = ws
	f.Member(t, ws, user, "owner")
	f.Member(t, ws2, user, "owner")
	rt := f.Runtime(t, "CLI runtime")
	agent := f.Agent(t, "CLI agent", rt)
	rt2 := f.Runtime(t, "CLI runtime two", testutil.Cols{"workspace_id": ws2})
	agent2 := f.Agent(t, "CLI agent two", rt2, testutil.Cols{"workspace_id": ws2})
	chat := f.ChatSession(t, agent, testutil.Cols{"title": "中文🙂 activity", "explicitly_created_at": testutil.Raw("now()"), "last_read_at": testutil.Raw("now()-interval '1 hour'")})
	f.Insert(t, "chat_message", testutil.Cols{"chat_session_id": chat, "role": "assistant", "content": "新消息🙂"})
	f.Issue(t, "CLI issue one")
	f.Issue(t, "CLI issue two", testutil.Cols{"workspace_id": ws2})
	for i := 0; i < 17; i++ {
		a, r := agent, rt
		if i%2 == 1 {
			a, r = agent2, rt2
		}
		cols := testutil.Cols{"runtime_id": r, "status": "running"}
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
		f.Task(t, a, cols)
	}
	q := db.New(pool)
	member, err := q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: util.MustParseUUID(user), WorkspaceID: util.MustParseUUID(ws)})
	if err != nil {
		t.Fatal(err)
	}
	h := handler.New(q, pool, realtime.NewHub(), events.New(), nil, nil, nil, nil, handler.Config{})
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		r.Header.Set("X-User-ID", user)
		r = r.WithContext(middleware.SetMemberContext(r.Context(), ws, member))
		switch r.Method + " " + r.URL.Path {
		case "POST /api/fast-access/v1/read":
			h.ReadFastAccess(w, r)
		case "GET /api/fast-access/v1/runs":
			h.ListFastAccessRuns(w, r)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("MULTICA_TOKEN", "mat_fixture_identity_boundary")
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", ws)

	invoke := func(runs bool, args ...string) (map[string]any, error) {
		t.Helper()
		args = append(args, "--output", "json")
		var out, notes bytes.Buffer
		var err error
		// The CLI does not retry a 429 itself; its documented contract is to
		// wait for Retry-After and retry. Do that, a bounded number of times.
		for attempt := 1; ; attempt++ {
			before := requests.Load()
			out.Reset()
			notes.Reset()
			if binary := os.Getenv("MULTICA_FAST_ACCESS_BINARY"); binary != "" {
				name := "changes"
				if runs {
					name = "runs"
				}
				c := exec.CommandContext(ctx, binary, append([]string{name}, args...)...)
				c.Stdout = &out
				c.Stderr = &notes
				err = c.Run()
			} else {
				c := newFastAccessCommand(runs)
				c.SilenceErrors = true
				c.SilenceUsage = true
				c.SetOut(&out)
				c.SetErr(&notes)
				c.SetArgs(args)
				err = c.Execute()
			}
			if requests.Load()-before != 1 {
				t.Fatalf("command made %d business requests", requests.Load()-before)
			}
			t.Logf("command runs=%v args=%q error=%v bytes=%d stdout=%s stderr=%s", runs, args, err, out.Len(), out.String(), notes.String())
			wait, limited := fastAccessRetryAfter(notes.String())
			if err == nil || !limited || attempt == 3 {
				break
			}
			time.Sleep(wait)
		}
		if err != nil {
			if out.Len() != 0 {
				t.Fatal("failure emitted stdout")
			}
			return nil, err
		}
		if out.Len() > 50000 {
			t.Fatal("budget exceeded")
		}
		var envelope map[string]any
		if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope, nil
	}
	full, err := invoke(false, "--all-workspaces")
	if err != nil {
		t.Fatal(err)
	}
	if full["complete"] != true || full["active_runs_complete"] != true || len(full["active_runs"].([]any)) != 17 {
		t.Fatalf("incomplete initial snapshot: %v", full)
	}
	seenWS := map[string]bool{}
	seenState := map[string]bool{}
	for _, raw := range full["active_runs"].([]any) {
		r := raw.(map[string]any)
		seenWS[r["workspace_id"].(string)] = true
		seenState[fmt.Sprint(r["status"], "/", r["phase"])] = true
	}
	if len(seenWS) != 2 || !seenState["deferred/waiting"] || !seenState["waiting_local_directory/waiting"] || !seenState["running/executing"] {
		t.Fatal(seenWS, seenState)
	}
	active, err := invoke(true, "--active", "--all-workspaces")
	if err != nil || len(active["active_runs"].([]any)) != 17 {
		t.Fatal(active, err)
	}
	explicit, err := invoke(true, "--active", "--workspace", ws2)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range explicit["active_runs"].([]any) {
		if raw.(map[string]any)["workspace_id"] != ws2 {
			t.Fatal("scope escaped")
		}
	}
	// The epoch is private to this test (openPrivateEpochDatabase) and nothing
	// writes during the walk, so a scope_reset here is a real failure.
	page, err := invoke(false, "--all-workspaces", "--limit", "5")
	if err != nil {
		t.Fatal(err)
	}
	if page["complete"] != false || page["checkpoint"] != nil {
		t.Fatal("first page falsely complete")
	}
	unique := map[string]bool{}
	pages := 0
	batchAsOf := page["as_of"]
	for {
		pages++
		if page["as_of"] != batchAsOf {
			t.Fatal("batch boundary changed")
		}
		for _, section := range []string{"active_runs", "changes"} {
			for _, raw := range page[section].([]any) {
				r := raw.(map[string]any)
				key := fmt.Sprint(r["workspace_id"], r["kind"], r["id"])
				if unique[key] {
					t.Fatal("duplicate item")
				}
				unique[key] = true
			}
		}
		if page["complete"] == true {
			break
		}
		cursor, ok := page["next_cursor"].(string)
		if !ok || pages > 8 {
			t.Fatal("pagination did not progress")
		}
		page, err = invoke(false, "--cursor", cursor, "--limit", "5")
		if err != nil {
			t.Fatal(err)
		}
	}
	want := len(full["active_runs"].([]any)) + len(full["changes"].([]any))
	if len(unique) != want || page["active_runs_complete"] != true || page["checkpoint"] == nil {
		t.Fatal("incomplete pagination", len(unique), want, page)
	}
	if _, err := invoke(false, "--cursor", page["checkpoint"].(string)); err != nil {
		t.Fatal("durable checkpoint must be accepted as a cursor", err)
	}
	t.Logf("REAL HANDLER/POSTGRES CLI: two workspaces, 17 active runs; explicit scope; %d pages, %d unique items; durable checkpoint resumed; token middleware not exercised", pages, len(unique))
}

// fastAccessRetryAfter reads the wait the CLI reports for a 429 from its
// stderr note, capped at five seconds.
func fastAccessRetryAfter(notes string) (time.Duration, bool) {
	_, after, found := strings.Cut(notes, "Rate limited; Retry-After: ")
	if !found {
		return 0, false
	}
	seconds, _, _ := strings.Cut(after, ".")
	n, err := strconv.Atoi(strings.TrimSpace(seconds))
	if err != nil || n < 1 {
		n = 1
	}
	return time.Duration(min(n, 5)) * time.Second, true
}
