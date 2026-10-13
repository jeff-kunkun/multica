package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestStaticModelCatalogsAreValid(t *testing.T) {
	t.Parallel()
	catalogs := map[string][]Model{
		"claude": claudeStaticModels(),
		"codex":  codexStaticModels(),
	}
	for provider, models := range catalogs {
		if len(models) == 0 {
			t.Errorf("%s static catalog returned no models", provider)
		}
		defaultCount := 0
		for i, model := range models {
			if model.ID == "" {
				t.Errorf("%s static catalog[%d] has empty ID", provider, i)
			}
			if model.Label == "" {
				t.Errorf("%s static catalog[%d] has empty Label", provider, i)
			}
			if model.Default {
				defaultCount++
			}
		}
		if defaultCount > 1 {
			t.Errorf("%s: %d models marked Default, want 0 or 1", provider, defaultCount)
		}
	}
}

func TestFindACPConfigOptionRequiresExactID(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
  "config_options": [{
    "type": "select",
    "id": "thinking",
    "category": "thought_level",
    "current_value": "high"
  }]
}`)
	option, ok := findACPConfigOption(raw, "thinking")
	if !ok || option.ID != "thinking" {
		t.Fatalf("findACPConfigOption = (%+v, %v), want exact thinking option", option, ok)
	}
	value, ok := acpConfigOptionCurrentValue(raw, "thinking")
	if !ok || value != "high" {
		t.Errorf("acpConfigOptionCurrentValue = (%q, %v), want (high, true)", value, ok)
	}

	for _, invalidID := range []string{"reasoning", "Thinking", " thinking "} {
		renamed := []byte(fmt.Sprintf(`{"configOptions":[{"id":%q,"category":"thought_level","currentValue":"high"}]}`, invalidID))
		if option, ok := findACPConfigOption(renamed, "thinking"); ok {
			t.Fatalf("non-exact id %q exposed config option: %+v", invalidID, option)
		}
	}
}

func thinkingValues(thinking *ModelThinking) []string {
	if thinking == nil {
		return nil
	}
	values := make([]string, 0, len(thinking.SupportedLevels))
	for _, level := range thinking.SupportedLevels {
		values = append(values, level.Value)
	}
	return values
}

func TestModelKnownIncompatibleWithProvider(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		model    string
		want     bool
	}{
		{
			name:     "claude model is incompatible with codex",
			provider: "codex",
			model:    "claude-sonnet-4-6",
			want:     true,
		},
		{
			name:     "codex model is compatible with codex",
			provider: "codex",
			model:    "gpt-5.5",
			want:     false,
		},
		{
			name:     "codex bundled gpt-6-astra is compatible",
			provider: "codex",
			model:    "gpt-6-astra",
			want:     false,
		},
		{
			name:     "codex model is incompatible with claude",
			provider: "claude",
			model:    "o3",
			want:     true,
		},
		{
			name:     "exact claude model is compatible with claude",
			provider: "claude",
			model:    "claude-opus-4-7",
			want:     false,
		},
		{
			name:     "claude context variant is compatible with claude",
			provider: "claude",
			model:    "claude-opus-5[1m]",
			want:     false,
		},
		{
			name:     "future-shaped claude context variant is compatible with claude",
			provider: "claude",
			model:    "claude-opus-5[500k]",
			want:     false,
		},
		{
			name:     "malformed claude context variant is incompatible with claude",
			provider: "claude",
			model:    "claude-opus-5[weird]",
			want:     true,
		},
		{
			name:     "future claude model stays compatible after context normalization",
			provider: "claude",
			model:    "claude-opus-5-5[1m]",
			want:     false,
		},
		{
			name:     "provider-prefixed openai model is incompatible with codex",
			provider: "codex",
			model:    "openai/gpt-4o",
			want:     true,
		},
		{
			name:     "provider-prefixed anthropic model is incompatible with claude",
			provider: "claude",
			model:    "anthropic/claude-opus-4.7",
			want:     true,
		},
		{
			name:     "future codex model is not rejected by the static fallback catalog",
			provider: "codex",
			model:    "gpt-6-sol",
			want:     false,
		},
		{
			name:     "unknown custom model is not classified",
			provider: "codex",
			model:    "private-lab-model",
			want:     false,
		},
		{
			name:     "unknown target provider does not clear",
			provider: "opencode",
			model:    "claude-sonnet-4-6",
			want:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ModelKnownIncompatibleWithProvider(tc.provider, tc.model); got != tc.want {
				t.Fatalf("ModelKnownIncompatibleWithProvider(%q, %q) = %v, want %v", tc.provider, tc.model, got, tc.want)
			}
		})
	}
}

func TestListModelsUnknownProvider(t *testing.T) {
	ctx := context.Background()
	_, err := ListModels(ctx, "nonexistent", Command{Path: ""})
	if err == nil {
		t.Fatal("ListModels(unknown) expected error")
	}
}

func TestParseOpenCodeModels(t *testing.T) {
	input := `PROVIDER/MODEL                     CONTEXT  MAX_OUT
