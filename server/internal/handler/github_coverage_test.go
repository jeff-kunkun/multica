package handler

import (
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestGitHubRepoFullName(t *testing.T) {
	cases := map[string]string{
		"https://github.com/kkunkunya/online-tarot":  "kkunkunya/online-tarot",
		"https://github.com/jeff-kunkun/multica.git": "jeff-kunkun/multica",
		"git@github.com:jeff-kunkun/ai100.git":       "jeff-kunkun/ai100",
		"ssh://git@github.com/Org/Repo":              "Org/Repo",
		"https://GitHub.com/Org/Repo/":               "Org/Repo",
	}
	for in, want := range cases {
		got, ok := githubRepoFullName(in)
		if !ok || got != want {
			t.Errorf("githubRepoFullName(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "https://gitlab.com/a/b", "https://github.com/only-owner", "https://github.com/a/b/tree/main"} {
		if got, ok := githubRepoFullName(in); ok {
			t.Errorf("githubRepoFullName(%q) = %q, want not ok", in, got)
		}
	}
}

func TestBuildGitHubCoverage(t *testing.T) {
	insts := []db.GithubInstallation{
		{InstallationID: 1, AccountLogin: "kkunkunya"},
		{InstallationID: 2, AccountLogin: "jeff-kunkun"},
	}
	repos := map[int64][]GitHubRepositoryResponse{
		1: {{FullName: "kkunkunya/online-tarot", HTMLURL: "https://github.com/kkunkunya/online-tarot"}},
		2: {
			{FullName: "jeff-kunkun/multica", HTMLURL: "https://github.com/jeff-kunkun/multica"},
			{FullName: "jeff-kunkun/extra", HTMLURL: "https://github.com/jeff-kunkun/extra"},
		},
	}
	registered := []string{
		"https://github.com/KKunkunya/online-tarot",
		"https://github.com/jeff-kunkun/multica.git",
		"https://github.com/jeff-kunkun/multica",
		"https://github.com/jeff-kunkun/ai100.git",
		"/Users/local/checkout",
	}
	got := buildGitHubCoverage(registered, insts, repos)

	if len(got.Registered) != 3 {
		t.Fatalf("registered = %+v, want 3 deduped github repos", got.Registered)
	}
	want := map[string][]string{
		"KKunkunya/online-tarot": {"kkunkunya"},
		"jeff-kunkun/multica":    {"jeff-kunkun"},
		"jeff-kunkun/ai100":      {},
	}
	for _, r := range got.Registered {
		w, ok := want[r.FullName]
		if !ok {
			t.Fatalf("unexpected registered repo %q", r.FullName)
		}
		if len(r.CoveredBy) != len(w) || (len(w) == 1 && r.CoveredBy[0] != w[0]) {
			t.Errorf("%s covered_by = %v, want %v", r.FullName, r.CoveredBy, w)
		}
	}
	if len(got.Unregistered) != 1 || got.Unregistered[0].FullName != "jeff-kunkun/extra" || got.Unregistered[0].AccountLogin != "jeff-kunkun" {
		t.Errorf("unregistered = %+v, want only jeff-kunkun/extra", got.Unregistered)
	}
}
