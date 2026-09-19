package repocache

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// withFakeGit returns a context whose git commands run script (a POSIX shell
// body that sees the real git as $REAL_GIT) instead of the real git. Only call
// trees handed this context see the fake: the process PATH is left alone,
// because every other goroutine in the test binary shares it.
func withFakeGit(t *testing.T, script string) context.Context {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell git wrapper is POSIX-only")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not found: %v", err)
	}
	fake := filepath.Join(t.TempDir(), "git")
	body := "#!/bin/sh\nREAL_GIT='" + realGit + "'\n" + script
	if err := os.WriteFile(fake, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return context.WithValue(context.Background(), gitBinaryKey{}, fake)
}

// stallGitFetchFor returns a context whose git blocks every `fetch` touching a
// path containing marker until release() is called, and passes every other
// invocation straight to the real git. It stands in for a repository whose
// first-time download takes a long time. The returned function reads the
// command lines the wrapper saw for marker.
func stallGitFetchFor(t *testing.T, marker string) (ctx context.Context, release func(), seen func() string) {
	t.Helper()
	stateDir := t.TempDir()
	releaseFile := filepath.Join(stateDir, "release")
	logFile := filepath.Join(stateDir, "seen.log")
	ctx = withFakeGit(t, ""+
		"case \"$*\" in\n"+
		"  *"+marker+"*)\n"+
		"    echo \"$*\" >> '"+logFile+"'\n"+
		"    case \" $* \" in *' fetch '*) while [ ! -e '"+releaseFile+"' ]; do sleep 0.02; done ;; esac ;;\n"+
		"esac\n"+
		"exec \"$REAL_GIT\" \"$@\"\n")

	release = func() {
		if err := os.WriteFile(releaseFile, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(release)
	seen = func() string {
		data, _ := os.ReadFile(logFile)
		return string(data)
	}
	return ctx, release, seen
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestLongDownloadDoesNotBlockOtherRepos is the DENE-598 acceptance check:
// while one repository sits in a long first-time download, another repository
// in the same Sync call still gets cached and checked out, a checkout of the
// downloading one is told the truth, and nobody is told to delete anything.
func TestLongDownloadDoesNotBlockOtherRepos(t *testing.T) {
	slowDir := filepath.Join(t.TempDir(), "slowrepo-dene598")
	if err := os.MkdirAll(slowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	slowURL := createTestRepoAt(t, slowDir)
	fastURL := createTestRepo(t)
	slowCtx, release, seen := stallGitFetchFor(t, "slowrepo-dene598")

	cache := New(t.TempDir(), testLogger())
	const ws = "ws-1"

	// The slow repository is listed first: a serial Sync would never reach
	// the fast one while it is stuck.
	syncDone := make(chan error, 1)
	go func() {
		syncDone <- cache.SyncContext(slowCtx, ws, []RepoInfo{{URL: slowURL}, {URL: fastURL}})
	}()

	waitFor(t, "the fast repo to be cached while the slow one downloads", func() bool {
		return cache.Lookup(ws, fastURL) != ""
	})
	status, buildDone, building := cache.BuildInProgress(ws, slowURL)
	if !building || !status.Active || buildDone == nil {
		t.Fatalf("BuildInProgress(slow) = %+v, done=%v, ok=%v; want an active download", status, buildDone != nil, building)
	}

	// A second Sync of the fast repo, and a checkout of it, both complete.
	if err := cache.Sync(ws, []RepoInfo{{URL: fastURL}}); err != nil {
		t.Fatalf("Sync(fast) during the slow download: %v", err)
	}
	if _, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID: ws, RepoURL: fastURL, WorkDir: t.TempDir(), AgentName: "a", TaskID: "task-1",
		LockWaitTimeout: time.Second,
	}); err != nil {
		t.Fatalf("CreateWorktree(fast) during the slow download: %v", err)
	}

	// A checkout of the downloading repo gets progress, not "busy", not
	// "not found", and above all not advice to delete the cache.
	_, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID: ws, RepoURL: slowURL, WorkDir: t.TempDir(), AgentName: "a", TaskID: "task-2",
		LockWaitTimeout: time.Second,
	})
	if !errors.Is(err, ErrRepoBuilding) {
		t.Fatalf("CreateWorktree(slow) error = %v, want ErrRepoBuilding", err)
	}
	msg := err.Error()
	for _, want := range []string{"not finished yet", "step 1 of 3", "not corrupted"} {
		if !strings.Contains(msg, want) {
			t.Errorf("building error %q does not mention %q", msg, want)
		}
	}
	if strings.Contains(msg, "delete it and retry") {
		t.Errorf("building error still advises deleting the cache: %q", msg)
	}

	// A second Sync of the slow repo joins the download in flight.
	joined := make(chan error, 1)
	go func() { joined <- cache.SyncContext(slowCtx, ws, []RepoInfo{{URL: slowURL}}) }()
	select {
	case err := <-joined:
		t.Fatalf("joining Sync returned before the download finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	release()
	for name, ch := range map[string]chan error{"owning": syncDone, "joining": joined} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("%s Sync: %v", name, err)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("%s Sync did not return after release", name)
		}
	}
	if cache.Lookup(ws, slowURL) == "" {
		t.Fatal("slow repo is not ready after its download finished")
	}
	if _, _, building := cache.BuildInProgress(ws, slowURL); building {
		t.Fatal("BuildInProgress still reports a finished download")
	}
	select {
	case <-buildDone:
	default:
		t.Fatal("the build's done channel did not close")
	}
	// The joiner must not have queued up a refresh of the cache it just
	// watched being built: `fetch origin` with no depth/deepen is that refresh.
	for _, line := range strings.Split(seen(), "\n") {
		if strings.HasSuffix(line, " fetch origin") {
			t.Errorf("redundant fetch after the build: %q", line)
		}
	}
}

