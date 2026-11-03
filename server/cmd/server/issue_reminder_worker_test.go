package main

import (
	"context"
	"github.com/google/uuid"
	"net/http"
	"strings"
	"testing"
)

func TestOwnerReminderWorkerAuthorization(t *testing.T) {
	t.Setenv("MULTICA_ISSUE_WATCHDOG_ENABLED", "false")
	t.Setenv("MULTICA_ISSUE_WATCHDOG_WORKSPACE_ID", testWorkspaceID)
	ctx := context.Background()
	var oldRole string
	if err := testPool.QueryRow(ctx, `SELECT role FROM member WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, testUserID).Scan(&oldRole); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `UPDATE member SET role=$3 WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, testUserID, oldRole)
	})
	path := "/api/workspaces/" + testWorkspaceID + "/issue-reminders/claim"
	for _, role := range []string{"owner", "admin", "member", "guest"} {
		if _, err := testPool.Exec(ctx, `UPDATE member SET role=$3 WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, testUserID, role); err != nil {
			t.Fatal(err)
		}
		response := authRequest(t, http.MethodPost, path, map[string]any{"claim_token": uuid.NewString()})
		response.Body.Close()
		want := http.StatusForbidden
		if role == "owner" || role == "admin" {
			want = http.StatusOK
		}
		if response.StatusCode != want {
			t.Fatalf("%s claim status=%d want=%d", role, response.StatusCode, want)
		}
	}
	if _, err := testPool.Exec(ctx, `UPDATE member SET role='owner' WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, testUserID); err != nil {
		t.Fatal(err)
	}
	var agentID string
	if err := testPool.QueryRow(ctx, `SELECT id::text FROM agent WHERE workspace_id=$1 LIMIT 1`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	taskID := ensureAgentTask(t, agentID)
	token := mintAgentTaskToken(t, agentID, taskID, testUserID)
	for _, suffix := range []string{"claim", "1/receipt"} {
		req, err := http.NewRequest(http.MethodPost, ""+testServer.URL+"/api/workspaces/"+testWorkspaceID+"/issue-reminders/"+suffix, strings.NewReader(`{"claim_token":"`+uuid.NewString()+`","result":"sent"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Actor-Source", "member")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("task token %s status=%d", suffix, resp.StatusCode)
		}
	}
}
