package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Model describes a single LLM model exposed by an agent provider.
// The dropdown groups by Provider when the ID uses the
// `provider/model` form (e.g. "openai/gpt-4o" from opencode).
// Default is a *display* hint: the UI badges the entry the
// runtime advertises as its preferred pick (e.g. Claude Code's
// shipped default, or hermes' currentModelId). It has no effect
// at execution time — when agent.model is empty the daemon passes
// "" to the backend so each provider's own CLI resolves its own
// default, which is always closer to what the user's account /
// environment actually supports than a static guess here.
type Model struct {
	ID                                  string             `json:"id"`
	Label                               string             `json:"label"`
	Provider                            string             `json:"provider,omitempty"`
	Default                             bool               `json:"default,omitempty"`
	ServiceTiers                        []ModelServiceTier `json:"service_tiers,omitempty"`
	SupportsExplicitStandardServiceTier bool               `json:"supports_explicit_standard_service_tier,omitempty"`
	// Thinking advertises the runtime's reasoning/effort catalog for this
	// model. nil means the runtime/model has no thinking-level control
	// (or the daemon couldn't discover one); the UI hides its picker. The
	// catalog is per-model because Codex's `codex debug models` is itself
	// per-model and Claude's `--effort` superset has known per-model gaps
	// (`xhigh` is Opus-only, `max` is session-only). See MUL-2339.
	Thinking *ModelThinking `json:"thinking,omitempty"`
}

// UnavailableModel is a model the runtime named but will not run on this host —
// today only Claude Code, reporting one that needs a newer CLI than the
// installed one. Reason is the runtime's own remedy ("Update to 2.1.255+ to use
// Fable 5.1"), forwarded verbatim so the copy stays right without Multica
// tracking upstream version floors.
//
// It is a distinct type, and travels in a distinct list, precisely so it can
// never be mistaken for something selectable. Marking such a row with a flag
// inside the models list was the first attempt and it was wrong twice over:
// every consumer that did not learn the flag (the inspector picker, the agent
// builder) kept offering it, and — the part a flag cannot fix — an already
// installed desktop client does not know the field at all, so it would render
// the row as an ordinary model and persist a model id the CLI rejects. Keeping
// the two lists separate makes old clients correct by construction, since they
// only ever read `models` (MUL-6961).
type UnavailableModel struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Reason is display copy, not a machine contract: it comes from the
	// runtime and may be empty when it offered none.
	Reason string `json:"reason,omitempty"`
}

// ModelServiceTier is one runtime-native execution tier advertised for a
// model. ID is sent back to the provider protocol unchanged; Name and
// Description are display copy owned by the runtime catalog.
type ModelServiceTier struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ModelThinking carries the per-model reasoning/effort catalog
// surfaced by an agent runtime. Values are runtime-native — Codex can emit
// "none|minimal|low|medium|high|xhigh|max|ultra"; Claude emits
// "low|medium|high|xhigh|max"; Pi emits
// "off|minimal|low|medium|high|xhigh|max". The frontend renders
// SupportedLevels as-is so what users see matches each CLI's own UI.
type ModelThinking struct {
	SupportedLevels []ThinkingLevel `json:"supported_levels"`
	// DefaultLevel is the value the runtime picks when no override is
	// provided. Empty means "the runtime picks, we don't know" — the
	// UI shows "Default" as a generic option.
	DefaultLevel string `json:"default_level,omitempty"`
}

// ThinkingLevel is one entry in a ModelThinking.SupportedLevels list.
// Value is the literal token passed to the CLI (Claude `--effort <value>`
// or Codex `model_reasoning_effort=<value>`); Label is a display string;
// Description is optional helper copy lifted from the upstream catalog
// when available (Codex's `description` field).
type ThinkingLevel struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Catalog is the outcome of one model-discovery round: the models to show,
// plus whether they were actually discovered.
type Catalog struct {
	Models []Model
	// Unavailable carries models the runtime named but will not run here. It is
	// deliberately NOT part of Models: every capability lookup in this package
	// walks Models, so keeping these out is what makes an unrunnable model fail
	// closed everywhere without each lookup having to remember a flag.
	Unavailable []UnavailableModel
	// Fallback reports that discovery did not succeed and Models is a static
	// stand-in rather than the runtime's real catalog.
	//
	// Several providers (codebuddy, copilot, cursor, grok) answer a failed
	// discovery with a baked-in list so the picker still has something to
	// show. That is a reasonable UI affordance and a terrible thing to
	// persist: it is not what the runtime actually supports, and for
	// codebuddy the static IDs and the real ones do not overlap at all, so
	// every pick is an ID the CLI rejects. Callers must therefore never
	// treat a fallback catalog as authoritative — in particular it must not
	// enter the server's day-scale model-catalog cache, which would pin one
	// transient failure as the answer for 24h (MUL-5549).
	Fallback bool
	// CLIThinkingLevels is the effort vocabulary the installed binary itself
	// accepts, read from the binary (claude's `--help`) rather than from any
	// model list. nil means it is unknown; a non-nil empty slice means the
	// binary has no effort flag at all.
	//
	// It is the one capability an unverified catalog still enforces: it
	// describes the executable, not a guess about models, so it holds whether
	// or not discovery succeeded (MUL-7691).
	CLIThinkingLevels []string
}

