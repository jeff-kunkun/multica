package handler

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	signupTOTPSecretEnv = "MULTICA_SIGNUP_TOTP_SECRET"
	signupTOTPDigits    = 6
	signupTOTPPeriod    = int64(30)
	signupTOTPWindow    = int64(1)
)

var (
	signupTOTPMisconfigOnce sync.Once
	signupTOTPReplayMu      sync.Mutex
	signupTOTPLastStep      int64
)

func signupTOTPSecret() (secret []byte, required bool) {
	raw := strings.TrimSpace(os.Getenv(signupTOTPSecretEnv))
	if raw == "" {
		return nil, false
	}
	decoded, err := decodeTOTPSecret(raw)
	if err != nil || len(decoded) == 0 {
		signupTOTPMisconfigOnce.Do(func() {
			slog.Warn("MULTICA_SIGNUP_TOTP_SECRET is set but is not valid base32; signup 2FA rejects all attempts")
		})
		return nil, true
	}
	return decoded, true
}

func decodeTOTPSecret(raw string) ([]byte, error) {
	s := strings.ToUpper(strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return r
	}, strings.TrimSpace(raw)))
	s = strings.TrimRight(s, "=")
	return base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(s)
}

func totpCodeAt(secret []byte, unix int64) string {
	if unix < 0 {
		unix = 0
	}
	return hotp(secret, uint64(unix/signupTOTPPeriod), signupTOTPDigits)
}

func hotp(secret []byte, counter uint64, digits int) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, secret)
	_, _ = mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	bin := int(sum[offset]&0x7f)<<24 |
		int(sum[offset+1])<<16 |
		int(sum[offset+2])<<8 |
		int(sum[offset+3])
	mod := 1
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, bin%mod)
}

func verifySignupTOTP(secret []byte, code string, now time.Time) (step int64, ok bool) {
	if len(secret) == 0 {
		return 0, false
	}
	code = strings.TrimSpace(code)
	if len(code) != signupTOTPDigits {
		return 0, false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return 0, false
		}
	}

	current := now.Unix() / signupTOTPPeriod
	var matched int64
	found := false
	for delta := -signupTOTPWindow; delta <= signupTOTPWindow; delta++ {
		candidate := current + delta
		if candidate < 0 {
			continue
		}
		expected := hotp(secret, uint64(candidate), signupTOTPDigits)
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			if !found || absInt64(delta) < absInt64(matched-current) {
				matched = candidate
				found = true
			}
		}
	}
	return matched, found
}

func consumeSignupTOTP(secret []byte, code string, now time.Time) bool {
	step, ok := verifySignupTOTP(secret, code, now)
	if !ok {
		return false
	}
	signupTOTPReplayMu.Lock()
	defer signupTOTPReplayMu.Unlock()
	if step <= signupTOTPLastStep {
		return false
	}
	signupTOTPLastStep = step
	return true
}

func resetSignupTOTPReplayForTest() {
	signupTOTPReplayMu.Lock()
	signupTOTPLastStep = 0
	signupTOTPReplayMu.Unlock()
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
