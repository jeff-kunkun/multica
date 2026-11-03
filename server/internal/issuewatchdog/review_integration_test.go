package issuewatchdog

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/testutil"
	"path/filepath"
	"testing"
	"time"
)

type reviewBench struct {
	t                                                *testing.T
	ctx                                              context.Context
	pool                                             *pgxpool.Pool
	fx                                               *testutil.Fixture
	workspaceID, dev, reviewer, runtimeID, projectID string
	properties                                       []byte
	now                                              time.Time
}

func newReviewBench(t *testing.T) *reviewBench {
	ctx := context.Background()
	pool := testutil.OpenTestDatabase(ctx, t)
	fx, ws, dev, _, seed := watchdogFixture(t, pool)
	b := &reviewBench{t: t, ctx: ctx, pool: pool, fx: fx, workspaceID: ws, dev: dev, now: time.Now().UTC().Truncate(time.Second)}
	if err := pool.QueryRow(ctx, `SELECT project_id::text,properties FROM issue WHERE id=$1::uuid`, seed).Scan(&b.projectID, &b.properties); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT runtime_id::text FROM agent WHERE id=$1::uuid`, dev).Scan(&b.runtimeID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE issue SET status='backlog' WHERE id=$1::uuid`, seed); err != nil {
		t.Fatal(err)
	}
	b.reviewer = fx.Agent(t, "Reviewer "+seed[:8], b.runtimeID)
	return b
}
func (b *reviewBench) watchdog(cfg Config) *Watchdog {
	cfg.Enabled = true
	cfg.WorkspaceID = b.workspaceID
	cfg.Owners = OwnerTable{ProjectManagers: map[string]string{b.projectID: "CTO·扳手"}}
	cfg.HeartbeatFile = filepath.Join(b.t.TempDir(), "last-run")
	w := New(b.pool, cfg, nil)
	w.now = func() time.Time { return b.now }
	return w
}
func (b *reviewBench) issue(title, status, assignee string, cols testutil.Cols) string {
	base := testutil.Cols{"project_id": b.projectID, "properties": json.RawMessage(b.properties), "status": status,
		"created_at": b.now.Add(-2 * time.Hour), "updated_at": b.now.Add(-2 * time.Hour)}
	if assignee != "" {
		base["assignee_type"], base["assignee_id"] = "agent", assignee
	}
	for k, v := range cols {
		base[k] = v
	}
	id := b.fx.Issue(b.t, title, base)
	if _, err := b.pool.Exec(b.ctx, `UPDATE office_event SET occurred_at=$2 WHERE issue_id=$1::uuid`, id, b.now.Add(-2*time.Hour)); err != nil {
		b.t.Fatal(err)
	}
	return id
}
func (b *reviewBench) run(w *Watchdog, at time.Time) {
	b.t.Helper()
	w.now = func() time.Time { return at }
	if err := w.Run(b.ctx, at); err != nil {
		b.t.Fatal(err)
	}
}
func TestReviewRunCountsEvenWhenReviewerIsNotAssignee(t *testing.T) {
	b := newReviewBench(t)
	id := b.issue("Review in flight", "in_review", b.dev, nil)
	b.fx.Insert(t, "agent_task_queue", testutil.Cols{"issue_id": id, "agent_id": b.reviewer, "status": "running", "runtime_id": b.runtimeID, "started_at": b.now.Add(-time.Hour)})
	w := b.watchdog(Config{})
	b.run(w, b.now)
	var count int
	if err := b.pool.QueryRow(b.ctx, `SELECT count(*) FROM office_issue_watchdog_attempt WHERE issue_id=$1::uuid`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("live reviewer was treated as stalled")
	}
}
