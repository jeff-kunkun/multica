package main

import (
	"encoding/json"
	"testing"

	"github.com/spf13/cobra"
)

func TestParseSwitchableModels(t *testing.T) {
	got, err := parseSwitchableModels(`[{"model":"claude-opus-5","role":"default","note":"首选"},{"model":"gpt-6","role":"batch"}]`)
	if err != nil {
		t.Fatalf("parseSwitchableModels() error = %v", err)
	}
	if len(got) != 2 || got[0].Model != "claude-opus-5" || got[1].Role != "batch" || got[0].Note != "首选" {
		t.Fatalf("parseSwitchableModels() = %#v", got)
	}

	cleared, err := parseSwitchableModels(`[]`)
	if err != nil {
		t.Fatalf("parseSwitchableModels([]) error = %v", err)
	}
	if encoded, _ := json.Marshal(cleared); string(encoded) != "[]" {
		t.Fatalf("clear encodes as %s, want []", encoded)
	}

	for _, raw := range []string{"", "  ", "null", `{"model":"x"}`, "not json"} {
		if _, err := parseSwitchableModels(raw); err == nil {
			t.Fatalf("parseSwitchableModels(%q) error = nil, want error", raw)
		}
	}
}

func TestApplySwitchableModelsFlagOnlyWhenChanged(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("switchable-models", "", "")

	body := map[string]any{}
	if err := applySwitchableModelsFlag(cmd, body); err != nil {
		t.Fatalf("applySwitchableModelsFlag() error = %v", err)
	}
	if _, ok := body["switchable_models"]; ok {
		t.Fatalf("unset --switchable-models must be omitted; got %v", body)
	}

	_ = cmd.Flags().Set("switchable-models", `[]`)
	if err := applySwitchableModelsFlag(cmd, body); err != nil {
		t.Fatalf("applySwitchableModelsFlag() error = %v", err)
	}
	if encoded, _ := json.Marshal(body["switchable_models"]); string(encoded) != "[]" {
		t.Fatalf("explicit clear body = %s, want []", encoded)
	}
}
