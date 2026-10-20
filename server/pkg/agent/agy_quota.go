package agent

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const defaultAgyQuotaReset = time.Hour

// AgyQuotaState is one exhausted account directory and when it may be retried.
// The name is historical: the store it describes holds every CLI's accounts.
type AgyQuotaState struct {
	Dir     string
	ResetAt time.Time
}

var (
	agyResetsInRe = regexp.MustCompile(`(?i)resets\s+in\s+((?:\d+d)?(?:\d+h)?(?:\d+m)?(?:\d+s)?)`)
	agyDurationRe = regexp.MustCompile(`(\d+)([dhms])`)
)

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

// DefaultQuotaResetAt returns now+1h when the provider omitted a reset.
//
// One hour is deliberately short for a deadline nobody reported: the overlay
// it feeds hides an account from selection, and a guess that outlives the real
// reset costs more than one that expires early and lets the next run re-learn
// the truth.
func DefaultQuotaResetAt(now, resetAt time.Time) time.Time {
	if !resetAt.IsZero() {
		return resetAt
	}
	if now.IsZero() {
		now = time.Now()
	}
	return now.Add(defaultAgyQuotaReset)
}

// ParseQuotaResetHint reads a "Resets in 3h20m" style deadline out of any
// CLI's quota error text, or the zero time when the text carries none.
//
// The phrasing originated in AGY's individual-quota error, but nothing about
// the pattern is AGY-specific. This does NOT decide whether the text is a
// quota failure at all; the caller has already classified it.
func ParseQuotaResetHint(text string, now time.Time) time.Time {
	return parseAgyResetsIn(strings.ToLower(text), now)
}

// NormalizeAgyDir trims trailing separators so host and UI paths compare.
func NormalizeAgyDir(dir string) string {
	trimmed := strings.TrimSpace(dir)
	if trimmed == "" {
		return ""
	}
	return strings.TrimRight(filepath.Clean(trimmed), `/\`)
}
