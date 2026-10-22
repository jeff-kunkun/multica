package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/internal/coderesolve"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

func decisionTask(t *testing.T, daemonID string, resources []ProjectResourceData, decision coderesolve.Decision) Task {
	t.Helper()
	return Task{
		ID:               "task-1",
		WorkspaceID:      "ws-1",
		ProjectResources: resources,
		CodeDecision:     &decision,
	}
}

func TestDecisionNamesTheDirectoryInsteadOfTheFirstRow(t *testing.T) {
	const daemonID = "d-mine"
	first, second := t.TempDir(), t.TempDir()
	resources := []ProjectResourceData{
		localDirResource(t, "r1", first, daemonID),
		localDirResource(t, "r2", second, daemonID),
	}
	decision := coderesolve.NewLocalInPlace(coderesolve.LocalTarget{
		ResourceID: "r2",
		Path:       second,
	}).WithReadOnly([]coderesolve.Dir{{
		ResourceID: "r1",
		Path:       first,
		Name:       "first",
	}})

	chosen, readOnly, err := localDirectoryPlanForTask(decisionTask(t, daemonID, resources, decision), daemonID)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if chosen == nil || chosen.AbsPath != second || chosen.ResourceID != "r2" {
		t.Fatalf("chosen = %+v, want the decision's directory %q", chosen, second)
	}
	if len(readOnly) != 1 || readOnly[0].AbsPath != first {
		t.Fatalf("read-only = %+v, want only %q", readOnly, first)
	}
	if readOnly[0].RealPath == chosen.RealPath && first != second {
		t.Fatalf("read-only directory collapsed onto the writable one")
	}
}

func TestDecisionDoesNotFallThroughToAnotherDirectory(t *testing.T) {
	const daemonID = "d-mine"
	here := t.TempDir()
	resources := []ProjectResourceData{
		localDirResource(t, "r-other", here, daemonID),
	}
	decision := coderesolve.NewLocalInPlace(coderesolve.LocalTarget{
		ResourceID: "r-missing",
		Path:       here,
	})

	chosen, _, err := localDirectoryPlanForTask(decisionTask(t, daemonID, resources, decision), daemonID)
	if chosen != nil {
		t.Fatalf("chosen = %+v, want no directory — a missing id must not pick %q", chosen, here)
	}
	var placed *codePlacementError
	if err == nil || !asPlacement(err, &placed) || placed.Code != string(coderesolve.CodePinnedResourceNotFound) {
		t.Fatalf("err = %v, want pinned_resource_not_found", err)
	}
}

func TestDecisionUnresolvableFailsWithItsCode(t *testing.T) {
	decision := coderesolve.NewUnresolvable(coderesolve.CodeMalformedResource, "resource_ref is not readable")
	_, _, err := localDirectoryPlanForTask(Task{ID: "t", CodeDecision: &decision}, "d-mine")
	var placed *codePlacementError
	if err == nil || !asPlacement(err, &placed) || placed.Code != string(coderesolve.CodeMalformedResource) {
		t.Fatalf("err = %v, want malformed_resource", err)
	}
}

func TestDecisionRemoteCacheDoesNotPickALocalDirectory(t *testing.T) {
	const daemonID = "d-mine"
	dir := t.TempDir()
	decision := coderesolve.NewRemoteCache(coderesolve.RemoteTarget{URL: "https://github.com/o/r"})
	chosen, _, err := localDirectoryPlanForTask(decisionTask(t, daemonID, []ProjectResourceData{
		localDirResource(t, "r1", dir, daemonID),
	}, decision), daemonID)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if chosen != nil {
		t.Fatalf("remote decision chose local directory %+v", chosen)
	}
}

// Nothing queues on a folder: two tasks in one folder both run, and each is
// told about the other. Leaving is idempotent so a deferred leave after an
// early return cannot drop someone else's entry.
func TestFolderRunsListTheOtherTasksInTheSameFolder(t *testing.T) {
	var reg folderRunRegistry
	others, leaveA := reg.Enter("/work/app", "task-a", folderRun{Identifier: "DENE-1", Title: "Fix login", Branch: "fix-login"})
	if len(others) != 0 {
		t.Fatalf("first task saw %+v, want nobody", others)
	}
	others, leaveB := reg.Enter("/work/app", "task-b", folderRun{Identifier: "DENE-2", Title: "Docs", Branch: "main"})
	if len(others) != 1 || others[0].Identifier != "DENE-1" || others[0].Branch != "fix-login" {
		t.Fatalf("second task saw %+v, want DENE-1 on fix-login", others)
	}
	if others, leave := reg.Enter("/work/other", "task-c", folderRun{Identifier: "DENE-3"}); len(others) != 0 {
		t.Fatalf("a different folder saw %+v", others)
	} else {
		leave()
	}
	leaveA()
	leaveA()
	others, leaveD := reg.Enter("/work/app", "task-d", folderRun{Identifier: "DENE-4"})
	if len(others) != 1 || others[0].Identifier != "DENE-2" {
		t.Fatalf("after DENE-1 left, saw %+v, want only DENE-2", others)
	}
	leaveB()
	leaveD()
}

