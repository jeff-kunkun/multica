package daemon

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// A project may now pin several directories on one machine (DENE-617). These
// cover which one a run writes, and what happens to the rest.

func localDirResource(t *testing.T, id, path, daemonID string) ProjectResourceData {
	t.Helper()
	raw, err := json.Marshal(localDirectoryRef{LocalPath: path, DaemonID: daemonID, Label: filepath.Base(path)})
	if err != nil {
		t.Fatal(err)
	}
	return ProjectResourceData{ID: id, ResourceType: localDirectoryResourceType, ResourceRef: raw}
}

// Invariant 1: one run writes one directory. Which one is stated, not raced —
// resources arrive in `position` order, so the first pinned to this machine is
// the project's default working directory and reordering the list changes it.
//
// Until this change a second row on the same daemon FAILED the task, because
// the server allowed only one and two could only mean corrupt data. Several
// are now legal, so there is nothing left to guess.
func TestSeveralLocalDirectoriesPickTheFirstAndKeepTheRestReadOnly(t *testing.T) {
	const daemonID = "d-mine"
	first, second, third := t.TempDir(), t.TempDir(), t.TempDir()
	task := Task{ID: "t1", ProjectResources: []ProjectResourceData{
		localDirResource(t, "r1", first, daemonID),
		localDirResource(t, "r2", second, daemonID),
		localDirResource(t, "r3", third, daemonID),
	}}

	chosen, readOnly, err := localDirectoryPlanForTask(task, daemonID)
	if err != nil {
		t.Fatalf("localDirectoryPlanForTask: %v", err)
	}
	if chosen == nil {
		t.Fatal("no directory was chosen")
	}
	if chosen.AbsPath != first {
		t.Fatalf("chosen = %q, want the first in position order %q", chosen.AbsPath, first)
	}
	if len(readOnly) != 2 || readOnly[0].AbsPath != second || readOnly[1].AbsPath != third {
		t.Fatalf("read-only = %+v, want %q and %q in order", readOnly, second, third)
	}

	// The narrow accessor used by every existing caller keeps returning just
	// the writable one, so nothing downstream had to learn about the rest.
	only, err := localDirectoryAssignmentForTask(task, daemonID)
	if err != nil || only == nil || only.AbsPath != first {
		t.Fatalf("localDirectoryAssignmentForTask = %+v, %v; want the writable directory %q", only, err, first)
	}
}

// Another machine's directories are not this machine's business, in either
// bucket: naming a path that does not exist here would send the agent looking
// for it.
func TestAnotherMachinesDirectoriesAreNeitherChosenNorListed(t *testing.T) {
	const daemonID = "d-mine"
	mine, theirs := t.TempDir(), t.TempDir()
	chosen, readOnly, err := localDirectoryPlanForTask(Task{ID: "t1", ProjectResources: []ProjectResourceData{
		localDirResource(t, "r1", theirs, "d-other"),
		localDirResource(t, "r2", mine, daemonID),
	}}, daemonID)
	if err != nil {
		t.Fatalf("localDirectoryPlanForTask: %v", err)
	}
	if chosen == nil || chosen.AbsPath != mine {
		t.Fatalf("chosen = %+v, want %q", chosen, mine)
	}
	if len(readOnly) != 0 {
		t.Fatalf("read-only = %+v, want none — the other entry belongs to a different machine", readOnly)
	}
}

// Across several projects (a multi-project chat) the first project that pins a
// directory here decides the writable one; every other local directory on this
// machine, including that project's own lower-priority rows, is read-only.
func TestTheFirstProjectThatPinsADirectoryDecidesTheWritableOne(t *testing.T) {
	const daemonID = "d-mine"
	a1, a2, b1 := t.TempDir(), t.TempDir(), t.TempDir()
	chosen, readOnly, err := localDirectoryPlanForTask(Task{
		ID: "t1",
		Projects: []ProjectContextData{
			{ID: "p1", Resources: []ProjectResourceData{localDirResource(t, "r1", a1, daemonID), localDirResource(t, "r2", a2, daemonID)}},
			{ID: "p2", Resources: []ProjectResourceData{localDirResource(t, "r3", b1, daemonID)}},
		},
	}, daemonID)
	if err != nil {
		t.Fatalf("localDirectoryPlanForTask: %v", err)
	}
	if chosen == nil || chosen.AbsPath != a1 {
		t.Fatalf("chosen = %+v, want the first project's first directory %q", chosen, a1)
	}
	got := map[string]bool{}
	for _, d := range readOnly {
		got[d.AbsPath] = true
	}
	if len(got) != 2 || !got[a2] || !got[b1] {
		t.Fatalf("read-only = %+v, want %q and %q", readOnly, a2, b1)
	}
}

// A structurally broken row still fails the task rather than being skipped on
// the way to someone else's directory: a resource the daemon cannot even name
// is not something to quietly step past.
func TestABrokenLocalDirectoryRowStillFailsTheTask(t *testing.T) {
	const daemonID = "d-mine"
	raw, err := json.Marshal(localDirectoryRef{LocalPath: "relative/path", DaemonID: daemonID})
	if err != nil {
		t.Fatal(err)
	}
	_, _, planErr := localDirectoryPlanForTask(Task{ID: "t1", ProjectResources: []ProjectResourceData{
		{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: raw},
	}}, daemonID)
	if planErr == nil {
		t.Fatal("a local_directory with a relative path was accepted")
	}
}

// The read-only directories reach the agent as read-only. The brief is the
// only place a run learns that a directory it can see is not a place to work.
func TestReadOnlyDirectoriesReachTheCodeSource(t *testing.T) {
	const daemonID = "d-mine"
	writable, other := t.TempDir(), t.TempDir()
	chosen, readOnly, err := localDirectoryPlanForTask(Task{ID: "t1", ProjectResources: []ProjectResourceData{
		localDirResource(t, "r1", writable, daemonID),
		localDirResource(t, "r2", other, daemonID),
	}}, daemonID)
	if err != nil {
		t.Fatalf("localDirectoryPlanForTask: %v", err)
	}
	src := resolveTaskCodeSource(chosen, readOnly, "", nil, func(string) []string { return nil })
	if src.LocalPath != writable {
		t.Fatalf("code source local path = %q, want %q", src.LocalPath, writable)
	}
	if len(src.ReadOnlyDirs) != 1 || src.ReadOnlyDirs[0].Path != other {
		t.Fatalf("read-only dirs = %+v, want %q", src.ReadOnlyDirs, other)
	}
	if src.ReadOnlyDirs[0].Name != filepath.Base(other) {
		t.Fatalf("read-only dir name = %q, want the directory's label %q", src.ReadOnlyDirs[0].Name, filepath.Base(other))
	}
}
