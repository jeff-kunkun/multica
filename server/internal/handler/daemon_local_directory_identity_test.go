package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestBackfillLocalDirectoryIdentityFillsMissingRealPath(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	project := createTestProjectForResources(t, "DENE-618 identity backfill")
	const daemonID = "daemon-backfill-identity"
	created := createLocalDirectoryResourceFor(t, project.ID, map[string]any{
		"local_path": "/tmp/legacy-dir",
		"daemon_id":  daemonID,
	})

	req := newDaemonTokenRequest("POST", "/api/daemon/project-resources/"+created.ID+"/identity", map[string]any{
		"real_path":   "/private/tmp/legacy-dir",
		"repo_key":    "https://github.com/Acme/App.git",
		"is_git_repo": true,
	}, testWorkspaceID, daemonID)
	req = withURLParam(req, "resourceId", created.ID)
	w := testutil.Call(t, testHandler.BackfillLocalDirectoryIdentity, req).Want(http.StatusOK)

	var resp struct {
		Applied bool `json:"applied"`
	}
	w.JSON(&resp)
	if !resp.Applied {
		t.Fatal("expected the missing identity to be written")
	}

	got := getProjectResource(t, project.ID, created.ID)
	var ref localDirectoryRef
	if err := json.Unmarshal(got.ResourceRef, &ref); err != nil {
		t.Fatal(err)
	}
	if ref.RealPath != "/private/tmp/legacy-dir" {
		t.Errorf("real_path = %q", ref.RealPath)
	}
	if ref.RepoKey != "github.com/acme/app" {
		t.Errorf("repo_key = %q, want normalized github.com/acme/app", ref.RepoKey)
	}
	if ref.IsGitRepo == nil || !*ref.IsGitRepo {
		t.Error("is_git_repo was not stored")
	}
}

func TestBackfillLocalDirectoryIdentityConflictDoesNotFailTheDaemon(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	project := createTestProjectForResources(t, "DENE-618 identity backfill conflict")
	const daemonID = "daemon-backfill-conflict"
	first := createLocalDirectoryResourceFor(t, project.ID, map[string]any{
		"local_path": "/private/tmp/same-dir",
		"daemon_id":  daemonID,
	})
	second := createLocalDirectoryResourceFor(t, project.ID, map[string]any{
		"local_path": "/tmp/same-dir",
		"daemon_id":  daemonID,
	})

	req := newDaemonTokenRequest("POST", "/api/daemon/project-resources/"+second.ID+"/identity", map[string]any{
		"real_path": "/private/tmp/same-dir",
	}, testWorkspaceID, daemonID)
	req = withURLParam(req, "resourceId", second.ID)
	w := testutil.Call(t, testHandler.BackfillLocalDirectoryIdentity, req).Want(http.StatusOK)

	var resp struct {
		Applied  bool `json:"applied"`
		Conflict bool `json:"conflict"`
	}
	w.JSON(&resp)
	if resp.Applied || !resp.Conflict {
		t.Fatalf("expected a skipped conflict, got applied=%v conflict=%v", resp.Applied, resp.Conflict)
	}

	got := getProjectResource(t, project.ID, second.ID)
	var ref localDirectoryRef
	if err := json.Unmarshal(got.ResourceRef, &ref); err != nil {
		t.Fatal(err)
	}
	if ref.RealPath != "" {
		t.Errorf("conflicting backfill must leave the second row untouched, got real_path %q", ref.RealPath)
	}
	_ = first
}

func TestBackfillLocalDirectoryIdentityRefusesAnotherDaemonsRow(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	project := createTestProjectForResources(t, "DENE-618 identity backfill ownership")
	created := createLocalDirectoryResourceFor(t, project.ID, map[string]any{
		"local_path": "/tmp/owned-elsewhere",
		"daemon_id":  "daemon-other",
	})
	req := newDaemonTokenRequest("POST", "/api/daemon/project-resources/"+created.ID+"/identity", map[string]any{
		"real_path": "/private/tmp/owned-elsewhere",
	}, testWorkspaceID, "daemon-this")
	req = withURLParam(req, "resourceId", created.ID)
	testutil.Call(t, testHandler.BackfillLocalDirectoryIdentity, req).Want(http.StatusForbidden)
}

