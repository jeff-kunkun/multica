package agent

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// Qwen Code thinking levels (DENE-1950).
//
// Qwen Code has no effort flag. What it does have is a per-model capability
// declaration in its own settings, modelProviders.<auth>[].capabilities.reasoning
// (Qwen Code 0.24+), and a per-request body patch, generationConfig.extra_body
// (0.20+). Multica reads the first to know which levels a model takes, and
// writes the second into a per-task system-settings file to deliver one.
//
// Capability is bound to the model row the settings name, on the endpoint the
// runtime resolves to, never to the provider name: a Qwen runtime pointed at a
// model nobody declared gets no picker, and a saved level is refused.
//
// Only disableField "enable_thinking" is delivered. That is the chat-template
// switch llama.cpp / vLLM Qwen templates read through chat_template_kwargs; the
// other two fields Qwen knows ("reasoning_effort", "thinking") are vendor API
// shapes Multica has not verified, so a row declaring them stays undeclared.

// qwenThinkingNone is the Multica token for "thinking off". Qwen Code's own
// picker calls the same state "none".
const qwenThinkingNone = "none"

// qwenReasoningCapability mirrors Qwen Code's parseModelReasoningCapabilities
// input. Pointers distinguish "absent" from false where Qwen does.
type qwenReasoningCapability struct {
	Thinking      bool     `json:"thinking"`
	DisableField  string   `json:"disableField"`
	Efforts       []string `json:"efforts"`
	DefaultEffort string   `json:"defaultEffort"`
	ToggleOnly    *bool    `json:"toggleOnly"`
	CanDisable    *bool    `json:"canDisable"`
}

// qwenEffortTiers is Qwen Code's REASONING_EFFORT_TIERS, in its order.
var qwenEffortTiers = []string{"low", "medium", "high", "xhigh", "max"}

var qwenEffortLabels = map[string]string{
	"low":            "Low",
	"medium":         "Medium",
	"high":           "High",
	"xhigh":          "Extra high",
	"max":            "Max",
	qwenThinkingNone: "Off",
}

// qwenModelThinking turns one declared capability into Multica's catalog
// entry, or nil when Qwen Code itself would reject the declaration or Multica
// cannot deliver it. source names where the declaration lives.
func qwenModelThinking(raw json.RawMessage, source string) *ModelThinking {
	if len(raw) == 0 {
		return nil
	}
	var capability qwenReasoningCapability
	if err := json.Unmarshal(raw, &capability); err != nil {
		return nil
	}
	if !capability.Thinking || capability.DisableField != "enable_thinking" {
		return nil
	}
	if capability.CanDisable != nil && *capability.CanDisable {
		// Qwen only accepts canDisable:false; true makes the whole row invalid.
		return nil
	}
	canDisable := capability.CanDisable == nil
	thinking := &ModelThinking{Source: source}
	if capability.ToggleOnly != nil && *capability.ToggleOnly {
		if !canDisable {
			return nil
		}
		thinking.SupportedLevels = []ThinkingLevel{qwenLevel(qwenThinkingNone)}
		return thinking
	}
	if len(capability.Efforts) == 0 {
		return nil
	}
	seen := map[string]bool{}
	for _, effort := range capability.Efforts {
		if seen[effort] || !slices.Contains(qwenEffortTiers, effort) {
			return nil
		}
		seen[effort] = true
	}
	if capability.DefaultEffort != "" && !seen[capability.DefaultEffort] {
		return nil
	}
	for _, tier := range qwenEffortTiers {
		if seen[tier] {
			thinking.SupportedLevels = append(thinking.SupportedLevels, qwenLevel(tier))
		}
	}
	if canDisable {
		thinking.SupportedLevels = append(thinking.SupportedLevels, qwenLevel(qwenThinkingNone))
	}
	thinking.DefaultLevel = capability.DefaultEffort
	return thinking
}

func qwenLevel(value string) ThinkingLevel {
	return ThinkingLevel{Value: value, Label: qwenEffortLabels[value]}
}

// qwenSettingsLayer is one settings file Qwen Code reads, in the order it
// merges them. modelProviders is replaced per auth type, not merged, so the
// highest layer that declares an auth type's rows owns all of them.
type qwenSettingsLayer struct {
	Path     string
	Display  string
	Settings qwenSettings
}

// qwenSystemSettingsPath is where Qwen Code reads its system settings: the
// environment override first, then the platform default.
func qwenSystemSettingsPath(overlay map[string]string) string {
	if path := overlayValue(overlay, "QWEN_CODE_SYSTEM_SETTINGS_PATH"); path != "" {
		return path
	}
	if path := strings.TrimSpace(os.Getenv("QWEN_CODE_SYSTEM_SETTINGS_PATH")); path != "" {
		return path
	}
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/QwenCode/settings.json"
	case "windows":
		return `C:\ProgramData\qwen-code\settings.json`
	default:
		return "/etc/qwen-code/settings.json"
	}
}

