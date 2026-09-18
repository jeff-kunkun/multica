package repoident

import "testing"

func TestNormalizeURLCollapsesTransportAndCredentials(t *testing.T) {
	same := []string{
		"https://github.com/jeff-kunkun/multica",
		"https://github.com/jeff-kunkun/multica.git",
		"https://github.com/jeff-kunkun/multica/",
		"http://github.com/jeff-kunkun/multica",
		"git@github.com:jeff-kunkun/multica.git",
		"ssh://git@github.com:22/jeff-kunkun/multica",
		"git://github.com/jeff-kunkun/multica.git",
		"https://token:x-oauth-basic@github.com/jeff-kunkun/multica.git",
		"https://GitHub.com/jeff-kunkun/Multica",
	}
	want := Key("github.com/jeff-kunkun/multica")
	for _, in := range same {
		if got := NormalizeURL(in); got != want {
			t.Errorf("NormalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeURLRejectsNonRepositories(t *testing.T) {
	for _, in := range []string{"", "   ", "github.com", "https://github.com", "https://github.com/", "/Users/kunkun/.agents/multica", "./multica"} {
		if got := NormalizeURL(in); got != "" {
			t.Errorf("NormalizeURL(%q) = %q, want empty", in, got)
		}
	}
}

func TestSameRepoNeedsBothIdentified(t *testing.T) {
	if !SameRepo("git@github.com:o/r.git", "https://github.com/o/r") {
		t.Error("equivalent URLs should be the same repo")
	}
	if SameRepo("https://github.com/o/r", "https://gitlab.com/o/r") {
		t.Error("same path on different hosts is not the same repo")
	}
	// Two unidentifiable inputs must not collapse into one repository.
	if SameRepo("nonsense", "other nonsense") {
		t.Error("unidentifiable URLs must never compare equal")
	}
}

func TestNameFromLocalPathAndWeakMatch(t *testing.T) {
	if got := NameFromLocalPath("/Users/kunkun/.agents/Multica/"); got != "multica" {
		t.Errorf("NameFromLocalPath = %q, want multica", got)
	}
	if !LooksLikeSameRepo("https://github.com/jeff-kunkun/multica", "/Users/kunkun/.agents/multica") {
		t.Error("basename match should raise the duplicate hint")
	}
	if LooksLikeSameRepo("https://github.com/jeff-kunkun/multica", "/Users/kunkun/projects/online-tarot") {
		t.Error("different basenames must not hint duplicate")
	}
	// An empty path must not match a URL whose name is also empty-ish.
	if LooksLikeSameRepo("", "") {
		t.Error("empty inputs must never hint duplicate")
	}
}

func TestMatchesRemotesIsTheStrongCheck(t *testing.T) {
	remotes := []string{"git@github.com:jeff-kunkun/multica.git", "https://github.com/multica-ai/multica.git"}
	if !MatchesRemotes("https://github.com/jeff-kunkun/multica", remotes) {
		t.Error("origin should match regardless of transport")
	}
	if MatchesRemotes("https://github.com/someone/else", remotes) {
		t.Error("unrelated URL must not match")
	}
	if MatchesRemotes("", remotes) {
		t.Error("unidentifiable URL must not match any remote")
	}
}
