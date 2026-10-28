package execenv

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Every platform defaults Codex to danger-full-access, written explicitly into
// the per-task config.toml. The daemon used to keep macOS on a version gate
// waiting for openai/codex#10390 (Seatbelt ignoring network_access under
// workspace-write); with no fixed release named, that gate always fell back to
// full access anyway, so the default is now stated rather than derived.
// Linux runs full access as a product decision (MUL-5578, #6218) and Windows as
// a compatibility choice (MUL-4957) — see codexSandboxPolicyFor.

// CodexFullAccessSandboxMode is the sandbox_mode every platform gets by default.
const CodexFullAccessSandboxMode = "danger-full-access"

// codexSandboxPolicy describes how the per-task Codex config.toml should
// configure the sandbox.
type codexSandboxPolicy struct {
	// Mode is the value written as `sandbox_mode = "..."`.
	Mode string
	// NetworkAccess controls `[sandbox_workspace_write] network_access`.
	// Only meaningful when Mode is "workspace-write".
	NetworkAccess bool
	// Reason is a short human-readable label used in warn-level logs.
	Reason string
	// Hint is an optional, actionable remediation surfaced in warn-level logs
	// when Mode is danger-full-access. It is empty when there is no generic
	// action to surface (e.g. the Windows compatibility fallback, where
	// enabling Codex's native sandbox is deferred follow-up work), so the log
	// omits the hint instead of showing an irrelevant one.
	Hint string
}

// resolveGOOS returns goos, or runtime.GOOS when goos is empty. Callers pass an
// explicit goos in tests; production leaves it empty to use the host platform.
func resolveGOOS(goos string) string {
	if goos == "" {
		return runtime.GOOS
	}
	return goos
}

// codexSandboxPolicyFor picks the default policy for the given platform. It is
// the platform baseline; per-task user config can refine it on Windows (see
// codexSandboxPolicyForConfig). Every platform gets danger-full-access:
//
//   - Linux (and any other non-darwin, non-windows platform): tasks run on the
//     daemon user's real HOME. Landlock could enforce workspace-write here, but
//     enforcing it meant redirecting HOME/XDG per task and seeding a
//     hand-maintained allowlist of credential paths back into it, which left
//     every un-listed host CLI (aws, kube, gcloud, glab, rclone, …)
//     unconfigured inside tasks and could not converge. The containment it
//     bought was also narrower than it looked: workspace-write restricts writes
//     only — reads and network were already unrestricted, so credentials under
//     the real HOME were readable and exfiltratable regardless. The task
//     filesystem boundary is therefore the boundary the daemon itself runs
//     inside (VM, container, or dedicated Unix user), matching macOS/Windows.
//     See MUL-5578 / #6218, and apps/docs security-model.
//   - macOS: Seatbelt under workspace-write blocks DNS even with
//     network_access = true (openai/codex#10390), so the Multica CLI could not
//     reach the API from inside a sandboxed task. Full access is the explicit
//     default regardless of the Codex version.
//   - Windows: a deliberate compatibility choice. Codex ships a native Windows
//     sandbox (windows.sandbox = "unelevated" via a Restricted Token, or
//     "elevated"), but it is still experimental with known reliability
//     limitations, so the daemon does not enable it by default. When no native
//     windows.sandbox is configured, Codex cannot enforce workspace-write on
//     Windows (it downgrades to read-only) and then rejects non-safe mutation
//     commands "by policy" — e.g. `multica issue create` fails — because under
//     approval_policy = "never" the request never reaches the daemon's
//     auto-approver. Enabling the native sandbox is tracked as separate
//     follow-up work. See MUL-4957. A user who has opted into windows.sandbox
//     (via config.toml or a `-c` custom arg) keeps workspace-write instead, and
//     an undecidable config fails closed; that logic lives in
//     codexSandboxPolicyForConfig / resolveWindowsSandboxState.
func codexSandboxPolicyFor(goos string) codexSandboxPolicy {
	goos = resolveGOOS(goos)
	switch goos {
	case "windows":
		return codexSandboxPolicy{
			Mode:   CodexFullAccessSandboxMode,
			Reason: "codex on windows: compatibility fallback; no native windows.sandbox configured, so workspace-write cannot be enforced (MUL-4957)",
		}
	case "darwin":
		return codexSandboxPolicy{
			Mode:   CodexFullAccessSandboxMode,
			Reason: "codex on macOS: full access by default; seatbelt workspace-write blocks network access (openai/codex#10390)",
			Hint:   codexHostIsolationHint(),
		}
	default:
		return codexSandboxPolicy{
			Mode:   CodexFullAccessSandboxMode,
			Reason: "codex on " + goos + ": tasks run with the daemon user's real HOME and full filesystem access; isolation comes from the boundary the daemon runs inside (MUL-5578)",
			Hint:   codexHostIsolationHint(),
		}
	}
}

