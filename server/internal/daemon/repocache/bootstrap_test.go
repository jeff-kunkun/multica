package repocache

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// createDeepTestRepo builds a filter-capable source with more commits than the
// first deepen slice covers, so a bootstrap has to take several slices.
func createDeepTestRepo(t *testing.T, commits int) string {
	t.Helper()
	sourceURL := createFilterableTestRepo(t)
	dir := strings.TrimPrefix(sourceURL, "file://")
	for i := 0; i < commits; i++ {
		runGitAuthored(t, dir, "commit", "--allow-empty", "-m", "filler "+strconv.Itoa(i))
	}
	return sourceURL
}

func commitCount(t *testing.T, repoPath string) int {
	t.Helper()
	out, err := runGitOutput("-C", repoPath, "rev-list", "--all", "--count")
	if err != nil {
		t.Fatalf("rev-list: %v", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("parse commit count %q: %v", out, err)
	}
	return n
}

// TestHeadOnlyDirectoryIsNotReady is the DENE-594 regression: `git clone`
// writes HEAD, config, refs/ and objects/ in its first second, so a directory
// holding all of them with zero refs is a download in progress, not a cache.
func TestHeadOnlyDirectoryIsNotReady(t *testing.T) {
	t.Parallel()
	sourceRepo := createFilterableTestRepo(t)
	cache := New(t.TempDir(), testLogger())
	barePath := cache.BarePath("ws-1", sourceRepo)

	if out, err := runGitCombinedOutput("init", "--bare", barePath); err != nil {
		t.Fatalf("init half-finished cache: %s: %v", out, err)
	}
	if !isBareRepo(barePath) {
		t.Fatal("precondition: the half-finished cache must have a HEAD file")
	}

	if IsReady(barePath) {
		t.Fatal("a directory with HEAD and no refs must not be ready")
	}
	if got := cache.Lookup("ws-1", sourceRepo); got != "" {
		t.Fatalf("Lookup = %q, want \"\" for a half-finished cache", got)
	}
	if adoptLegacyCacheContext(t.Context(), barePath) {
		t.Fatal("a cache without refs must not be adopted as complete")
	}
	_, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-1", RepoURL: sourceRepo, WorkDir: t.TempDir(),
		AgentName: "agent", TaskID: "11111111-1111-1111-1111-111111111111",
	})
	// Refused, and named for what it is (DENE-598): unfinished, not missing
	// and not corrupted.
	if !errors.Is(err, ErrRepoBuilding) {
		t.Fatalf("CreateWorktree error = %v, want ErrRepoBuilding", err)
	}

	// Sync must treat it as unfinished and complete it in place.
	if err := cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	if cache.Lookup("ws-1", sourceRepo) != barePath {
		t.Fatal("expected the resumed cache to be ready after Sync")
	}
}

