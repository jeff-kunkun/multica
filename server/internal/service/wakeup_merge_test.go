package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestIssueWakeWaitsForStopCommitAndRechecksHalt(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	_, _, agentID, issueID := seedAttributionFixture(t, pool)
	q := db.New(pool)
	svc := &TaskService{Queries: q, TxStarter: pool}
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "explicit"}[explicit], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			id, _ := util.ParseUUID(issueID)
			agent, _ := util.ParseUUID(agentID)
			if _, err := pool.Exec(ctx, `UPDATE issue SET metadata='{}' WHERE id=$1`, issueID); err != nil {
				t.Fatal(err)
			}
			stop, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer stop.Rollback(ctx)
			if _, err = q.WithTx(stop).LockIssueRunControl(ctx, id); err != nil {
				t.Fatal(err)
			}
			if explicit {
				ctx = WithSeparateIssueRun(ctx)
			}
			result := make(chan error, 1)
			go func() {
				_, tx, e := svc.lockDefaultIssueEnqueue(ctx, id, agent, true, pgtype.UUID{}, pgtype.UUID{}, attribution.Result{})
				if tx != nil {
					_ = tx.Rollback(ctx)
				}
				result <- e
			}()
			select {
			case err := <-result:
				t.Fatalf("enqueue crossed uncommitted stop: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			if _, err = stop.Exec(ctx, `UPDATE issue SET metadata='{"agent_halted":true}' WHERE id=$1`, issueID); err != nil {
				t.Fatal(err)
			}
			if err = stop.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-result; !errors.Is(err, ErrAgentChainBudgetExceeded) {
				t.Fatalf("stopped issue admitted wake: %v", err)
			}
		})
	}
}
