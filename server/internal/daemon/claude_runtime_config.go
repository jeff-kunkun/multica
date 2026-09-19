package daemon

import (
	"encoding/json"
	"log/slog"
)

const (
	defaultClaudeAutoCompactTokens = 200000
	minClaudeAutoCompactTokens     = 100000
	maxClaudeAutoCompactTokens     = 1000000
)

type claudeRuntimeConfig struct {
	AutoCompactTokens int `json:"autocompact_tokens"`
}

func decodeClaudeRuntimeConfig(raw json.RawMessage, logger *slog.Logger) int {
	if len(raw) == 0 {
		return 0
	}
	var cfg claudeRuntimeConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		if logger != nil {
			logger.Warn("claude runtime_config: parse failed; using default autocompact window", "error", err)
		}
		return 0
	}
	if cfg.AutoCompactTokens == 0 {
		return 0
	}
	if cfg.AutoCompactTokens < minClaudeAutoCompactTokens || cfg.AutoCompactTokens > maxClaudeAutoCompactTokens {
		if logger != nil {
			logger.Warn("claude runtime_config: autocompact_tokens outside supported range; using default",
				"value", cfg.AutoCompactTokens, "min", minClaudeAutoCompactTokens, "max", maxClaudeAutoCompactTokens)
		}
		return defaultClaudeAutoCompactTokens
	}
	return cfg.AutoCompactTokens
}
