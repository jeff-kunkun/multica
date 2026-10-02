package agent

import "testing"

func TestValidateCustomArgsForProviderRejectsCodexConfigSyntax(t *testing.T) {
	for _, provider := range []string{"claude", "codebuddy", "antigravity", "grok", "qwen"} {
		t.Run(provider, func(t *testing.T) {
			if err := ValidateCustomArgsForProvider(provider, []string{"-c", "model_reasoning_effort=high"}); err == nil {
				t.Fatalf("expected Codex -c syntax to be rejected for %s", provider)
			}
			if err := ValidateCustomArgsForProvider(provider, []string{"-c=model_reasoning_effort=high"}); err == nil {
				t.Fatalf("expected inline Codex -c syntax to be rejected for %s", provider)
			}
		})
	}
}

func TestValidateCustomArgsForProviderAllowsNativeOrCodexArgs(t *testing.T) {
	if err := ValidateCustomArgsForProvider("claude", []string{"-c", "--max-turns", "7"}); err != nil {
		t.Fatalf("native Claude continuation form should not be rejected: %v", err)
	}
	if err := ValidateCustomArgsForProvider("codex", []string{"-c", "model_reasoning_effort=high"}); err != nil {
		t.Fatalf("Codex config syntax should remain valid for Codex: %v", err)
	}
}
