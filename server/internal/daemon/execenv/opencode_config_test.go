package execenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteSharedOpencodeConfigNamesSidecarBriefAndSkills(t *testing.T) {
	t.Parallel()

	sidecar := t.TempDir()
	if err := writeSharedOpencodeConfig(sidecar); err != nil {
		t.Fatalf("writeSharedOpencodeConfig: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(sidecar, sharedOpencodeConfigFile))
	if err != nil {
		t.Fatalf("read opencode.json: %v", err)
	}
	var cfg sharedOpencodeConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal opencode.json: %v\n%s", err, raw)
	}

	abs, err := filepath.Abs(sidecar)
	if err != nil {
		t.Fatalf("abs sidecar: %v", err)
	}
	wantBrief := RuntimeConfigFilePath(abs, "opencode")
	wantSkills := skillsDirPath(abs, "opencode")
	if len(cfg.Instructions) != 1 || cfg.Instructions[0] != wantBrief {
		t.Errorf("instructions = %#v, want [%q]", cfg.Instructions, wantBrief)
	}
	if len(cfg.Skills.Paths) != 1 || cfg.Skills.Paths[0] != wantSkills {
		t.Errorf("skills.paths = %#v, want [%q]", cfg.Skills.Paths, wantSkills)
	}
	if !filepath.IsAbs(cfg.Instructions[0]) || !filepath.IsAbs(cfg.Skills.Paths[0]) {
		t.Errorf("paths must be absolute: instructions=%q skills=%q", cfg.Instructions[0], cfg.Skills.Paths[0])
	}
	if !strings.HasSuffix(cfg.Instructions[0], string(filepath.Separator)+"AGENTS.md") {
		t.Errorf("instructions[0] = %q, want .../AGENTS.md", cfg.Instructions[0])
	}
	if !strings.HasSuffix(cfg.Skills.Paths[0], filepath.Join(".opencode", "skills")) {
		t.Errorf("skills.paths[0] = %q, want .../.opencode/skills", cfg.Skills.Paths[0])
	}
}

func TestWriteSharedOpencodeConfigRejectsEmptySidecar(t *testing.T) {
	t.Parallel()
	if err := writeSharedOpencodeConfig(""); err == nil {
		t.Fatal("empty sidecar: want an error, got nil")
	} else if !strings.Contains(err.Error(), "sidecar") {
		t.Errorf("error = %q, want it to name the sidecar", err)
	}
}
