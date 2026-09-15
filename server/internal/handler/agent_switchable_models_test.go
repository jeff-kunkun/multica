package handler

import (
	"encoding/json"
	"strings"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestNormaliseAgentSwitchableModels(t *testing.T) {
	t.Run("trims entries and keeps order", func(t *testing.T) {
		got, err := normaliseAgentSwitchableModels([]AgentSwitchableModel{
			{Model: " claude-opus-5 ", Role: "default", Note: " 首选 "},
			{Model: "gpt-6", Role: "fallback"},
			{Model: "grok-5", Role: "batch", Note: "夜间跑批"},
		})
		if err != nil {
			t.Fatalf("normaliseAgentSwitchableModels() error = %v", err)
		}
		want := []AgentSwitchableModel{
			{Model: "claude-opus-5", Role: "default", Note: "首选"},
			{Model: "gpt-6", Role: "fallback"},
			{Model: "grok-5", Role: "batch", Note: "夜间跑批"},
		}
		if len(got) != len(want) {
			t.Fatalf("normaliseAgentSwitchableModels() = %#v, want %#v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("entry %d = %#v, want %#v", i, got[i], want[i])
			}
		}
	})

	t.Run("empty list clears", func(t *testing.T) {
		got, err := normaliseAgentSwitchableModels([]AgentSwitchableModel{})
		if err != nil || got == nil || len(got) != 0 {
			t.Fatalf("normaliseAgentSwitchableModels([]) = %#v, %v; want empty non-nil slice", got, err)
		}
		encoded, _ := json.Marshal(got)
		if string(encoded) != "[]" {
			t.Fatalf("encoded clear = %s, want []", encoded)
		}
	})

	tooMany := make([]AgentSwitchableModel, maxAgentSwitchableModels+1)
	for i := range tooMany {
		tooMany[i] = AgentSwitchableModel{Model: "m", Role: "batch"}
	}
	for name, models := range map[string][]AgentSwitchableModel{
		"too many":     tooMany,
		"blank model":  {{Model: " ", Role: "default"}},
		"unknown role": {{Model: "m", Role: "primary"}},
		"missing role": {{Model: "m"}},
		"long model":   {{Model: strings.Repeat("m", maxAgentSwitchableModelIDLength+1), Role: "default"}},
		"long note":    {{Model: "m", Role: "default", Note: strings.Repeat("n", maxAgentSwitchableModelNoteLength+1)}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normaliseAgentSwitchableModels(models); err == nil {
				t.Fatalf("normaliseAgentSwitchableModels(%s) error = nil, want error", name)
			}
		})
	}
}

func TestAgentToResponseSwitchableModels(t *testing.T) {
	h := &Handler{}

	t.Run("decodes stored lineup", func(t *testing.T) {
		resp := h.agentToResponse(db.Agent{
			SwitchableModels: []byte(`[{"model":"claude-opus-5","role":"default","note":""},{"model":"gpt-6","role":"batch","note":"借档"}]`),
		})
		if len(resp.SwitchableModels) != 2 || resp.SwitchableModels[1].Model != "gpt-6" || resp.SwitchableModels[1].Note != "借档" {
			t.Fatalf("SwitchableModels = %#v", resp.SwitchableModels)
		}
	})

	t.Run("missing or malformed column serializes as empty array", func(t *testing.T) {
		for _, raw := range [][]byte{nil, []byte(`{"not":"an array"}`)} {
			resp := h.agentToResponse(db.Agent{SwitchableModels: raw})
			encoded, _ := json.Marshal(resp.SwitchableModels)
			if string(encoded) != "[]" {
				t.Fatalf("SwitchableModels for %q = %s, want []", raw, encoded)
			}
		}
	})
}