// Verified reports whether the catalog is the runtime's own answer, and so may
// be used to reject, rewrite, or drop a saved model, thinking level, or
// service tier.
//
// A fallback is a static stand-in for a discovery that failed, and an empty
// catalog is what the providers without one return in the same situation.
// Both mean "unknown", not "unsupported": the saved value goes to the CLI
// as-is and the CLI is the judge (MUL-7691).
func (c Catalog) Verified() bool {
	return !c.Fallback && len(c.Models) > 0
}

// discovered adapts a plain `([]Model, error)` discovery function to Catalog
// for providers that have no static fallback — for them a failure is already
// reported as an empty list or an error, which downstream guards handle.
//
// An empty list deliberately stays unflagged. The server treats a completed
// empty report as fresh truth and drops its cached catalog, while a fallback
// report leaves the cache alone; flagging it here would change that. The
// capability checks read emptiness themselves (Catalog.Verified).
func discovered(models []Model, err error) (Catalog, error) {
	return Catalog{Models: models}, err
}

// modelCache memoizes dynamic discovery calls so repeated UI loads
// don't re-shell the agent CLI. Entries expire after cacheTTL.
type modelCacheEntry struct {
	models      []Model
	unavailable []UnavailableModel
	expiresAt   time.Time
}

var (
	modelCacheMu sync.Mutex
	modelCache   = map[string]modelCacheEntry{}
)

const modelCacheTTL = 60 * time.Second

// ListModels returns the models supported by the given agent provider.
// For providers with a known static catalog it returns the baked-in
// list; for providers with a CLI discovery mechanism (claude, codex,
// opencode, pi, openclaw) it shells out with caching and falls back where the
// provider has a safe static catalog.
//
// For claude, codex, opencode, pi, and kimi, the catalog carries per-model
// thinking-level options taken from the local CLI. Claude and Codex discovery
// failures fall back to a model + thinking snapshot; providers without a safe
// fallback leave Thinking nil, which makes the UI hide the thinking picker.
//
// runtimeCmd lets the caller point at a non-default binary; pass the zero
// Command to use the provider's default name on PATH. Its launch prefix — a
// custom runtime profile's fixed_args — is carried into every discovery
// subprocess, so a wrapper that only reaches the real CLI through a
// subcommand (`ccms start q36`) is enumerated as the CLI it actually runs
// rather than as the wrapper (GH #7046).
func ListModels(ctx context.Context, providerType string, runtimeCmd Command) (Catalog, error) {
	// Built-in runtime identities (e.g. "omp") declare their model discovery
	// strategy in the descriptor. Resolve generically before the protocol-
	// family switch so no runtime-specific case is needed below. When the
	// descriptor has no ModelDiscovery strategy, return an empty catalog
	// (not the family's default) — running a semantically incompatible
	// discovery command (e.g. omp rejecting --list-models) is worse than
	// degrading to manual entry.
	if desc, ok := BuiltinRuntimeByID(providerType); ok {
		if desc.ModelDiscovery != nil {
			return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
				return discovered(desc.ModelDiscovery(ctx, runtimeCmd))
			})
		}
		return Catalog{Models: []Model{}}, nil
	}
	switch providerType {
	case "claude":
		return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
			return discoverClaudeCatalog(ctx, runtimeCmd), nil
		})
	case "codex":
		return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
			return discoverCodexCatalog(ctx, runtimeCmd), nil
		})
	case "antigravity":
		// agy 1.0.6 added a `--model` flag plus an `agy models` catalog
		// command. Enumerate it on demand like the other dynamic-discovery
		// backends.
		return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
			return discovered(discoverAntigravityModels(ctx, runtimeCmd))
		})
	case "opencode":
		return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
			return discovered(discoverOpenCodeModels(ctx, runtimeCmd))
		})
	default:
		return Catalog{}, fmt.Errorf("unknown agent type: %q", providerType)
	}
}

