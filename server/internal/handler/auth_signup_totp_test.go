package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// RFC 6238 Appendix B SHA-1 secret, as base32 (the encoding TOTP apps use).
const rfc6238TOTPSecretBase32 = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

func rfc6238TOTPSecret(t *testing.T) []byte {
	t.Helper()
	secret, err := decodeTOTPSecret(rfc6238TOTPSecretBase32)
	if err != nil {
		t.Fatalf("decode RFC 6238 secret: %v", err)
	}
	return secret
}

func postPasswordSignupTOTP(username, password, email, totp string) *httptest.ResponseRecorder {
	body := map[string]string{
		"username": username,
		"password": password,
		"email":    email,
	}
	if totp != "" {
		body["totp"] = totp
	}
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest(http.MethodPost, "/auth/signup", &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	testHandler.PasswordSignup(w, req)
	return w
}

func assertSignupUserAbsent(t *testing.T, email string) {
	t.Helper()
	_, err := testHandler.Queries.GetUserByEmail(context.Background(), email)
	if err == nil {
		t.Fatalf("user %q must not have been created", email)
	}
	if !isNotFound(err) {
		t.Fatalf("lookup %q: %v", email, err)
	}
}

func cleanupSignupTOTPUsedSteps(t *testing.T, secret []byte) {
	t.Helper()
	if testPool == nil {
		return
	}
	fp := signupTOTPFingerprint(secret)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `DELETE FROM signup_totp_used_step WHERE secret_fingerprint = $1`, fp); err != nil {
		t.Fatalf("clear signup_totp_used_step: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM signup_totp_used_step WHERE secret_fingerprint = $1`, fp)
	})
}

func TestTOTPRFC6238SHA1SixDigits(t *testing.T) {
	secret := rfc6238TOTPSecret(t)
	// RFC 6238 Appendix B: T=59 → 8-digit SHA-1 94287082, so 6 digits 287082.
	if got := totpCodeAt(secret, 59); got != "287082" {
		t.Fatalf("TOTP at unix 59: got %q, want 287082", got)
	}
	if _, ok := verifySignupTOTP(secret, "287082", time.Unix(59, 0)); !ok {
		t.Fatal("RFC vector at T=59 should verify with ±1 step window")
	}
	if _, ok := verifySignupTOTP(secret, "000000", time.Unix(59, 0)); ok {
		t.Fatal("wrong code must not verify")
	}
}

