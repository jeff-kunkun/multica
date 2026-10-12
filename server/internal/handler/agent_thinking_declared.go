package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// declaredThinkingRejection checks a level for a runtime whose levels exist
// only because its own config declares them per model (Qwen Code, DENE-1950).
// It answers from the catalog the runtime last reported, bound to that
// runtime's endpoint and model, and returns "" when the level is declared.
//
// Unlike the other dynamic providers, an unknown answer is a refusal: a level
// the daemon later drops would look saved while doing nothing.
func (h *Handler) declaredThinkingRejection(ctx context.Context, provider string, runtimeID pgtype.UUID, model, level string) string {
	if level == "" || !agent.ThinkingDeclaredPerModel(provider) {
		return ""
	}
	snapshot := h.cachedModelCatalog(ctx, uuidToString(runtimeID))
	if snapshot == nil || len(snapshot.Models) == 0 {
		return fmt.Sprintf(
			"cannot confirm thinking levels for runtime %q: it has not reported its models yet, or is offline. Run `multica runtime models <runtime-id>` (or open the model picker) and retry, or leave thinking_level empty to use the model default",
			provider,
		)
	}
	target := strings.TrimSpace(model)
	if target == "" {
		for _, m := range snapshot.Models {
			if m.Default {
				target = m.ID
				break
			}
		}
	}
	if target == "" {
		return fmt.Sprintf(
			"runtime %q reports no default model, so a thinking level cannot be checked; pick a model first or leave thinking_level empty",
			provider,
		)
	}
	for _, m := range snapshot.Models {
		if m.ID != target {
			continue
		}
		if m.Thinking == nil || len(m.Thinking.SupportedLevels) == 0 {
			return fmt.Sprintf(
				"model %q on runtime %q declares no thinking levels; declare capabilities.reasoning for it in the runtime's own settings, or leave thinking_level empty",
				target, provider,
			)
		}
		values := make([]string, 0, len(m.Thinking.SupportedLevels))
		for _, lvl := range m.Thinking.SupportedLevels {
			if lvl.Value == level {
				return ""
			}
			values = append(values, lvl.Value)
		}
		return fmt.Sprintf(
			"thinking_level %q is not declared for model %q on runtime %q; declared: %s, or empty for the model default",
			level, target, provider, strings.Join(values, ", "),
		)
	}
	return fmt.Sprintf(
		"model %q is not in the catalog runtime %q last reported; refresh its models or leave thinking_level empty",
		target, provider,
	)
}
