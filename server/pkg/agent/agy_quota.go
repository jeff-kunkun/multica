package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	agySlotsRuntimeKey  = "agy_slots"
	maxAgyAccountNumber = 32
	agyAccount1Dir      = ".gemini"
	// DefaultQuotaReset is the exhaustion window applied when a provider
	// reports an account as out of quota without saying when it recovers. It
	// began as the AGY-only fallback (DENE-305) and is now the shared default
	// for every CLI's quota accounting, which is why it carries the neutral
	// name while only the AGY parser below uses it directly.
	DefaultQuotaReset = time.Hour
)

// AgyQuotaHit is a parsed Antigravity/AGY quota exhaustion from CLI output.
type AgyQuotaHit struct {
	ResetAt time.Time // zero when the error did not include "Resets in …"
}

// AgyQuotaState is one exhausted Gemini directory and when it may be retried.
type AgyQuotaState struct {
	Dir     string
	ResetAt time.Time
}

// AgySlotDir is one isolation directory in an agent's AGY slot pool.
type AgySlotDir struct {
	Account int // 1-based numbered slot; 0 means a custom --gemini_dir
	Dir     string
}

var (
	agyResetsInRe   = regexp.MustCompile(`(?i)resets\s+in\s+((?:\d+d)?(?:\d+h)?(?:\d+m)?(?:\d+s)?)`)
	agyDurationRe   = regexp.MustCompile(`(\d+)([dhms])`)
	agyAccountDirRe = regexp.MustCompile(`^\.gemini-account(\d+)$`)
)

// ParseAgyQuotaError reports whether text is an AGY/Antigravity individual
// quota exhaustion. Matching is intentionally narrower than the generic
// plan-limits 429 classifier: a transient rate limit must not burn a slot.
func ParseAgyQuotaError(text string) (AgyQuotaHit, bool) {
	lower := strings.ToLower(text)
	if lower == "" {
		return AgyQuotaHit{}, false
	}
	matched := strings.Contains(lower, "individual quota reached") ||
		strings.Contains(lower, "resource_exhausted") ||
		strings.Contains(lower, "quota exceeded") ||
		strings.Contains(lower, "quota_exceeded")
	resetAt := parseAgyResetsIn(lower, time.Time{})
	if !matched && !resetAt.IsZero() && strings.Contains(lower, "quota") {
		matched = true
	}
	if !matched {
		return AgyQuotaHit{}, false
	}
	return AgyQuotaHit{ResetAt: resetAt}, true
}

// ParseAgyQuotaErrorAt is ParseAgyQuotaError with an explicit observation time
// so tests can pin "Resets in …" without sleeping.
func ParseAgyQuotaErrorAt(text string, now time.Time) (AgyQuotaHit, bool) {
	hit, ok := ParseAgyQuotaError(text)
	if !ok {
		return hit, false
	}
	if reset := parseAgyResetsIn(strings.ToLower(text), now); !reset.IsZero() {
		hit.ResetAt = reset
	}
	return hit, true
}

func parseAgyResetsIn(lower string, now time.Time) time.Time {
	match := agyResetsInRe.FindStringSubmatch(lower)
	if len(match) < 2 || match[1] == "" {
		return time.Time{}
	}
	var d time.Duration
	for _, part := range agyDurationRe.FindAllStringSubmatch(match[1], -1) {
		n, err := strconv.Atoi(part[1])
		if err != nil || n < 0 {
			continue
		}
		switch part[2] {
		case "d":
			d += time.Duration(n) * 24 * time.Hour
		case "h":
			d += time.Duration(n) * time.Hour
		case "m":
			d += time.Duration(n) * time.Minute
		case "s":
			d += time.Duration(n) * time.Second
		}
	}
	if d <= 0 {
		return time.Time{}
	}
	if now.IsZero() {
		now = time.Now()
	}
	return now.Add(d)
}

// DefaultAgyQuotaResetAt returns now+DefaultQuotaReset when the provider
// omitted a reset.
func DefaultAgyQuotaResetAt(now, resetAt time.Time) time.Time {
	if !resetAt.IsZero() {
		return resetAt
	}
	if now.IsZero() {
		now = time.Now()
	}
	return now.Add(DefaultQuotaReset)
}

