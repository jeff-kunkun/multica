package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRemotes builds a gitRemotesFunc from a dir→remotes map. Directories
// absent from the map are "not a git working tree".
func fakeRemotes(m map[string][]string) gitRemotesFunc {
	return func(dir string) []string { return m[dir] }
}

func assignment(t *testing.T, path, mode string) *localDirectoryAssignment {
	t.Helper()
	return &localDirectoryAssignment{
		Ref:      localDirectoryRef{LocalPath: path, DaemonID: "d1", ExecutionMode: mode},
		AbsPath:  path,
		RealPath: path,
	}
}

func TestNoAssignmentAlwaysChecksOutRemotely(t *testing.T) {
	out := decideLocalCheckout(nil, "", "https://github.com/o/r", fakeRemotes(nil))
	if out.Path != "" || out.Refusal != "" {
		t.Fatalf("a project with no local directory must fall through to the clone path, got %+v", out)
	}
}

func TestProvenLocalDirectoryReplacesTheClone(t *testing.T) {
	dir := t.TempDir()
	remotes := fakeRemotes(map[string][]string{dir: {"git@github.com:jeff-kunkun/multica.git"}})
	out := decideLocalCheckout(assignment(t, dir, "in_place"), dir, "https://github.com/jeff-kunkun/multica", remotes)
	if out.Path != dir {
		t.Fatalf("expected the local directory %q, got %+v", dir, out)
	}
	if out.Refusal != "" {
		t.Fatalf("a proven match must not be refused: %s", out.Refusal)
	}
}

func TestUnrelatedRepoStillClones(t *testing.T) {
	dir := t.TempDir()
	remotes := fakeRemotes(map[string][]string{dir: {"git@github.com:jeff-kunkun/multica.git"}})
	// A project pinned to one directory may legitimately reference other
	// repositories; pinning must not break them.
	out := decideLocalCheckout(assignment(t, dir, "in_place"), dir, "https://github.com/other/thing", remotes)
	if out.Path != "" || out.Refusal != "" {
		t.Fatalf("an unrelated repo must still clone, got %+v", out)
	}
}

func TestNameMatchWithoutProofRefusesInsteadOfCloning(t *testing.T) {
	// The directory is named after the repo but carries no git remote at all
	// — the path was never checked out, or is not a git tree. This is the case
	// that used to fall back to the network and produce a second copy.
	base := t.TempDir()
	dir := filepath.Join(base, "multica")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out := decideLocalCheckout(assignment(t, dir, "in_place"), dir, "https://github.com/jeff-kunkun/multica", fakeRemotes(nil))
	if out.Path != "" {
		t.Fatalf("an unproven directory must not be handed out as the checkout: %+v", out)
	}
	if out.Refusal == "" {
		t.Fatal("an unproven directory must be refused with a reason, never silently cloned")
	}
	for _, want := range []string{dir, "https://github.com/jeff-kunkun/multica"} {
		if !strings.Contains(out.Refusal, want) {
			t.Errorf("refusal must name %q so the user can act on it; got: %s", want, out.Refusal)
		}
	}
}

func TestSharedUmbrellaDirectoryMatchesOneLevelDown(t *testing.T) {
	// shared mode's whole reason for existing: one directory holding several
	// repositories side by side. Looking only at the umbrella would clone
	// every one of them.
	base := t.TempDir()
	child := filepath.Join(base, "online-tarot")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	remotes := fakeRemotes(map[string][]string{child: {"https://github.com/kun/online-tarot.git"}})
	out := decideLocalCheckout(assignment(t, base, "shared"), base, "git@github.com:kun/online-tarot.git", remotes)
	if out.Path != child {
		t.Fatalf("expected the child checkout %q, got %+v", child, out)
	}
}

