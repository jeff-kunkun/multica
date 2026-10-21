package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func deleteAgentAs(userID, agentID string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := withURLParam(newRequestAs(userID, http.MethodDelete, "/api/agents/"+agentID, nil), "id", agentID)
	testHandler.DeleteAgent(w, req)
	return w
}

func agentRowCount(t *testing.T, agentID string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent WHERE id = $1`, agentID).Scan(&n); err != nil {
		t.Fatalf("count agent rows: %v", err)
	}
	return n
}

func cleanupAgentBackups(t *testing.T, agentID string) {
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_backup WHERE agent_id = $1`, agentID)
	})
}

// TestDeleteAgent_BacksUpDeletesAndRestores walks the whole lifecycle: the
// delete keeps a backup with the env values, really removes the agent and its
// tasks, never echoes a secret, and the backup brings the agent back intact.
func TestDeleteAgent_BacksUpDeletesAndRestores(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	agentID, ownerUserID := agentEnvOwnerFixture(t, "delete-lifecycle-agent", "delete-lifecycle-owner@multica.test")
	cleanupAgentBackups(t, agentID)
	createHandlerTestTaskForAgent(t, agentID)
	issueID := dbfx.Issue(t, "delete-lifecycle-assigned", testutil.Cols{
		"assignee_type": "agent",
		"assignee_id":   agentID,
	})

	w := deleteAgentAs(ownerUserID, agentID)
	if w.Code != http.StatusOK {
		t.Fatalf("DeleteAgent as agent owner: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret-value") {
		t.Fatalf("delete response must not carry env values: %s", w.Body.String())
	}
	var resp DeleteAgentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	if resp.Backup.ID == "" || resp.Backup.EnvKeyCount != 1 {
		t.Fatalf("expected a backup holding 1 env key, got %+v", resp.Backup)
	}

	if n := agentRowCount(t, agentID); n != 0 {
		t.Fatalf("agent row still present after delete")
	}
	var tasks, targets int
	testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE agent_id = $1`, agentID).Scan(&tasks)
	testPool.QueryRow(ctx, `SELECT count(*) FROM agent_invocation_target WHERE agent_id = $1`, agentID).Scan(&targets)
	if tasks != 0 || targets != 0 {
		t.Fatalf("expected tasks and invocation targets gone, got tasks=%d targets=%d", tasks, targets)
	}
	var stillAssigned bool
	testPool.QueryRow(ctx, `SELECT assignee_id IS NOT NULL FROM issue WHERE id = $1`, issueID).Scan(&stillAssigned)
	if stillAssigned {
		t.Fatalf("issues assigned to the deleted agent must be unassigned")
	}
	var storedEnv string
	if err := testPool.QueryRow(ctx, `SELECT snapshot->'agent'->>'custom_env' FROM agent_backup WHERE id = $1`, resp.Backup.ID).Scan(&storedEnv); err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if !strings.Contains(storedEnv, "secret-value") {
		t.Fatalf("backup must hold the env values, got %s", storedEnv)
	}

	w = httptest.NewRecorder()
	testHandler.ListAgentBackups(w, newRequestAs(ownerUserID, http.MethodGet, "/api/agent-backups", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), resp.Backup.ID) {
		t.Fatalf("ListAgentBackups: expected the backup, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret-value") {
		t.Fatalf("backup list must not carry env values: %s", w.Body.String())
	}

	restore := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := withURLParam(newRequestAs(ownerUserID, http.MethodPost, "/api/agent-backups/"+resp.Backup.ID+"/restore", nil), "backupId", resp.Backup.ID)
		testHandler.RestoreAgentBackup(w, req)
		return w
	}
	if w := restore(); w.Code != http.StatusOK {
		t.Fatalf("RestoreAgentBackup: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var restoredEnv string
	var archived bool
	if err := testPool.QueryRow(ctx, `SELECT custom_env::text, archived_at IS NOT NULL FROM agent WHERE id = $1`, agentID).Scan(&restoredEnv, &archived); err != nil {
		t.Fatalf("read restored agent: %v", err)
	}
	if !strings.Contains(restoredEnv, "secret-value") || archived {
		t.Fatalf("restored agent should carry its env and be active, env=%s archived=%v", restoredEnv, archived)
	}
	testPool.QueryRow(ctx, `SELECT count(*) FROM agent_invocation_target WHERE agent_id = $1`, agentID).Scan(&targets)
	if targets != 1 {
		t.Fatalf("expected the invocation target restored, got %d", targets)
	}
	if w := restore(); w.Code != http.StatusConflict {
		t.Fatalf("restoring over a live agent: expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

// TestDeleteAgent_StopsWhenBackupFails: no backup, no delete.
func TestDeleteAgent_StopsWhenBackupFails(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	agentID, ownerUserID := agentEnvOwnerFixture(t, "delete-backup-fails-agent", "delete-backup-fails@multica.test")
	cleanupAgentBackups(t, agentID)
	if _, err := testPool.Exec(ctx, `ALTER TABLE agent_backup ADD CONSTRAINT agent_backup_test_reject CHECK (agent_name <> 'delete-backup-fails-agent') NOT VALID`); err != nil {
		t.Fatalf("add rejecting constraint: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `ALTER TABLE agent_backup DROP CONSTRAINT IF EXISTS agent_backup_test_reject`)
	})

	w := deleteAgentAs(ownerUserID, agentID)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "not deleted") {
		t.Fatalf("expected the delete to stop on a failed backup, got %d: %s", w.Code, w.Body.String())
	}
	if n := agentRowCount(t, agentID); n != 1 {
		t.Fatalf("agent must survive a failed backup")
	}
}

// TestDeleteAgent_KeepsTheEnvGate: deleting copies env values into a backup,
// so only someone who may read them can delete — never an unrelated member or
// an agent acting for the owner.
func TestDeleteAgent_KeepsTheEnvGate(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	agentID, ownerUserID := agentEnvOwnerFixture(t, "delete-gate-agent", "delete-gate-owner@multica.test")
	cleanupAgentBackups(t, agentID)
	strangerID := createPermissionTestMember(t, "delete-gate-stranger@multica.test")

	if w := deleteAgentAs(strangerID, agentID); w.Code != http.StatusForbidden {
		t.Fatalf("unrelated member: expected 403, got %d: %s", w.Code, w.Body.String())
	}

	hostAgentID := createHandlerTestAgent(t, "delete-gate-host-agent")
	hostTaskID := createHandlerTestTaskForAgent(t, hostAgentID)
	w := httptest.NewRecorder()
	req := withURLParam(newRequestAs(ownerUserID, http.MethodDelete, "/api/agents/"+agentID, nil), "id", agentID)
	req.Header.Set("X-Agent-ID", hostAgentID)
	req.Header.Set("X-Task-ID", hostTaskID)
	testHandler.DeleteAgent(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("agent actor: expected 403, got %d: %s", w.Code, w.Body.String())
	}

	if n := agentRowCount(t, agentID); n != 1 {
		t.Fatalf("a rejected delete must leave the agent in place")
	}
	var backups int
	testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_backup WHERE agent_id = $1`, agentID).Scan(&backups)
	if backups != 0 {
		t.Fatalf("a rejected delete must not write a backup, got %d", backups)
	}
}