// ModelSelectorMustBeProviderQualified reports whether a runtime's CLI
// refuses a model id that does not carry its `<provider>/` prefix.
//
// This is an execution contract, not a statement about catalog shape. It
// decides one thing: whether the daemon has to read the runtime catalog before
// launching a task that pins a model. That read costs a CLI subprocess with a
// 15-30s ceiling which cachedDiscovery deliberately does not memoize when it
// comes back empty or as a fallback (#3729, MUL-5549), so a logged-out runtime
// pays the ceiling on every read — worth spending only where the launch
// genuinely fails without it (MUL-6471 review).
//
// opencode's `run --model` resolves strictly through `provider/model` and
// rejects anything else — verified against opencode 1.18.14, which answers a
// bare id with `UnknownError` before any provider call.
//
// Built-in runtime identities resolve through their protocol family, so a fork
// inherits the contract from its descriptor entry rather than a name here.
func ModelSelectorMustBeProviderQualified(providerType string) bool {
	if desc, ok := BuiltinRuntimeByID(providerType); ok {
		providerType = desc.ProtocolFamily
	}
	switch providerType {
	case "opencode":
		return true
	default:
		return false
	}
}

// QualifyModelID resolves a persisted model string to the canonical ID the
// runtime's own catalog advertises, and reports whether it rewrote anything.
// catalog is the runtime's discovered catalog; callers already holding one
// (the daemon reads it for the thinking-level and service-tier checks) pay
// nothing extra.
//
// Runtimes that namespace their catalog (opencode's `provider/model`, pi's
// `provider/id` selector) want the qualified form, but `agent.model` holds
// whatever was persisted — and for gateway-style providers the bare model id
// is itself slash-shaped (`claude/claude-opus-5` under provider
// `multica-anthropic`). The slash is therefore not a provider boundary and
// cannot be guessed at: the catalog is the only thing that knows which
// provider owns an id. Callers get the qualified id when exactly one provider
// claims the value, and the input untouched otherwise.
//
// Untouched is the deliberate answer for every uncertain case — the catalog is
// a static fallback rather than the runtime's real list, the value already
// matches a catalog ID, or two providers expose the same bare id. Manual model
// entry is supported everywhere, so a value this function cannot confidently
// place must still reach the CLI verbatim; the CLI's own resolver is a better
// judge than a guess here would be.
func QualifyModelID(catalog Catalog, model string) (string, bool) {
	model = strings.TrimSpace(model)
	// A fallback catalog is a static stand-in, not what the runtime actually
	// supports (see Catalog.Verified) — qualifying against it would rewrite a
	// working id into one the CLI never advertised.
	if model == "" || !catalog.Verified() {
		return model, false
	}
	for _, m := range catalog.Models {
		if m.ID == model {
			return model, false
		}
	}
	qualified := ""
	for _, m := range catalog.Models {
		if m.Provider == "" || m.ID != m.Provider+"/"+model {
			continue
		}
		if qualified != "" && qualified != m.ID {
			// Two providers expose this same bare id. Picking one would be a
			// coin flip that silently routes the task to the wrong gateway.
			return model, false
		}
		qualified = m.ID
	}
	if qualified == "" {
		return model, false
	}
	return qualified, true
}

// ModelSelectionSupported reports whether setting `agent.model` has
// any effect for the given provider. Every built-in provider now honours
// `opts.Model` end-to-end — Hermes routes it through the ACP
// `session/set_model` RPC before each prompt; Claude / Codex / Cursor /
// Gemini / Copilot / Kimi / Reasonix / Kiro / OpenCode / OpenClaw / Pi / Antigravity
// pass it via flag or session config (Antigravity gained `--model` in agy
// 1.0.6 — MUL-3125).
//
// The hook is retained — rather than inlining `true` at the call sites — so
// a model-less runtime can opt out in one place, which makes the UI
// render a disabled "Managed by runtime" picker instead of an empty
// dropdown plus a silently-ignored manual-entry field.
func ModelSelectionSupported(providerType string) bool {
	// Every runtime this office still ships accepts a model selection.
	return true
}

