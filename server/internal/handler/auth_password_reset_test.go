package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

func postPasswordReset(username, totp, newPassword string) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(PasswordResetRequest{Username: username, Totp: totp, NewPassword: newPassword})
	req := httptest.NewRequest(http.MethodPost, "/auth/reset-password", &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	testHandler.PasswordReset(w, req)
	return w
}

// passwordUser inserts a user with a password credential and returns the user
// id and the (lowercase) username.
func passwordUser(t *testing.T, password string) (string, string) {
	t.Helper()
	username := "reset" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	userID := dbfx.User(t, username, username+"@signup.invalid")
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := testPool.Exec(t.Context(),
		`INSERT INTO user_password_credential (user_id, username, password_hash) VALUES ($1, $2, $3)`,
		parseUUID(userID), username, string(hash),
	); err != nil {
		t.Fatalf("insert credential: %v", err)
	}
	return userID, username
}

func enablePasswordReset(t *testing.T) []byte {
	t.Helper()
	setPasswordAuthEnv(t, "kun", "s3cret-bootstrap", "password-auth-bootstrap@multica.ai")
	t.Setenv(signupTOTPSecretEnv, rfc6238TOTPSecretBase32)
	secret := rfc6238TOTPSecret(t)
	cleanupSignupTOTPUsedSteps(t, secret)
	return secret
}

func TestPasswordResetSelfServiceFlow(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	secret := enablePasswordReset(t)
	_, username := passwordUser(t, "old-password-1")

	code := totpCodeAt(secret, time.Now().Unix())
	if w := postPasswordReset(strings.ToUpper(username), code, "new-password-2"); w.Code != http.StatusNoContent {
		t.Fatalf("reset: expected 204, got %d: %s", w.Code, w.Body.String())
	}
	if w := postPasswordLogin(username, "old-password-1"); w.Code != http.StatusUnauthorized {
		t.Fatalf("old password: expected 401, got %d", w.Code)
	}
	if w := postPasswordLogin(username, "new-password-2"); w.Code != http.StatusOK {
		t.Fatalf("new password: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// The same code cannot be used twice.
	if w := postPasswordReset(username, code, "third-password-3"); w.Code != http.StatusUnauthorized {
		t.Fatalf("replayed code: expected 401, got %d", w.Code)
	}
	if w := postPasswordLogin(username, "new-password-2"); w.Code != http.StatusOK {
		t.Fatalf("replay must not change the password, login got %d", w.Code)
	}
}

func TestPasswordResetRejectionsLookAlike(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	secret := enablePasswordReset(t)
	_, username := passwordUser(t, "old-password-1")
	good := totpCodeAt(secret, time.Now().Unix())
	bad := "000000"
	if bad == good {
		bad = "111111"
	}

	cases := map[string]*httptest.ResponseRecorder{
		"wrong code":       postPasswordReset(username, bad, "new-password-2"),
		"unknown username": postPasswordReset("nobody-"+uuid.NewString()[:8], good, "new-password-2"),
		"bootstrap user":   postPasswordReset("kun", good, "new-password-2"),
	}
	for name, w := range cases {
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: expected 401, got %d: %s", name, w.Code, w.Body.String())
		}
		if msg := signupErrorMessage(t, w); msg != passwordResetRejectedMessage {
			t.Fatalf("%s: message %q leaks which part was wrong", name, msg)
		}
	}
	if w := postPasswordLogin(username, "old-password-1"); w.Code != http.StatusOK {
		t.Fatalf("rejected resets must not change the password, login got %d", w.Code)
	}

	if w := postPasswordReset(username, good, "short"); w.Code != http.StatusBadRequest {
		t.Fatalf("short password: expected 400, got %d", w.Code)
	}
}

func TestPasswordResetDisabledWithoutTeam2FA(t *testing.T) {
	setPasswordAuthEnv(t, "kun", "s3cret-bootstrap", "password-auth-bootstrap@multica.ai")
	t.Setenv(signupTOTPSecretEnv, "")
	if w := postPasswordReset("anyone", "123456", "new-password-2"); w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 without team 2FA, got %d", w.Code)
	}
}