// CodexStartupSandboxMode is the sandbox_mode Codex tasks on goos get from the
// shared Codex config ($CODEX_HOME or ~/.codex). The daemon logs it at startup
// so the effective permission is visible without opening a task home. On
// Windows it reads the shared config.toml the same way a task does: a
// windows.sandbox opt-in, or a config that cannot be read or parsed, keeps
// workspace-write. A per-agent `-c windows.sandbox=…` custom arg can still move
// that agent's tasks to workspace-write; it is only known per task.
func CodexStartupSandboxMode(goos string) string {
	state := windowsSandboxAbsent
	if resolveGOOS(goos) == "windows" {
		state = sharedWindowsSandboxState(resolveSharedCodexHome())
	}
	return codexSandboxPolicyForConfig(goos, state).Mode
}

// sharedWindowsSandboxState classifies windows.sandbox in the shared
// config.toml, failing closed when the file exists but cannot be read.
func sharedWindowsSandboxState(sharedHome string) windowsSandboxConfig {
	data, err := os.ReadFile(filepath.Join(sharedHome, "config.toml"))
	switch {
	case err == nil:
		return windowsSandboxFromConfig(string(data))
	case os.IsNotExist(err):
		return windowsSandboxAbsent
	default:
		return windowsSandboxUndecidable
	}
}

// windowsSandboxConfig is the tri-state of a native Codex Windows sandbox
// selection. It is three-valued (not a bool) so an undecidable config fails
// closed — the daemon never loosens to danger-full-access when it cannot
// confirm the user's intent. See MUL-4957.
type windowsSandboxConfig int

const (
	// windowsSandboxAbsent: confidently no native sandbox is selected anywhere,
	// so the danger-full-access compatibility fallback is safe to apply.
	windowsSandboxAbsent windowsSandboxConfig = iota
	// windowsSandboxNative: a valid windows.sandbox = "unelevated"|"elevated"
	// is selected, so keep workspace-write and let Codex enforce isolation.
	windowsSandboxNative
	// windowsSandboxUndecidable: the config could not be read/parsed, or holds
	// a windows.sandbox value Codex does not accept. The daemon cannot tell
	// whether the user asked for isolation, so it must NOT loosen — fail closed.
	windowsSandboxUndecidable
)

// codexSandboxPolicyForConfig returns the platform default from
// codexSandboxPolicyFor for linux/darwin. On Windows it applies the resolved
// native-sandbox state (see resolveWindowsSandboxState): a user who opted into
// windows.sandbox keeps workspace-write, an undecidable config fails closed to
// workspace-write, and only a confidently absent sandbox gets the
// danger-full-access compatibility fallback. See MUL-4957.
//
// This is intentionally the branch point for the eventual native-sandbox
// rollout: flipping the Windows default later means writing windows.sandbox
// ourselves and defaulting winState to native, not restructuring callers.
func codexSandboxPolicyForConfig(goos string, winState windowsSandboxConfig) codexSandboxPolicy {
	goos = resolveGOOS(goos)
	if goos == "windows" {
		return codexSandboxPolicyForWindows(winState)
	}
	return codexSandboxPolicyFor(goos)
}

// codexSandboxPolicyForWindows maps a resolved native-sandbox state to a
// policy. Native and Undecidable both keep workspace-write (Undecidable is the
// fail-closed case — it never loosens on doubt); only a confidently absent
// native sandbox gets the danger-full-access compatibility fallback.
func codexSandboxPolicyForWindows(state windowsSandboxConfig) codexSandboxPolicy {
	switch state {
	case windowsSandboxNative:
		return codexSandboxPolicy{
			Mode:          "workspace-write",
			NetworkAccess: true,
			Reason:        "codex on windows: native windows.sandbox configured; keeping workspace-write so Codex enforces task isolation",
		}
	case windowsSandboxUndecidable:
		return codexSandboxPolicy{
			Mode:          "workspace-write",
			NetworkAccess: true,
			Reason:        "codex on windows: windows.sandbox config undecidable (unreadable/unparseable/invalid); failing closed to workspace-write rather than loosening (MUL-4957)",
		}
	default: // windowsSandboxAbsent
		return codexSandboxPolicy{
			Mode:   CodexFullAccessSandboxMode,
			Reason: "codex on windows: compatibility fallback; no native windows.sandbox configured (MUL-4957)",
		}
	}
}

