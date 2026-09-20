package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/repocache"
)

// initGitRepoWithRemote makes dir a real git working tree whose origin is
// remoteURL. Real git rather than a stub because the endpoint reads remotes
// through the git binary, and a stub there would test the wrong seam.
func initGitRepoWithRemote(t *testing.T, dir, remoteURL string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", remoteURL},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func checkoutBody(repoURL, workspaceID, workDir string) *strings.Reader {
	return strings.NewReader(`{"url":"` + repoURL + `","workspace_id":"` + workspaceID +
		`","workdir":"` + workDir + `","task_id":"task-1"}`)
}

// The defect, end to end: a project carrying both a github_repo and a
// local_directory for one repository downloaded a second copy and left the
// agent working in the one the user could not see.
func TestRepoCheckoutUsesTheProjectsLocalDirectoryInsteadOfCloning(t *testing.T) {
	const workspaceID = "ws-checkout"
	const repoURL = "https://github.com/jeff-kunkun/multica"

	cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
	workDir := t.TempDir()
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, workDir, cache)

	localDir := t.TempDir()
	initGitRepoWithRemote(t, localDir, "git@github.com:jeff-kunkun/multica.git")
	d.registerActiveRepoCheckoutTask("mat_repo_checkout_test", activeRepoCheckoutTask{
		WorkspaceID:    workspaceID,
		TaskID:         "task-1",
		AgentID:        "agent-1",
		AgentName:      "Test Agent",
		WorkDir:        workDir,
		LocalDirectory: &localDirectoryAssignment{AbsPath: localDir, RealPath: localDir},
	})

	rec := httptest.NewRecorder()
	d.repoCheckoutHandler().ServeHTTP(rec, authorizedRepoCheckoutRequest(checkoutBody(repoURL, workspaceID, workDir)))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got localCheckoutResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v (%s)", err, rec.Body.String())
	}
	if got.Path != localDir {
		t.Errorf("path = %q, want the project's local directory %q", got.Path, localDir)
	}
	if got.Source != localDirectoryCheckoutSource {
		t.Errorf("source = %q, want %q so the CLI can say nothing was cloned", got.Source, localDirectoryCheckoutSource)
	}
	// The point of the change: no network work happened at all.
	if params := cache.lastCreateParams(); params != (repocache.WorktreeParams{}) {
		t.Fatalf("the repo cache was still asked to clone: %+v", params)
	}
}

// A project pinned to one directory may legitimately reference other
// repositories. Pinning must not break them.
func TestRepoCheckoutStillClonesARepoTheLocalDirectoryDoesNotHold(t *testing.T) {
	const workspaceID = "ws-checkout"
	const repoURL = "https://github.com/org/repo.git"

	cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
	workDir := t.TempDir()
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, workDir, cache)

	localDir := t.TempDir()
	initGitRepoWithRemote(t, localDir, "git@github.com:jeff-kunkun/multica.git")
	d.registerActiveRepoCheckoutTask("mat_repo_checkout_test", activeRepoCheckoutTask{
		WorkspaceID:    workspaceID,
		TaskID:         "task-1",
		AgentID:        "agent-1",
		AgentName:      "Test Agent",
		WorkDir:        workDir,
		LocalDirectory: &localDirectoryAssignment{AbsPath: localDir, RealPath: localDir},
	})

	rec := httptest.NewRecorder()
	d.repoCheckoutHandler().ServeHTTP(rec, authorizedRepoCheckoutRequest(checkoutBody(repoURL, workspaceID, workDir)))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if cache.lastCreateParams().RepoURL != repoURL {
		t.Fatalf("an unrelated repository must still be checked out, got %+v", cache.lastCreateParams())
	}
}

// The directory is named after the repository but proves nothing. Falling back
// to the network here is exactly the behaviour being removed, so the request
// must end with a reason instead.
func TestRepoCheckoutRefusesRatherThanFallingBackToTheNetwork(t *testing.T) {
	const workspaceID = "ws-checkout"
	const repoURL = "https://github.com/jeff-kunkun/multica"

	cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
	workDir := t.TempDir()
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, workDir, cache)

	// A plain directory called "multica": no git tree, no remotes.
	localDir := t.TempDir() + "/multica"
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatal(err)
	}
	d.registerActiveRepoCheckoutTask("mat_repo_checkout_test", activeRepoCheckoutTask{
		WorkspaceID:    workspaceID,
		TaskID:         "task-1",
		AgentID:        "agent-1",
		AgentName:      "Test Agent",
		WorkDir:        workDir,
		LocalDirectory: &localDirectoryAssignment{AbsPath: localDir, RealPath: localDir},
	})

	rec := httptest.NewRecorder()
	d.repoCheckoutHandler().ServeHTTP(rec, authorizedRepoCheckoutRequest(checkoutBody(repoURL, workspaceID, workDir)))

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if params := cache.lastCreateParams(); params != (repocache.WorktreeParams{}) {
		t.Fatalf("a refused checkout still reached the repo cache: %+v", params)
	}
	for _, want := range []string{localDir, repoURL, "Refusing to clone a second copy"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("refusal must name %q; got: %s", want, rec.Body.String())
		}
	}
}