func TestSyncResumesShallowCacheToFullHistory(t *testing.T) {
	t.Parallel()
	const filler = bootstrapInitialDeepen + 20
	sourceRepo := createDeepTestRepo(t, filler)
	want := commitCount(t, strings.TrimPrefix(sourceRepo, "file://"))
	cache := New(t.TempDir(), testLogger())
	barePath := cache.BarePath("ws-1", sourceRepo)

	// Reproduce an interrupted bootstrap: the first slice landed, the rest did not.
	for _, args := range [][]string{
		{"init", "--bare", barePath},
		{"-C", barePath, "config", "remote.origin.url", sourceRepo},
		{"-C", barePath, "config", "remote.origin.fetch", modernFetchRefspec},
		{"-C", barePath, "fetch", "--depth=1", "--filter=" + partialCloneFilter, "origin"},
	} {
		if out, err := runGitCombinedOutput(args...); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	if !hasAnyRefContext(t.Context(), barePath) {
		t.Fatal("precondition: the interrupted cache should already have refs")
	}
	if cache.Lookup("ws-1", sourceRepo) != "" || adoptLegacyCacheContext(t.Context(), barePath) {
		t.Fatal("a shallow, unfinished cache must not be treated as ready")
	}
	before := commitCount(t, barePath)

	if err := cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	if cache.Lookup("ws-1", sourceRepo) == "" {
		t.Fatal("expected cache to be ready after resume")
	}
	if isShallowContext(t.Context(), barePath) {
		t.Fatal("a ready cache must carry full history")
	}
	if got := commitCount(t, barePath); got != want || got <= before {
		t.Fatalf("commit count = %d (was %d), want %d", got, before, want)
	}
}

func TestSyncBuildsBloblessCacheWithReadableWorktree(t *testing.T) {
	t.Parallel()
	sourceRepo := createFilterableTestRepo(t)
	cache := New(t.TempDir(), testLogger())

	if err := cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	barePath := cache.Lookup("ws-1", sourceRepo)
	if barePath == "" {
		t.Fatal("expected cached repo")
	}
	if !isPartialClone(barePath) {
		t.Fatal("a cold cache from a filter-capable server must be a partial clone")
	}
	out, err := runGitOutput("-C", barePath, "config", "--get", "remote.origin.partialclonefilter")
	if err != nil || strings.TrimSpace(string(out)) != partialCloneFilter {
		t.Fatalf("partialclonefilter = %q (%v), want %q", out, err, partialCloneFilter)
	}
	if out, err := runGitOutput("-C", barePath, "rev-parse", "--verify", "HEAD"); err != nil {
		t.Fatalf("bare HEAD must resolve like it did after git clone --bare: %s: %v", out, err)
	}

	for _, isolated := range []bool{false, true} {
		result, err := cache.CreateWorktree(WorktreeParams{
			WorkspaceID: "ws-1", RepoURL: sourceRepo, WorkDir: t.TempDir(),
			AgentName: "agent", TaskID: "11111111-1111-1111-1111-111111111111",
			IsolatedGitMetadata: isolated,
		})
		if err != nil {
			t.Fatalf("CreateWorktree(isolated=%v) failed: %v", isolated, err)
		}
		assertCheckoutIsComplete(t, result.Path)
		for _, name := range partialCloneTestFiles {
			data, err := os.ReadFile(filepath.Join(result.Path, name))
			if err != nil || string(data) != "contents of "+name+"\n" {
				t.Fatalf("isolated=%v: %s = %q (%v)", isolated, name, data, err)
			}
		}
	}
}

// missingBlobCount reports how many objects of ref's tree are absent locally.
func missingBlobCount(t *testing.T, repoPath, ref string) int {
	t.Helper()
	out, err := runGitOutput("-C", repoPath, "rev-list", "--objects", "--missing=print", "--no-object-names", ref+"^{tree}")
	if err != nil {
		t.Fatalf("rev-list --missing: %v", err)
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "?") {
			n++
		}
	}
	return n
}

// A blobless cache that left the default branch's files to be fetched lazily
// would make the first checkout download them in one non-resumable request.
func TestReadyBloblessCacheHoldsDefaultBranchFiles(t *testing.T) {
	t.Parallel()
	sourceRepo := createFilterableTestRepo(t)
	cache := New(t.TempDir(), testLogger())
	barePath := cache.BarePath("ws-1", sourceRepo)

	// An interrupted bootstrap: full blobless history, no file contents yet.
	if out, err := runGitCombinedOutput("clone", "--bare", "--filter="+partialCloneFilter, sourceRepo, barePath); err != nil {
		t.Fatalf("seed blobless history: %s: %v", out, err)
	}
	if err := ensureRemoteTrackingLayout(barePath); err != nil {
		t.Fatalf("ensure refspec: %v", err)
	}
	if got := missingBlobCount(t, barePath, "refs/remotes/origin/HEAD"); got != len(partialCloneTestFiles) {
		t.Fatalf("precondition: missing blobs = %d, want %d", got, len(partialCloneTestFiles))
	}

	// Regression from the real-link run: full history plus refs looks exactly
	// like a complete legacy cache, so only the building marker keeps an
	// interrupted build from being adopted with its files still missing.
	if err := os.WriteFile(filepath.Join(barePath, buildingFile), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if adoptLegacyCacheContext(t.Context(), barePath) || IsReady(barePath) {
		t.Fatal("an unfinished build must not be adopted as a complete cache")
	}
	if err := cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("resume sync failed: %v", err)
	}
	if !IsReady(barePath) || isBuilding(barePath) {
		t.Fatal("a finished build must be ready and drop the building marker")
	}
	if got := missingBlobCount(t, barePath, "refs/remotes/origin/HEAD"); got != 0 {
		t.Fatalf("missing blobs after resumed sync = %d, want 0", got)
	}
}