openai/gpt-4o                      128000   16384
anthropic/claude-sonnet-4-6        200000   8192
openai/gpt-4o                      128000   16384
nonprefixed-line
`
	models := parseOpenCodeModels(input)
	if len(models) != 2 {
		t.Fatalf("expected 2 models (header skipped, duplicate deduped, non-slash skipped), got %d: %+v", len(models), models)
	}
	if models[0].ID != "openai/gpt-4o" || models[0].Provider != "openai" {
		t.Errorf("unexpected first model: %+v", models[0])
	}
	if models[1].ID != "anthropic/claude-sonnet-4-6" || models[1].Provider != "anthropic" {
		t.Errorf("unexpected second model: %+v", models[1])
	}
}

func TestParseOpenCodeModelsVerboseVariants(t *testing.T) {
	input := `openai/gpt-5
{
  "id": "gpt-5",
  "name": "GPT-5",
  "reasoning": true,
  "variants": {
    "high": { "reasoningEffort": "high" },
    "low": { "reasoningEffort": "low" },
    "xhigh": { "reasoningEffort": "xhigh" },
    "fast-mode": { "reasoningEffort": "low" },
    "disabled": { "disabled": true }
  }
}
anthropic/claude-sonnet-4-6
{
  "id": "claude-sonnet-4-6",
  "reasoning": true,
  "variants": {
    "max": { "thinking": { "type": "enabled", "budgetTokens": 32000 } },
    "high": { "thinking": { "type": "enabled", "budgetTokens": 16000 } }
  }
}
`
	models := parseOpenCodeModels(input)
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d: %+v", len(models), models)
	}
	if models[0].Thinking == nil {
		t.Fatalf("expected first model to expose thinking variants")
	}
	got := make([]string, 0, len(models[0].Thinking.SupportedLevels))
	for _, lvl := range models[0].Thinking.SupportedLevels {
		got = append(got, lvl.Value)
		if lvl.Value == "xhigh" && lvl.Label != "Extra high" {
			t.Errorf("xhigh label: got %q, want Extra high", lvl.Label)
		}
		if lvl.Value == "fast-mode" && lvl.Label != "Fast Mode" {
			t.Errorf("custom variant label: got %q, want Fast Mode", lvl.Label)
		}
	}
	want := []string{"low", "high", "xhigh", "fast-mode"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("variant order/values: got %v, want %v", got, want)
	}
	if models[1].Thinking == nil || len(models[1].Thinking.SupportedLevels) != 2 {
		t.Fatalf("expected second model variants, got %+v", models[1].Thinking)
	}
}

func TestParseOpenCodeModelsMalformedVerboseBlockKeepsFollowingModels(t *testing.T) {
	input := `openai/gpt-5
{
  "id": "gpt-5",
  "reasoning": true,
  "variants": {
    "high": {}
  }
anthropic/claude-sonnet-4-6
{
  "id": "claude-sonnet-4-6",
  "reasoning": true,
  "variants": {
    "high": {},
    "max": {}
  }
}
`
	models := parseOpenCodeModels(input)
	if len(models) != 2 {
		t.Fatalf("expected both model rows to survive malformed JSON, got %d: %+v", len(models), models)
	}
	if models[0].ID != "openai/gpt-5" {
		t.Fatalf("unexpected first model: %+v", models[0])
	}
	if models[0].Thinking != nil {
		t.Fatalf("malformed first JSON block should not annotate thinking: %+v", models[0].Thinking)
	}
	if models[1].ID != "anthropic/claude-sonnet-4-6" {
		t.Fatalf("unexpected second model: %+v", models[1])
	}
	if models[1].Thinking == nil || len(models[1].Thinking.SupportedLevels) != 2 {
		t.Fatalf("valid following JSON block should still annotate thinking: %+v", models[1].Thinking)
	}
}

func TestDiscoverOpenCodeModelsFallsBackWhenVerboseFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary requires a POSIX shell")
	}

	dir := t.TempDir()
	fake := filepath.Join(dir, "opencode")
	script := `#!/bin/sh
if [ "$1" = "models" ] && [ "$2" = "--verbose" ]; then
  exit 2
fi
if [ "$1" = "models" ]; then
  cat <<'EOF'
PROVIDER/MODEL                     CONTEXT  MAX_OUT
openai/gpt-4o                      128000   16384
EOF
  exit 0
fi
exit 1
`
	writeTestExecutable(t, fake, []byte(script))

	models, err := discoverOpenCodeModels(context.Background(), Command{Path: fake})
	if err != nil {
		t.Fatalf("discoverOpenCodeModels: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("expected fallback non-verbose model, got %d: %+v", len(models), models)
	}
	if models[0].ID != "openai/gpt-4o" || models[0].Thinking != nil {
		t.Fatalf("unexpected fallback model: %+v", models[0])
	}
}

// TestCachedDiscoveryDoesNotCacheEmpty verifies that an empty discovery result
// is not cached, so a transient failure (e.g. a `pi --list-models` timeout)
// doesn't keep the model picker blank for the full TTL. A non-empty result is
// still cached. See #3729.
func TestCachedDiscoveryDoesNotCacheEmpty(t *testing.T) {
	const emptyKey, nonEmptyKey = "test-cache-empty", "test-cache-nonempty"
	// modelCache is a package-level global; clear our keys up front and on
	// cleanup so the test stays hermetic under `go test -count=N` (a leftover
	// non-empty entry from a prior run would otherwise skip the callback).
	resetCache := func() {
		modelCacheMu.Lock()
		delete(modelCache, emptyKey)
		delete(modelCache, nonEmptyKey)
		modelCacheMu.Unlock()
	}
	resetCache()
	t.Cleanup(resetCache)

	emptyCalls := 0
	empty := func() (Catalog, error) {
		emptyCalls++
		return Catalog{Models: []Model{}}, nil
	}
	for i := 0; i < 2; i++ {
		got, err := cachedDiscovery(emptyKey, empty)
		if err != nil {
			t.Fatalf("cachedDiscovery: %v", err)
		}
		if len(got.Models) != 0 {
			t.Fatalf("expected empty result, got %+v", got)
		}
	}
	if emptyCalls != 2 {
		t.Fatalf("empty result must not be cached: expected fn called 2x, got %d", emptyCalls)
	}

	nonEmptyCalls := 0
	nonEmpty := func() (Catalog, error) {
		nonEmptyCalls++
		return Catalog{Models: []Model{{ID: "provider/model"}}}, nil
	}
	for i := 0; i < 2; i++ {
		if _, err := cachedDiscovery(nonEmptyKey, nonEmpty); err != nil {
			t.Fatalf("cachedDiscovery: %v", err)
		}
	}
	if nonEmptyCalls != 1 {
		t.Fatalf("non-empty result must be cached: expected fn called 1x, got %d", nonEmptyCalls)
	}
}

func writeFakePiRPCModelsBinary(t *testing.T) string {
	t.Helper()
	fakePath := filepath.Join(t.TempDir(), "pi")
	script := `#!/bin/sh
if [ "$1" = "--mode" ] && [ "$2" = "rpc" ]; then
  IFS= read -r _state_request
  IFS= read -r _models_request
  printf '%s\n' '{"id":"multica-state","type":"response","command":"get_state","success":true,"data":{"model":{"id":"gpt-5.6-luna","name":"Luna","provider":"openai-multi","reasoning":true,"thinkingLevelMap":{"off":"none","minimal":"none","low":"low","medium":null,"high":"high","xhigh":"xhigh","max":"max"}},"thinkingLevel":"max"}}'
  printf '%s\n' '{"id":"multica-models","type":"response","command":"get_available_models","success":true,"data":{"models":[{"id":"gpt-5.6-sol","name":"Sol","provider":"openai-multi","reasoning":true},{"id":"gpt-5.6-luna","name":"Luna","provider":"openai-multi","reasoning":true,"thinkingLevelMap":{"off":"none","minimal":"none","low":"low","medium":null,"high":"high","xhigh":"xhigh","max":"max"}},{"id":"plain-chat","name":"Plain chat","provider":"openai-multi","reasoning":false}]}}'
  exit 0
fi
printf '%s\n' 'provider model context max-out thinking images'
printf '%s\n' 'fallback fallback-model 128K 8K yes no'
`
	writeTestExecutable(t, fakePath, []byte(script))
	return fakePath
}

func TestDiscoverOpenCodeModelsFallsBackOnVerboseNoise(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake opencode binary is a /bin/sh script")
	}

	// `opencode models --verbose` => $2 == "--verbose": emit noise + exit 1.
	// `opencode models`           => no $2: print the plain catalog.
	script := "#!/bin/sh\n" +
		"if [ \"$2\" = \"--verbose\" ]; then\n" +
		"  echo 'panic: catalog sync failed'\n" +
		"  exit 1\n" +
		"fi\n" +
		"echo 'openai/gpt-4o'\n"

	fakePath := filepath.Join(t.TempDir(), "opencode")
	writeTestExecutable(t, fakePath, []byte(script))

	models, err := discoverOpenCodeModels(context.Background(), Command{Path: fakePath})
	if err != nil {
		t.Fatalf("discoverOpenCodeModels: %v", err)
	}
	if len(models) != 1 || models[0].ID != "openai/gpt-4o" {
		t.Fatalf("expected fallback to plain `opencode models` to yield [openai/gpt-4o], got %+v", models)
	}
}

func TestParseAntigravityModels(t *testing.T) {
	t.Parallel()

	out := strings.Join([]string{
		"Gemini 3.5 Flash (Medium)",
		"Claude Opus 4.6 (Thinking)",
		"", // blank line — skipped
		"GPT-OSS 120B (Medium)",
		"Claude Opus 4.6 (Thinking)", // duplicate — collapsed
	}, "\n")

	got := parseAntigravityModels(out)
	want := []Model{
		{ID: "Gemini 3.5 Flash (Medium)", Label: "Gemini 3.5 Flash (Medium)", Provider: "antigravity"},
		{ID: "Claude Opus 4.6 (Thinking)", Label: "Claude Opus 4.6 (Thinking)", Provider: "antigravity"},
		{ID: "GPT-OSS 120B (Medium)", Label: "GPT-OSS 120B (Medium)", Provider: "antigravity"},
	}
	if len(got) != len(want) {
		t.Fatalf("parseAntigravityModels len = %d, want %d (%+v)", len(got), len(want), got)
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("model[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestParseAntigravityModelsTabSeparated covers the catalog format introduced
// by agy 1.1.11: the first tab-delimited field is the value accepted by
// --model, while the second field is the human-readable picker label.
func TestParseAntigravityModelsTabSeparated(t *testing.T) {
	t.Parallel()

	out := strings.Join([]string{
		"gemini-3.6-flash-high\tGemini 3.6 Flash (High)",
		"claude-opus-4-6-thinking\tClaude Opus 4.6 (Thinking)\tfuture metadata is ignored",
		"gemini-3.6-flash-high\tDuplicate label is ignored",
	}, "\n")

	got := parseAntigravityModels(out)
	want := []Model{
		{ID: "gemini-3.6-flash-high", Label: "Gemini 3.6 Flash (High)", Provider: "antigravity"},
		{ID: "claude-opus-4-6-thinking", Label: "Claude Opus 4.6 (Thinking)", Provider: "antigravity"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseAntigravityModels = %+v, want %+v", got, want)
	}

	if err := antigravityModelError("gemini-3.6-flash-high", got); err != nil {
		t.Fatalf("exact model slug from tab-separated catalog was rejected: %v", err)
	}
}

// TestParseAntigravityModelsEmpty pins that empty / whitespace-only output
// yields no models (so cachedDiscovery treats it as a transient miss and
// retries rather than caching a blank catalog).
func TestParseAntigravityModelsEmpty(t *testing.T) {
	t.Parallel()
	if got := parseAntigravityModels("   \n\t\n"); len(got) != 0 {
		t.Errorf("expected no models for blank output, got %+v", got)
	}
}

func TestCachedDiscovery(t *testing.T) {
	calls := 0
	fn := func() (Catalog, error) {
		calls++
		return Catalog{Models: []Model{{ID: "x", Label: "x"}}}, nil
	}
	// First call populates the cache; reset for isolation.
	modelCacheMu.Lock()
	delete(modelCache, "testkey")
	modelCacheMu.Unlock()

	if _, err := cachedDiscovery("testkey", fn); err != nil {
		t.Fatal(err)
	}
	if _, err := cachedDiscovery("testkey", fn); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("expected 1 underlying call due to cache, got %d", calls)
	}
}

// TestDiscoveryCacheKeyIsolatesByExecutable verifies that two different
// executable paths for the same provider type produce different cache keys,
// so a built-in and a custom Dim-compatible executable do not serve each
// other's model catalog during the TTL.
func TestDiscoveryCacheKeyIsolatesByExecutable(t *testing.T) {
	key1 := discoveryCacheKey("dim", Command{Path: "/usr/bin/dim"})
	key2 := discoveryCacheKey("dim", Command{Path: "/opt/custom/dim"})
	if key1 == key2 {
		t.Fatalf("different executables must produce different cache keys: both %q", key1)
	}
	keyEmpty := discoveryCacheKey("dim", Command{Path: ""})
	if keyEmpty != "dim" {
		t.Fatalf("empty executable should fall back to provider type, got %q", keyEmpty)
	}
	if keyEmpty == key1 {
		t.Fatal("empty executable key must differ from a non-empty executable key")
	}

	calls := 0
	fn := func() (Catalog, error) {
		calls++
		return Catalog{Models: []Model{{ID: "m"}}}, nil
	}
	modelCacheMu.Lock()
	delete(modelCache, key1)
	delete(modelCache, key2)
	modelCacheMu.Unlock()

	if _, err := cachedDiscovery(key1, fn); err != nil {
		t.Fatal(err)
	}
	if _, err := cachedDiscovery(key2, fn); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("expected 2 underlying calls (one per executable), got %d", calls)
	}
}

// TestQualifyModelID covers GH #7300: a persisted model id that
// omits its provider must be promoted to the catalog's canonical selector,
// but only when exactly one provider claims it. opencode rejects an
// unqualified id outright, and every capability lookup keyed on the model id
// misses until the two agree.
func TestQualifyModelID(t *testing.T) {
	t.Parallel()

	gateway := []Model{
		{ID: "multica-anthropic/claude/claude-opus-5", Provider: "multica-anthropic"},
		{ID: "multica-anthropic/claude/claude-sonnet-5", Provider: "multica-anthropic"},
		{ID: "multica-codex/codex/gpt-5.6-sol", Provider: "multica-codex"},
	}

	tests := []struct {
		name          string
		catalog       Catalog
		model         string
		want          string
		wantRewritten bool
	}{
		{
			name:          "slash-shaped id gains its provider",
			catalog:       Catalog{Models: gateway},
			model:         "claude/claude-opus-5",
			want:          "multica-anthropic/claude/claude-opus-5",
			wantRewritten: true,
		},
		{
			name:    "already canonical is left alone",
			catalog: Catalog{Models: gateway},
			model:   "multica-anthropic/claude/claude-opus-5",
			want:    "multica-anthropic/claude/claude-opus-5",
		},
		{
			name: "an exact catalog id wins over a qualifiable one",
			// Both a bare `shared-id` model and a `vendor/shared-id` model
			// exist. The exact match is what the user picked; promoting it to
			// the other provider's entry would silently reroute the task.
			catalog: Catalog{Models: []Model{
				{ID: "shared-id", Provider: ""},
				{ID: "vendor/shared-id", Provider: "vendor"},
			}},
			model: "shared-id",
			want:  "shared-id",
		},
		{
			name: "ambiguous across providers stays untouched",
			catalog: Catalog{Models: []Model{
				{ID: "gateway-a/claude/claude-opus-5", Provider: "gateway-a"},
				{ID: "gateway-b/claude/claude-opus-5", Provider: "gateway-b"},
			}},
			model: "claude/claude-opus-5",
			want:  "claude/claude-opus-5",
		},
		{
			name:    "unknown model is passed through for the CLI to judge",
			catalog: Catalog{Models: gateway},
			model:   "something-nobody-advertises",
			want:    "something-nobody-advertises",
		},
		{
			name:    "empty catalog cannot qualify anything",
			catalog: Catalog{},
			model:   "claude/claude-opus-5",
			want:    "claude/claude-opus-5",
		},
		{
			// A static stand-in is not what the runtime actually supports, so
			// promoting against it would invent an id the CLI never advertised.
			name:    "a fallback catalog is never authoritative",
			catalog: Catalog{Models: gateway, Fallback: true},
			model:   "claude/claude-opus-5",
			want:    "claude/claude-opus-5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, rewritten := QualifyModelID(tt.catalog, tt.model)
			if got != tt.want || rewritten != tt.wantRewritten {
				t.Errorf("QualifyModelID(%q) = (%q, %v), want (%q, %v)",
					tt.model, got, rewritten, tt.want, tt.wantRewritten)
			}
		})
	}
}

// A blank model means "let the runtime pick its own default" — there is
// nothing to qualify, and the runtime resolves its own selection.
func TestQualifyModelIDIgnoresBlankModel(t *testing.T) {
	t.Parallel()
	catalog := Catalog{Models: []Model{{ID: "vendor/only-model", Provider: "vendor"}}}
	got, rewritten := QualifyModelID(catalog, "   ")
	if got != "" || rewritten {
		t.Errorf("QualifyModelID(blank) = (%q, %v), want (\"\", false)", got, rewritten)
	}
}

// TestSlashShapedPiModelKeepsItsThinkingCatalog walks the chain that GH #7300
// reported as a dropped thinking_level: pi's RPC catalog carries a
// gateway-style model whose own id contains a slash, the agent persisted that
// bare id, and every capability lookup keyed on it missed. Qualifying the id
// first is what puts the persisted value back on the catalog entry that
// actually advertises the levels.
func TestModelSelectorMustBeProviderQualifiedIsAnExecutionContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		provider string
		want     bool
		why      string
	}{
		{"opencode", true, "run --model resolves strictly through provider/model"},
		{"pi", false, "pi's own resolver accepts bare and slash-shaped ids"},
		{"omp", false, "pi-family fork, inherits pi's resolver"},
		{"claude", false, "bare model ids, no provider segment to miss"},
		{"codex", false, "bare model ids"},
		{"copilot", false, "bare model ids under a display-name provider"},
		{"dsh", false, "resolves its own provider-prefixed ids"},
		{"", false, "unknown provider must not spend discovery"},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			t.Parallel()
			if got := ModelSelectorMustBeProviderQualified(tt.provider); got != tt.want {
				t.Errorf("ModelSelectorMustBeProviderQualified(%q) = %v, want %v (%s)",
					tt.provider, got, tt.want, tt.why)
			}
		})
	}
}

// This fixture accepts OMP's discovery surface and rejects Pi-only flags (#8379).
