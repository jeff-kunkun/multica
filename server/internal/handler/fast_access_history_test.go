package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// EXPLAIN the actual projection SQL with production indexes. The populated
// fixture must not walk all historical attempts before returning a tiny page.
type historyPlanDB struct {
	dbExecutor
	t *testing.T
}

func (d historyPlanDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "FROM agent_task_queue t") {
		var raw []byte
		if err := d.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+sql, args...).Scan(&raw); err != nil {
			d.t.Fatal(err)
		}
		var plan []map[string]any
		if err := json.Unmarshal(raw, &plan); err != nil {
			d.t.Fatal(err)
		}
		var check func(map[string]any)
		check = func(n map[string]any) {
			if n["Relation Name"] == "agent_task_queue" {
				rows, _ := n["Actual Rows"].(float64)
				removed, _ := n["Rows Removed by Filter"].(float64)
				if rows+removed > 10000 {
					d.t.Fatalf("projection scanned old history: %s", raw)
				}
				d.t.Logf("run projection plan: %v, index=%v, rows=%v, removed=%v", n["Node Type"], n["Index Name"], rows, removed)
			}
			if children, ok := n["Plans"].([]any); ok {
				for _, child := range children {
					check(child.(map[string]any))
				}
			}
		}
		check(plan[0]["Plan"].(map[string]any))
	}
	return d.dbExecutor.Query(ctx, sql, args...)
}

func TestFastDurableOldHistoryPages(t *testing.T) {
	rt := dbfx.Runtime(t, "history runtime")
	a := dbfx.Agent(t, "history agent", rt)
	fastMustExec(t, `INSERT INTO agent_task_queue(agent_id,runtime_id,status,created_at,completed_at) SELECT $1::uuid,$2::uuid,'completed',now()-interval '30 days',now()-interval '30 days' FROM generate_series(1,10001)`, a, rt)
	fastMustExec(t, `UPDATE fast_access_meta SET coverage_from=now()-interval '1 hour',permission_changed_at=now()-interval '1 hour'`)
	fastMustExec(t, `UPDATE fast_access_journal SET changed_at=now()-interval '30 days' WHERE kind='run' AND workspace_id=$1`, testWorkspaceID)
	// Rebuilding a cache after retention also excludes all-time terminal history.
	cp := fastSnapshot(t, "issues", "run_changes", "active_runs")
	since := time.Now()
	issue := dbfx.Issue(t, "recent issue")
	task := dbfx.Task(t, a, testutil.Cols{"runtime_id": rt, "status": "queued", "created_at": testutil.Raw("now()-interval '30 days'")})
	for _, status := range []string{"dispatched", "running", "completed"} {
		fastMustExec(t, `UPDATE agent_task_queue SET status=$2 WHERE id=$1`, parseUUID(task), status)
	}
	active := map[string]bool{}
	for _, status := range []string{"queued", "dispatched", "running", "waiting_local_directory", "deferred"} {
		id := dbfx.Task(t, a, testutil.Cols{"runtime_id": rt, "status": status, "created_at": testutil.Raw("now()-interval '30 days'"), "context": []byte(`{"wakeup_id":"11111111-1111-1111-1111-111111111111"}`)})
		active[id] = true
	}

	fastMustExec(t, "ANALYZE agent_task_queue")
	reqPlan := fastRequest{Scope: &fastScope{Mode: "workspace_ids", WorkspaceIDs: []string{testWorkspaceID}}, Include: []string{"run_changes", "active_runs"}, activeOnly: true}
	scopes, err := testHandler.fastScopes(withChatTestWorkspaceCtx(t, newRequest("GET", "/", nil)), testUserID, reqPlan)
	if err != nil {
		t.Fatal(err)
	}
	reader := *testHandler
	reader.DB = historyPlanDB{testHandler.DB, t}
	if _, err := reader.fastRows(context.Background(), scopes, reqPlan, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	reqPlan.activeOnly = false
	if _, err := reader.fastRows(context.Background(), scopes, reqPlan, time.Now(), []fastItem{{WorkspaceID: testWorkspaceID, Kind: "run", ID: task}}); err != nil {
		t.Fatal(err)
	}
	var expectedEvents string
	for _, mode := range []string{"since", "checkpoint"} {
		t.Run(mode, func(t *testing.T) {
			req := fastRequest{Limit: 1, MaxBytes: 2000}
			if mode == "since" {
				req.Since = &since
				req.Scope = &fastScope{Mode: "workspace_ids", WorkspaceIDs: []string{testWorkspaceID}}
			} else {
				req.Cursor = *cp.Checkpoint
			}
			gotActive := map[string]bool{}
			events := []string{}
			statuses := []string{}
			foundIssue := false
			var frozen time.Time
			for n := 0; ; n++ {
				if n > 60 {
					t.Fatal("pagination did not terminate")
				}
				resetFastQuota()
				page, w := fastCall(t, req)
				if w.Code != 200 || w.Body.Len() > 2000 {
					t.Fatalf("page: %d %s", w.Code, w.Body.String())
				}
				if n == 0 {
					frozen = page.AsOf
				}
				if page.AsOf != frozen {
					t.Fatal("watermark moved")
				}
				for _, v := range page.ActiveRuns {
					if gotActive[v.ID] {
						t.Fatal("duplicate active")
					}
					gotActive[v.ID] = true
				}
				for _, v := range page.Changes {
					if v.ID == issue {
						foundIssue = true
					}
					if v.ID == task {
						statuses = append(statuses, v.Status)
						events = append(events, v.EventID)
						if v.EventID == "" {
							t.Fatal("missing event identity")
						}
					}
				}
				if page.Complete {
					if page.Checkpoint == nil {
						t.Fatal("missing checkpoint")
					}
					break
				}
				if page.NextCursor == nil || page.Checkpoint != nil {
					t.Fatal("false completion")
				}
				req = fastRequest{Cursor: *page.NextCursor, Limit: 1, MaxBytes: 2000}
			}
			if !foundIssue || len(gotActive) != len(active) {
				t.Fatalf("missing objects: issue=%v active=%v", foundIssue, gotActive)
			}
			for id := range active {
				if !gotActive[id] {
					t.Fatal("missing active", id)
				}
			}
			if strings.Join(statuses, ",") != "queued,dispatched,running,completed" {
				t.Fatal(statuses)
			}
			joined := strings.Join(events, ",")
			if expectedEvents == "" {
				expectedEvents = joined
			} else if expectedEvents != joined {
				t.Fatal("unstable events")
			}
			t.Logf("10001 old runs; all %d active runs and four transitions paged at <=2000 bytes (%s)", len(active), mode)
		})
	}
	// More than 10000 retained transitions also remain a journal page, not a
	// terminal-object materialization. A few tiny pages prove the bounded path.
	fastMustExec(t, `INSERT INTO agent_task_queue(agent_id,runtime_id,status) SELECT $1::uuid,$2::uuid,'completed' FROM generate_series(1,10001)`, a, rt)
	req := fastRequest{Cursor: *cp.Checkpoint, Limit: 10}
	for n := 0; n < 3; n++ {
		resetFastQuota()
		page, w := fastCall(t, req)
		if w.Code != 200 || page.Complete || page.NextCursor == nil {
			t.Fatalf("retained page %d: %d %s", n, w.Code, w.Body.String())
		}
		req = fastRequest{Cursor: *page.NextCursor, Limit: 10}
	}
	t.Log(fmt.Sprintf("retained history >10000 distinct runs remains pageable"))
}