// ModelKnownIncompatibleWithProvider reports whether a saved model is a known
// mismatch for a target runtime provider. Only Claude and Codex are judged, and
// only by model-family prefix: they gain same-family model IDs through live
// discovery without a Multica release, so no static list could be an
// allow-list here. Unknown/custom model strings still return false because the
// UI and CLI allow manual entries and the server should not erase values it
// cannot confidently classify.
func ModelKnownIncompatibleWithProvider(providerType, model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}

	lookupID := modelIDForCapabilityLookup(providerType, model)
	switch providerType {
	case "claude":
		if strings.HasPrefix(lookupID, "claude-") && !strings.ContainsAny(lookupID, "[]") {
			return false
		}
	case "codex":
		if strings.HasPrefix(lookupID, "gpt-") || isOpenAIReasoningSeriesID(lookupID) {
			return false
		}
	default:
		return false
	}
	return isRuntimeSpecificModelID(model)
}

// claudeContextWindowTagRe recognises Claude Code's trailing context-window
// model modifier (for example, claude-opus-5[1m]). Keep this narrower than a
// generic bracket suffix: capability lookup may inherit the base model's
// effort catalog only when the modifier is syntactically a context size.
var claudeContextWindowTagRe = regexp.MustCompile(`\[[1-9][0-9]*[km]\]$`)

// modelIDForCapabilityLookup returns the catalog identity for a runtime-native
// model string. It never changes the value persisted on the agent or passed to
// the provider CLI. Claude context-window variants share their base model's
// capabilities; every other provider and malformed/unknown modifier retains
// exact-match behavior.
func modelIDForCapabilityLookup(providerType, model string) string {
	if providerType != "claude" {
		return model
	}
	return claudeContextWindowTagRe.ReplaceAllString(model, "")
}

func isRuntimeSpecificModelID(model string) bool {
	if strings.Contains(model, "/") {
		return true
	}
	return modelHasKnownPrefix(model)
}

func modelHasKnownPrefix(model string) bool {
	return strings.HasPrefix(model, "claude-") ||
		strings.HasPrefix(model, "gpt-") ||
		strings.HasPrefix(model, "gemini-") ||
		strings.HasPrefix(model, "auto-gemini-") ||
		isOpenAIReasoningSeriesID(model)
}

// cachedDiscovery invokes fn and caches the result for modelCacheTTL.
// Callers build the key with discoveryCacheKey so two runtimes of the same
// protocol family never share one memo entry when they enumerate different
// binaries.
func cachedDiscovery(key string, fn func() (Catalog, error)) (Catalog, error) {
	modelCacheMu.Lock()
	if entry, ok := modelCache[key]; ok && time.Now().Before(entry.expiresAt) {
		out, unavailable := entry.models, entry.unavailable
		modelCacheMu.Unlock()
		return Catalog{Models: out, Unavailable: unavailable}, nil
	}
	modelCacheMu.Unlock()

	catalog, err := fn()
	if err != nil {
		return Catalog{}, err
	}

	// Don't cache an empty result. Zero models is almost always a transient
	// failure (discovery CLI timeout, not-logged-in, network blip) rather than
	// a runtime that genuinely has no models; caching it would keep the picker
	// blank for the full TTL even after the cause clears. Skipping the cache
	// lets the next request retry immediately. See #3729.
	//
	// A fallback catalog is the same situation wearing a disguise: non-empty,
	// but only because discovery failed and the provider substituted a static
	// list. Caching it would hold the stand-in for the full TTL and stop the
	// next request from retrying, which is exactly the recovery path we want
	// to keep open (MUL-5549).
	if len(catalog.Models) == 0 || catalog.Fallback {
		return catalog, nil
	}

	modelCacheMu.Lock()
	modelCache[key] = modelCacheEntry{
		models:      catalog.Models,
		unavailable: catalog.Unavailable,
		expiresAt:   time.Now().Add(modelCacheTTL),
	}
	modelCacheMu.Unlock()
	return catalog, nil
}

