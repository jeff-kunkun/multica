package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFindLocalDirectoryAssignments(t *testing.T) {
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
		got, err := findLocalDirectoryAssignments(nil, thisDaemon)
		if err != nil || got != nil {
			t.Fatalf("expected (nil, nil), got (%+v, %v)", got, err)
		}
	})

	t.Run("other daemon is skipped", func(t *testing.T) {
		got, err := findLocalDirectoryAssignments([]ProjectResourceData{
			{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: mkRef(t, localDirectoryRef{LocalPath: tmp, DaemonID: otherDaemon})},
		}, thisDaemon)
		if err != nil || got != nil {
			t.Fatalf("expected (nil, nil), got (%+v, %v)", got, err)
		}
	})

	t.Run("non-matching type is skipped", func(t *testing.T) {
		got, err := findLocalDirectoryAssignments([]ProjectResourceData{
			{ID: "r1", ResourceType: "github_repo", ResourceRef: json.RawMessage(`{"url":"https://x"}`)},
		}, thisDaemon)
		if err != nil || got != nil {
			t.Fatalf("expected (nil, nil), got (%+v, %v)", got, err)
		}
	})

	t.Run("matching daemon returns assignment", func(t *testing.T) {
		got, err := findLocalDirectoryAssignments([]ProjectResourceData{
			{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: mkRef(t, localDirectoryRef{LocalPath: tmp, DaemonID: thisDaemon})},
		}, thisDaemon)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected one assignment, got %+v", got)
		}
		if got[0].AbsPath != filepath.Clean(tmp) {
			t.Errorf("AbsPath = %q, want %q", got[0].AbsPath, filepath.Clean(tmp))
		}
	})

	t.Run("legacy execution_mode is ignored", func(t *testing.T) {
		// Older refs carry in_place / worktree / shared. Every one of them
		// runs directly in the directory now.
		for _, mode := range []string{"in_place", "worktree", "shared", "snapshot"} {
			raw := json.RawMessage(`{"local_path":` + strconv.Quote(tmp) + `,"daemon_id":"` + thisDaemon + `","execution_mode":"` + mode + `"}`)
			got, err := findLocalDirectoryAssignments([]ProjectResourceData{
				{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: raw},
			}, thisDaemon)
			if err != nil {
				t.Fatalf("mode %q: err: %v", mode, err)
			}
			if len(got) != 1 || got[0].AbsPath != filepath.Clean(tmp) {
				t.Fatalf("mode %q: assignment = %+v, want the directory itself", mode, got)
			}
		}
	})

	t.Run("missing daemon_id is rejected", func(t *testing.T) {
		_, err := findLocalDirectoryAssignments([]ProjectResourceData{
			{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: mkRef(t, localDirectoryRef{LocalPath: tmp})},
		}, thisDaemon)
		if err == nil {
			t.Fatalf("expected error for missing daemon_id")
		}
	})

	t.Run("relative path is rejected", func(t *testing.T) {
		_, err := findLocalDirectoryAssignments([]ProjectResourceData{
			{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: mkRef(t, localDirectoryRef{LocalPath: "relative/path", DaemonID: thisDaemon})},
		}, thisDaemon)
		if err == nil {
			t.Fatalf("expected error for relative path")
		}
	})

	t.Run("malformed ref json fails", func(t *testing.T) {
		_, err := findLocalDirectoryAssignments([]ProjectResourceData{
			{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: json.RawMessage(`{not json`)},
		}, thisDaemon)
		if err == nil {
			t.Fatalf("expected error for malformed json")
		}
	})

	t.Run("every folder on this daemon is returned in resource order", func(t *testing.T) {
		// A project may carry several folders on one machine. The first is
		// the working directory; the rest are listed to the agent. The
		// top-level label names the folder over the copy inside the ref.
		tmp2 := t.TempDir()
		tmp3 := t.TempDir()
		got, err := findLocalDirectoryAssignments([]ProjectResourceData{
			{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: mkRef(t, localDirectoryRef{LocalPath: tmp, DaemonID: thisDaemon})},
			{ID: "r2", ResourceType: localDirectoryResourceType, ResourceRef: mkRef(t, localDirectoryRef{LocalPath: tmp2, DaemonID: otherDaemon})},
			{ID: "r3", ResourceType: localDirectoryResourceType, ResourceRef: mkRef(t, localDirectoryRef{LocalPath: tmp2, DaemonID: thisDaemon, Label: "old"}), Label: "notes"},
			{ID: "r4", ResourceType: localDirectoryResourceType, ResourceRef: mkRef(t, localDirectoryRef{LocalPath: tmp3, DaemonID: thisDaemon})},
		}, thisDaemon)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		var paths []string
		for _, dir := range got {
			paths = append(paths, dir.AbsPath)
		}
		want := []string{filepath.Clean(tmp), filepath.Clean(tmp2), filepath.Clean(tmp3)}
		if !reflect.DeepEqual(paths, want) {
			t.Fatalf("paths = %v, want %v", paths, want)
		}
		if got[1].Ref.Label != "notes" {
			t.Errorf("label = %q, want the top-level label %q", got[1].Ref.Label, "notes")
		}
	})

	t.Run("local_directory rows on different daemons coexist", func(t *testing.T) {
		// Different daemons MAY each carry one row — same path on
		// different machines is allowed; this daemon only resolves
		// its own row regardless of how many other-daemon rows are
		// in the list.
		got, err := findLocalDirectoryAssignments([]ProjectResourceData{
			{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: mkRef(t, localDirectoryRef{LocalPath: tmp, DaemonID: thisDaemon})},
			{ID: "r2", ResourceType: localDirectoryResourceType, ResourceRef: mkRef(t, localDirectoryRef{LocalPath: tmp, DaemonID: otherDaemon})},
		}, thisDaemon)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected one assignment, got %+v", got)
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

// Any execution_mode an older client stored — including the kun line's
// "shared" that used to make the daemon refuse the task — must start the task
// in the directory, and a second task on the same directory must not wait for
// the first: nothing about a usable directory reaches the server.
func TestLocalDirectoryUsableForEveryLegacyMode(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected daemon call: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	const daemonID = "d-mine"
	tmp := t.TempDir()
	d := &Daemon{
		cfg:    Config{DaemonID: daemonID},
		client: NewClient(srv.URL),
		logger: slog.Default(),
	}
	for _, mode := range []string{"", "in_place", "worktree", "shared"} {
		raw := json.RawMessage(`{"local_path":` + strconv.Quote(tmp) + `,"daemon_id":"` + daemonID + `","execution_mode":"` + mode + `"}`)
		task := Task{
			ID:               "task-" + mode,
			ProjectResources: []ProjectResourceData{{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: raw}},
		}
		if d.localDirectoryUnusable(context.Background(), task, slog.Default()) {
			t.Errorf("mode %q: task aborted, want it to run in %s", mode, tmp)
		}
	}
}

func TestLocalDirectoryUnusableReportsFailure(t *testing.T) {
	t.Parallel()

	var failCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !strings.HasSuffix(req.URL.Path, "/fail") {
			t.Errorf("unexpected daemon call: %s %s", req.Method, req.URL.Path)
		}
		failCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	const daemonID = "d-test"
	d := &Daemon{
		client: NewClient(srv.URL),
		logger: slog.Default(),
		cfg:    Config{DaemonID: daemonID},
	}
	missingPathRef, err := json.Marshal(localDirectoryRef{
		LocalPath: filepath.Join(t.TempDir(), "missing"),
		DaemonID:  daemonID,
	})
	if err != nil {
		t.Fatalf("marshal ref: %v", err)
	}
	usableRef, err := json.Marshal(localDirectoryRef{LocalPath: t.TempDir(), DaemonID: daemonID})
	if err != nil {
		t.Fatalf("marshal ref: %v", err)
	}
	tests := []struct {
		name string
		refs []json.RawMessage
	}{
		{name: "resource resolution", refs: []json.RawMessage{json.RawMessage(`{not-json`)}},
		{name: "path validation", refs: []json.RawMessage{missingPathRef}},
		// Every folder is validated, not just the working directory: the
		// agent is pointed at the others too.
		{name: "second folder validation", refs: []json.RawMessage{usableRef, missingPathRef}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			task := Task{ID: "task-invalid-local-directory"}
			for i, ref := range tc.refs {
				task.ProjectResources = append(task.ProjectResources, ProjectResourceData{
					ID: "r" + strconv.Itoa(i), ResourceType: localDirectoryResourceType, ResourceRef: ref,
				})
			}
			// A cancelled parent must not swallow the failure report.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if !d.localDirectoryUnusable(ctx, task, slog.Default()) {
				t.Fatal("expected local_directory failure to abort task")
			}
		})
	}
	if got := failCalls.Load(); got != int32(len(tests)) {
		t.Fatalf("fail callback calls = %d, want %d", got, len(tests))
	}
}
