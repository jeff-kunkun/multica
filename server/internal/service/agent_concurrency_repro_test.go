package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// DENE-1947 reproduction: why an agent whose max_concurrent_tasks is 10 can
// look stuck at 2. The claim path stacks three gates, and each is exercised
// here against a real database:
//
//  1. one live run per (issue, agent) — "same-issue serial";
//  2. the agent's own max_concurrent_tasks;
//  3. the machine's shared run slots (default 12, all providers on one box).
//
// Nothing in this file stubs the claim: it drives TaskService.ClaimTask the
// way the daemon's batch poller does, one call per free slot.
func TestAgentConcurrencyGatesAndSlotRelease(t *testing.T) {
	ctx := context.Background()
	pool := sharedTestPool(t)
	q := db.New(pool)
	f := &testutil.Fixture{Pool: pool}
	suffix := time.Now().UnixNano()
	f.UserID = f.User(t, "Concurrency owner", fmt.Sprintf("conc-%d@test.invalid", suffix))
	f.WorkspaceID = f.Workspace(t, "Concurrency", fmt.Sprintf("conc-%d", suffix))
	f.Member(t, f.WorkspaceID, f.UserID, "owner")
	daemon := fmt.Sprintf("conc-%d", suffix)
	rt := f.Runtime(t, "Codex", testutil.Cols{"daemon_id": daemon})
	key, err := q.GetRuntimeMachineKey(ctx, util.MustParseUUID(rt))
	if err != nil {
		t.Fatal(err)
	}
	f.Cleanup(t, "DELETE FROM runtime_machine_capacity WHERE machine_key=$1", key)
	svc := NewTaskService(q, pool, nil, events.New())

	claimAll := func(agentID string) []*db.AgentTaskQueue {
		var got []*db.AgentTaskQueue
		for i := 0; i < 40; i++ {
			task, err := svc.ClaimTask(ctx, util.MustParseUUID(agentID))
			if err != nil {
				t.Fatalf("claim: %v", err)
			}
			if task == nil {
				break
			}
			got = append(got, task)
		}
		return got
	}
	queuedOnIssue := func(agentID string, issueID string) string {
		return f.Task(t, agentID, testutil.Cols{"runtime_id": rt, "issue_id": issueID})
	}

	// 1. Same agent, same issue: only one runs; extra queued runs wait.
	agent := f.Agent(t, "Trankes", rt, testutil.Cols{"max_concurrent_tasks": 10})
	issueA := f.Issue(t, "A")
	queuedOnIssue(agent, issueA)
	queuedOnIssue(agent, issueA)
	queuedOnIssue(agent, issueA)
	got := claimAll(agent)
	if len(got) != 1 {
		t.Fatalf("same issue: want 1 running, got %d", len(got))
	}
	if n := f.Count(t, "SELECT count(*) FROM agent_task_queue WHERE agent_id=$1 AND status='queued'", agent); n != 2 {
		t.Fatalf("same issue: want 2 still queued, got %d", n)
	}
	t.Logf("same agent + same issue: 3 queued -> 1 claimed, 2 held (issue_serial)")

	// 2. Different issues are not capped at 2: with limit 10 and 12 issues,
	//    exactly 10 run (the agent limit), the rest wait.
	for i := 0; i < 11; i++ {
		queuedOnIssue(agent, f.Issue(t, fmt.Sprintf("issue %d", i)))
	}
	more := claimAll(agent)
	running := 1 + len(more)
	if running != 10 {
		t.Fatalf("agent limit 10 across issues: want 10 running, got %d", running)
	}
	t.Logf("same agent, 12 distinct issues queued, limit 10 -> %d running (not 2)", running)

	// 3. Raising the agent limit takes effect on the very next claim.
	f.Exec(t, "UPDATE agent SET max_concurrent_tasks=12 WHERE id=$1", agent)
	extra := claimAll(agent)
	if len(extra) != 2 {
		t.Fatalf("after raising limit to 12: want 2 more claims, got %d", len(extra))
	}
	t.Logf("limit raised 10 -> 12 without restart: next claims admitted %d more", len(extra))

	// Machine is now at 12/12 across this one agent.
	capacity, err := q.GetRuntimeMachineCapacity(ctx, key)
	if err != nil || capacity.ActiveRuns != 12 || capacity.MaxConcurrentRuns != 12 {
		t.Fatalf("capacity=%+v err=%v", capacity, err)
	}

	// 4. A second provider on the same machine shares the same 12 slots.
	rt2 := f.Runtime(t, "Claude", testutil.Cols{"daemon_id": daemon, "provider": "handler_test_runtime_2"})
	other := f.Agent(t, "Other", rt2, testutil.Cols{"max_concurrent_tasks": 10})
	f.Task(t, other, testutil.Cols{"runtime_id": rt2, "issue_id": f.Issue(t, "other issue")})
	if blocked := claimAll(other); len(blocked) != 0 {
		t.Fatalf("machine full at 12/12 but other provider claimed %d", len(blocked))
	}
	t.Logf("machine 12/12: second provider on the same machine is blocked")

	// 5. Slot release: complete, fail and cancel each free exactly one slot,
	//    and the waiting provider takes it.
	all := append(append([]*db.AgentTaskQueue{}, got...), append(more, extra...)...)
	release := map[string]func(id pgtype.UUID){
		"complete": func(id pgtype.UUID) {
			if _, err := svc.StartTask(ctx, id); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.CompleteTask(ctx, id, []byte(`{}`), "", "", "", false, "", ""); err != nil {
				t.Fatal(err)
			}
		},
		"fail": func(id pgtype.UUID) {
			if _, err := svc.StartTask(ctx, id); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.FailTask(ctx, id, "boom", "", "", "", "agent_error", false, "", ""); err != nil {
				t.Fatal(err)
			}
		},
		"cancel": func(id pgtype.UUID) {
			if _, err := svc.CancelTask(ctx, id); err != nil {
				t.Fatal(err)
			}
		},
	}
	for i, mode := range []string{"complete", "fail", "cancel"} {
		before, _ := q.GetRuntimeMachineCapacity(ctx, key)
		release[mode](all[i].ID)
		after, _ := q.GetRuntimeMachineCapacity(ctx, key)
		if after.ActiveRuns != before.ActiveRuns-1 {
			t.Fatalf("%s: machine active %d -> %d, want one slot freed", mode, before.ActiveRuns, after.ActiveRuns)
		}
		f.Task(t, other, testutil.Cols{"runtime_id": rt2, "issue_id": f.Issue(t, "after "+mode)})
		if n := len(claimAll(other)); n < 1 {
			t.Fatalf("%s: freed slot was not reusable", mode)
		}
		t.Logf("%s frees one machine slot (%d -> %d) and a waiting provider takes it", mode, before.ActiveRuns, after.ActiveRuns)
	}
}