// qwenThinkingLayers reads the user and system settings. Project settings are
// per task directory and are not part of a runtime-wide catalog.
func qwenThinkingLayers(overlay map[string]string) []qwenSettingsLayer {
	var layers []qwenSettingsLayer
	if home := qwenConfigHome(overlay); home != "" {
		path := filepath.Join(home, "settings.json")
		if settings, err := readQwenSettings(path); err == nil {
			layers = append(layers, qwenSettingsLayer{Path: path, Display: displayQwenPath(path), Settings: settings})
		}
	}
	path := qwenSystemSettingsPath(overlay)
	if settings, err := readQwenSettings(path); err == nil {
		layers = append(layers, qwenSettingsLayer{Path: path, Display: displayQwenPath(path), Settings: settings})
	}
	return layers
}

func displayQwenPath(path string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(filepath.Join("~", rel))
		}
	}
	return path
}

// annotateQwenThinking attaches each declared capability to the discovered
// model it names. A row matches when its id is the model id and its baseUrl is
// empty or the endpoint the runtime actually calls.
func annotateQwenThinking(models []Model, overlay map[string]string, baseURL string) []Model {
	layers := qwenThinkingLayers(overlay)
	auth := "openai"
	var rows []qwenProviderRow
	var display string
	for _, layer := range layers {
		if selected := strings.TrimSpace(layer.Settings.Security.Auth.SelectedType); selected != "" {
			auth = selected
		}
	}
	for _, layer := range layers {
		if layerRows, ok := layer.Settings.ModelProviders[auth]; ok {
			rows = layerRows
			display = layer.Display
		}
	}
	if len(rows) == 0 {
		return models
	}
	for i := range models {
		for _, row := range rows {
			if strings.TrimSpace(row.ID) != models[i].ID || !sameQwenBaseURL(row.BaseURL, baseURL) {
				continue
			}
			source := fmt.Sprintf("%s · modelProviders.%s[%s].capabilities.reasoning", display, auth, models[i].ID)
			if thinking := qwenModelThinking(row.Capabilities.Reasoning, source); thinking != nil {
				models[i].Thinking = thinking
			}
			break
		}
	}
	return models
}

func sameQwenBaseURL(rowBase, endpointBase string) bool {
	rowBase = strings.TrimSpace(rowBase)
	if rowBase == "" {
		return true
	}
	return normalizeQwenBaseURL(rowBase) == normalizeQwenBaseURL(endpointBase)
}

func normalizeQwenBaseURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed.String()
}

// qwenThinkingPatch is what one level writes into generationConfig.
func qwenThinkingPatch(level string) (map[string]any, string) {
	if level == qwenThinkingNone {
		return map[string]any{
			"extra_body": map[string]any{
				"chat_template_kwargs": map[string]any{"enable_thinking": false},
			},
			// Qwen 0.24+ also sends its own top-level switch; keep it off so the
			// two never disagree.
			"reasoning": false,
		}, ""
	}
	return map[string]any{
		"extra_body": map[string]any{
			"chat_template_kwargs": map[string]any{"enable_thinking": true, "reasoning_effort": level},
		},
	}, level
}

