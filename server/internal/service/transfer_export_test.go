package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

type fakeTransferSource struct {
	payloads map[string]any
	errors   map[string]error
}

func (f *fakeTransferSource) GetJSON(_ context.Context, path string, out any) error {
	if err, ok := f.errors[path]; ok {
		return err
	}
	v, ok := f.payloads[path]
	if !ok {
		return json.Unmarshal([]byte("[]"), out)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (f *fakeTransferSource) GetBytes(context.Context, string) ([]byte, error) {
	return nil, fmt.Errorf("not implemented")
}

func TestExportAgentsGroup_SystemKeyWithoutKind(t *testing.T) {
	src := &fakeTransferSource{payloads: map[string]any{
		"/api/agents": []map[string]any{
			{"id": "sys-1", "system_key": "mika", "instructions": "hi", "name": "Mika"},
			{"id": "usr-1", "name": "Bot", "instructions": "do work"},
			{"id": "bldr-1", "system_key": "agent_builder:hidden", "name": "Builder"},
		},
	}}
	bundle := &ConfigBundle{Entities: ConfigEntities{}, Stats: map[string]int{}}
	var gaps []TransferExportGap
	exportAgentsGroup(context.Background(), src, bundle, &gaps, func(string, error) {})

	if len(bundle.Entities.SystemAgents) != 1 || bundle.Entities.SystemAgents[0].SystemKey != "mika" {
		t.Fatalf("system_agents=%v", bundle.Entities.SystemAgents)
	}
	if len(bundle.Entities.Agents) != 1 || bundle.Entities.Agents[0].Name != "Bot" {
		t.Fatalf("agents=%v", bundle.Entities.Agents)
	}
	if bundle.Stats["system_agents"] != 1 || bundle.Stats["agents"] != 1 {
		t.Fatalf("stats=%v", bundle.Stats)
	}
}

func TestSourceExportSkills_ExcludesPluginResources(t *testing.T) {
	wsID := "ws-1"
	src := &fakeTransferSource{payloads: map[string]any{
		"/api/workspaces/ws-1/plugins": map[string]any{
			"plugins": []map[string]any{{
				"plugin_key": "demo",
				"resources": []map[string]any{
					{"type": "skill", "key": "pr-review"},
					{"type": "surface", "key": "not-a-skill"},
				},
			}},
		},
		"/api/skills": []map[string]any{
			{"id": "sk-1", "name": "pr-review", "content": "plugin skill"},
			{"id": "sk-2", "name": "my-notes", "content": "user skill"},
		},
	}}
	bundle := &ConfigBundle{Entities: ConfigEntities{}, Stats: map[string]int{}}
	var gaps []TransferExportGap
	sourceExportSkills(context.Background(), src, wsID, bundle, &gaps, func(string, error) {})
	if len(gaps) != 0 {
		t.Fatalf("unexpected gaps=%v", gaps)
	}
	if len(bundle.Entities.Skills) != 1 || bundle.Entities.Skills[0].Name != "my-notes" {
		t.Fatalf("skills=%v", bundle.Entities.Skills)
	}
}

func TestSourceExportSkills_PluginsUnavailableExportsAllWithGap(t *testing.T) {
	wsID := "ws-1"
	src := &fakeTransferSource{
		payloads: map[string]any{
			"/api/skills": []map[string]any{
				{"id": "sk-1", "name": "pr-review", "content": "plugin skill"},
				{"id": "sk-2", "name": "my-notes", "content": "user skill"},
			},
		},
		errors: map[string]error{
			"/api/workspaces/ws-1/plugins": &TransferHTTPError{Status: 503, Err: fmt.Errorf("Plugin management is not enabled")},
		},
	}
	bundle := &ConfigBundle{Entities: ConfigEntities{}, Stats: map[string]int{}}
	var gaps []TransferExportGap
	sourceExportSkills(context.Background(), src, wsID, bundle, &gaps, func(string, error) {})
	if len(bundle.Entities.Skills) != 2 {
		t.Fatalf("want all skills exported, got %d", len(bundle.Entities.Skills))
	}
	if len(gaps) != 1 || gaps[0].Reason != "plugin_skills_unfiltered" || gaps[0].Group != "skills" || gaps[0].Status != 503 {
		t.Fatalf("gaps=%v", gaps)
	}
}