// windowsSandboxFromConfig classifies the windows.sandbox selection in a
// config.toml body. Codex accepts only the exact-lowercase variants
// "unelevated"/"elevated" and refuses to load a config with any other value, so
// anything else present is treated as undecidable (fail closed) rather than a
// safe "absent". Unparseable TOML is likewise undecidable — Codex would reject
// the same file. An absent windows.sandbox key is a genuine "absent".
func windowsSandboxFromConfig(config string) windowsSandboxConfig {
	var probe struct {
		Windows struct {
			Sandbox string `toml:"sandbox"`
		} `toml:"windows"`
	}
	if err := toml.Unmarshal([]byte(config), &probe); err != nil {
		return windowsSandboxUndecidable
	}
	return classifyWindowsSandboxValue(probe.Windows.Sandbox)
}

// codexConfigOverrideValueRe matches the value token of a Codex `-c` /
// `--config` windows.sandbox override, e.g. `windows.sandbox = "unelevated"`.
// It tolerates whitespace around the dotted key and the `=`, matching Codex's
// own lenient `-c` parsing.
var codexWindowsSandboxOverrideRe = regexp.MustCompile(`^\s*windows\s*\.\s*sandbox\s*=`)

// windowsSandboxFromCustomArgs classifies a native Windows sandbox selection
// passed via Codex `-c windows.sandbox=...` / `--config windows.sandbox=...`
// args. These never land in config.toml (they stay in argv and are applied on
// top of it), so config-only detection would miss them — the MUL-4957 review's
// second must-fix. Mirrors the override-parsing shape in server/pkg/agent's
// buildCodexArgs: inline (`-c=windows.sandbox=x`) and two-token
// (`-c windows.sandbox=x`) forms, last occurrence winning (Codex is last-wins).
func windowsSandboxFromCustomArgs(args []string) windowsSandboxConfig {
	state := windowsSandboxAbsent
	for i := 0; i < len(args); i++ {
		arg := args[i]
		flag := arg
		value := ""
		hasInlineValue := false
		if idx := strings.Index(arg, "="); idx > 0 {
			flag = arg[:idx]
			value = arg[idx+1:]
			hasInlineValue = true
		}
		if flag != "-c" && flag != "--config" {
			continue
		}
		if !hasInlineValue {
			if i+1 >= len(args) {
				continue
			}
			i++
			value = args[i]
		}
		if !codexWindowsSandboxOverrideRe.MatchString(value) {
			continue
		}
		// A windows.sandbox override token: take the part after its first `=`.
		if eq := strings.Index(value, "="); eq >= 0 {
			state = classifyWindowsSandboxValue(value[eq+1:])
		}
	}
	return state
}

// classifyWindowsSandboxValue maps a raw windows.sandbox value (from config.toml
// or a `-c` arg, possibly surrounded by whitespace/quotes) to a tri-state. Only
// the exact-lowercase variants Codex accepts count as native; a present but
// unaccepted value is undecidable (Codex would refuse the config); an empty
// value is absent.
func classifyWindowsSandboxValue(raw string) windowsSandboxConfig {
	v := strings.TrimSpace(raw)
	v = strings.Trim(v, `"'`)
	v = strings.TrimSpace(v)
	switch v {
	case "":
		return windowsSandboxAbsent
	case "unelevated", "elevated":
		return windowsSandboxNative
	default:
		return windowsSandboxUndecidable
	}
}

// resolveWindowsSandbox folds per-layer states into one. Undecidable wins over
// everything (any broken/ambiguous layer means fail closed), then Native over
// Absent (an opt-in in any layer keeps isolation).
func resolveWindowsSandbox(states ...windowsSandboxConfig) windowsSandboxConfig {
	result := windowsSandboxAbsent
	for _, s := range states {
		if s == windowsSandboxUndecidable {
			return windowsSandboxUndecidable
		}
		if s == windowsSandboxNative {
			result = windowsSandboxNative
		}
	}
	return result
}

// codexHostIsolationHint returns the actionable remediation for the full-access
// default: the containment has to come from the boundary the daemon runs inside.
func codexHostIsolationHint() string {
	return "run the daemon inside a VM, container, or dedicated Unix user — tasks can read and write everything that user can"
}

// multicaManagedBeginMarker / multicaManagedEndMarker delimit the block the
// daemon writes into the per-task config.toml. Everything between the markers
// is owned by the daemon and will be rewritten idempotently; anything outside
// the markers is preserved as-is.
const (
	multicaManagedBeginMarker = "# BEGIN multica-managed (do not edit; regenerated by daemon)"
	multicaManagedEndMarker   = "# END multica-managed"
)

