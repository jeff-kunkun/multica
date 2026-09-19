package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// A local_directory's identity fields and the rules they enable (DENE-617).
// The uniqueness rules themselves are also Postgres partial unique indexes
// (migrations 498/499); these cover the application's copy, which exists to
// turn a violation into a sentence naming the directory.

func localRef(t *testing.T, fields map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func normalizeLocalRef(t *testing.T, fields map[string]any) localDirectoryRef {
	t.Helper()
	out, err := validateAndNormalizeResourceRef("local_directory", localRef(t, fields))
	if err != nil {
		t.Fatalf("validateAndNormalizeResourceRef: %v", err)
	}
	var ref localDirectoryRef
	if err := json.Unmarshal(out, &ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

// real_path is the directory's identity. Absent, it falls back to local_path —
// which is what the rule compared before the field existed, so old rows keep
// their meaning and the DB index (which COALESCEs the same two) agrees.
func TestLocalDirectoryIdentityPrefersRealPathAndFallsBackToLocalPath(t *testing.T) {
	withReal := localDirectoryRef{LocalPath: "/tmp/link", RealPath: "/private/tmp/target"}
	if got := localDirectoryIdentity(withReal); got != "/private/tmp/target" {
		t.Fatalf("identity = %q, want the resolved real_path", got)
	}
	legacy := localDirectoryRef{LocalPath: "/Users/me/code/app"}
	if got := localDirectoryIdentity(legacy); got != "/Users/me/code/app" {
		t.Fatalf("identity of a row with no real_path = %q, want its local_path", got)
	}
	if got := localDirectoryIdentity(localDirectoryRef{}); got != "" {
		t.Fatalf("identity of an empty ref = %q, want empty — an unidentifiable ref must not collide with another", got)
	}
}

// repo_key is normalized on the way in, so a client sending a full remote URL
// and one sending the already-normalized key produce the same stored value.
// Without that, one project could hold two rows for one repository simply
// because two clients spelled it differently.
func TestLocalDirectoryRepoKeyIsNormalizedOnSave(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://github.com/Owner/Repo.git", "github.com/owner/repo"},
		{"git@github.com:Owner/Repo.git", "github.com/owner/repo"},
		{"github.com/owner/repo", "github.com/owner/repo"},
		// A path is a location on one machine, not a repository identity —
		// letting it parse would make two unrelated folders compare equal.
		{"/Users/me/code/app", ""},
		{"", ""},
	}
	for _, tc := range cases {
		ref := normalizeLocalRef(t, map[string]any{
			"local_path": "/Users/me/code/app", "daemon_id": "d1", "repo_key": tc.in,
		})
		if ref.RepoKey != tc.want {
			t.Errorf("repo_key %q normalized to %q, want %q", tc.in, ref.RepoKey, tc.want)
		}
	}
}

// Invariant 12: a folder the machine holding it proved has no repository
// cannot be stored as parallel mode. Parallel mode delivers work as a branch;
// without a repository every single task on it would fail.
func TestWorktreeModeIsRefusedOnAProvenNonGitFolder(t *testing.T) {
	_, err := validateAndNormalizeResourceRef("local_directory", localRef(t, map[string]any{
		"local_path": "/Users/me/notes", "daemon_id": "d1",
		"execution_mode": "worktree", "is_git_repo": false,
	}))
	if err == nil {
		t.Fatal("parallel mode was accepted on a folder proven not to be a repository")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("error = %v, want it to say the folder is not a git repository", err)
	}

	// Proven-a-repo is fine, and so is absent: nobody checked is not proof,
	// and the daemon re-checks authoritatively at task time. Guessing "not a
	// repo" here would block a perfectly valid setup on an older client.
	for name, fields := range map[string]map[string]any{
		"proven a repo": {"local_path": "/Users/me/code/app", "daemon_id": "d1", "execution_mode": "worktree", "is_git_repo": true},
		"nobody looked": {"local_path": "/Users/me/code/app", "daemon_id": "d1", "execution_mode": "worktree"},
	} {
		if _, err := validateAndNormalizeResourceRef("local_directory", localRef(t, fields)); err != nil {
			t.Errorf("%s: parallel mode was refused: %v", name, err)
		}
	}

	// A plain folder stays perfectly legal in every other mode (form C).
	if _, err := validateAndNormalizeResourceRef("local_directory", localRef(t, map[string]any{
		"local_path": "/Users/me/notes", "daemon_id": "d1", "is_git_repo": false,
	})); err != nil {
		t.Errorf("a plain folder was refused in the default mode: %v", err)
	}
}

func TestLocalDirectoryPathsMustBeAbsolute(t *testing.T) {
	for _, field := range []string{"real_path", "worktree_root"} {
		_, err := validateAndNormalizeResourceRef("local_directory", localRef(t, map[string]any{
			"local_path": "/Users/me/code/app", "daemon_id": "d1", field: "relative/path",
		}))
		if err == nil {
			t.Errorf("%s accepted a relative path; it would resolve against whatever the daemon's cwd happens to be", field)
		}
	}
}

// Invariant 15/18: what a daemon receives is a subset of what it declared it
// can handle. A daemon that predates user-disk working copies would ignore
// worktree_root and build the copy inside its env root — so the field is
// stripped, and that daemon behaves exactly as it does today rather than
// appearing to honour a setting it cannot implement.
func TestWorktreeRootIsStrippedForDaemonsThatCannotHonourIt(t *testing.T) {
	resources := []ProjectResourceData{
		{ResourceType: "local_directory", ResourceRef: localRef(t, map[string]any{
			"local_path": "/Users/me/code/app", "daemon_id": "d1",
			"execution_mode": "worktree", "worktree_root": "/Users/me/code/app.multica-worktrees",
			"label": "app",
		})},
		{ResourceType: "github_repo", ResourceRef: localRef(t, map[string]any{"url": "https://github.com/o/r"})},
	}

	stripped := filterResourcesForDaemonCapabilities(resources, false)
	var ref map[string]any
	if err := json.Unmarshal(stripped[0].ResourceRef, &ref); err != nil {
		t.Fatal(err)
	}
	if _, present := ref["worktree_root"]; present {
		t.Fatal("worktree_root reached a daemon that cannot honour it")
	}
	// Everything else the old daemon DOES implement must survive untouched,
	// or stripping one field would silently change where the task runs.
	for key, want := range map[string]any{
		"local_path": "/Users/me/code/app", "daemon_id": "d1", "execution_mode": "worktree", "label": "app",
	} {
		if ref[key] != want {
			t.Errorf("%s = %v after stripping, want %v", key, ref[key], want)
		}
	}
	if string(stripped[1].ResourceRef) != string(resources[1].ResourceRef) {
		t.Error("a github_repo resource was rewritten by the local_directory filter")
	}

	// The caller's slice is not mutated: the same response is built once and
	// the gates below read it.
	var original map[string]any
	if err := json.Unmarshal(resources[0].ResourceRef, &original); err != nil {
		t.Fatal(err)
	}
	if _, present := original["worktree_root"]; !present {
		t.Fatal("filtering mutated the caller's resource slice")
	}

	// A capable daemon gets the field.
	kept := filterResourcesForDaemonCapabilities(resources, true)
	if !strings.Contains(string(kept[0].ResourceRef), "worktree_root") {
		t.Fatal("worktree_root was stripped for a daemon that advertises support for it")
	}
}

// The capability string is part of the wire contract in both directions:
// renaming it on one side alone silently downgrades every daemon.
func TestUserWorktreeRootCapabilityName(t *testing.T) {
	if protocol.DaemonCapabilityLocalWorktreeUserRootV1 != "local-worktree-user-root-v1" {
		t.Fatalf("capability = %q; daemons advertise the literal string, so renaming it strips worktree_root for every one of them",
			protocol.DaemonCapabilityLocalWorktreeUserRootV1)
	}
}
