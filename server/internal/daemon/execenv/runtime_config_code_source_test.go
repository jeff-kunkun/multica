package execenv

import (
	"strings"
	"testing"
)

func briefWithCodeSource(t *testing.T, ctx TaskContextForEnv) string {
	t.Helper()
	return buildMetaSkillContent("claude", ctx)
}

func TestBriefOmitsCodeSourceWhenNoDirectoryIsPinned(t *testing.T) {
	brief := briefWithCodeSource(t, TaskContextForEnv{
		IssueID: "i1",
		Repos:   []RepoContextForEnv{{URL: "https://github.com/o/r"}},
	})
	if strings.Contains(brief, "## Code Source") {
		t.Fatal("a project with no local directory must read exactly as before")
	}
	if !strings.Contains(brief, "`multica repo checkout <url> [--ref <branch-or-sha>]` to fetch") {
		t.Fatal("the remote-checkout instruction must survive untouched for remote-only projects")
	}
}

func TestBriefTellsTheAgentNotToCloneACoveredRepo(t *testing.T) {
	brief := briefWithCodeSource(t, TaskContextForEnv{
		IssueID: "i1",
		Repos:   []RepoContextForEnv{{URL: "https://github.com/jeff-kunkun/multica"}},
		CodeSource: CodeSourceForEnv{
			Kind:          "local_directory",
			LocalPath:     "/Users/kunkun/.agents/multica",
			ExecutionMode: "worktree",
			DisplayName:   "multica",
			CoveredRepos:  []CodeSourceRepoForEnv{{URL: "https://github.com/jeff-kunkun/multica"}},
		},
	})
	if !strings.Contains(brief, "## Code Source") {
		t.Fatal("a pinned directory must produce a Code Source section")
	}
	if !strings.Contains(brief, "/Users/kunkun/.agents/multica") {
		t.Error("the brief must name the directory the task actually runs in")
	}
	if !strings.Contains(brief, "do NOT run `multica repo checkout` for these") {
		t.Error("a covered repo must carry an explicit do-not-clone instruction")
	}
	if !strings.Contains(brief, "your edits do NOT touch the user's working copy") {
		t.Error("worktree mode must be explained, not just named")
	}
	// The generic "fetch it with repo checkout" line is what produced the
	// duplicate clone; it must not still be sitting above the new section.
	if strings.Contains(brief, "`multica repo checkout <url> [--ref <branch-or-sha>]` to fetch") {
		t.Error("the unconditional checkout instruction must not survive a pinned local directory")
	}
}

func TestBriefReportsAnUnverifiedDirectoryInsteadOfHidingIt(t *testing.T) {
	brief := briefWithCodeSource(t, TaskContextForEnv{
		IssueID: "i1",
		Repos:   []RepoContextForEnv{{URL: "https://github.com/kun/ai100"}},
		CodeSource: CodeSourceForEnv{
			Kind:          "local_directory",
			LocalPath:     "/Users/kunkun/code",
			ExecutionMode: "in_place",
			UnprovenRepos: []CodeSourceRepoForEnv{{URL: "https://github.com/kun/ai100", Detail: "no git remote there matches it"}},
		},
	})
	if !strings.Contains(brief, "no git remote there matches it") {
		t.Error("the reason must reach the agent verbatim; a silent skip is how this failed before")
	}
	if !strings.Contains(brief, "will refuse these") {
		t.Error("the brief must say the checkout refuses rather than clones, so the agent reports instead of retrying")
	}
}

func TestBriefKeepsRemoteOnlyReposCheckoutable(t *testing.T) {
	brief := briefWithCodeSource(t, TaskContextForEnv{
		IssueID: "i1",
		Repos:   []RepoContextForEnv{{URL: "https://github.com/o/elsewhere"}},
		CodeSource: CodeSourceForEnv{
			Kind:        "local_directory",
			LocalPath:   "/Users/kunkun/code",
			RemoteRepos: []CodeSourceRepoForEnv{{URL: "https://github.com/o/elsewhere"}},
		},
	})
	if !strings.Contains(brief, "Not in that directory — check these out normally") {
		t.Error("pinning one directory must not strand a project's other repositories")
	}
}
