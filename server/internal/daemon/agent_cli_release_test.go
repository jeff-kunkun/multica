package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCLIVersionNewer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		latest, current string
		newer, ok       bool
	}{
		{"2.1.9", "2.1.5 (Claude Code)", true, true},
		{"codex-cli 0.118.0", "0.118.0", false, true},
		{"1.2.0", "1.2.0", false, true},
		{"1.0.0", "1.0.1", false, true},
		{"dev", "1.0.0", false, false},
		{"2.0.0", "", false, false},
	}
	for _, tc := range cases {
		newer, ok := cliVersionNewer(tc.latest, tc.current)
		if newer != tc.newer || ok != tc.ok {
			t.Errorf("cliVersionNewer(%q, %q) = %v, %v; want %v, %v", tc.latest, tc.current, newer, ok, tc.newer, tc.ok)
		}
	}
}

func TestNPMInstallOfFollowsTheCopyInUse(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pkgDir := filepath.Join(root, "lib", "node_modules", "@anthropic-ai", "claude-code", "bin")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(pkgDir, "claude")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(binDir, "claude")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	prefix, owned := npmInstallOf(link, "@anthropic-ai/claude-code")
	if !owned {
		t.Fatal("symlink into the npm package should count as that install")
	}
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if prefix != wantRoot {
		t.Fatalf("prefix = %q, want %q", prefix, wantRoot)
	}

	other := filepath.Join(root, "elsewhere", "claude")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, owned := npmInstallOf(other, "@anthropic-ai/claude-code"); owned {
		t.Fatal("a binary outside node_modules must not be treated as the npm install")
	}
}

func TestPlanAgentCLIUpgradeKeepsTheOtherCopy(t *testing.T) {
	t.Parallel()
	spec, ok := agentCLIReleaseFor("claude")
	if !ok {
		t.Fatal("claude release missing")
	}
	steps, why := planAgentCLIUpgrade(spec, "/usr/local/bin/claude", "")
	if why != "" {
		t.Fatal(why)
	}
	if len(steps) != 1 || steps[0].Name != "native" {
		t.Fatalf("native-only plan = %#v", steps)
	}
	if steps[0].Argv[0] != "/usr/local/bin/claude" || steps[0].Argv[1] != "update" {
		t.Fatalf("argv = %#v", steps[0].Argv)
	}
}

func TestPlanAgentCLIUpgradeAddsNPMForThatPrefix(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := filepath.Join(root, "lib", "node_modules", "@openai", "codex", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec, _ := agentCLIReleaseFor("codex")
	steps, why := planAgentCLIUpgrade(spec, bin, "")
	if why != "" {
		t.Fatal(why)
	}
	if len(steps) != 1 || steps[0].Name != "npm" {
		t.Fatalf("an npm copy of codex updates only through its own prefix; plan = %#v", steps)
	}
	wantPrefix, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	gotPrefix := ""
	for i, arg := range steps[0].Argv {
		if arg == "--prefix" && i+1 < len(steps[0].Argv) {
			gotPrefix = steps[0].Argv[i+1]
		}
	}
	if gotPrefix != wantPrefix {
		t.Fatalf("prefix = %q, want %q (argv %v)", gotPrefix, wantPrefix, steps[0].Argv)
	}
}

func TestParseLatestVersions(t *testing.T) {
	t.Parallel()
	npm, err := parseNPMLatestVersion([]byte(`{"version":"2.1.9"}`))
	if err != nil || npm != "2.1.9" {
		t.Fatalf("npm = %q, %v", npm, err)
	}
	tag, err := parseGitHubLatestVersion([]byte(`{"tag_name":"v1.2.3"}`))
	if err != nil || tag != "v1.2.3" {
		t.Fatalf("github = %q, %v", tag, err)
	}
}

func TestPlanAgentCLIUpgradeCodexStandaloneUsesNativeUpdate(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	real := filepath.Join(home, ".codex", "packages", "standalone", "releases", "0.157.1-aarch64-apple-darwin", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(home, ".local", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, launcher); err != nil {
		t.Skipf("symlink: %v", err)
	}
	spec, _ := agentCLIReleaseFor("codex")
	steps, why := planAgentCLIUpgrade(spec, launcher, "")
	if why != "" {
		t.Fatal(why)
	}
	if len(steps) != 1 || steps[0].Name != "native" {
		t.Fatalf("standalone codex plan = %#v", steps)
	}
	if steps[0].Argv[0] != launcher || steps[0].Argv[1] != "update" {
		t.Fatalf("argv = %#v", steps[0].Argv)
	}
}

func TestPlanAgentCLIUpgradeCodexElsewhereHasNoUpdater(t *testing.T) {
	t.Parallel()
	spec, _ := agentCLIReleaseFor("codex")
	steps, why := planAgentCLIUpgrade(spec, "/opt/homebrew/bin/codex", "")
	if len(steps) != 0 || why == "" {
		t.Fatalf("an unknown codex copy must not be upgraded; plan = %#v, why = %q", steps, why)
	}
}

func TestCLIVersionAllowedHonorsCeiling(t *testing.T) {
	t.Parallel()
	opencode, ok := agentCLIReleaseFor("opencode")
	if !ok {
		t.Fatal("opencode release missing")
	}
	claude, _ := agentCLIReleaseFor("claude")
	cases := []struct {
		spec    agentCLIRelease
		version string
		want    bool
	}{
		{opencode, "1.19.4", true},
		{opencode, "v1.99.99", true},
		{opencode, "2.0.0", false},
		{opencode, "2.1.0", false},
		{opencode, "dev", false},
		{claude, "9.0.0", true},
	}
	for _, tc := range cases {
		if got := cliVersionAllowed(tc.spec, tc.version); got != tc.want {
			t.Errorf("cliVersionAllowed(%s, %q) = %v, want %v", tc.spec.Provider, tc.version, got, tc.want)
		}
	}
}

func TestPlanAgentCLIUpgradePinsCeilingedTarget(t *testing.T) {
	t.Parallel()
	spec, ok := agentCLIReleaseFor("opencode")
	if !ok {
		t.Fatal("opencode release missing")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "lib", "node_modules", "opencode-ai", "bin", "opencode")
	steps, why := planAgentCLIUpgrade(spec, bin, "v1.19.4")
	if why != "" || len(steps) != 2 {
		t.Fatalf("steps = %#v, why = %q", steps, why)
	}
	native := steps[0].Argv
	if steps[0].Name != "native" || len(native) != 3 || native[1] != "upgrade" || native[2] != "1.19.4" {
		t.Fatalf("native = %#v, want `opencode upgrade 1.19.4`", native)
	}
	if npm := steps[1].Argv; steps[1].Name != "npm" || npm[3] != "opencode-ai@1.19.4" {
		t.Fatalf("npm = %#v, want opencode-ai@1.19.4", npm)
	}

	claude, _ := agentCLIReleaseFor("claude")
	steps, _ = planAgentCLIUpgrade(claude, "/usr/local/bin/claude", "2.1.9")
	if len(steps) != 1 || len(steps[0].Argv) != 2 {
		t.Fatalf("claude without a ceiling must not pin: %#v", steps)
	}
}
