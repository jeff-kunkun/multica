package daemon

import (
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
	for _, p := range []string{"codex", "hermes", "cursor", "copilot", "opencode", "pi", "mcode", "", "made-up"} {
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