// discoveryCacheKey scopes a discovery memo to the binary it enumerated.
// A custom runtime profile (MUL-3284) runs a different executable under a
// built-in protocol family, so a provider-only key would let a built-in
// runtime and a same-family profile runtime on one host serve each other's
// catalog for the TTL — the same mismatch the daemon's path resolution
// fixes, reintroduced at the cache layer (MUL-5789). An empty path keeps
// the bare provider key, which is what a built-in on PATH resolves to.
//
// The key spans the launch prefix too, not just the path: two profiles can
// wrap one binary with different fixed_args — `ccms start q36` and `ccms
// start opus` — and enumerate genuinely different catalogs out of it.
func discoveryCacheKey(providerType string, runtimeCmd Command) string {
	if runtimeCmd.Path == "" && len(runtimeCmd.Prefix) == 0 {
		return providerType
	}
	return providerType + ":" + runtimeCmd.cacheKey()
}

// ── Static catalogs ──

// claudeStaticModels reflects the Claude Code CLI's accepted --model
// values. It only fills the picker when live discovery fails and is never
// validated against (Catalog.Verified), so listing a current model is cheap:
// a CLI too old to know it reports that itself at launch. Add new models
// here; drop entries only once the CLI stops accepting them. Default = Sonnet
// because it's the everyday workhorse (Opus is reserved for advisor-style
// flows).
func claudeStaticModels() []Model {
	return []Model{
		{ID: "claude-opus-5-5", Label: "Claude Opus 5.5", Provider: "anthropic"},
		{ID: "claude-sonnet-5", Label: "Claude Sonnet 5", Provider: "anthropic"},
		{ID: "claude-sonnet-4-6", Label: "Claude Sonnet 4.6", Provider: "anthropic", Default: true},
		{ID: "claude-fable-5-1", Label: "Claude Fable 5.1", Provider: "anthropic"},
		{ID: "claude-fable-5", Label: "Claude Fable 5", Provider: "anthropic"},
		{ID: "claude-opus-5", Label: "Claude Opus 5", Provider: "anthropic"},
		{ID: "claude-opus-4-8", Label: "Claude Opus 4.8", Provider: "anthropic"},
		{ID: "claude-opus-4-7", Label: "Claude Opus 4.7", Provider: "anthropic"},
		{ID: "claude-haiku-4-5-20251001", Label: "Claude Haiku 4.5", Provider: "anthropic"},
		{ID: "claude-opus-4-6", Label: "Claude Opus 4.6", Provider: "anthropic"},
		{ID: "claude-sonnet-4-5", Label: "Claude Sonnet 4.5", Provider: "anthropic"},
	}
}