// buildQwenThinkingSettings returns the per-task system settings that deliver
// level, layered on the existing system settings so nothing an administrator
// set is lost. An existing system settings file that cannot be read or parsed
// is an error: launching without it would drop the administrator's policy.
//
// The level goes into model.generationConfig.extra_body, which Qwen Code uses
// for a model without a provider row, and into the matching row's own
// generationConfig, because a matching row replaces model.generationConfig.
// model.reasoningEffort is set too: Qwen 0.24+ may add a top-level
// reasoning_effort, and llama-server lets that override the template kwarg,
// so both must say the same thing.
func buildQwenThinkingSettings(level, model, cwd string, overlay map[string]string) ([]byte, error) {
	systemPath := qwenSystemSettingsPath(overlay)
	base := map[string]any{}
	if body, err := os.ReadFile(systemPath); err == nil {
		if err := json.Unmarshal(body, &base); err != nil {
			return nil, fmt.Errorf("existing Qwen system settings %s is not valid JSON", systemPath)
		}
		if base == nil {
			base = map[string]any{}
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read existing Qwen system settings %s: %w", systemPath, err)
	}

	patch, effort := qwenThinkingPatch(level)
	modelSection := childMap(base, "model")
	generation := childMap(modelSection, "generationConfig")
	deepMergeInto(generation, patch)
	if effort != "" {
		modelSection["reasoningEffort"] = effort
	}

	auth, rows := qwenEffectiveProviderRows(overlay, cwd, base)
	if auth != "" && len(rows) > 0 {
		target := strings.TrimSpace(model)
		if target == "" {
			target = qwenConfiguredModel(overlay)
		}
		patched := false
		for _, row := range rows {
			id, _ := row["id"].(string)
			if strings.TrimSpace(id) != target {
				continue
			}
			deepMergeInto(childMap(row, "generationConfig"), clonePatch(patch))
			patched = true
		}
		if patched {
			providers := childMap(base, "modelProviders")
			list := make([]any, len(rows))
			for i, row := range rows {
				list[i] = row
			}
			providers[auth] = list
		}
	}
	return json.MarshalIndent(base, "", "  ")
}

// qwenEffectiveProviderRows returns the selected auth type and the rows Qwen
// Code would use for it: system, then project, then user — the first layer
// that declares the auth type owns its rows.
func qwenEffectiveProviderRows(overlay map[string]string, cwd string, system map[string]any) (string, []map[string]any) {
	var layers []map[string]any
	if home := qwenConfigHome(overlay); home != "" {
		layers = append(layers, readQwenSettingsMap(filepath.Join(home, "settings.json")))
	}
	if cwd != "" {
		layers = append(layers, readQwenSettingsMap(filepath.Join(cwd, ".qwen", "settings.json")))
	}
	layers = append(layers, system)

	auth := "openai"
	for _, layer := range layers {
		if selected, _ := childMapRead(childMapRead(layer, "security"), "auth")["selectedType"].(string); strings.TrimSpace(selected) != "" {
			auth = strings.TrimSpace(selected)
		}
	}
	var rows []any
	for _, layer := range layers {
		if list, ok := childMapRead(layer, "modelProviders")[auth].([]any); ok {
			rows = list
		}
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if obj, ok := row.(map[string]any); ok {
			out = append(out, cloneJSONMap(obj))
		}
	}
	return auth, out
}

// qwenConfiguredModel is the model a task without --model runs: the env
// layers' OPENAI_MODEL / QWEN_MODEL, else settings model.name.
func qwenConfiguredModel(overlay map[string]string) string {
	home := qwenConfigHome(overlay)
	if home == "" {
		return ""
	}
	endpoint, err := resolveQwenEndpoint(home, overlay)
	if err != nil {
		return ""
	}
	return endpoint.Model
}

func readQwenSettingsMap(path string) map[string]any {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(body, &out) != nil {
		return nil
	}
	return out
}

func childMap(parent map[string]any, key string) map[string]any {
	if existing, ok := parent[key].(map[string]any); ok {
		return existing
	}
	child := map[string]any{}
	parent[key] = child
	return child
}

func childMapRead(parent map[string]any, key string) map[string]any {
	if parent == nil {
		return nil
	}
	child, _ := parent[key].(map[string]any)
	return child
}

// deepMergeInto merges src into dst: objects recurse, everything else
// replaces, the way Qwen Code merges settings layers.
func deepMergeInto(dst, src map[string]any) {
	for key, value := range src {
		if srcChild, ok := value.(map[string]any); ok {
			if dstChild, ok := dst[key].(map[string]any); ok {
				deepMergeInto(dstChild, srcChild)
				continue
			}
			dst[key] = cloneJSONMap(srcChild)
			continue
		}
		dst[key] = value
	}
}

func clonePatch(patch map[string]any) map[string]any { return cloneJSONMap(patch) }

func cloneJSONMap(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for key, value := range src {
		switch typed := value.(type) {
		case map[string]any:
			out[key] = cloneJSONMap(typed)
		case []any:
			list := make([]any, len(typed))
			for i, item := range typed {
				if obj, ok := item.(map[string]any); ok {
					list[i] = cloneJSONMap(obj)
				} else {
					list[i] = item
				}
			}
			out[key] = list
		default:
			out[key] = value
		}
	}
	return out
}

// writeQwenThinkingSettings materialises the per-task file in a private temp
// directory and returns its path. cleanupQwenThinkingSettings removes it.
func writeQwenThinkingSettings(body []byte) (string, error) {
	dir, err := os.MkdirTemp("", "multica-qwen-settings-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return path, nil
}

func cleanupQwenThinkingSettings(path string) {
	if path == "" {
		return
	}
	dir := filepath.Dir(path)
	if strings.HasPrefix(filepath.Base(dir), "multica-qwen-settings-") {
		_ = os.RemoveAll(dir)
		return
	}
	_ = os.Remove(path)
}
