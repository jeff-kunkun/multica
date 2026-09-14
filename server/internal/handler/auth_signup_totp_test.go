package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
	resetSignupTOTPReplayForTest()
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
	resetSignupTOTPReplayForTest()
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
	resetSignupTOTPReplayForTest()
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
	resetSignupTOTPReplayForTest()
	cleanupPasswordSignupUser(t, email)

	secret := rfc6238TOTPSecret(t)
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
	resetSignupTOTPReplayForTest()
	cleanupPasswordSignupUser(t, email1)
	cleanupPasswordSignupUser(t, email2)

	secret := rfc6238TOTPSecret(t)
	code := totpCodeAt(secret, time.Now().Unix())

	w := postPasswordSignupTOTP(username1, password, email1, code)
	if w.Code != http.StatusOK {
		t.Fatalf("first TOTP use: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = postPasswordSignupTOTP(username2, password, email2, code)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("replayed TOTP: expected 401, got %d: %s", w.Code, w.Body.String())
	}
	assertSignupUserAbsent(t, email2)
}