// codexStaticModels is the fallback for Codex versions older than 0.122.0
// and for failed/malformed live and bundled discovery calls. It lists the
// visible entries of the newest locally verified live catalog — which can run
// ahead of the bundled one (gpt-6.1-sol was live-only on codex-cli 0.159.0)
// — plus still-common models from older Codex releases.
// Each entry carries its own reasoning catalog so old/offline CLIs retain the
// same model + thinking picker contract as dynamic discovery. Service tiers are
// intentionally NOT guessed here: they are runtime/version/account-sensitive,
// so a discovery failure hides the speed picker. None of this is validated
// against — a saved model, effort, or tier reaches the CLI as-is while this
// list stands in (Catalog.Verified, MUL-7691).
func codexStaticModels() []Model {
	// `Default` here is NOT a user-facing "default model" badge — the picker
	// stopped rendering that (Multica follows the CLI config when the model is
	// unset). It only marks the current flagship (multica#2009). It is
	// deliberately NOT used to validate effort for an empty (follow-CLI-config)
	// model: that config can resolve to any model, so ValidateThinkingLevel
	// fails an empty codex model closed rather than borrowing this entry's
	// catalog (Astra/Sol/Terra advertise `ultra`; Luna does not) — see
	// ValidateThinkingLevel and MUL-4347. Keep exactly one entry flagged.
	standardThinking := func(defaultLevel string, includeMax, includeUltra bool) *ModelThinking {
		levels := []ThinkingLevel{
			{Value: "low", Label: "Low", Description: "Fast responses with lighter reasoning"},
			{Value: "medium", Label: "Medium", Description: "Balances speed and reasoning depth for everyday tasks"},
			{Value: "high", Label: "High", Description: "Greater reasoning depth for complex problems"},
			{Value: "xhigh", Label: "Extra high", Description: "Extra high reasoning depth for complex problems"},
		}
		if includeMax {
			levels = append(levels, ThinkingLevel{Value: "max", Label: "Max", Description: "Maximum reasoning depth for the hardest problems"})
		}
		if includeUltra {
			levels = append(levels, ThinkingLevel{Value: "ultra", Label: "Ultra", Description: "Maximum reasoning with automatic task delegation"})
		}
		return &ModelThinking{DefaultLevel: defaultLevel, SupportedLevels: levels}
	}
	gpt52Thinking := func() *ModelThinking {
		return &ModelThinking{
			DefaultLevel: "medium",
			SupportedLevels: []ThinkingLevel{
				{Value: "low", Label: "Low", Description: "Balances speed with some reasoning; useful for straightforward queries and short explanations"},
				{Value: "medium", Label: "Medium", Description: "Provides a solid balance of reasoning depth and latency for general-purpose tasks"},
				{Value: "high", Label: "High", Description: "Maximizes reasoning depth for complex or ambiguous problems"},
				{Value: "xhigh", Label: "Extra high", Description: "Extra high reasoning for complex problems"},
			},
		}
	}
	return []Model{
		{ID: "gpt-6-astra", Label: "GPT-6 Astra", Provider: "openai", Default: true, Thinking: standardThinking("low", true, true)},
		{ID: "gpt-6.1-sol", Label: "GPT-6.1 Sol", Provider: "openai", Thinking: standardThinking("low", true, true)},
		{ID: "gpt-6-sol", Label: "GPT-6 Sol", Provider: "openai", Thinking: standardThinking("medium", true, true)},
		{ID: "gpt-6-luna", Label: "GPT-6 Luna", Provider: "openai", Thinking: standardThinking("medium", true, false)},
		{ID: "gpt-5.6-sol", Label: "GPT-5.6 Sol", Provider: "openai", Thinking: standardThinking("low", true, true)},
		{ID: "gpt-5.6-terra", Label: "GPT-5.6 Terra", Provider: "openai", Thinking: standardThinking("medium", true, true)},
		{ID: "gpt-5.6-luna", Label: "GPT-5.6 Luna", Provider: "openai", Thinking: standardThinking("medium", true, false)},
		{ID: "gpt-5.5", Label: "GPT-5.5", Provider: "openai", Thinking: standardThinking("medium", false, false)},
		{ID: "gpt-5.4", Label: "GPT-5.4", Provider: "openai", Thinking: standardThinking("medium", false, false)},
		{ID: "gpt-5.4-mini", Label: "GPT-5.4-Mini", Provider: "openai", Thinking: standardThinking("medium", false, false)},
		{ID: "gpt-5.3-codex", Label: "GPT-5.3-Codex", Provider: "openai", Thinking: standardThinking("medium", false, false)},
		{ID: "gpt-5.2", Label: "GPT-5.2", Provider: "openai", Thinking: gpt52Thinking()},
	}
}

// discoverTraecliModels spins up a throwaway `traecli acp serve --yolo` process
// and parses the model catalog traecli returns from session/new (same shape as
// Kiro/Qoder). The official TRAE CLI must be logged in for the catalog to be
// non-empty; on any failure the caller falls back to the manual-entry field.
func isOpenAIReasoningSeriesID(id string) bool {
	if len(id) < 2 || id[0] != 'o' {
		return false
	}
	i := 1
	for i < len(id) && id[i] >= '0' && id[i] <= '9' {
		i++
	}
	if i == 1 {
		return false
	}
	return i == len(id) || id[i] == '-'
}

// ── Dynamic discovery ──

// discoverOpenCodeModels runs `opencode models --verbose` and parses its
// output. The CLI prints `provider/model` rows, followed by JSON metadata
// when verbose mode is enabled; we emit IDs verbatim so what the user sees
// matches what `--model` accepts, and project any model `variants` into the
// thinking-level picker because OpenCode's `run --variant` flag is its
// provider-specific reasoning-effort surface.
// On any failure (CLI missing, parse error, timeout) we fall back to
// an empty list so the creatable UI still works.
func discoverOpenCodeModels(ctx context.Context, runtimeCmd Command) ([]Model, error) {
	if runtimeCmd.Path == "" {
		runtimeCmd.Path = "opencode"
	}
	if _, err := exec.LookPath(runtimeCmd.Path); err != nil {
		return []Model{}, nil
	}
	// Newer opencode (1.15+) syncs its hosted free-model catalog over the
	// network on `opencode models`, which can take ~6s; the previous 5s cap
	// timed out and returned an empty list, so the runtime showed online but
	// the model picker was empty. See multica-ai/multica#3627.
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := runtimeCmd.exec(runCtx, "models", "--verbose")
	hideAgentWindow(cmd)
	// Parse whatever the verbose command printed, even on a non-zero exit — a
	// stale config entry can make `opencode models` exit non-zero while still
	// listing the resolvable catalog (mirrors the pi path; see #3729/#3627).
	out, _ := outputOwned(cmd, runtimeCmd.logger)
	models := parseOpenCodeModels(string(out))
	if len(models) == 0 {
		// Verbose yielded nothing usable (unsupported flag, error text, or an
		// empty list). Retry the plain command, which omits the per-model JSON
		// but still prints the IDs.
		cmd = runtimeCmd.exec(runCtx, "models")
		hideAgentWindow(cmd)
		out, _ = outputOwned(cmd, runtimeCmd.logger)
		models = parseOpenCodeModels(string(out))
	}
	if len(models) == 0 {
		return []Model{}, nil
	}
	return models, nil
}

