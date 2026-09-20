package daemon

import (
	"context"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func durationFromEnv(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	d, err := parseFlexDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration %q: %w", key, value, err)
	}
	return d, nil
}

// bytesFromEnv reads a byte count, accepting a plain number of bytes or a
// binary size suffix ("20GiB", "512mb", "2G"). Sizes are the natural unit for
// a cache ceiling and nobody should have to spell 20 GiB in decimal.
func bytesFromEnv(key string, fallback int64) (int64, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	n, err := parseByteSize(value)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid size %q: %w", key, value, err)
	}
	return n, nil
}

// byteSizeUnits maps a normalised suffix to its multiplier. Both the "kb" and
// "kib" spellings mean 1024: a disk-cache ceiling is never quoted in powers of
// ten, and honouring the SI reading would silently shrink every configured
// limit by 7% at the GiB scale.
var byteSizeUnits = []struct {
	suffix string
	scale  int64
}{
	{"tib", 1 << 40}, {"gib", 1 << 30}, {"mib", 1 << 20}, {"kib", 1 << 10},
	{"tb", 1 << 40}, {"gb", 1 << 30}, {"mb", 1 << 20}, {"kb", 1 << 10},
	{"t", 1 << 40}, {"g", 1 << 30}, {"m", 1 << 20}, {"k", 1 << 10},
	{"b", 1},
}

func parseByteSize(value string) (int64, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	for _, unit := range byteSizeUnits {
		if !strings.HasSuffix(normalized, unit.suffix) {
			continue
		}
		number := strings.TrimSpace(strings.TrimSuffix(normalized, unit.suffix))
		amount, err := strconv.ParseFloat(number, 64)
		if err != nil {
			return 0, err
		}
		if amount < 0 {
			return 0, fmt.Errorf("negative size")
		}
		scaled := amount * float64(unit.scale)
		if scaled > float64(math.MaxInt64) {
			return 0, fmt.Errorf("size overflows int64")
		}
		return int64(scaled), nil
	}
	n, err := strconv.ParseInt(normalized, 10, 64)
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, fmt.Errorf("negative size")
	}
	return n, nil
}

// dayUnit matches a decimal number (with optional leading digits) followed by
// `d` (days), so both "5d" and "1.5d" are captured whole and expanded to hours.
var dayUnit = regexp.MustCompile(`(\d*\.\d+|\d+)d`)

// parseFlexDuration accepts the standard Go time.ParseDuration syntax plus a
// `d` (day) suffix, which the stdlib rejects. "5d" → 120h, "1d12h" → 36h,
// "0.5d" → 12h. Overflow or malformed numbers propagate as errors.
func parseFlexDuration(value string) (time.Duration, error) {
	var convErr error
	expanded := dayUnit.ReplaceAllStringFunc(value, func(match string) string {
		days, err := strconv.ParseFloat(match[:len(match)-1], 64)
		if err != nil {
			convErr = err
			return match
		}
		// time.ParseDuration handles fractional hours natively, and rejects
		// overflow on its own.
		return strconv.FormatFloat(days*24, 'f', -1, 64) + "h"
	})
	if convErr != nil {
		return 0, convErr
	}
	return time.ParseDuration(expanded)
}

// boolFromEnv reads a boolean env override, returning fallback when the
// variable is unset or carries an unrecognized token. Accepted (case
// insensitive): true/1/yes/on and false/0/no/off.
func boolFromEnv(key string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "false", "0", "no", "off":
		return false
	case "true", "1", "yes", "on":
		return true
	}
	return fallback
}

func intFromEnv(key string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid integer %q: %w", key, value, err)
	}
	return n, nil
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func sleepWithContextOrWakeup(ctx context.Context, d time.Duration, wakeups <-chan struct{}) error {
	if wakeups == nil {
		return sleepWithContext(ctx, d)
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-wakeups:
		return nil
	case <-timer.C:
		return nil
	}
}