func TestPrefetchDefaultBranchBlobs(t *testing.T) {
	t.Parallel()
	sourceRepo := createFilterableTestRepo(t)
	barePath := filepath.Join(t.TempDir(), "cache.git")
	if out, err := runGitCombinedOutput("clone", "--bare", "--filter="+partialCloneFilter, sourceRepo, barePath); err != nil {
		t.Fatalf("seed blobless history: %s: %v", out, err)
	}
	if err := ensureRemoteTrackingLayout(barePath); err != nil {
		t.Fatalf("ensure refspec: %v", err)
	}

	if err := prefetchDefaultBranchBlobsContext(t.Context(), barePath, nil, nil); err != nil {
		t.Fatalf("prefetch failed: %v", err)
	}
	if got := missingBlobCount(t, barePath, "refs/remotes/origin/HEAD"); got != 0 {
		t.Fatalf("missing blobs after prefetch = %d, want 0", got)
	}
	if !isPartialClone(barePath) {
		t.Fatal("prefetching must keep the cache a partial clone")
	}

	// And the full path: a cache Sync declares ready has nothing left to fetch
	// for a default-branch checkout.
	synced := New(t.TempDir(), testLogger())
	if err := synced.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	if got := missingBlobCount(t, synced.Lookup("ws-1", sourceRepo), "refs/remotes/origin/HEAD"); got != 0 {
		t.Fatalf("ready cache is missing %d default-branch blobs", got)
	}
}

func TestSyncFallsBackToFullCloneWithoutFilterSupport(t *testing.T) {
	t.Parallel()
	sourceRepo := createFilterableTestRepo(t)
	runGitAuthored(t, strings.TrimPrefix(sourceRepo, "file://"), "config", "uploadpack.allowFilter", "false")
	cache := New(t.TempDir(), testLogger())

	if err := cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("sync must not fail when the server ignores filters: %v", err)
	}
	barePath := cache.Lookup("ws-1", sourceRepo)
	if barePath == "" {
		t.Fatal("expected cached repo")
	}
	if isPartialClone(barePath) {
		t.Fatal("a cache whose server ignored the filter holds every object and must not claim a promisor remote")
	}
}

func TestFailedDownloadKeepsDirectoryAndLastUsedStamp(t *testing.T) {
	t.Parallel()
	cache := New(t.TempDir(), testLogger())
	missing := "file://" + filepath.Join(t.TempDir(), "does-not-exist")
	barePath := cache.BarePath("ws-1", missing)
	if err := os.MkdirAll(barePath, 0o755); err != nil {
		t.Fatal(err)
	}
	MarkUsed(barePath, nil)
	stamp, ok := LastUsed(barePath)
	if !ok {
		t.Fatal("precondition: stamp should exist")
	}

	if err := cache.Sync("ws-1", []RepoInfo{{URL: missing}}); err == nil {
		t.Fatal("expected sync to fail for a missing remote")
	}
	if !isBareRepo(barePath) {
		t.Fatal("a failed download must leave its directory for the next attempt")
	}
	if cache.Lookup("ws-1", missing) != "" {
		t.Fatal("a failed download must not be ready")
	}
	if got, ok := LastUsed(barePath); !ok || !got.Equal(stamp) {
		t.Fatalf("last-used stamp = %v (ok=%v), want %v preserved", got, ok, stamp)
	}
}

func TestSyncAdoptsCompleteCacheFromBeforeTheMarker(t *testing.T) {
	t.Parallel()
	sourceRepo := createFilterableTestRepo(t)
	cache := New(t.TempDir(), testLogger())
	barePath := cache.BarePath("ws-1", sourceRepo)
	if out, err := runGitCombinedOutput("clone", "--bare", sourceRepo, barePath); err != nil {
		t.Fatalf("legacy clone: %s: %v", out, err)
	}
	if IsReady(barePath) {
		t.Fatal("precondition: a legacy cache has no marker yet")
	}

	// Lookup alone must surface it: after an upgrade the serial Sync loop may
	// not reach this repo for a long time.
	if cache.Lookup("ws-1", sourceRepo) == "" {
		t.Fatal("Lookup must adopt a complete pre-marker cache")
	}
	if err := cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	if cache.Lookup("ws-1", sourceRepo) == "" {
		t.Fatal("a complete pre-marker cache must be adopted, not re-downloaded")
	}
	if isPartialClone(barePath) {
		t.Fatal("adoption must keep the existing objects rather than rebuild the cache")
	}
}

func TestSetGitTimeout(t *testing.T) {
	t.Cleanup(func() { SetGitTimeout(0) })
	SetGitTimeout(42 * time.Minute)
	if got := gitTimeout(); got != 42*time.Minute {
		t.Fatalf("gitTimeout = %s, want 42m", got)
	}
	SetGitTimeout(0)
	if got := gitTimeout(); got != DefaultGitTimeout {
		t.Fatalf("gitTimeout = %s, want default %s", got, DefaultGitTimeout)
	}
}
