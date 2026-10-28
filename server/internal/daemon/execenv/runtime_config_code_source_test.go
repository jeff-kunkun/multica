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
			Kind:         "local_directory",
			LocalPath:    "/Users/kunkun/.agents/multica",
			DisplayName:  "multica",
			CoveredRepos: []CodeSourceRepoForEnv{{URL: "https://github.com/jeff-kunkun/multica"}},
		},
	})
	if !strings.Contains(brief, "## Code Source") {
		t.Fatal("a pinned directory must produce a Code Source section")
	}
	if !strings.Contains(brief, "/Users/kunkun/.agents/multica") {
		t.Error("the brief must name the directory the task actually runs in")
	}
	if !strings.Contains(brief, "Already here, do NOT `multica repo checkout`: https://github.com/jeff-kunkun/multica") {
		t.Error("a covered repo must carry an explicit do-not-clone instruction")
	}
	if !strings.Contains(brief, "don't switch branches, reset, or discard changes you didn't make") {
		t.Error("a bound folder must carry the shared-folder risk sentence")
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
			UnprovenRepos: []CodeSourceRepoForEnv{{URL: "https://github.com/kun/ai100", Detail: "no git remote there matches it"}},
		},
	})
	if !strings.Contains(brief, "no git remote there matches it") {
		t.Error("the reason must reach the agent verbatim; a silent skip is how this failed before")
	}
	if !strings.Contains(brief, "`multica repo checkout` refuses them; report it") {
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
	if !strings.Contains(brief, "Not here, check out with `multica repo checkout <url>`: https://github.com/o/elsewhere") {
		t.Error("pinning one directory must not strand a project's other repositories")
	}
}

func TestBriefNamesTheBranchAndTheOtherTasksInTheFolder(t *testing.T) {
	brief := briefWithCodeSource(t, TaskContextForEnv{
		IssueID: "i1",
		CodeSource: CodeSourceForEnv{
			Kind:      "local_directory",
			LocalPath: "/Users/kunkun/code/app",
			Branch:    "main",
			CoRunners: []CodeSourceCoRunnerForEnv{
				{Identifier: "DENE-7", Title: "Fix login", Branch: "fix-login"},
				{Title: "Chat about the build"},
			},
		},
	})
	// The identifier is enough to look a co-runner up (DENE-2679); a run
	// without one is named by its title.
	for _, want := range []string{
		"You run in `/Users/kunkun/code/app` (branch `main`)",
		"- Also running here now: DENE-7 (branch `fix-login`), Chat about the build\n",
		"put a separate change in a git worktree beside the folder",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief missing %q", want)
		}
	}
}

func TestBriefOmitsTheCoRunnerListWhenAloneInTheFolder(t *testing.T) {
	brief := briefWithCodeSource(t, TaskContextForEnv{
		IssueID:    "i1",
		CodeSource: CodeSourceForEnv{Kind: "local_directory", LocalPath: "/Users/kunkun/code/app"},
	})
	if strings.Contains(brief, "Also running here now") {
		t.Error("no co-runner list without other tasks")
	}
	if strings.Contains(brief, "(branch `") {
		t.Error("no branch when the branch is unknown")
	}
	if !strings.Contains(brief, "Others may work here too") {
		t.Error("the risk sentence applies even when the folder is free right now")
	}
}

// Without a bound folder the checkout sits in the task's own workdir, which
// GC removes later, so committed work has to reach the remote. In a bound
// folder the work stays on the user's disk and the sentence would mislead.
func TestBriefAsksToPushOnlyWithoutABoundFolder(t *testing.T) {
	const push = "push your branch to the remote before you finish"
	remote := briefWithCodeSource(t, TaskContextForEnv{
		IssueID: "i1",
		Repos:   []RepoContextForEnv{{URL: "https://github.com/o/r"}},
	})
	if !strings.Contains(remote, push) {
		t.Error("a run in its own workdir is not told to push its branch")
	}
	local := briefWithCodeSource(t, TaskContextForEnv{
		IssueID:    "i1",
		Repos:      []RepoContextForEnv{{URL: "https://github.com/o/r"}},
		CodeSource: CodeSourceForEnv{Kind: "local_directory", LocalPath: "/Users/kunkun/code/app"},
	})
	if strings.Contains(local, push) {
		t.Error("a run in the bound folder was told its workdir is cleaned up")
	}
}