func TestResolveTaskCodeSourceSortsReposIntoBuckets(t *testing.T) {
	base := t.TempDir()
	named := filepath.Join(base, "ai100")
	if err := os.Mkdir(named, 0o755); err != nil {
		t.Fatal(err)
	}
	// in_place: the agent is in the user's directory, so the buckets are
	// decided by the directory alone.
	remotes := fakeRemotes(map[string][]string{base: {"https://github.com/kun/BNB.git"}})

	src := resolveTaskCodeSource(assignment(t, base, "in_place"), nil, base, []string{
		"https://github.com/kun/BNB",       // proven
		"https://github.com/kun/ai100",     // named but unproven
		"https://github.com/kun/elsewhere", // genuinely remote
	}, remotes)

	if src.Kind != codeSourceKindLocalDirectory {
		t.Fatalf("kind = %q, want local_directory", src.Kind)
	}
	if src.ExecutionMode != localDirectoryModeInPlace {
		t.Errorf("execution mode = %q", src.ExecutionMode)
	}
	if len(src.CoveredRepos) != 1 || src.CoveredRepos[0] != "https://github.com/kun/BNB" {
		t.Errorf("covered = %v", src.CoveredRepos)
	}
	if len(src.UnprovenRepos) != 1 || src.UnprovenRepos[0].URL != "https://github.com/kun/ai100" {
		t.Errorf("unproven = %+v", src.UnprovenRepos)
	}
	if len(src.RemoteRepos) != 1 || src.RemoteRepos[0] != "https://github.com/kun/elsewhere" {
		t.Errorf("remote = %v", src.RemoteRepos)
	}
}

func TestResolveTaskCodeSourceWithoutAssignmentKeepsEveryRepoRemote(t *testing.T) {
	src := resolveTaskCodeSource(nil, nil, "", []string{"https://github.com/o/a", "https://github.com/o/b"}, fakeRemotes(nil))
	if src.Kind != codeSourceKindRemoteCheckout {
		t.Fatalf("kind = %q, want remote_checkout", src.Kind)
	}
	if len(src.RemoteRepos) != 2 {
		t.Fatalf("every repo must stay remote, got %v", src.RemoteRepos)
	}
	if src.LocalPath != "" {
		t.Errorf("no directory is pinned, so none may be reported: %q", src.LocalPath)
	}
}

func TestExecutionModeIsReportedEvenWhenTheRefOmitsIt(t *testing.T) {
	// An absent execution_mode means in_place. A brief that showed it blank
	// would leave the agent unable to tell whether its edits land in the
	// user's working copy.
	src := resolveTaskCodeSource(assignment(t, t.TempDir(), ""), nil, "", nil, fakeRemotes(nil))
	if src.ExecutionMode != localDirectoryModeInPlace {
		t.Fatalf("execution mode = %q, want in_place", src.ExecutionMode)
	}
}

// --- worktree mode: the task's own checkout, never the user's -------------
//
// Canonical matrix for the mapping itself lives with taskDirFor's callers
// below; these pin the two outcomes a user can be harmed by.

func TestWorktreeModeNeverHandsBackTheUsersCheckout(t *testing.T) {
	// The accident this guards: worktree mode promises the agent's edits do
	// not touch the user's working copy, but the checkout endpoint used to
	// answer with assignment.AbsPath regardless of mode. An agent following
	// that answer commits into the directory the user is working in.
	userDir := t.TempDir()
	workDir := t.TempDir()
	repo := "https://github.com/jeff-kunkun/multica"
	remotes := fakeRemotes(map[string][]string{
		userDir: {"git@github.com:jeff-kunkun/multica.git"},
		workDir: {"git@github.com:jeff-kunkun/multica.git"},
	})

	out := decideLocalCheckout(assignment(t, userDir, "worktree"), workDir, repo, remotes)
	if out.Path == userDir {
		t.Fatalf("worktree mode must never hand back the user's checkout %q", userDir)
	}
	if out.Path != workDir {
		t.Fatalf("path = %q, want this task's worktree %q", out.Path, workDir)
	}
	if out.ExecutionMode != "worktree" {
		t.Errorf("execution mode = %q, want worktree so the CLI can say whose checkout this is", out.ExecutionMode)
	}
}

