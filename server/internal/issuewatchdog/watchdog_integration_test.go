package issuewatchdog

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestSingleOwnerReminderPerStall(t *testing.T) {
	b := newReviewBench(t)
	id := b.issue("Stalled test", "in_progress", b.dev, nil)
	w := b.watchdog(Config{})
	b.run(w, b.now)
	b.run(w, b.now.Add(3*time.Hour))
	var count int
	var manager, target string
	if err := b.pool.QueryRow(b.ctx, `SELECT count(*),min(manager_name),min(target) FROM office_issue_watchdog_attempt WHERE issue_id=$1::uuid AND event_type=$2`, id, ownerStalledEvent).Scan(&count, &manager, &target); err != nil {
		t.Fatal(err)
	}
	if count != 1 || manager != "CTO·扳手" || target != "manager" {
		t.Fatalf("count=%d manager=%s target=%s", count, manager, target)
	}
	if _, err := b.pool.Exec(b.ctx, `UPDATE issue SET updated_at=$2 WHERE id=$1::uuid`, id, b.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	b.run(w, b.now.Add(3*time.Hour))
	if count = b.reminders(id, ownerStalledEvent); count != 2 {
		t.Fatalf("new progress must start new episode: %d", count)
	}
}

func watchdogFixture(t *testing.T, pool *pgxpool.Pool) (*testutil.Fixture, string, string, string, string) {
	t.Helper()
	fx := testutil.New(pool, "", "")
	userID := fx.Insert(t, "user", testutil.Cols{
		"name": "Watchdog Test", "email": "watchdog-" + uuid.NewString() + "@example.invalid",
	})
	workspaceID := fx.Insert(t, "workspace", testutil.Cols{
		"name": "Issue Watchdog Test", "slug": "issue-watchdog-" + uuid.NewString(), "issue_prefix": "TST",
	})
	fx.WorkspaceID, fx.UserID = workspaceID, userID
	fx.Member(t, workspaceID, userID, "owner")
	runtimeID := fx.Runtime(t, "Watchdog test runtime")
	agentID := fx.Agent(t, "Watchdog employee", runtimeID)
	projectName := "Watchdog project " + uuid.NewString()
	projectID := fx.Project(t, projectName)
	optionID := uuid.NewString()
	propertyID := fx.Insert(t, "issue_property", testutil.Cols{
		"workspace_id": workspaceID, "name": "负责经理", "type": "select",
		"config": json.RawMessage(fmt.Sprintf(`{"options":[{"id":%q,"name":"CTO·扳手"}]}`, optionID)),
	})
	properties, err := json.Marshal(map[string]string{propertyID: optionID})
	if err != nil {
		t.Fatal(err)
	}
	issueID := fx.Issue(t, "Watchdog full cycle", testutil.Cols{
		"workspace_id": workspaceID, "project_id": projectID,
		"status": "todo", "assignee_type": "agent", "assignee_id": agentID,
		"properties": properties,
	})
	return fx, workspaceID, agentID, projectName, issueID
}

func testUserIDForWorkspace(t *testing.T, pool *pgxpool.Pool, workspaceID string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `
SELECT user_id::text FROM member WHERE workspace_id = $1::uuid LIMIT 1`, workspaceID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
