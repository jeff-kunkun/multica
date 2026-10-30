package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// openIssueMux answers every legacy per-issue gc-check with an open issue, the
// case in which nothing but the local_directory clock may remove a task dir.
func openIssueMux(issueID string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/api/daemon/issues/%s/gc-check", issueID), func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"status": "in_progress", "updated_at": time.Now()})
	})
	return mux
}

func TestShouldCleanTaskDir_LocalDirectoryExpiresAfterTTL(t *testing.T) {
	t.Parallel()
	issueID := "dddddddd-dddd-dddd-dddd-ddddddddddd1"
	d := newGCTestDaemon(t, openIssueMux(issueID))
	d.cfg.GCLocalDirectoryTTL = 72 * time.Hour

	expired := createTaskDir(t, d.cfg.WorkspacesRoot, "ws1", "local-expired", &execenv.GCMeta{
		Kind: execenv.GCKindIssue, IssueID: issueID, WorkspaceID: "ws1",
		CompletedAt: time.Now().Add(-73 * time.Hour), LocalDirectory: true,
	})
	if got := d.shouldCleanTaskDir(context.Background(), expired); got != gcActionClean {
		t.Fatalf("expired local_directory env on an open issue: got %d, want gcActionClean", got)
	}

	recent := createTaskDir(t, d.cfg.WorkspacesRoot, "ws1", "local-recent", &execenv.GCMeta{
		Kind: execenv.GCKindIssue, IssueID: issueID, WorkspaceID: "ws1",
		CompletedAt: time.Now().Add(-71 * time.Hour), LocalDirectory: true,
	})
	if got := d.shouldCleanTaskDir(context.Background(), recent); got != gcActionCleanArtifacts {
		t.Fatalf("local_directory env inside its TTL: got %d, want artifact-only cleanup", got)
	}

	// A task that never reported completion has no end to count from.
	neverCompleted := createTaskDir(t, d.cfg.WorkspacesRoot, "ws1", "local-no-completion", &execenv.GCMeta{
		Kind: execenv.GCKindIssue, IssueID: issueID, WorkspaceID: "ws1", LocalDirectory: true,
	})
	if got := d.shouldCleanTaskDir(context.Background(), neverCompleted); got == gcActionClean {
		t.Fatal("local_directory env without completed_at must not expire")
	}
}

func TestShouldCleanTaskDir_LocalDirectoryExpiryCoversEveryKind(t *testing.T) {
	t.Parallel()
	sessionID := "dddddddd-dddd-dddd-dddd-ddddddddddd2"
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/api/daemon/chat-sessions/%s/gc-check", sessionID), func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"status": "active", "updated_at": time.Now()})
	})
	d := newGCTestDaemon(t, mux)
	d.cfg.GCLocalDirectoryTTL = 72 * time.Hour

	chatDir := createTaskDir(t, d.cfg.WorkspacesRoot, "ws1", "local-chat", &execenv.GCMeta{
		Kind: execenv.GCKindChat, ChatSessionID: sessionID, WorkspaceID: "ws1",
		CompletedAt: time.Now().Add(-4 * 24 * time.Hour), LocalDirectory: true,
	})
	if got := d.shouldCleanTaskDir(context.Background(), chatDir); got != gcActionClean {
		t.Fatalf("expired local_directory chat env on an active session: got %d, want gcActionClean", got)
	}
}

func TestShouldCleanTaskDir_LocalDirectoryExpiryNeverTouchesActiveEnv(t *testing.T) {
	t.Parallel()
	issueID := "dddddddd-dddd-dddd-dddd-ddddddddddd3"
	d := newGCTestDaemon(t, openIssueMux(issueID))
	d.cfg.GCLocalDirectoryTTL = 72 * time.Hour

	taskDir := createTaskDir(t, d.cfg.WorkspacesRoot, "ws1", "local-active", &execenv.GCMeta{
		Kind: execenv.GCKindIssue, IssueID: issueID, WorkspaceID: "ws1",
		CompletedAt: time.Now().Add(-30 * 24 * time.Hour), LocalDirectory: true,
	})
	d.markActiveEnvRoot(taskDir)
	defer d.unmarkActiveEnvRoot(taskDir)

	if got := d.shouldCleanTaskDir(context.Background(), taskDir); got != gcActionSkip {
		t.Fatalf("running local_directory env: got %d, want gcActionSkip", got)
	}
	d.runGC(context.Background())
	if _, err := os.Stat(taskDir); err != nil {
		t.Fatalf("running local_directory env was removed: %v", err)
	}
}