func TestWorktreeModeRefusesARepoItsWorktreeDoesNotHold(t *testing.T) {
	// A nested repository beside the pinned path is on the machine but has no
	// counterpart inside the task's worktree. Neither answer left is safe to
	// give silently: the user's copy is off-limits, and cloning is the
	// fallback DENE-595 exists to stop.
	base := t.TempDir()
	child := filepath.Join(base, "online-tarot")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	remotes := fakeRemotes(map[string][]string{
		child:   {"https://github.com/kun/online-tarot.git"},
		workDir: {"git@github.com:kun/umbrella.git"},
	})

	out := decideLocalCheckout(assignment(t, base, "worktree"), workDir, "git@github.com:kun/online-tarot.git", remotes)
	if out.Path != "" {
		t.Fatalf("no path may be handed out when the worktree does not hold the repo, got %q", out.Path)
	}
	if out.Refusal == "" {
		t.Fatal("must refuse with a reason rather than fall back to the user's copy or to cloning")
	}
	if strings.Contains(out.Refusal, "\n") {
		t.Errorf("refusal should read as one paragraph: %s", out.Refusal)
	}
}

func TestCodeSourceReportsTheWorktreeAsTheTasksDirectory(t *testing.T) {
	// The brief's "Directory:" line and its execution-mode line must agree.
	// Naming the user's path next to "your edits do NOT touch the user's
	// working copy" is the contradiction the agent resolves the wrong way.
	userDir := t.TempDir()
	workDir := t.TempDir()
	remotes := fakeRemotes(map[string][]string{
		userDir: {"git@github.com:jeff-kunkun/multica.git"},
		workDir: {"git@github.com:jeff-kunkun/multica.git"},
	})

	src := resolveTaskCodeSource(assignment(t, userDir, "worktree"), nil, workDir,
		[]string{"https://github.com/jeff-kunkun/multica"}, remotes)

	if src.LocalPath != workDir {
		t.Fatalf("LocalPath = %q, want the task's worktree %q", src.LocalPath, workDir)
	}
	if len(src.CoveredRepos) != 1 {
		t.Fatalf("the repo is in the worktree, so it must stay covered: %+v", src)
	}
}

func TestCodeSourceDemotesARepoTheWorktreeCannotReach(t *testing.T) {
	base := t.TempDir()
	child := filepath.Join(base, "online-tarot")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	remotes := fakeRemotes(map[string][]string{
		child:   {"https://github.com/kun/online-tarot.git"},
		workDir: {"git@github.com:kun/umbrella.git"},
	})

	src := resolveTaskCodeSource(assignment(t, base, "worktree"), nil, workDir,
		[]string{"https://github.com/kun/online-tarot"}, remotes)

	if len(src.CoveredRepos) != 0 {
		t.Fatalf("a repo the task cannot reach must not be listed as already here: %v", src.CoveredRepos)
	}
	if len(src.UnprovenRepos) != 1 {
		t.Fatalf("it must be surfaced with a reason instead: %+v", src.UnprovenRepos)
	}
}

func TestInPlaceAndSharedStillUseTheUsersDirectory(t *testing.T) {
	// The fix must not leak into the modes whose promise IS the user's
	// directory.
	for _, mode := range []string{"in_place", "shared", ""} {
		dir := t.TempDir()
		remotes := fakeRemotes(map[string][]string{dir: {"git@github.com:jeff-kunkun/multica.git"}})
		out := decideLocalCheckout(assignment(t, dir, mode), "/some/other/place",
			"https://github.com/jeff-kunkun/multica", remotes)
		if out.Path != dir {
			t.Errorf("mode %q: path = %q, want the user's directory %q", mode, out.Path, dir)
		}
	}
}
