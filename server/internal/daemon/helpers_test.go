package daemon

import (
	"testing"
	"time"
)

func TestParseFlexDuration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"5d", 5 * 24 * time.Hour},
		{"1d", 24 * time.Hour},
		{"1d12h", 36 * time.Hour},
		{"2d30m", 2*24*time.Hour + 30*time.Minute},
		{"0.5d", 12 * time.Hour},
		{"1.5d", 36 * time.Hour},
		{".5d", 12 * time.Hour},
		{"120h", 120 * time.Hour},
		{"24h", 24 * time.Hour},
		{"30m", 30 * time.Minute},
	}
	for _, tc := range cases {
		got, err := parseFlexDuration(tc.in)
		if err != nil {
			t.Errorf("parseFlexDuration(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseFlexDuration(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseFlexDuration_Invalid(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		"",
		"xyz",
		"5days",
		"abc5d",
		// Overflow: 30 digits is well past int64/float64 safe range; must error
		// rather than silently produce 0h.
		"999999999999999999999999999999d",
	} {
		if _, err := parseFlexDuration(in); err == nil {
			t.Errorf("parseFlexDuration(%q) expected error, got nil", in)
		}
	}
}

func TestBytesFromEnv(t *testing.T) {
	const key = "MULTICA_TEST_BYTE_SIZE"
	for _, tc := range []struct {
		value string
		want  int64
	}{
		{"", 7},
		{"0", 0},
		{"4096", 4096},
		{"20GiB", 20 << 30},
		{"20gb", 20 << 30}, // a cache ceiling is never quoted in powers of ten
		{"512mb", 512 << 20},
		{"1.5g", 1536 << 20},
		{"2T", 2 << 40},
	} {
		t.Run("value="+tc.value, func(t *testing.T) {
			t.Setenv(key, tc.value)
			got, err := bytesFromEnv(key, 7)
			if err != nil {
				t.Fatalf("bytesFromEnv(%q): %v", tc.value, err)
			}
			if got != tc.want {
				t.Errorf("bytesFromEnv(%q) = %d, want %d", tc.value, got, tc.want)
			}
		})
	}

	for _, bad := range []string{"-1", "lots", "12 gigs", "-3GiB"} {
		t.Run("rejects="+bad, func(t *testing.T) {
			t.Setenv(key, bad)
			if _, err := bytesFromEnv(key, 7); err == nil {
				t.Errorf("bytesFromEnv(%q) accepted an invalid size", bad)
			}
		})
	}
}