func TestDecisionFailureIsReportedWithItsCode(t *testing.T) {
	const daemonID = "d-mine"
	var gotReason string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if reason, _ := body["failure_reason"].(string); reason != "" {
			gotReason = reason
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	decision := coderesolve.NewLocalInPlace(coderesolve.LocalTarget{
		ResourceID: "r-missing", Path: dir,
	})
	d := &Daemon{
		client: NewClient(srv.URL),
		logger: discardLogger(),
		cfg:    Config{DaemonID: daemonID},
	}
	abort := d.checkLocalDirectory(context.Background(), decisionTask(t, daemonID, []ProjectResourceData{
		localDirResource(t, "r-other", dir, daemonID),
	}, decision), discardLogger())
	if !abort {
		t.Fatal("abort=false, want the task failed")
	}
	if gotReason != string(coderesolve.CodePinnedResourceNotFound) {
		t.Fatalf("failure_reason = %q, want pinned_resource_not_found", gotReason)
	}
}

func TestInPlaceNonGitDirectoryKeepsMulticaOutAndCreatesNoWorkCopy(t *testing.T) {
	workspaces := t.TempDir()
	userDir := t.TempDir() // not a git work tree
	if err := os.WriteFile(filepath.Join(userDir, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	env, err := execenv.Prepare(execenv.PrepareParams{
		WorkspacesRoot:  workspaces,
		WorkspaceID:     "ws-inplace",
		TaskID:          "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
		AgentName:       "Test Agent",
		Provider:        "claude",
		LocalWorkDir:    userDir,
		IsolateSidecars: true,
		Task: execenv.TaskContextForEnv{
			IssueID: "issue-1",
			AgentID: "agent-1",
			ProjectResources: []execenv.ProjectResourceForEnv{{
				ID:           "r1",
				ResourceType: "local_directory",
				ResourceRef:  json.RawMessage(`{"local_path":"/tmp/x","daemon_id":"d1"}`),
			}},
		},
	}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup(true)

	if _, err := os.Stat(filepath.Join(userDir, ".multica")); !os.IsNotExist(err) {
		t.Fatalf(".multica landed in the non-git user directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.RootDir, "workdir")); !os.IsNotExist(err) {
		t.Fatalf("in-place run created a work copy at %s: %v", filepath.Join(env.RootDir, "workdir"), err)
	}
	if _, err := os.Stat(filepath.Join(env.SidecarRoot, ".multica")); err != nil {
		t.Fatalf(".multica missing from the sidecar root: %v", err)
	}
}

func TestIsolateSidecarsForANonGitDirectoryEvenWhenTheRuntimeCan(t *testing.T) {
	dir := t.TempDir()
	a := &localDirectoryAssignment{AbsPath: dir, Ref: localDirectoryRef{LocalPath: dir}}
	isolate, err := isolateUserDirectorySidecars(a, "claude", false)
	if err != nil || !isolate {
		t.Fatalf("non-git in-place isolate=%v err=%v, want isolated", isolate, err)
	}
	isolate, err = isolateUserDirectorySidecars(a, "claude", true)
	if err != nil || !isolate {
		t.Fatalf("git in-place with a supported runtime isolate=%v err=%v, want isolated", isolate, err)
	}
	// A runtime that can only read the cwd keeps today's write-and-clean
	// behavior on a git checkout, and is refused on a folder with no git.
	isolate, err = isolateUserDirectorySidecars(a, "hermes", true)
	if err != nil || isolate {
		t.Fatalf("git in-place hermes isolate=%v err=%v, want the existing in-directory path", isolate, err)
	}
	if _, err := isolateUserDirectorySidecars(a, "hermes", false); err == nil {
		t.Fatal("non-git hermes was allowed to write .multica into the user directory")
	}
}

func asPlacement(err error, dst **codePlacementError) bool {
	return errors.As(err, dst)
}

func mustLocalRef(t *testing.T, ref localDirectoryRef) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func initTempGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}