// parseOpenCodeModels accepts the `opencode models` text output and
// extracts IDs. Non-verbose output is one `provider/model` row per line.
// Verbose output appends a pretty-printed JSON object after each ID; when
// that object contains `variants`, each enabled variant becomes a thinking
// level that the backend later passes through `opencode run --variant`.
func parseOpenCodeModels(output string) []Model {
	lines := strings.Split(output, "\n")
	var models []Model
	indexByID := map[string]int{}
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		id := parseOpenCodeModelIDLine(line)
		if id == "" {
			continue
		}
		idx, seen := indexByID[id]
		if !seen {
			provider := ""
			if slash := strings.Index(id, "/"); slash > 0 {
				provider = id[:slash]
			}
			idx = len(models)
			indexByID[id] = idx
			models = append(models, Model{ID: id, Label: id, Provider: provider})
		}

		next := i + 1
		for next < len(lines) && strings.TrimSpace(lines[next]) == "" {
			next++
		}
		if next >= len(lines) || !strings.HasPrefix(strings.TrimSpace(lines[next]), "{") {
			continue
		}
		raw, resumeAt := collectOpenCodeModelJSON(lines, next)
		if json.Valid(raw) {
			annotateOpenCodeModelMetadata(&models[idx], raw)
		}
		i = resumeAt - 1
	}
	return models
}

func parseOpenCodeModelIDLine(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	id := fields[0]
	if strings.HasPrefix(id, `"`) || strings.HasPrefix(id, "{") || strings.HasPrefix(id, "[") {
		return ""
	}
	if !strings.Contains(id, "/") {
		return ""
	}
	// Skip header rows such as PROVIDER/MODEL.
	if id == strings.ToUpper(id) {
		return ""
	}
	return id
}

func collectOpenCodeModelJSON(lines []string, start int) ([]byte, int) {
	var b strings.Builder
	for i := start; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if i > start && parseOpenCodeModelIDLine(line) != "" {
			return []byte(b.String()), i
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(lines[i])
		if json.Valid([]byte(b.String())) {
			return []byte(b.String()), i + 1
		}
	}
	return []byte(b.String()), len(lines)
}

type opencodeModelMetadata struct {
	Reasoning bool                            `json:"reasoning"`
	Variants  map[string]opencodeModelVariant `json:"variants"`
}

type opencodeModelVariant struct {
	Disabled        bool            `json:"disabled"`
	ReasoningEffort string          `json:"reasoningEffort"`
	Thinking        json.RawMessage `json:"thinking"`
}

var opencodeVariantLabel = map[string]string{
	"none":    "None",
	"minimal": "Minimal",
	"low":     "Low",
	"medium":  "Medium",
	"high":    "High",
	"xhigh":   "Extra high",
	"max":     "Max",
}

var opencodeVariantOrder = map[string]int{
	"none":    0,
	"minimal": 1,
	"low":     2,
	"medium":  3,
	"high":    4,
	"xhigh":   5,
	"max":     6,
}

func annotateOpenCodeModelMetadata(model *Model, raw []byte) {
	var meta opencodeModelMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		return
	}
	if !meta.Reasoning && !openCodeVariantsLookReasoning(meta.Variants) {
		return
	}
	levels := openCodeThinkingLevelsFromVariants(meta.Variants)
	if len(levels) == 0 {
		return
	}
	model.Thinking = &ModelThinking{SupportedLevels: levels}
}

func openCodeVariantsLookReasoning(variants map[string]opencodeModelVariant) bool {
	for name, variant := range variants {
		if _, known := opencodeVariantOrder[name]; known {
			return true
		}
		if variant.ReasoningEffort != "" || len(variant.Thinking) > 0 {
			return true
		}
	}
	return false
}