func TestInterruptedDownloadReportsResumeNotCorruption(t *testing.T) {
	t.Parallel()
	cache := New(t.TempDir(), testLogger())
	const (
		ws      = "ws-1"
		repoURL = "https://github.com/org/half-done.git"
	)
	barePath := cache.BarePath(ws, repoURL)
	if err := os.MkdirAll(barePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(barePath, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := cache.CreateWorktreeContext(context.Background(), WorktreeParams{WorkspaceID: ws, RepoURL: repoURL, WorkDir: t.TempDir()})
	if !errors.Is(err, ErrRepoBuilding) {
		t.Fatalf("error = %v, want ErrRepoBuilding", err)
	}
	var building *RepoBuildingError
	if !errors.As(err, &building) || building.Status.Active {
		t.Fatalf("error = %#v, want an inactive RepoBuildingError", err)
	}
	if !strings.Contains(err.Error(), "resumes from where it stopped") || strings.Contains(err.Error(), "delete it and retry") {
		t.Errorf("unexpected wording: %q", err)
	}

	// A repository nobody ever started downloading is still plain "not found".
	_, err = cache.CreateWorktreeContext(context.Background(), WorktreeParams{WorkspaceID: ws, RepoURL: "https://github.com/org/never.git", WorkDir: t.TempDir()})
	if err == nil || errors.Is(err, ErrRepoBuilding) {
		t.Fatalf("error = %v, want a not-found error", err)
	}
}

func TestBuildStatusDescribe(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	files := BuildStatus{Active: true, Phase: BuildPhaseFiles, Done: 1000, Total: 4000, StartedAt: now.Add(-5 * time.Minute), PhaseStartedAt: now.Add(-time.Minute)}
	if left, ok := files.Remaining(now); !ok || left != 3*time.Minute {
		t.Errorf("Remaining = %s, %v; want 3m0s, true", left, ok)
	}
	if got := files.Describe(now); !strings.Contains(got, "1000 of 4000") || !strings.Contains(got, "about 3m0s left") {
		t.Errorf("files Describe = %q", got)
	}
	// History has no known total, so it must not invent an estimate.
	history := BuildStatus{Active: true, Phase: BuildPhaseHistory, Done: 2, StartedAt: now.Add(-time.Minute), PhaseStartedAt: now}
	if _, ok := history.Remaining(now); ok {
		t.Error("history phase reported an estimate it cannot know")
	}
	if got := history.Describe(now); !strings.Contains(got, "2 slices fetched") || strings.Contains(got, "about") {
		t.Errorf("history Describe = %q", got)
	}
}

// TestCreateWorktreeReportsStaleWhenFetchFails: a failed refresh used to be a
// daemon log line only. The checkout still succeeds, and says it may be stale.
func TestCreateWorktreeReportsStaleWhenFetchFails(t *testing.T) {
	t.Parallel()
	sourceDir := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	repoURL := createTestRepoAt(t, sourceDir)
	cache := New(t.TempDir(), testLogger())
	const ws = "ws-1"
	if err := cache.Sync(ws, []RepoInfo{{URL: repoURL}}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	fresh, err := cache.CreateWorktree(WorktreeParams{WorkspaceID: ws, RepoURL: repoURL, WorkDir: t.TempDir(), AgentName: "a", TaskID: "task-1"})
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if fresh.Stale || fresh.StaleReason != "" {
		t.Fatalf("a checkout after a good fetch is marked stale: %+v", fresh)
	}

	// Take the remote away: the next fetch fails, the cache still serves.
	if err := os.Rename(sourceDir, sourceDir+".gone"); err != nil {
		t.Fatal(err)
	}
	stale, err := cache.CreateWorktree(WorktreeParams{WorkspaceID: ws, RepoURL: repoURL, WorkDir: t.TempDir(), AgentName: "a", TaskID: "task-2"})
	if err != nil {
		t.Fatalf("CreateWorktree with the remote gone: %v", err)
	}
	if !stale.Stale || stale.StaleReason == "" {
		t.Fatalf("checkout built without a fetch is not marked stale: %+v", stale)
	}
}

// TestBootstrapGivesUpWhenHistoryStopsGrowing: a remote that answers every
// deepen with nothing must end the download, not spin under the repo lock.
func TestBootstrapGivesUpWhenHistoryStopsGrowing(t *testing.T) {
	// Every --deepen succeeds without fetching anything.
	fakeGit := withFakeGit(t, "case \"$*\" in *--deepen=*) exit 0 ;; esac\nexec \"$REAL_GIT\" \"$@\"\n")
	sourceURL := createDeepTestRepo(t, 5)

	dest := filepath.Join(t.TempDir(), "repo.git")
	ctx, cancel := context.WithTimeout(fakeGit, 30*time.Second)
	defer cancel()
	err := bootstrapBareContext(ctx, sourceURL, dest, testLogger(), nil)
	if err == nil || !strings.Contains(err.Error(), "history stopped growing") {
		t.Fatalf("bootstrap error = %v, want the no-progress breaker", err)
	}
	if IsReady(dest) {
		t.Fatal("a download that gave up was marked ready")
	}
}