func TestGetConfigExposesSignupTotpRequired(t *testing.T) {
	t.Setenv(signupTOTPSecretEnv, "")
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	w := httptest.NewRecorder()
	testHandler.GetConfig(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetConfig: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var off AppConfig
	if err := json.Unmarshal(w.Body.Bytes(), &off); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if off.SignupTotpRequired {
		t.Fatal("signup_totp_required: want omitted/false by default")
	}

	t.Setenv(signupTOTPSecretEnv, rfc6238TOTPSecretBase32)
	w = httptest.NewRecorder()
	testHandler.GetConfig(w, req)
	var on AppConfig
	if err := json.Unmarshal(w.Body.Bytes(), &on); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if !on.SignupTotpRequired {
		t.Fatal("signup_totp_required: want true when the secret env is set")
	}
}

func TestPasswordSignupTotpUnconfiguredBackwardCompatible(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	const (
		email    = "signup-totp-compat@multica.ai"
		username = "totpcompat"
		password = "correct-horse-battery"
	)
	setPasswordAuthEnv(t, "kun", "s3cret-bootstrap", "password-auth-bootstrap@multica.ai")
	t.Setenv(signupTOTPSecretEnv, "")
	cleanupPasswordSignupUser(t, email)

	w := postPasswordSignup(username, password, email)
	if w.Code != http.StatusOK {
		t.Fatalf("PasswordSignup without TOTP env: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPasswordSignupTotpMissingRejected(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	const email = "signup-totp-missing@multica.ai"
	setPasswordAuthEnv(t, "kun", "s3cret-bootstrap", "password-auth-bootstrap@multica.ai")
	t.Setenv(signupTOTPSecretEnv, rfc6238TOTPSecretBase32)
	cleanupSignupTOTPUsedSteps(t, rfc6238TOTPSecret(t))
	cleanupPasswordSignupUser(t, email)

	w := postPasswordSignup("totpmiss", "correct-horse", email)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing TOTP: expected 401, got %d: %s", w.Code, w.Body.String())
	}
	assertSignupUserAbsent(t, email)
}

func TestPasswordSignupTotpInvalidRejected(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	const email = "signup-totp-invalid@multica.ai"
	setPasswordAuthEnv(t, "kun", "s3cret-bootstrap", "password-auth-bootstrap@multica.ai")
	t.Setenv(signupTOTPSecretEnv, rfc6238TOTPSecretBase32)
	cleanupSignupTOTPUsedSteps(t, rfc6238TOTPSecret(t))
	cleanupPasswordSignupUser(t, email)

	w := postPasswordSignupTOTP("totpbad", "correct-horse", email, "000000")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("invalid TOTP: expected 401, got %d: %s", w.Code, w.Body.String())
	}
	assertSignupUserAbsent(t, email)
}

func TestPasswordSignupTotpSuccess(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	const (
		email    = "signup-totp-ok@multica.ai"
		username = "totpokuser"
		password = "correct-horse-battery"
	)
	setPasswordAuthEnv(t, "kun", "s3cret-bootstrap", "password-auth-bootstrap@multica.ai")
	t.Setenv(signupTOTPSecretEnv, rfc6238TOTPSecretBase32)
	secret := rfc6238TOTPSecret(t)
	cleanupSignupTOTPUsedSteps(t, secret)
	cleanupPasswordSignupUser(t, email)

	code := totpCodeAt(secret, time.Now().Unix())
	w := postPasswordSignupTOTP(username, password, email, code)
	if w.Code != http.StatusOK {
		t.Fatalf("valid TOTP: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp LoginResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode signup response: %v", err)
	}
	if resp.Token == "" || resp.User.Email != email {
		t.Fatalf("unexpected signup response: %+v", resp)
	}
}

func TestPasswordSignupTotpReplayRejected(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	const (
		email1    = "signup-totp-replay1@multica.ai"
		email2    = "signup-totp-replay2@multica.ai"
		username1 = "totprep1"
		username2 = "totprep2"
		password  = "correct-horse-battery"
	)
	setPasswordAuthEnv(t, "kun", "s3cret-bootstrap", "password-auth-bootstrap@multica.ai")
	t.Setenv(signupTOTPSecretEnv, rfc6238TOTPSecretBase32)
	secret := rfc6238TOTPSecret(t)
	cleanupSignupTOTPUsedSteps(t, secret)
	cleanupPasswordSignupUser(t, email1)
	cleanupPasswordSignupUser(t, email2)

	now := time.Now()
	code := totpCodeAt(secret, now.Unix())
	step, ok := verifySignupTOTP(secret, code, now)
	if !ok {
		t.Fatal("current TOTP must verify before replay test")
	}

	w := postPasswordSignupTOTP(username1, password, email1, code)
	if w.Code != http.StatusOK {
		t.Fatalf("first TOTP use: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var stored int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM signup_totp_used_step WHERE secret_fingerprint = $1 AND step = $2`,
		signupTOTPFingerprint(secret), step,
	).Scan(&stored); err != nil {
		t.Fatalf("lookup consumed step: %v", err)
	}
	if stored != 1 {
		t.Fatalf("consumed step rows: got %d, want 1", stored)
	}

	w = postPasswordSignupTOTP(username2, password, email2, code)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("replayed TOTP: expected 401, got %d: %s", w.Code, w.Body.String())
	}
	assertSignupUserAbsent(t, email2)
}

func TestPasswordSignupTotpReplayRejectedFromSharedStore(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	const (
		email    = "signup-totp-shared@multica.ai"
		username = "totpshared"
		password = "correct-horse-battery"
	)
	setPasswordAuthEnv(t, "kun", "s3cret-bootstrap", "password-auth-bootstrap@multica.ai")
	t.Setenv(signupTOTPSecretEnv, rfc6238TOTPSecretBase32)
	secret := rfc6238TOTPSecret(t)
	cleanupSignupTOTPUsedSteps(t, secret)
	cleanupPasswordSignupUser(t, email)

	now := time.Now()
	code := totpCodeAt(secret, now.Unix())
	step, ok := verifySignupTOTP(secret, code, now)
	if !ok {
		t.Fatal("current TOTP must verify")
	}

	// Another process/replica already persisted this (secret, step). This
	// handler has no in-memory last-step, so rejection must come from the
	// shared unique constraint.
	if err := testHandler.Queries.InsertSignupTOTPUsedStep(context.Background(), db.InsertSignupTOTPUsedStepParams{
		SecretFingerprint: signupTOTPFingerprint(secret),
		Step:              step,
	}); err != nil {
		t.Fatalf("seed consumed step: %v", err)
	}

	w := postPasswordSignupTOTP(username, password, email, code)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("TOTP already consumed in shared store: expected 401, got %d: %s", w.Code, w.Body.String())
	}
	assertSignupUserAbsent(t, email)
}

func TestSignupTOTPUsedStepUniqueConstraint(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	secret := rfc6238TOTPSecret(t)
	cleanupSignupTOTPUsedSteps(t, secret)
	params := db.InsertSignupTOTPUsedStepParams{
		SecretFingerprint: signupTOTPFingerprint(secret),
		Step:              42,
	}
	if err := testHandler.Queries.InsertSignupTOTPUsedStep(context.Background(), params); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	err := testHandler.Queries.InsertSignupTOTPUsedStep(context.Background(), params)
	if !isUniqueViolation(err) {
		t.Fatalf("second insert: want unique violation, got %v", err)
	}
}