// GeminiDirFromArgs returns the --gemini_dir value from a CLI argv region.
func GeminiDirFromArgs(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--gemini_dir" {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		if strings.HasPrefix(args[i], "--gemini_dir=") {
			return strings.TrimPrefix(args[i], "--gemini_dir=")
		}
	}
	return ""
}

// SetGeminiDirArgs replaces any existing --gemini_dir tokens. An empty path
// drops the flag so account 1 can keep agy's default ~/.gemini.
func SetGeminiDirArgs(args []string, dir string) []string {
	next := make([]string, 0, len(args)+2)
	for i := 0; i < len(args); i++ {
		if args[i] == "--gemini_dir" {
			i++
			continue
		}
		if strings.HasPrefix(args[i], "--gemini_dir=") {
			continue
		}
		next = append(next, args[i])
	}
	trimmed := strings.TrimSpace(dir)
	if trimmed == "" {
		return next
	}
	return append(next, "--gemini_dir", trimmed)
}

// ParseAgySlotAccounts reads runtime_config.agy_slots.accounts. Missing config
// seeds accounts 1–3 plus the numbered slot implied by geminiDir, matching the
// settings UI.
func ParseAgySlotAccounts(runtimeConfig json.RawMessage, geminiDir string) []int {
	type slotsFile struct {
		Accounts []int `json:"accounts"`
	}
	type cfgFile struct {
		AgySlots *slotsFile `json:"agy_slots"`
	}
	var cfg cfgFile
	if len(runtimeConfig) > 0 {
		_ = json.Unmarshal(runtimeConfig, &cfg)
	}
	if cfg.AgySlots != nil && cfg.AgySlots.Accounts != nil {
		return normalizeAgyAccountNumbers(cfg.AgySlots.Accounts)
	}
	seeded := []int{1, 2, 3}
	if n := accountNumberFromDir(geminiDir); n > 0 {
		seeded = append(seeded, n)
	}
	return normalizeAgyAccountNumbers(seeded)
}

