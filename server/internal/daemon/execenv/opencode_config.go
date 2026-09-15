package execenv

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// sharedOpencodeConfigFile is the daemon-owned OpenCode config written at the
// shared-mode sidecar root. OpenCode loads it when OPENCODE_CONFIG_DIR points
// at that root (additive with the user's global config). Non-shared tasks
// never write this file: the workdir's opencode.json is owned by the agent
// and user across turns.
const sharedOpencodeConfigFile = "opencode.json"

type sharedOpencodeConfig struct {
	Instructions []string                   `json:"instructions"`
	Skills       sharedOpencodeSkillsConfig `json:"skills"`
}

type sharedOpencodeSkillsConfig struct {
	Paths []string `json:"paths"`
}

// writeSharedOpencodeConfig writes {sidecarRoot}/opencode.json naming the
// sidecar AGENTS.md and .opencode/skills tree as absolute paths. Empty
// sidecarRoot fails closed: a shared-mode OpenCode task with no config file
// would start with no brief.
func writeSharedOpencodeConfig(sidecarRoot string) error {
	if strings.TrimSpace(sidecarRoot) == "" {
		return errors.New("shared mode: sidecar root is missing; cannot write OpenCode config")
	}
	abs, err := filepath.Abs(sidecarRoot)
	if err != nil {
		return fmt.Errorf("shared mode: resolve OpenCode sidecar root: %w", err)
	}
	cfg := sharedOpencodeConfig{
		Instructions: []string{RuntimeConfigFilePath(abs, "opencode")},
		Skills: sharedOpencodeSkillsConfig{
			Paths: []string{skillsDirPath(abs, "opencode")},
		},
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal OpenCode shared config: %w", err)
	}
	raw = append(raw, '\n')
	path := filepath.Join(abs, sharedOpencodeConfigFile)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