// A daemon signed in with a personal token has no daemon id on its credential.
// It names its task's runtime instead; before DENE-2667 PR 8 every such
// daemon logged a 401 here on every task.
func TestBackfillLocalDirectoryIdentityWithAPersonalToken(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	project := createTestProjectForResources(t, "DENE-2673 identity backfill with a personal token")
	const daemonID = "daemon-backfill-pat"
	created := createLocalDirectoryResourceFor(t, project.ID, map[string]any{
		"local_path": "/tmp/pat-dir",
		"daemon_id":  daemonID,
	})
	newRuntime := func(workspaceID, daemonID string) string {
		var id string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, last_seen_at)
			VALUES ($1, $2, 'PAT backfill runtime', 'local', 'claude', 'online', 'pat test', '{}'::jsonb, $3, now())
			RETURNING id
		`, workspaceID, daemonID, testUserID).Scan(&id); err != nil {
			t.Fatalf("create runtime: %v", err)
		}
		t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, id) })
		return id
	}
	runtimeID := newRuntime(testWorkspaceID, daemonID)

	call := func(userID string, body map[string]any) *testutil.Response {
		req := newRequestAs(userID, "POST", "/api/daemon/project-resources/"+created.ID+"/identity", body)
		return testutil.Call(t, testHandler.BackfillLocalDirectoryIdentity, withURLParam(req, "resourceId", created.ID))
	}
	call(testUserID, map[string]any{"real_path": "/private/tmp/pat-dir"}).Want(http.StatusUnauthorized)

	// Someone else's personal token cannot borrow the runtime.
	_, otherUser, _ := runtimeVisibilityFixture(t)
	call(otherUser, map[string]any{"real_path": "/private/tmp/pat-dir", "runtime_id": runtimeID}).Want(http.StatusNotFound)
	// The caller's own runtime on another machine does not hold this folder.
	call(testUserID, map[string]any{"real_path": "/private/tmp/pat-dir", "runtime_id": newRuntime(testWorkspaceID, daemonID+"-elsewhere")}).Want(http.StatusForbidden)
	// Nor does the same machine registered in another workspace.
	call(testUserID, map[string]any{"real_path": "/private/tmp/pat-dir", "runtime_id": newRuntime(createOtherTestWorkspace(t), daemonID)}).Want(http.StatusForbidden)
	// A daemon token speaks for its own daemon; a runtime_id in the body
	// cannot make it another one.
	req := newDaemonTokenRequest("POST", "/api/daemon/project-resources/"+created.ID+"/identity", map[string]any{
		"real_path": "/private/tmp/pat-dir", "runtime_id": runtimeID,
	}, testWorkspaceID, "daemon-not-this-one")
	testutil.Call(t, testHandler.BackfillLocalDirectoryIdentity, withURLParam(req, "resourceId", created.ID)).Want(http.StatusForbidden)
	var before localDirectoryRef
	if err := json.Unmarshal(getProjectResource(t, project.ID, created.ID).ResourceRef, &before); err != nil || before.RealPath != "" {
		t.Fatalf("a refused call wrote the identity: %+v %v", before, err)
	}

	w := call(testUserID, map[string]any{"real_path": "/private/tmp/pat-dir", "is_git_repo": false, "runtime_id": runtimeID}).Want(http.StatusOK)
	var resp struct {
		Applied bool `json:"applied"`
	}
	w.JSON(&resp)
	if !resp.Applied {
		t.Fatal("expected the identity to be written")
	}
	var ref localDirectoryRef
	if err := json.Unmarshal(getProjectResource(t, project.ID, created.ID).ResourceRef, &ref); err != nil {
		t.Fatal(err)
	}
	if ref.RealPath != "/private/tmp/pat-dir" {
		t.Errorf("real_path = %q", ref.RealPath)
	}
}

func getProjectResource(t *testing.T, projectID, resourceID string) ProjectResourceResponse {
	t.Helper()
	w := testutil.Call(t, testHandler.ListProjectResources, withURLParam(
		newRequest("GET", "/api/projects/"+projectID+"/resources", nil), "id", projectID,
	)).Want(http.StatusOK)
	var list struct {
		Resources []ProjectResourceResponse `json:"resources"`
	}
	w.JSON(&list)
	for _, r := range list.Resources {
		if r.ID == resourceID {
			return r
		}
	}
	t.Fatalf("resource %s not in project %s", resourceID, projectID)
	return ProjectResourceResponse{}
}

func httptestRecorderCreate(t *testing.T, projectID string, ref map[string]any) *testutil.Response {
	t.Helper()
	req := newRequest("POST", "/api/projects/"+projectID+"/resources", map[string]any{
		"resource_type": "local_directory",
		"resource_ref":  ref,
	})
	req = withURLParam(req, "id", projectID)
	return testutil.Call(t, testHandler.CreateProjectResource, req)
}