// renderMulticaManagedBlock produces the managed block for the given policy.
//
// The block contains only top-level key=value assignments — no `[table]`
// headers — and uses TOML dotted-key syntax for nested values. This is
// important because the block is inserted into a user-owned config.toml:
//
//   - If the block opened a `[sandbox_workspace_write]` header, any user
//     content that happened to sit below it would be silently reparented into
//     that table.
//   - If the block were appended after a file that already ends inside some
//     other table (e.g. `[permissions.multica]`), a bare `sandbox_mode = ...`
//     key would be parsed as a child of that preceding table.
//
// Keeping the block as pure top-level dotted-key assignments, and placing it
// at the top of the file (see upsertMulticaManagedBlock), avoids both traps.
func renderMulticaManagedBlock(policy codexSandboxPolicy) string {
	var b strings.Builder
	b.WriteString(multicaManagedBeginMarker)
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("sandbox_mode = %q\n", policy.Mode))
	if policy.Mode == "workspace-write" {
		b.WriteString(fmt.Sprintf("sandbox_workspace_write.network_access = %t\n", policy.NetworkAccess))
	}
	b.WriteString(multicaManagedEndMarker)
	b.WriteString("\n")
	return b.String()
}

// managedBlockRe captures the daemon-owned block (including the surrounding
// markers and any trailing blank lines) so it can be replaced idempotently.
// `\n*` rather than `\n?` so reruns don't accumulate blank lines when the
// block coexists with another managed block (e.g. multi-agent) in the file.
var managedBlockRe = regexp.MustCompile(
	`(?ms)^` + regexp.QuoteMeta(multicaManagedBeginMarker) +
		`.*?^` + regexp.QuoteMeta(multicaManagedEndMarker) + `\n*`)

// upsertMulticaManagedBlock returns the config content with the multica-managed
// block placed at the very top of the file. Any previously written managed
// block is removed in place; user content outside the markers is preserved.
//
// The block is always hoisted to the top (rather than replaced in place or
// appended to EOF) so that its top-level keys are parsed at the TOML root,
// regardless of whether the user's config ends inside a table like
// `[permissions.multica]` or `[profiles.foo]`. Combined with the dotted-key
// form used by renderMulticaManagedBlock, this means the managed block neither
// leaks into nor inherits from any surrounding table scope.
func upsertMulticaManagedBlock(content string, policy codexSandboxPolicy) string {
	// Drop any previously written managed block (wherever it sits).
	content = managedBlockRe.ReplaceAllString(content, "")
	block := renderMulticaManagedBlock(policy)
	// Trim leading blank lines left behind by the removal so we don't grow
	// the file on every idempotent rewrite.
	content = strings.TrimLeft(content, "\n")
	if content == "" {
		return block
	}
	return block + "\n" + content
}

// stripLegacySandboxDirectives removes top-level `sandbox_mode = ...` lines
// and any `[sandbox_workspace_write]` section that would otherwise conflict
// with the managed block. This lets the daemon migrate tasks whose config.toml
// was produced by an older daemon that wrote those values inline.
//
// Only top-level entries are stripped; anything under an unrelated section
// header (like `[permissions.foo]`) is preserved untouched.
func stripLegacySandboxDirectives(content string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	inLegacyWorkspaceWrite := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			// Entering a new section. Exit legacy-tracking if we were in one.
			inLegacyWorkspaceWrite = trimmed == "[sandbox_workspace_write]"
			if inLegacyWorkspaceWrite {
				continue
			}
			out = append(out, line)
			continue
		}
		if inLegacyWorkspaceWrite {
			// Drop the legacy section body until the next section.
			continue
		}
		if strings.HasPrefix(trimmed, "sandbox_mode") {
			// Drop legacy top-level sandbox_mode declarations.
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// ensureCodexSandboxConfig writes the multica-managed sandbox block into the
// given config.toml according to the policy. It is idempotent: running it
// twice produces the same file contents. The file is created if it doesn't
// exist.
//
// The function logs (at warn level) whenever the resolved mode is
// danger-full-access — the Linux default, the macOS seatbelt fallback, and the
// Windows no-native-sandbox fallback alike — so that every unsandboxed task is
// visible in daemon logs.
func ensureCodexSandboxConfig(configPath string, policy codexSandboxPolicy, detectedVersion string, logger *slog.Logger) error {
	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read config.toml: %w", err)
	}
	existing := string(data)

	// Drop inline sandbox_mode / [sandbox_workspace_write] from older daemon
	// versions so they don't collide with the managed block.
	if existing != "" && !managedBlockRe.MatchString(existing) {
		existing = stripLegacySandboxDirectives(existing)
	}

	updated := upsertMulticaManagedBlock(existing, policy)
	if updated == string(data) {
		return nil
	}

	if policy.Mode == CodexFullAccessSandboxMode && logger != nil {
		version := detectedVersion
		if version == "" {
			version = "unknown"
		}
		attrs := []any{
			"reason", policy.Reason,
			"codex_version", version,
			"config_path", configPath,
		}
		if policy.Hint != "" {
			attrs = append(attrs, "hint", policy.Hint)
		}
		logger.Warn("codex sandbox: running unsandboxed with danger-full-access", attrs...)
	}

	if err := os.WriteFile(configPath, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("write config.toml: %w", err)
	}
	return nil
}
