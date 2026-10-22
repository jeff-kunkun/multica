package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/coderesolve"
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

// Older clients still send execution_mode and worktree_root. Every run goes
// into the bound folder now, so both are accepted and dropped on save — any
// value, including ones the old enum refused.
func TestOldExecutionModeFieldsAreAcceptedAndDropped(t *testing.T) {
	for _, fields := range []map[string]any{
		{"execution_mode": "worktree", "is_git_repo": false},
		{"execution_mode": "shared"},
		{"execution_mode": "something-else"},
		{"worktree_root": "relative/path"},
	} {
		fields["local_path"] = "/Users/me/code/app"
		fields["daemon_id"] = "d1"
		out, err := validateAndNormalizeResourceRef("local_directory", localRef(t, fields))
		if err != nil {
			t.Fatalf("%v refused: %v", fields, err)
		}
		if strings.Contains(string(out), "execution_mode") || strings.Contains(string(out), "worktree_root") {
			t.Errorf("stored ref kept an old mode field: %s", out)
		}
	}
}

func TestGithubRepoStoresNormalizedRepoKey(t *testing.T) {
	out, err := validateAndNormalizeResourceRef("github_repo", localRef(t, map[string]any{
		"url": "git@github.com:Owner/Repo.git",
	}))
	if err != nil {
		t.Fatal(err)
	}
	var ref githubRepoRef
	if err := json.Unmarshal(out, &ref); err != nil {
		t.Fatal(err)
	}
	if ref.RepoKey != "github.com/owner/repo" {
		t.Fatalf("repo_key = %q, want github.com/owner/repo (host and path lowercased: DNS and GitHub are case-insensitive, so mixed case would let the same repository bind twice)", ref.RepoKey)
	}
}

func TestLocalDirectoryPathsMustBeAbsolute(t *testing.T) {
	for _, field := range []string{"real_path"} {
		_, err := validateAndNormalizeResourceRef("local_directory", localRef(t, map[string]any{
			"local_path": "/Users/me/code/app", "daemon_id": "d1", field: "relative/path",
		}))
		if err == nil {
			t.Errorf("%s accepted a relative path; it would resolve against whatever the daemon's cwd happens to be", field)
		}
	}
}

// The capability-narrowing matrix is canonical in
// internal/coderesolve/filter_test.go, where the rule lives. What is left here
// is the handler's own contract: the conversion is faithful and the caller's
// slice is not mutated, because the same response is built once and read
// again afterwards.
func TestFilterForDaemonCapabilitiesConvertsWithoutMutatingTheCaller(t *testing.T) {
	resources := []ProjectResourceData{
		{ID: "r1", ResourceType: "local_directory", Label: "app", ResourceRef: localRef(t, map[string]any{
			"local_path": "/Users/me/code/app", "daemon_id": "d1",
		})},
		{ID: "r2", ResourceType: "github_repo", ResourceRef: localRef(t, map[string]any{"url": "https://github.com/o/r"})},
		{ID: "r3", ResourceType: "local_directory", ResourceRef: localRef(t, map[string]any{
			"local_path": "/Users/me/code/docs", "daemon_id": "d1",
		})},
	}

	narrowed := filterResourcesForDaemonCapabilities(resources, coderesolve.Daemon{ID: "d1"})

	if len(narrowed) != 2 || narrowed[0].ID != "r1" || narrowed[1].ID != "r2" {
		t.Fatalf("delivered %+v, want r1 and r2", narrowed)
	}
	if narrowed[0].Label != "app" || narrowed[0].ResourceType != "local_directory" {
		t.Errorf("conversion lost fields: %+v", narrowed[0])
	}
	if string(narrowed[1].ResourceRef) != string(resources[1].ResourceRef) {
		t.Error("a github_repo resource was rewritten by the local_directory filter")
	}
	if len(resources) != 3 || resources[2].ID != "r3" {
		t.Fatal("filtering mutated the caller's resource slice")
	}

	full := filterResourcesForDaemonCapabilities(resources, coderesolve.Daemon{
		ID: "d1", MultiLocalDirectory: true,
	})
	if len(full) != len(resources) {
		t.Errorf("a capable daemon received %d of %d resources", len(full), len(resources))
	}
}

// Shipped daemons declare several directories through this capability (see
// daemonForRequest), so the literal string is a wire contract.
func TestUserWorktreeRootCapabilityName(t *testing.T) {
	if protocol.DaemonCapabilityLocalWorktreeUserRootV1 != "local-worktree-user-root-v1" {
		t.Fatalf("capability = %q; daemons advertise the literal string, so renaming it narrows every one of them to a single directory",
			protocol.DaemonCapabilityLocalWorktreeUserRootV1)
	}
}
