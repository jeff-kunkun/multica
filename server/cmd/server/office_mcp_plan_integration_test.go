package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The project-manager Bot's read/approve loop through the real router: the
// MCP tools must reach the plan routes, and the Bot's release flag alone
// decides whether it may approve.

type mcpToolResult struct {
	Text    string
	IsError bool
}

func callOfficeMCP(t *testing.T, token, tool string, args map[string]any) mcpToolResult {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	req, _ := http.NewRequest(http.MethodPost, testServer.URL+"/api/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("mcp request: %v", err)
	}
	var out struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error any `json:"error"`
	}
	readJSON(t, resp, &out)
	if resp.StatusCode != http.StatusOK || out.Error != nil || len(out.Result.Content) == 0 {
		t.Fatalf("%s: status %d, error %v", tool, resp.StatusCode, out.Error)
	}
	return mcpToolResult{Text: out.Result.Content[0].Text, IsError: out.Result.IsError}
}

type officeBot struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	Token  string `json:"token"`
}

func createOfficeBot(t *testing.T, name string, canRelease bool) officeBot {
	t.Helper()
	resp := authRequest(t, http.MethodPost, "/api/bots", map[string]any{"name": name, "can_release": canRelease})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create bot: %d", resp.StatusCode)
	}
	var bot officeBot
	readJSON(t, resp, &bot)
	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM project_member WHERE user_id = $1`, bot.UserID)
		testPool.Exec(ctx, `DELETE FROM personal_access_token WHERE user_id = $1`, bot.UserID)
		testPool.Exec(ctx, `DELETE FROM member WHERE user_id = $1`, bot.UserID)
		testPool.Exec(ctx, `DELETE FROM workspace_bot WHERE id = $1`, bot.ID)
		testPool.Exec(ctx, `DELETE FROM "user" WHERE id = $1`, bot.UserID)
	})
	return bot
}

// planIssueSharedWith creates an issue in a project shared with the given
// users, with two plan versions proposed by the owner.
func planIssueSharedWith(t *testing.T, userIDs ...string) string {
	t.Helper()
	ctx := context.Background()
	var projectID string
	if err := testPool.QueryRow(ctx, `INSERT INTO project (workspace_id, title) VALUES ($1, $2) RETURNING id`,
		testWorkspaceID, "MCP plan "+t.Name()).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	for _, userID := range userIDs {
		if _, err := testPool.Exec(ctx, `INSERT INTO project_member (workspace_id, project_id, user_id, created_by) VALUES ($1, $2, $3, $4)`,
			testWorkspaceID, projectID, userID, testUserID); err != nil {
			t.Fatalf("share project: %v", err)
		}
	}
	resp := authRequest(t, http.MethodPost, "/api/issues", map[string]any{"title": "MCP plan " + t.Name(), "project_id": projectID})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create issue: %d", resp.StatusCode)
	}
	var issue struct {
		ID string `json:"id"`
	}
	readJSON(t, resp, &issue)
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM issue WHERE parent_issue_id = $1`, issue.ID)
		testPool.Exec(ctx, `DELETE FROM plan WHERE issue_id = $1`, issue.ID)
		testPool.Exec(ctx, `DELETE FROM comment WHERE issue_id = $1`, issue.ID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issue.ID)
		testPool.Exec(ctx, `DELETE FROM project_member WHERE project_id = $1`, projectID)
		testPool.Exec(ctx, `DELETE FROM project WHERE id = $1`, projectID)
	})
	for range 2 {
		resp := authRequest(t, http.MethodPost, "/api/issues/"+issue.ID+"/plans", map[string]any{
			"goal": "Ship it", "out_of_scope": "Nothing else", "done_when": "It ships", "project_id": projectID,
			"steps": []map[string]any{
				{"title": "Build", "assignee_type": "member", "assignee_id": testUserID},
				{"title": "Check", "assignee_type": "member", "assignee_id": testUserID},
			},
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("propose plan: %d", resp.StatusCode)
		}
		resp.Body.Close()
	}
	return issue.ID
}

func childIssues(t *testing.T, parentID string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM issue WHERE parent_issue_id = $1`, parentID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOfficeMCPReadsAndApprovesPlans(t *testing.T) {
	releaser := createOfficeBot(t, "MCP Releaser", true)
	helper := createOfficeBot(t, "MCP Helper", false)
	issueID := planIssueSharedWith(t, releaser.UserID, helper.UserID)

	read := callOfficeMCP(t, releaser.Token, "read_plan", map[string]any{"issue": issueID})
	if read.IsError {
		t.Fatalf("read_plan: %s", read.Text)
	}
	var plans struct {
		Plans []struct {
			Version int    `json:"version"`
			Status  string `json:"status"`
		} `json:"plans"`
		CanApprove bool `json:"can_approve"`
	}
	if err := json.Unmarshal([]byte(read.Text), &plans); err != nil {
		t.Fatalf("read_plan result %q: %v", read.Text, err)
	}
	if len(plans.Plans) != 2 || plans.Plans[0].Version != 2 || !plans.CanApprove {
		t.Fatalf("read_plan = %+v", plans)
	}

	// A Bot without release is refused and starts nothing.
	if got := callOfficeMCP(t, helper.Token, "approve_plan", map[string]any{"issue": issueID, "version": 2}); !got.IsError || !strings.Contains(got.Text, "403") {
		t.Fatalf("helper approve_plan = %+v", got)
	}
	// The replaced version cannot be approved.
	if got := callOfficeMCP(t, releaser.Token, "approve_plan", map[string]any{"issue": issueID, "version": 1}); !got.IsError || !strings.Contains(got.Text, "409") {
		t.Fatalf("stale approve_plan = %+v", got)
	}
	if n := childIssues(t, issueID); n != 0 {
		t.Fatalf("refused approvals created %d issues", n)
	}

	if got := callOfficeMCP(t, releaser.Token, "approve_plan", map[string]any{"issue": issueID, "version": 2}); got.IsError {
		t.Fatalf("approve_plan: %s", got.Text)
	}
	if n := childIssues(t, issueID); n != 2 {
		t.Fatalf("approval created %d issues, want 2", n)
	}

	// Revoking the flag stops the Bot on the next plan.
	resp := authRequest(t, http.MethodPatch, "/api/bots/"+releaser.ID, map[string]any{"can_release": false})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke release: %d", resp.StatusCode)
	}
	resp.Body.Close()
	next := planIssueSharedWith(t, releaser.UserID)
	if got := callOfficeMCP(t, releaser.Token, "approve_plan", map[string]any{"issue": next, "version": 2}); !got.IsError || !strings.Contains(got.Text, "403") {
		t.Fatalf("revoked approve_plan = %+v", got)
	}
	if n := childIssues(t, next); n != 0 {
		t.Fatalf("revoked bot created %d issues", n)
	}
}
