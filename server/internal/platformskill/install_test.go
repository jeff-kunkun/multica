package platformskill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallWritesCanonicalCopyAndLinks(t *testing.T) {
	home := t.TempDir()
	if err := Install(home); err != nil {
		t.Fatalf("Install: %v", err)
	}
	canonical := filepath.Join(home, ".agents", "skills", Name)
	body, err := os.ReadFile(filepath.Join(canonical, "SKILL.md"))
	if err != nil || !strings.Contains(string(body), "name: "+Name) {
		t.Fatalf("canonical SKILL.md missing or wrong: %v", err)
	}
	if _, err := os.Stat(filepath.Join(canonical, "references", "issues.md")); err != nil {
		t.Fatalf("references not written: %v", err)
	}
	for _, dir := range []string{".claude", ".codex"} {
		link := filepath.Join(home, dir, "skills", Name)
		dest, err := os.Readlink(link)
		if err != nil || dest != canonical {
			t.Fatalf("%s: want link to %s, got %q (%v)", link, canonical, dest, err)
		}
	}

	// Re-running replaces the managed copy, dropping files this build no
	// longer ships.
	stale := filepath.Join(canonical, "references", "skill-import.md")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Install(home); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale file survived reinstall: %v", err)
	}
}

func TestInstallLeavesUserOwnedCopiesAlone(t *testing.T) {
	home := t.TempDir()
	own := filepath.Join(home, ".claude", "skills", Name)
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(own, "SKILL.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := Install(home)
	if err == nil || !strings.Contains(err.Error(), own) {
		t.Fatalf("want an error naming %s, got %v", own, err)
	}
	if got, _ := os.ReadFile(filepath.Join(own, "SKILL.md")); string(got) != "mine" {
		t.Fatalf("user copy was overwritten: %q", got)
	}
	// The other directories are still served.
	if _, err := os.Readlink(filepath.Join(home, ".codex", "skills", Name)); err != nil {
		t.Fatalf("codex link not created: %v", err)
	}

	canonicalOwn := t.TempDir()
	if err := os.MkdirAll(filepath.Join(canonicalOwn, ".agents", "skills", Name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Install(canonicalOwn); err == nil {
		t.Fatal("want an error when the canonical directory is user-owned")
	}
}
