package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/workspacelink"
)

// TestManagedLinkThroughRouter drives DENE-1663 end to end on the real
// router: a viewer-side agent run, carrying the linked-workspace header,
// works on the source's issues and autopilots as the person who started the
// run, and every way out of that grant is refused.
func TestManagedLinkThroughRouter(t *testing.T) {
	if testServer == nil {
		t.Skip("integration server not available")
	}
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := testPool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	scan := func(sql string, args ...any) string {
		t.Helper()
		var v string
		if err := testPool.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return v
	}

	const sourceSlug = "dene1663-managed-source"
	exec(`DELETE FROM workspace WHERE slug = $1`, sourceSlug)
	exec(`DELETE FROM "user" WHERE email IN ('dene1663-originator@multica.ai', 'dene1663-outsider@multica.ai')`)
	source := scan(`INSERT INTO workspace (name, slug, description, issue_prefix) VALUES ('Managed Source', $1, '', 'MGD') RETURNING id::text`, sourceSlug)
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, source)
		testPool.Exec(ctx, `DELETE FROM "user" WHERE email IN ('dene1663-originator@multica.ai', 'dene1663-outsider@multica.ai')`)
	})
	originator := scan(`INSERT INTO "user" (name, email) VALUES ('Originator', 'dene1663-originator@multica.ai') RETURNING id::text`)
	outsider := scan(`INSERT INTO "user" (name, email) VALUES ('Outsider', 'dene1663-outsider@multica.ai') RETURNING id::text`)
	exec(`INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`, source, testUserID)
	exec(`INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'admin')`, source, originator)
	exec(`INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'member'), ($1, $3, 'member')`, testWorkspaceID, originator, outsider)

	sourceRuntime := scan(`INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, last_seen_at)
		VALUES ($1, 'src rt', 'cloud', 'integration_test_runtime', 'online', '', '{}'::jsonb, $2, now()) RETURNING id::text`, source, testUserID)
	sourceAgent := scan(`INSERT INTO agent (workspace_id, name, description, runtime_mode, runtime_config, runtime_id, visibility, max_concurrent_tasks, owner_id)
		VALUES ($1, 'Source Agent', '', 'cloud', '{}'::jsonb, $2, 'workspace', 1, $3) RETURNING id::text`, source, sourceRuntime, testUserID)

	// The viewer-side run: the runtime belongs to testUser, the run was
	// started by originator.
	agentID := scan(`SELECT id::text FROM agent WHERE workspace_id = $1 ORDER BY created_at LIMIT 1`, testWorkspaceID)
	agentName := scan(`SELECT name FROM agent WHERE id = $1`, agentID)
	runFor := func(user string) string {
		t.Helper()
		task := scan(`INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, originator_user_id, accountable_user_id)
			SELECT id, runtime_id, 'running', 0, $2, $2 FROM agent WHERE id = $1 RETURNING id::text`, agentID, user)
		t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, task) })
		return mintAgentTaskToken(t, agentID, task, testUserID)
	}
	token := runFor(originator)

	link := scan(`INSERT INTO workspace_link (source_workspace_id, target_workspace_id, status, created_by)
		VALUES ($1, $2, 'active', $3) RETURNING id::text`, source, testWorkspaceID, testUserID)

	do := func(bearer, method, path string, body any, linked bool) (int, map[string]any) {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			json.NewEncoder(&buf).Encode(body)
		}
		req, _ := http.NewRequest(method, testServer.URL+path, &buf)
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		if linked {
			req.Header.Set(workspacelink.LinkedWorkspaceHeader, sourceSlug)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		out := map[string]any{}
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// Managed is off: the link alone lends nothing.
	if code, _ := do(token, "GET", "/api/issues", nil, true); code != http.StatusForbidden {
		t.Fatalf("managed off: status %d, want 403", code)
	}
	exec(`UPDATE workspace_link SET managed = true WHERE id = $1`, link)

	// Issue: create, change status, comment.
	code, issue := do(token, "POST", "/api/issues", map[string]any{"title": "made through the link"}, true)
	if code != http.StatusCreated {
		t.Fatalf("create issue: %d %v", code, issue)
	}
	issueID, _ := issue["id"].(string)
	if ws := scan(`SELECT workspace_id::text FROM issue WHERE id = $1`, issueID); ws != source {
		t.Fatalf("issue landed in %s, want the source", ws)
	}
	if typ, id := scan(`SELECT creator_type FROM issue WHERE id = $1`, issueID), scan(`SELECT creator_id::text FROM issue WHERE id = $1`, issueID); typ != "member" || id != originator {
		t.Fatalf("issue creator = %s %s, want the originator", typ, id)
	}
	if code, body := do(token, "PUT", "/api/issues/"+issueID, map[string]any{"status": "in_progress"}, true); code != http.StatusOK {
		t.Fatalf("update issue: %d %v", code, body)
	}
	if code, body := do(token, "POST", "/api/issues/"+issueID+"/comments", map[string]any{"content": "from the viewer"}, true); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("comment: %d %v", code, body)
	}
	if n := scan(`SELECT count(*)::text FROM activity_log WHERE issue_id = $1 AND action = 'linked_write'
		AND actor_id = $2 AND details->>'agent_name' = $3 AND details->>'via_slug' = $4`, issueID, originator, agentName, integrationTestWorkspaceSlug); n != "3" {
		t.Fatalf("linked_write activity rows = %s, want 3", n)
	}

	// Autopilot: create, pause, resume, then delete.
	code, ap := do(token, "POST", "/api/autopilots", map[string]any{
		"title": "linked autopilot", "assignee_id": sourceAgent, "execution_mode": "create_issue",
	}, true)
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create autopilot: %d %v", code, ap)
	}
	apID, _ := ap["id"].(string)
	if ws := scan(`SELECT workspace_id::text FROM autopilot WHERE id = $1`, apID); ws != source {
		t.Fatalf("autopilot landed in %s", ws)
	}
	for _, status := range []string{"paused", "active"} {
		if code, body := do(token, "PATCH", "/api/autopilots/"+apID, map[string]any{"status": status}, true); code != http.StatusOK {
			t.Fatalf("autopilot %s: %d %v", status, code, body)
		}
		if got := scan(`SELECT status FROM autopilot WHERE id = $1`, apID); got != status {
			t.Fatalf("autopilot status = %s, want %s", got, status)
		}
	}
	if n := scan(`SELECT count(*)::text FROM workspace_link_audit WHERE link_id = $1 AND action = 'managed_write' AND detail->>'autopilot_id' = $2`, link, apID); n != "2" {
		t.Fatalf("autopilot audit rows = %s, want 2 (pause, resume; create has no id in the path)", n)
	}

	// Not on the whitelist: members, settings, link management.
	for _, c := range []struct{ method, path string }{
		{"PATCH", "/api/workspaces/" + source},
		{"POST", "/api/workspaces/" + source + "/members"},
		{"GET", "/api/workspace-links"},
		{"PATCH", "/api/workspace-links/" + link},
	} {
		if code, _ := do(token, c.method, c.path, map[string]any{}, true); code != http.StatusForbidden {
			t.Errorf("%s %s: %d, want 403", c.method, c.path, code)
		}
	}

	// Originator without rights in the source.
	if code, _ := do(runFor(outsider), "GET", "/api/issues", nil, true); code != http.StatusForbidden {
		t.Fatalf("outsider: %d, want 403", code)
	}

	// Without the header the token still is bound to its own workspace.
	if code, _ := do(token, "GET", "/api/issues/"+issueID, nil, false); code == http.StatusOK {
		t.Fatalf("plain token read the source issue")
	}

	// Switched off, then disconnected: the very next call is refused.
	exec(`UPDATE workspace_link SET managed = false WHERE id = $1`, link)
	if code, _ := do(token, "GET", "/api/issues", nil, true); code != http.StatusForbidden {
		t.Fatalf("after switch-off: %d", code)
	}
	exec(`UPDATE workspace_link SET managed = true WHERE id = $1`, link)
	if code, _ := do(token, "DELETE", "/api/autopilots/"+apID, nil, true); code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("delete autopilot: %d", code)
	}
	exec(`DELETE FROM workspace_link WHERE id = $1`, link)
	if code, _ := do(token, "GET", "/api/issues", nil, true); code != http.StatusForbidden {
		t.Fatalf("after disconnect: %d", code)
	}
}
