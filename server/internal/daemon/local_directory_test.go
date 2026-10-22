package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFindLocalDirectoryAssignment(t *testing.T) {
	const thisDaemon = "d-mine"
	otherDaemon := "d-other"

	mkRef := func(t *testing.T, ref localDirectoryRef) json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(ref)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return raw
	}

	tmp := t.TempDir()

	t.Run("no resources returns nil", func(t *testing.T) {
		got, err := findLocalDirectoryAssignment(nil, thisDaemon)
		if err != nil || got != nil {
			t.Fatalf("expected (nil, nil), got (%+v, %v)", got, err)
		}
	})

	t.Run("other daemon is skipped", func(t *testing.T) {
		got, err := findLocalDirectoryAssignment([]ProjectResourceData{
			{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: mkRef(t, localDirectoryRef{LocalPath: tmp, DaemonID: otherDaemon})},
		}, thisDaemon)
		if err != nil || got != nil {
			t.Fatalf("expected (nil, nil), got (%+v, %v)", got, err)
		}
	})

	t.Run("non-matching type is skipped", func(t *testing.T) {
		got, err := findLocalDirectoryAssignment([]ProjectResourceData{
			{ID: "r1", ResourceType: "github_repo", ResourceRef: json.RawMessage(`{"url":"https://x"}`)},
		}, thisDaemon)
		if err != nil || got != nil {
			t.Fatalf("expected (nil, nil), got (%+v, %v)", got, err)
		}
	})

	t.Run("matching daemon returns assignment", func(t *testing.T) {
		got, err := findLocalDirectoryAssignment([]ProjectResourceData{
			{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: mkRef(t, localDirectoryRef{LocalPath: tmp, DaemonID: thisDaemon})},
		}, thisDaemon)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got == nil {
			t.Fatalf("expected assignment, got nil")
		}
		if got.AbsPath != filepath.Clean(tmp) {
			t.Errorf("AbsPath = %q, want %q", got.AbsPath, filepath.Clean(tmp))
		}
		if got.RealPath == "" {
			t.Errorf("RealPath empty")
		}
	})

	t.Run("missing daemon_id is rejected", func(t *testing.T) {
		_, err := findLocalDirectoryAssignment([]ProjectResourceData{
			{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: mkRef(t, localDirectoryRef{LocalPath: tmp})},
		}, thisDaemon)
		if err == nil {
			t.Fatalf("expected error for missing daemon_id")
		}
	})

	t.Run("relative path is rejected", func(t *testing.T) {
		_, err := findLocalDirectoryAssignment([]ProjectResourceData{
			{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: mkRef(t, localDirectoryRef{LocalPath: "relative/path", DaemonID: thisDaemon})},
		}, thisDaemon)
		if err == nil {
			t.Fatalf("expected error for relative path")
		}
	})

	t.Run("malformed ref json fails", func(t *testing.T) {
		_, err := findLocalDirectoryAssignment([]ProjectResourceData{
			{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: json.RawMessage(`{not json`)},
		}, thisDaemon)
		if err == nil {
			t.Fatalf("expected error for malformed json")
		}
	})

	t.Run("two local_directory rows on this daemon pick the first", func(t *testing.T) {
		// A project may pin several directories on one machine
		// (DENE-617). Resources arrive in `position` order, so the
		// first is the project's default working directory here and
		// the rest are read-only for the run — see
		// TestSeveralLocalDirectoriesPickTheFirstAndKeepTheRestReadOnly
		// for the full rule. Until then this was an error, because the
		// server allowed only one row and two could only mean the
		// constraint had been bypassed.
		tmp2 := t.TempDir()
		got, err := findLocalDirectoryAssignment([]ProjectResourceData{
			{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: mkRef(t, localDirectoryRef{LocalPath: tmp, DaemonID: thisDaemon})},
			{ID: "r2", ResourceType: localDirectoryResourceType, ResourceRef: mkRef(t, localDirectoryRef{LocalPath: tmp2, DaemonID: thisDaemon})},
		}, thisDaemon)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got == nil || got.AbsPath != filepath.Clean(tmp) {
			t.Fatalf("assignment = %+v, want the first row %q", got, filepath.Clean(tmp))
		}
	})

	t.Run("local_directory rows on different daemons coexist", func(t *testing.T) {
		// Different daemons MAY each carry one row — same path on
		// different machines is allowed; this daemon only resolves
		// its own row regardless of how many other-daemon rows are
		// in the list.
		got, err := findLocalDirectoryAssignment([]ProjectResourceData{
			{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: mkRef(t, localDirectoryRef{LocalPath: tmp, DaemonID: thisDaemon})},
			{ID: "r2", ResourceType: localDirectoryResourceType, ResourceRef: mkRef(t, localDirectoryRef{LocalPath: tmp, DaemonID: otherDaemon})},
		}, thisDaemon)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got == nil {
			t.Fatalf("expected assignment, got nil")
		}
	})
}

func TestValidateLocalPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("blacklist constants are POSIX-only in this test")
	}

	dir := t.TempDir()

	t.Run("accepts a writable directory", func(t *testing.T) {
		if err := validateLocalPath(dir); err != nil {
			t.Errorf("unexpected: %v", err)
		}
	})

	t.Run("rejects relative path", func(t *testing.T) {
		if err := validateLocalPath("relative"); err == nil {
			t.Errorf("expected error")
		}
	})

	t.Run("rejects empty path", func(t *testing.T) {
		if err := validateLocalPath(""); err == nil {
			t.Errorf("expected error")
		}
	})

	t.Run("rejects system roots", func(t *testing.T) {
		for _, banned := range []string{"/", "/Users", "/home"} {
			if err := validateLocalPath(banned); err == nil {
				t.Errorf("expected error for %q", banned)
			}
		}
	})

	t.Run("rejects the user home directory", func(t *testing.T) {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			t.Skip("no home dir")
		}
		if err := validateLocalPath(home); err == nil {
			t.Errorf("expected error for $HOME")
		}
	})

	t.Run("rejects missing path", func(t *testing.T) {
		missing := filepath.Join(dir, "does-not-exist")
		if err := validateLocalPath(missing); err == nil {
			t.Errorf("expected error")
		}
	})

	t.Run("rejects a regular file", func(t *testing.T) {
		f := filepath.Join(dir, "afile")
		if err := os.WriteFile(f, []byte("hi"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := validateLocalPath(f); err == nil {
			t.Errorf("expected error")
		}
	})

	t.Run("rejects an unwritable directory", func(t *testing.T) {
		// chmod-based unwritable is unreliable as root; skip when uid==0.
		if os.Getuid() == 0 {
			t.Skip("test cannot run as root; chmod is a no-op")
		}
		ro := filepath.Join(dir, "ro")
		if err := os.Mkdir(ro, 0o555); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
		if err := validateLocalPath(ro); err == nil {
			t.Errorf("expected error for read-only directory")
		}
	})

	t.Run("rejects a symlink pointing at the user home", func(t *testing.T) {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			t.Skip("no home dir")
		}
		link := filepath.Join(dir, "home-link")
		if err := os.Symlink(home, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		// The literal cleaned path is dir/home-link, which is NOT in the
		// blacklist. Without the realpath check this used to pass.
		err = validateLocalPath(link)
		if err == nil {
			t.Fatal("expected error for symlink pointing at $HOME")
		}
		if !strings.Contains(err.Error(), "user's home directory") {
			t.Errorf("error %q did not flag the home-dir reason", err.Error())
		}
	})

	t.Run("rejects a symlink pointing at a system root", func(t *testing.T) {
		link := filepath.Join(dir, "root-link")
		// Pick a banned system root that's predictably present on the
		// host. /Users on macOS; /home on Linux. Fall back to /etc which
		// is in the blacklist and exists on both.
		target := "/etc"
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		err := validateLocalPath(link)
		if err == nil {
			t.Fatal("expected error for symlink pointing at a system root")
		}
		if !strings.Contains(err.Error(), "protected system root") {
			t.Errorf("error %q did not flag the system-root reason", err.Error())
		}
	})

	t.Run("accepts a symlink to a non-blacklisted directory", func(t *testing.T) {
		target := filepath.Join(dir, "real-proj")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		link := filepath.Join(dir, "proj-link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		if err := validateLocalPath(link); err != nil {
			t.Errorf("symlink to a regular directory should pass, got %v", err)
		}
	})

	// macOS aliases /tmp, /etc, /var to /private/{tmp,etc,var} via OS-level
	// symlinks. A user typing the canonical /private/... form in the picker
	// would pass the literal blacklist (it doesn't contain /private/tmp)
	// and EvalSymlinks would be a no-op (the input is already canonical),
	// so the old "only re-check when realPath != absPath" gate skipped it.
	// Cover the regression so the realpath blacklist always runs.
	t.Run("rejects canonical macOS /private/{tmp,etc,var}", func(t *testing.T) {
		if runtime.GOOS != "darwin" {
			t.Skip("macOS-only: /private/* aliases don't exist elsewhere")
		}
		for _, p := range []string{"/private/tmp", "/private/etc", "/private/var"} {
			if _, statErr := os.Stat(p); statErr != nil {
				t.Logf("skipping %q: %v", p, statErr)
				continue
			}
			err := validateLocalPath(p)
			if err == nil {
				t.Errorf("expected error for canonical %q", p)
				continue
			}
			if !strings.Contains(err.Error(), "protected system root") {
				t.Errorf("error %q for %q did not flag the system-root reason", err.Error(), p)
			}
		}
	})

}

// TestIsDriveRoot covers the Windows drive-root generalisation. Static
// enumeration in the old blacklist (C..F) missed mounts at G:\ and up; the
// new check goes through filepath.VolumeName so any drive letter (and UNC
// roots) is rejected.
func TestIsDriveRoot(t *testing.T) {
	if runtime.GOOS != "windows" {
		// filepath.VolumeName returns "" on POSIX, so isDriveRoot always
		// returns false off Windows. The semantic contract is enforced by
		// the early `runtime.GOOS != "windows"` guard; the case table
		// below is only meaningful on a Windows runner.
		t.Skip("windows-only behaviour")
	}
	cases := []struct {
		p    string
		want bool
	}{
		{`C:\`, true},
		{`G:\`, true},
		{`Z:\`, true},
		{`C:/`, true},
		{`C:`, true},
		{`\\srv\share`, true},
		{`\\srv\share\`, true},
		{`C:\Users`, false},
		{`D:\proj`, false},
		{`C:\Users\me\code`, false},
	}
	for _, c := range cases {
		if got := isDriveRoot(c.p); got != c.want {
			t.Errorf("isDriveRoot(%q) = %v, want %v", c.p, got, c.want)
		}
	}
}

// errorsNew is a tiny helper so the goroutine above can return a typed error
// without importing errors / fmt at the call site.
func errorsNew(msg string) error { return &waiterError{msg: msg} }

type waiterError struct{ msg string }

func (e *waiterError) Error() string { return e.msg }

func realPathForTest(t *testing.T, p string) string {
	t.Helper()
	real, err := resolveRealPath(p)
	if err != nil {
		t.Fatalf("resolve real path: %v", err)
	}
	return real
}

// A task can carry several projects (DENE-523). A run occupies exactly one
// directory, so the resolution order decides which project's local_directory
// the agent starts in, and a broken resource in the primary project must still
// fail loudly rather than silently running somewhere else.
func TestLocalDirectoryAssignmentForTask_MultipleProjects(t *testing.T) {
	const thisDaemon = "d-multi"

	mkLocalDir := func(t *testing.T, path, daemonID string) []ProjectResourceData {
		t.Helper()
		raw, err := json.Marshal(localDirectoryRef{LocalPath: path, DaemonID: daemonID})
		if err != nil {
			t.Fatalf("marshal ref: %v", err)
		}
		return []ProjectResourceData{{
			ID:           "r-" + filepath.Base(path),
			ResourceType: localDirectoryResourceType,
			ResourceRef:  raw,
		}}
	}

	primaryDir := t.TempDir()
	secondaryDir := t.TempDir()

	t.Run("first project with a matching resource wins", func(t *testing.T) {
		task := Task{
			Projects: []ProjectContextData{
				{ID: "p1", Resources: []ProjectResourceData{
					{ID: "r1", ResourceType: "github_repo", ResourceRef: json.RawMessage(`{"url":"https://github.com/org/one"}`)},
				}},
				{ID: "p2", Resources: mkLocalDir(t, secondaryDir, thisDaemon)},
			},
		}
		got, err := localDirectoryAssignmentForTask(task, thisDaemon)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got == nil || got.AbsPath != filepath.Clean(secondaryDir) {
			t.Fatalf("assignment = %+v, want the second project's directory %s", got, secondaryDir)
		}
	})

	t.Run("priority order decides between two matching projects", func(t *testing.T) {
		task := Task{
			Projects: []ProjectContextData{
				{ID: "p1", Resources: mkLocalDir(t, primaryDir, thisDaemon)},
				{ID: "p2", Resources: mkLocalDir(t, secondaryDir, thisDaemon)},
			},
		}
		got, err := localDirectoryAssignmentForTask(task, thisDaemon)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got == nil || got.AbsPath != filepath.Clean(primaryDir) {
			t.Fatalf("assignment = %+v, want the primary project's directory %s", got, primaryDir)
		}
	})

	t.Run("a broken primary resource does not fall through", func(t *testing.T) {
		task := Task{
			Projects: []ProjectContextData{
				{ID: "p1", Resources: []ProjectResourceData{{
					ID:           "broken",
					ResourceType: localDirectoryResourceType,
					ResourceRef:  json.RawMessage(`{"local_path":`),
				}}},
				{ID: "p2", Resources: mkLocalDir(t, secondaryDir, thisDaemon)},
			},
		}
		if _, err := localDirectoryAssignmentForTask(task, thisDaemon); err == nil {
			t.Fatal("expected the broken resource to fail the resolution, not to fall through to another project")
		}
	})

	t.Run("legacy singular fields still resolve", func(t *testing.T) {
		task := Task{
			ProjectID:        "p-legacy",
			ProjectResources: mkLocalDir(t, primaryDir, thisDaemon),
		}
		got, err := localDirectoryAssignmentForTask(task, thisDaemon)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got == nil || got.AbsPath != filepath.Clean(primaryDir) {
			t.Fatalf("assignment = %+v, want the legacy project's directory %s", got, primaryDir)
		}
	})
}
