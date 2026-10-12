package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Simultaneous claims span providers and workspaces, not just one agent.
func TestMachineCapacityConcurrentClaimsAndLowering(t *testing.T) {
	ctx := context.Background()
	pool := sharedTestPool(t)
	q := db.New(pool)
	f := &testutil.Fixture{Pool: pool}
	suffix := time.Now().UnixNano()
	f.UserID = f.User(t, "Capacity owner", fmt.Sprintf("machine-%d@test.invalid", suffix))
	f.WorkspaceID = f.Workspace(t, "Capacity A", fmt.Sprintf("machine-a-%d", suffix))
	f.Member(t, f.WorkspaceID, f.UserID, "owner")
	otherWS := f.Workspace(t, "Capacity B", fmt.Sprintf("machine-b-%d", suffix))
	f.Member(t, otherWS, f.UserID, "owner")
	daemon := fmt.Sprintf("capacity-%d", suffix)
	rtA := f.Runtime(t, "Provider A", testutil.Cols{"daemon_id": daemon})
	rtB := f.Runtime(t, "Provider B", testutil.Cols{"daemon_id": daemon, "workspace_id": otherWS})
	key, err := q.GetRuntimeMachineKey(ctx, util.MustParseUUID(rtA))
	if err != nil {
		t.Fatal(err)
	}
	f.Cleanup(t, "DELETE FROM runtime_machine_capacity WHERE machine_key=$1", key)
	agents := make([]pgtype.UUID, 15)
	for i := range agents {
		rt, ws := rtA, f.WorkspaceID
		if i%2 == 1 {
			rt, ws = rtB, otherWS
		}
		agentID := f.Agent(t, fmt.Sprintf("Agent %d", i), rt, testutil.Cols{"workspace_id": ws})
		f.Task(t, agentID, testutil.Cols{"runtime_id": rt})
		agents[i] = util.MustParseUUID(agentID)
	}
	svc := NewTaskService(q, pool, nil, events.New())
	start := make(chan struct{})
	results := make(chan *db.AgentTaskQueue, 15)
	errors := make(chan error, 15)
	var wg sync.WaitGroup
	for _, id := range agents {
		wg.Add(1)
		go func(id pgtype.UUID) {
			defer wg.Done()
			<-start
			task, err := svc.ClaimTask(ctx, id)
			if err != nil {
				errors <- err
				return
			}
			results <- task
		}(id)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	claimed := []*db.AgentTaskQueue{}
	queued := []pgtype.UUID{}
	for task := range results {
		if task != nil {
			claimed = append(claimed, task)
		}
	}
	for _, id := range agents {
		if f.Count(t, "SELECT count(*) FROM agent_task_queue WHERE agent_id=$1 AND status='queued'", util.UUIDToString(id)) > 0 {
			queued = append(queued, id)
		}
	}
	if len(claimed) != 12 || len(queued) != 3 {
		t.Fatalf("15 claims: active=%d queued=%d", len(claimed), len(queued))
	}
	capacity, err := q.GetRuntimeMachineCapacity(ctx, key)
	if err != nil || capacity.ActiveRuns != 12 || capacity.MaxConcurrentRuns != 12 {
		t.Fatalf("capacity=%+v err=%v", capacity, err)
	}
	t.Log("15 simultaneous claims: 12 dispatched, 3 queued; capacity 12/12 across two providers/workspaces")
	if err := svc.runInTx(ctx, func(txq *db.Queries) error {
		if err := txq.LockRuntimeMachineCapacity(ctx, key); err != nil {
			return err
		}
		return txq.SetRuntimeMachineCapacity(ctx, db.SetRuntimeMachineCapacityParams{MachineKey: key, MaxConcurrentRuns: 8})
	}); err != nil {
		t.Fatal(err)
	}
	// Lowering does not cancel any existing reservation.
	task, err := svc.ClaimTask(ctx, queued[0])
	if err != nil || task != nil {
		t.Fatalf("claim above lowered limit: task=%v err=%v", task, err)
	}
	for _, task := range claimed[:4] {
		f.Exec(t, "UPDATE agent_task_queue SET status='completed', completed_at=now() WHERE id=$1", util.UUIDToString(task.ID))
	}
	task, err = svc.ClaimTask(ctx, queued[0])
	if err != nil || task != nil {
		t.Fatalf("claim at 8/8: task=%v err=%v", task, err)
	}
	f.Exec(t, "UPDATE agent_task_queue SET status='completed', completed_at=now() WHERE id=$1", util.UUIDToString(claimed[4].ID))
	task, err = svc.ClaimTask(ctx, queued[0])
	if err != nil || task == nil {
		t.Fatalf("claim at 7/8: task=%v err=%v", task, err)
	}
	t.Log("lowered to 8: existing 12 preserved; blocks at 12/8 and 8/8; resumes at 7/8")
	// A different machine has independent capacity.
	rtOther := f.Runtime(t, "Other machine", testutil.Cols{"daemon_id": daemon + "-other"})
	a := f.Agent(t, "Other machine agent", rtOther)
	f.Task(t, a, testutil.Cols{"runtime_id": rtOther})
	task, err = svc.ClaimTask(ctx, util.MustParseUUID(a))
	if err != nil || task == nil {
		t.Fatalf("independent machine: task=%v err=%v", task, err)
	}
	// An unrelated account reusing a daemon name cannot consume or edit this
	// owner's slots, even when both runtimes are visible in one workspace.
	otherOwner := f.User(t, "Other owner", fmt.Sprintf("other-machine-%d@test.invalid", suffix))
	f.Member(t, f.WorkspaceID, otherOwner, "member")
	rtForeign := f.Runtime(t, "Foreign same-name machine", testutil.Cols{"daemon_id": daemon, "provider": "foreign_provider", "owner_id": otherOwner})
	foreignAgent := f.Agent(t, "Foreign owner agent", rtForeign, testutil.Cols{"owner_id": otherOwner})
	f.Task(t, foreignAgent, testutil.Cols{"runtime_id": rtForeign})
	task, err = svc.ClaimTask(ctx, util.MustParseUUID(foreignAgent))
	if err != nil || task == nil {
		t.Fatalf("same daemon name from another owner: task=%v err=%v", task, err)
	}
	t.Log("unrelated machines and other owners remain independent")

}
