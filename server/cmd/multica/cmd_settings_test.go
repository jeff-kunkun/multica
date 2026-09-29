package main

import "testing"

func TestSettingsPathRegistryCoversAISettings(t *testing.T) {
	keys := []string{
		"modules.visibility", "module.repos.visibility", "repo.visibility", "repo.shares",
		"runtime.id.visibility", "agent.id.access-passes", "agent.id.runtime-skill",
	}
	for _, key := range keys {
		if _, err := settingsPath(key); err != nil {
			t.Errorf("settingsPath(%q): %v", key, err)
		}
	}
}

func TestSettingsPathRejectsUnknownKey(t *testing.T) {
	if _, err := settingsPath("signing-secret"); err == nil {
		t.Fatal("unknown setting was accepted")
	}
}