func normalizeAgyAccountNumbers(accounts []int) []int {
	seen := map[int]struct{}{1: {}}
	for _, n := range accounts {
		if n >= 1 && n <= maxAgyAccountNumber {
			seen[n] = struct{}{}
		}
	}
	out := make([]int, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

func agyAccountDirLeaf(account int) string {
	if account <= 1 {
		return agyAccount1Dir
	}
	return fmt.Sprintf(".gemini-account%d", account)
}

func accountNumberFromDir(dir string) int {
	base := filepath.Base(strings.TrimRight(strings.TrimSpace(dir), `/\`))
	if base == agyAccount1Dir || dir == "" {
		return 1
	}
	match := agyAccountDirRe.FindStringSubmatch(base)
	if len(match) < 2 {
		return 0
	}
	n, err := strconv.Atoi(match[1])
	if err != nil || n < 1 || n > maxAgyAccountNumber {
		return 0
	}
	return n
}

// NormalizeAgyDir trims trailing separators so host and UI paths compare.
func NormalizeAgyDir(dir string) string {
	trimmed := strings.TrimSpace(dir)
	if trimmed == "" {
		return ""
	}
	return strings.TrimRight(filepath.Clean(trimmed), `/\`)
}

// ResolveAgyGeminiDir expands a missing or ~ --gemini_dir against home.
func ResolveAgyGeminiDir(dir, home string) string {
	trimmed := strings.TrimSpace(dir)
	home = strings.TrimSpace(home)
	if trimmed == "" {
		if home == "" {
			return ""
		}
		return NormalizeAgyDir(filepath.Join(home, agyAccount1Dir))
	}
	if home != "" {
		trimmed = expandUserHomeDir(trimmed, home)
	}
	return NormalizeAgyDir(trimmed)
}

// BuildAgySlotDirs materializes this agent's numbered isolation dirs plus a
// custom --gemini_dir that is not one of those numbered leaves.
func BuildAgySlotDirs(accounts []int, home, currentGeminiDir string) []AgySlotDir {
	home = strings.TrimSpace(home)
	accounts = normalizeAgyAccountNumbers(accounts)
	out := make([]AgySlotDir, 0, len(accounts)+1)
	seen := make(map[string]struct{}, len(accounts)+1)
	for _, n := range accounts {
		if home == "" {
			continue
		}
		dir := NormalizeAgyDir(filepath.Join(home, agyAccountDirLeaf(n)))
		if dir == "" {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		out = append(out, AgySlotDir{Account: n, Dir: dir})
	}
	current := NormalizeAgyDir(currentGeminiDir)
	if current != "" {
		if _, ok := seen[current]; !ok {
			out = append(out, AgySlotDir{Account: 0, Dir: current})
		}
	}
	return out
}

func agyDirStillExhausted(state AgyQuotaState, now time.Time) bool {
	if strings.TrimSpace(state.Dir) == "" || state.ResetAt.IsZero() {
		return false
	}
	return state.ResetAt.After(now)
}

func exhaustedUntil(dir string, exhausted []AgyQuotaState, now time.Time) time.Time {
	want := NormalizeAgyDir(dir)
	for _, item := range exhausted {
		if NormalizeAgyDir(item.Dir) != want {
			continue
		}
		if agyDirStillExhausted(item, now) {
			return item.ResetAt
		}
	}
	return time.Time{}
}

func agyDirLoggedIn(dir string, loggedIn []string) bool {
	if len(loggedIn) == 0 {
		return true
	}
	want := NormalizeAgyDir(dir)
	for _, item := range loggedIn {
		if NormalizeAgyDir(item) == want {
			return true
		}
	}
	return false
}

func slotIsAvailable(dir string, exhausted []AgyQuotaState, loggedIn []string, now time.Time) bool {
	if dir == "" {
		return false
	}
	if !exhaustedUntil(dir, exhausted, now).IsZero() {
		return false
	}
	return agyDirLoggedIn(dir, loggedIn)
}

// NextAvailableAgySlot walks the pool after current (wrapping) and returns the
// next logged-in, non-exhausted directory. An empty loggedIn list fails open
// so a probe miss cannot freeze failover.
func NextAvailableAgySlot(entries []AgySlotDir, current string, exhausted []AgyQuotaState, loggedIn []string, now time.Time) (string, bool) {
	if len(entries) == 0 {
		return "", false
	}
	current = NormalizeAgyDir(current)
	start := 0
	if current != "" {
		for i, entry := range entries {
			if NormalizeAgyDir(entry.Dir) == current {
				start = i + 1
				break
			}
		}
	}
	for i := 0; i < len(entries); i++ {
		entry := entries[(start+i)%len(entries)]
		dir := NormalizeAgyDir(entry.Dir)
		if dir == "" || dir == current {
			continue
		}
		if slotIsAvailable(dir, exhausted, loggedIn, now) {
			return dir, true
		}
	}
	return "", false
}

// SelectAgyLaunchDir returns current when it is still available, otherwise the
// next pool member. ok is false when the whole pool is unusable.
func SelectAgyLaunchDir(entries []AgySlotDir, current string, exhausted []AgyQuotaState, loggedIn []string, now time.Time) (string, bool) {
	current = NormalizeAgyDir(current)
	if current != "" && slotIsAvailable(current, exhausted, loggedIn, now) {
		return current, true
	}
	if next, ok := NextAvailableAgySlot(entries, current, exhausted, loggedIn, now); ok {
		return next, true
	}
	if current != "" && exhaustedUntil(current, exhausted, now).IsZero() {
		// Keep the user-selected dir when it is not exhausted even if login
		// probe data is incomplete; the process can still fail for auth.
		return current, true
	}
	return "", false
}

// FormatAgyPoolExhausted lists every slot's status for the terminal error.
// The text keeps the word "quota" so Classify still lands in provider_quota_limit.
func FormatAgyPoolExhausted(entries []AgySlotDir, exhausted []AgyQuotaState, loggedIn []string, now time.Time) string {
	var b strings.Builder
	b.WriteString("AGY quota exhausted on every slot for this agent:")
	if len(entries) == 0 {
		b.WriteString(" (no slots configured)")
		return b.String()
	}
	for _, entry := range entries {
		label := "custom"
		if entry.Account > 0 {
			label = fmt.Sprintf("account %d", entry.Account)
		}
		status := "available"
		if resetAt := exhaustedUntil(entry.Dir, exhausted, now); !resetAt.IsZero() {
			status = fmt.Sprintf("exhausted, resets %s", resetAt.UTC().Format(time.RFC3339))
		} else if len(loggedIn) > 0 && !agyDirLoggedIn(entry.Dir, loggedIn) {
			status = "signed out"
		}
		fmt.Fprintf(&b, "\n- %s (%s): %s", label, entry.Dir, status)
	}
	return b.String()
}