func openCodeThinkingLevelsFromVariants(variants map[string]opencodeModelVariant) []ThinkingLevel {
	if len(variants) == 0 {
		return nil
	}
	values := make([]string, 0, len(variants))
	for value, variant := range variants {
		if value == "" || variant.Disabled {
			continue
		}
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool {
		left, leftKnown := opencodeVariantOrder[values[i]]
		right, rightKnown := opencodeVariantOrder[values[j]]
		if leftKnown && rightKnown {
			return left < right
		}
		if leftKnown != rightKnown {
			return leftKnown
		}
		return values[i] < values[j]
	})
	levels := make([]ThinkingLevel, 0, len(values))
	for _, value := range values {
		label, ok := opencodeVariantLabel[value]
		if !ok {
			label = strings.Title(strings.ReplaceAll(value, "-", " ")) //nolint:staticcheck
		}
		levels = append(levels, ThinkingLevel{Value: value, Label: label})
	}
	return levels
}

var piThinkingLevelLabels = map[string]string{
	"off":     "Off",
	"minimal": "Minimal",
	"low":     "Low",
	"medium":  "Medium",
	"high":    "High",
	"xhigh":   "Extra high",
	"max":     "Max",
}

type acpConfigOptionState struct {
	ID                string `json:"id"`
	CurrentValue      string `json:"currentValue"`
	CurrentValueSnake string `json:"current_value"`
}

// findACPConfigOption returns one exact config-id match from an ACP response.
// Config ids are protocol identifiers, not display labels. Both camelCase and
// snake_case field spellings are accepted because ACP agents emit both.
func findACPConfigOption(raw json.RawMessage, configID string) (acpConfigOptionState, bool) {
	var response struct {
		ConfigOptions      []acpConfigOptionState `json:"configOptions"`
		ConfigOptionsSnake []acpConfigOptionState `json:"config_options"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return acpConfigOptionState{}, false
	}
	options := append(response.ConfigOptions, response.ConfigOptionsSnake...)
	for _, option := range options {
		if option.ID == configID {
			return option, true
		}
	}
	return acpConfigOptionState{}, false
}

func acpConfigOptionCurrentValue(raw json.RawMessage, configID string) (string, bool) {
	option, ok := findACPConfigOption(raw, configID)
	if !ok {
		return "", false
	}
	value := strings.TrimSpace(option.CurrentValue)
	if value == "" {
		value = strings.TrimSpace(option.CurrentValueSnake)
	}
	return value, value != ""
}

func discoverAntigravityModels(ctx context.Context, runtimeCmd Command) ([]Model, error) {
	if runtimeCmd.Path == "" {
		runtimeCmd.Path = "agy"
	}
	if _, err := exec.LookPath(runtimeCmd.Path); err != nil {
		return nil, nil
	}
	// `agy models` is a local enumeration (no network round-trip), so a
	// short cap is plenty; keep it generous enough to absorb cold starts.
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := runtimeCmd.exec(runCtx, "models")
	hideAgentWindow(cmd)
	out, err := outputOwned(cmd, runtimeCmd.logger)
	if err != nil && len(out) == 0 {
		return nil, nil
	}
	return parseAntigravityModels(string(out)), nil
}

// parseAntigravityModels turns `agy models` output into Model entries. agy
// 1.1.11 emits "id<TAB>display label" while older versions emit one value per
// line. For legacy output the value remains both ID and Label. Blank lines and
// duplicate IDs are skipped.
func parseAntigravityModels(output string) []Model {
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var models []Model
	seen := map[string]bool{}
	for scanner.Scan() {
		raw := scanner.Text()
		id := strings.TrimSpace(raw)
		label := id
		if idField, remaining, ok := strings.Cut(raw, "\t"); ok {
			id = strings.TrimSpace(idField)
			labelField, _, _ := strings.Cut(remaining, "\t")
			label = strings.TrimSpace(labelField)
			if label == "" {
				label = id
			}
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, Model{
			ID:       id,
			Label:    label,
			Provider: "antigravity",
		})
	}
	return models
}

// discoverGrokModels spins up `grok agent --always-approve stdio` and parses
// the model catalog from session/new (same shape as Kiro/Qoder/Trae). Requires
// an authenticated Grok CLI; on any failure falls back to grokStaticModels.
