package execenv

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// User skills reach the per-task CODEX_HOME as links, not copies (MUL-6000).
// Copying charged every task directory the full skill tree — ~100 MB for a
// user with npm-backed skills — re-paid on every task start and never
// reclaimed while the issue stayed open.

func writeUserSkill(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md in %s: %v", dir, err)
	}
}

// assertIsLink fails when path is a real directory rather than a link to one.
// A POSIX symlink reports ModeSymlink; a Windows junction reports
// ModeDir|ModeIrregular (Go 1.23+), which is why the mask covers both.
func assertIsLink(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0 {
		t.Fatalf("%s is a real directory (mode %v), want a link", path, fi.Mode())
	}
}

func TestSeedUserCodexSkillsLinksInsteadOfCopying(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv.
	sharedHome := t.TempDir()
	t.Setenv("CODEX_HOME", sharedHome)
	userSkill := filepath.Join(sharedHome, "skills", "alpha")
	writeUserSkill(t, userSkill, "alpha v1")

	codexHome := t.TempDir()
	if err := seedUserCodexSkills(codexHome, testLogger()); err != nil {
		t.Fatalf("seedUserCodexSkills: %v", err)
	}

	dst := filepath.Join(codexHome, "skills", "alpha")
	assertIsLink(t, dst)

	body, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
	if err != nil {
		t.Fatalf("read seeded SKILL.md: %v", err)
	}
	if string(body) != "alpha v1" {
		t.Errorf("seeded SKILL.md = %q, want %q", body, "alpha v1")
	}

	// The decisive proof it is not a copy: content added to the user's real
	// skill directory after seeding is visible through the per-task path.
	if err := os.WriteFile(filepath.Join(userSkill, "reference.md"), []byte("added later"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "reference.md")); err != nil {
		t.Errorf("per-task skill dir is a snapshot copy, not a link: %v", err)
	}
}

func TestSeedUserCodexSkillsResolvesInstallerSymlink(t *testing.T) {
	// Installers like lark-cli put a symlink in ~/.codex/skills/<name>
	// pointing into a shared ~/.agents/skills/<name>/.
	agentsRoot := t.TempDir()
	realSkill := filepath.Join(agentsRoot, "lark-base")
	writeUserSkill(t, realSkill, "lark-base v1")

	sharedHome := t.TempDir()
	t.Setenv("CODEX_HOME", sharedHome)
	if err := os.MkdirAll(filepath.Join(sharedHome, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	installerLink := filepath.Join(sharedHome, "skills", "lark-base")
	if err := os.Symlink(realSkill, installerLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	codexHome := t.TempDir()
	if err := seedUserCodexSkills(codexHome, testLogger()); err != nil {
		t.Fatalf("seedUserCodexSkills: %v", err)
	}

	dst := filepath.Join(codexHome, "skills", "lark-base")
	assertIsLink(t, dst)
	body, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
	if err != nil {
		t.Fatalf("symlink-installed skill unreadable through the per-task link: %v", err)
	}
	if string(body) != "lark-base v1" {
		t.Errorf("SKILL.md = %q, want %q", body, "lark-base v1")
	}

	// The per-task link must point at the real directory, not at the
	// installer's own link, so it stays valid if the installer re-points it.
	if runtime.GOOS != "windows" {
		target, err := os.Readlink(dst)
		if err != nil {
			t.Fatalf("readlink %s: %v", dst, err)
		}
		want, err := filepath.EvalSymlinks(realSkill)
		if err != nil {
			t.Fatal(err)
		}
		if target != want {
			t.Errorf("link target = %q, want the resolved skill dir %q", target, want)
		}
	}
}

func TestSeedUserCodexSkillsWithNoEligibleSkillsCreatesNoDir(t *testing.T) {
	sharedHome := t.TempDir()
	t.Setenv("CODEX_HOME", sharedHome)
	if err := os.MkdirAll(filepath.Join(sharedHome, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}

	codexHome := t.TempDir()
	if err := seedUserCodexSkills(codexHome, testLogger()); err != nil {
		t.Fatalf("seedUserCodexSkills: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(codexHome, "skills")); !os.IsNotExist(err) {
		t.Errorf("skills dir created with nothing to seed (err = %v)", err)
	}
}

// hydrateCodexSkills wipes the skills dir before every seed, on both the
// Prepare and the Reuse path. With links in place that RemoveAll must delete
// the link and never reach through it into the user's real skill directory.
func TestHydrateCodexSkillsWipeKeepsUserSkillContent(t *testing.T) {
	sharedHome := t.TempDir()
	t.Setenv("CODEX_HOME", sharedHome)
	userSkill := filepath.Join(sharedHome, "skills", "alpha")
	writeUserSkill(t, userSkill, "alpha v1")
	if err := os.WriteFile(filepath.Join(userSkill, "reference.md"), []byte("support"), 0o644); err != nil {
		t.Fatal(err)
	}

	codexHome := t.TempDir()

	// Twice: Prepare hydrates, then a follow-up task on the same env re-hydrates.
	for i := 0; i < 2; i++ {
		if err := hydrateCodexSkills(codexHome, testLogger()); err != nil {
			t.Fatalf("hydrateCodexSkills run %d: %v", i+1, err)
		}
	}

	for _, name := range []string{"SKILL.md", "reference.md"} {
		if _, err := os.Stat(filepath.Join(userSkill, name)); err != nil {
			t.Fatalf("hydrate deleted %s from the user's real skill dir: %v", name, err)
		}
	}
	assertIsLink(t, filepath.Join(codexHome, "skills", "alpha"))
}
