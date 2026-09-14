package daemon

import (
	"path/filepath"
	"strings"
	"testing"
)

// The provider table is what keeps shared mode honest: a provider is listed
// only with a verified sidecar-free route for the brief, and everything else
// is refused before Prepare instead of started with no brief and no skills.
func TestSharedModeBriefDelivery(t *testing.T) {
	t.Parallel()

	if got := sharedModeBriefDelivery("claude"); got != sharedBriefViaClaudeFlags {
		t.Errorf("claude = %v, want sharedBriefViaClaudeFlags (spike-verified --add-dir / --append-system-prompt-file)", got)
	}
	if got := sharedModeBriefDelivery("codex"); got != sharedBriefViaCodexHome {
		t.Errorf("codex = %v, want sharedBriefViaCodexHome (CODEX_HOME/AGENTS.md)", got)
	}
	if err := sharedModeProviderSupported("codex"); err != nil {
		t.Errorf("sharedModeProviderSupported(codex) = %v, want nil", err)
	}
	// DSH (and grok) load AGENTS.md from cwd in the non-shared path, so they
	// are not in providerNeedsInlineSystemPrompt. Shared mode still has to
	// prepend SystemPrompt because the brief file sits under the sidecar.
	if got := sharedModeBriefDelivery("dsh"); got != sharedBriefInline {
		t.Errorf("dsh = %v, want sharedBriefInline (execute prompt prepend)", got)
	}
	if err := sharedModeProviderSupported("dsh"); err != nil {
		t.Errorf("sharedModeProviderSupported(dsh) = %v, want nil", err)
	}
	if got := sharedModeBriefDelivery("grok"); got != sharedBriefInline {
		t.Errorf("grok = %v, want sharedBriefInline", got)
	}
	// Every provider that already runs on the inline brief in production must
	// keep working in shared mode, since inline delivery needs no cwd file.
	for _, p := range []string{"openclaw", "kimi", "traecli", "qwenpaw"} {
		if !providerNeedsInlineSystemPrompt(p) {
			t.Fatalf("%s no longer needs an inline brief; update this test's premise", p)
		}
		if got := sharedModeBriefDelivery(p); got != sharedBriefInline {
			t.Errorf("%s = %v, want sharedBriefInline", p, got)
		}
		if err := sharedModeProviderSupported(p); err != nil {
			t.Errorf("sharedModeProviderSupported(%s) = %v, want nil", p, err)
		}
	}
	// Disk-only readers stay refused until their own route is verified.
	// mcode ignores ExecOptions.SystemPrompt and only reads cwd AGENTS.md,
	// so it must not pass the shared-mode gate (DENE-125).
	for _, p := range []string{"hermes", "cursor", "copilot", "opencode", "pi", "mcode", "", "made-up"} {
		if got := sharedModeBriefDelivery(p); got != sharedBriefUnsupported {
			t.Errorf("%q = %v, want sharedBriefUnsupported", p, got)
		}
		err := sharedModeProviderSupported(p)
		if err == nil {
			t.Errorf("sharedModeProviderSupported(%q) = nil, want a refusal", p)
			continue
		}
		if !strings.Contains(err.Error(), "shared") || !strings.Contains(err.Error(), "in_place") {
			t.Errorf("refusal for %q should name the mode and an alternative, got %q", p, err)
		}
	}
}

func TestSharedModeBriefRoot(t *testing.T) {
	t.Parallel()

	workDir := "/user/project"
	sidecar := "/env/sidecar"
	codexHome := "/env/codex-home"

	got, err := sharedModeBriefRoot("claude", sidecar, "", workDir)
	if err != nil {
		t.Fatalf("claude shared: %v", err)
	}
	if got != sidecar {
		t.Errorf("claude shared brief root = %q, want sidecar %q", got, sidecar)
	}

	got, err = sharedModeBriefRoot("codex", sidecar, codexHome, workDir)
	if err != nil {
		t.Fatalf("codex shared: %v", err)
	}
	if got != codexHome {
		t.Errorf("codex shared brief root = %q, want CODEX_HOME %q", got, codexHome)
	}

	if _, err := sharedModeBriefRoot("codex", sidecar, "", workDir); err == nil {
		t.Fatal("codex shared with empty CODEX_HOME: want an error, got nil")
	} else if !strings.Contains(err.Error(), "CODEX_HOME") {
		t.Errorf("empty CODEX_HOME error = %q, want it to name CODEX_HOME", err)
	}

	got, err = sharedModeBriefRoot("codex", "", codexHome, workDir)
	if err != nil {
		t.Fatalf("codex non-shared: %v", err)
	}
	if got != workDir {
		t.Errorf("codex non-shared brief root = %q, want cwd %q (MUL-5392)", got, workDir)
	}
}

func TestSharedModeSkillsDir(t *testing.T) {
	t.Parallel()

	sidecar := "/env/sidecar"
	codexHome := "/env/codex-home"

	if got := sharedModeSkillsDir("codex", sidecar, codexHome); got != filepath.Join(codexHome, "skills") {
		t.Errorf("codex shared skills dir = %q, want CODEX_HOME/skills", got)
	}
	if got := sharedModeSkillsDir("claude", sidecar, ""); !strings.HasSuffix(got, filepath.Join(".claude", "skills")) {
		t.Errorf("claude shared skills dir = %q, want sidecar .claude/skills", got)
	}
	if got := sharedModeSkillsDir("codex", "", codexHome); got != "" {
		t.Errorf("codex non-shared skills dir = %q, want empty (native discovery)", got)
	}
}
