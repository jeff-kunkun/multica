package localdir

import (
	"path/filepath"
	"testing"
)

func TestProbeReportsIdentityForAnExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	got := Probe(dir)
	if !got.Exists {
		t.Fatal("Probe of a real directory reported Exists=false")
	}
	if got.RealPath == "" {
		t.Fatal("Probe of a real directory reported no real_path")
	}
	if got.IsGitRepo {
		t.Fatal("a fresh temp dir is not a git repository")
	}
}

func TestProbeOfAMissingPathReportsNothing(t *testing.T) {
	got := Probe(filepath.Join(t.TempDir(), "does-not-exist"))
	if got.Exists || got.RealPath != "" || got.IsGitRepo || got.RepoKey != "" {
		t.Fatalf("missing path produced identity: %+v", got)
	}
}