func TestShouldCleanTaskDir_LocalDirectoryTTLZeroKeepsEnv(t *testing.T) {
	t.Parallel()
	issueID := "dddddddd-dddd-dddd-dddd-ddddddddddd4"
	d := newGCTestDaemon(t, openIssueMux(issueID))
	d.cfg.GCLocalDirectoryTTL = 0

	taskDir := createTaskDir(t, d.cfg.WorkspacesRoot, "ws1", "local-kept", &execenv.GCMeta{
		Kind: execenv.GCKindIssue, IssueID: issueID, WorkspaceID: "ws1",
		CompletedAt: time.Now().Add(-30 * 24 * time.Hour), LocalDirectory: true,
	})
	if got := d.shouldCleanTaskDir(context.Background(), taskDir); got == gcActionClean {
		t.Fatal("GCLocalDirectoryTTL=0 must keep local_directory envs")
	}
}

// TestRunGC_LocalDirectoryExpiryRemovesEnvRootOnly drives the batch issue path
// end to end: the expired env root goes even though the parent issue is open
// and the batch lookup fails, while the user's project folder outside it, a
// regular task on the same failed lookup and a recent local_directory env all
// stay.
func TestRunGC_LocalDirectoryExpiryRemovesEnvRootOnly(t *testing.T) {
	t.Parallel()
	workspaceID := "dddddddd-0000-0000-0000-000000000005"
	mux := http.NewServeMux()
	mux.HandleFunc("/api/daemon/workspaces/"+workspaceID+"/issues/gc-check", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})
	d := newGCTestDaemon(t, mux)
	d.cfg.GCLocalDirectoryTTL = 72 * time.Hour

	projectFolder := t.TempDir()
	userFile := filepath.Join(projectFolder, "main.go")
	if err := os.WriteFile(userFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	expired := createTaskDir(t, d.cfg.WorkspacesRoot, workspaceID, "dene-1-aaaaaaaaaaa1", &execenv.GCMeta{
		Kind: execenv.GCKindIssue, IssueID: "dddddddd-0000-0000-0000-0000000000a1", WorkspaceID: workspaceID,
		CompletedAt: time.Now().Add(-4 * 24 * time.Hour), LocalDirectory: true,
	})
	if err := os.MkdirAll(filepath.Join(expired, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(projectFolder, filepath.Join(expired, "project-link")); err != nil {
		t.Fatal(err)
	}
	recent := createTaskDir(t, d.cfg.WorkspacesRoot, workspaceID, "dene-2-aaaaaaaaaaa2", &execenv.GCMeta{
		Kind: execenv.GCKindIssue, IssueID: "dddddddd-0000-0000-0000-0000000000a2", WorkspaceID: workspaceID,
		CompletedAt: time.Now().Add(-1 * time.Hour), LocalDirectory: true,
	})
	regular := createTaskDir(t, d.cfg.WorkspacesRoot, workspaceID, "dene-3-aaaaaaaaaaa3", &execenv.GCMeta{
		Kind: execenv.GCKindIssue, IssueID: "dddddddd-0000-0000-0000-0000000000a3", WorkspaceID: workspaceID,
		CompletedAt: time.Now().Add(-4 * 24 * time.Hour),
	})

	d.runGC(context.Background())

	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatalf("expired local_directory env root still present: %v", err)
	}
	for _, kept := range []string{recent, regular, userFile} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("%s should survive: %v", kept, err)
		}
	}
}
