package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func setPasswordAuthEnv(t *testing.T, username, password, email string) {
	t.Helper()
	t.Setenv(passwordAuthEnabledEnv, "true")
	t.Setenv(passwordAuthUsernameEnv, username)
	t.Setenv(passwordAuthPasswordEnv, password)
	t.Setenv(passwordAuthEmailEnv, email)
}

func postPasswordLogin(username, password string) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{
		"username": username,
		"password": password,
	})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	testHandler.PasswordLogin(w, req)
	return w
}

func TestPasswordAuthConfiguredRequiresAllFields(t *testing.T) {
	t.Setenv(passwordAuthEnabledEnv, "")
	t.Setenv(passwordAuthUsernameEnv, "")
	t.Setenv(passwordAuthPasswordEnv, "")
	t.Setenv(passwordAuthEmailEnv, "")
	if _, ok := passwordAuthConfigured(); ok {
		t.Fatal("password auth must stay off by default")
	}

	t.Setenv(passwordAuthEnabledEnv, "true")
	t.Setenv(passwordAuthUsernameEnv, "kun")
	t.Setenv(passwordAuthPasswordEnv, "")
	t.Setenv(passwordAuthEmailEnv, "kun@example.com")
	if _, ok := passwordAuthConfigured(); ok {
		t.Fatal("password auth must stay off when the password is missing")
	}

	setPasswordAuthEnv(t, "kun", "s3cret", "kun@example.com")
	creds, ok := passwordAuthConfigured()
	if !ok {
		t.Fatal("password auth should be on when all fields are set")
	}
	if creds.username != "kun" || creds.email != "kun@example.com" {
		t.Fatalf("unexpected creds: %+v", creds)
	}
}

func TestPasswordLoginDisabled(t *testing.T) {
	t.Setenv(passwordAuthEnabledEnv, "")
	w := postPasswordLogin("kun", "s3cret")
	if w.Code != http.StatusNotFound {
		t.Fatalf("PasswordLogin (disabled): expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPasswordLoginWrongPassword(t *testing.T) {
	const email = "password-wrong-test@multica.ai"
	setPasswordAuthEnv(t, "kun", "correct-horse", email)

	w := postPasswordLogin("kun", "wrong-password")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("PasswordLogin (wrong password): expected 401, got %d: %s", w.Code, w.Body.String())
	}

	w = postPasswordLogin("other", "correct-horse")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("PasswordLogin (wrong username): expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPasswordLoginSuccessIssuesJWT(t *testing.T) {
	const (
		email    = "password-login-test@multica.ai"
		username = "kun"
		password = "correct-horse-battery"
	)
	setPasswordAuthEnv(t, username, password, email)
	ctx := context.Background()

	t.Cleanup(func() {
		user, err := testHandler.Queries.GetUserByEmail(ctx, email)
		if err == nil {
			workspaces, listErr := testHandler.Queries.ListWorkspaces(ctx, user.ID)
			if listErr == nil {
				for _, workspace := range workspaces {
					_ = testHandler.Queries.DeleteWorkspace(ctx, workspace.ID)
				}
			}
		}
		testPool.Exec(ctx, `DELETE FROM "user" WHERE email = $1`, email)
	})

	w := postPasswordLogin(username, password)
	if w.Code != http.StatusOK {
		t.Fatalf("PasswordLogin: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp LoginResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if resp.Token == "" {
		t.Fatal("PasswordLogin: expected non-empty token")
	}
	if resp.User.Email != email {
		t.Fatalf("PasswordLogin: expected email %q, got %q", email, resp.User.Email)
	}

	foundAuthCookie := false
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "multica_auth" && cookie.Value != "" {
			foundAuthCookie = true
			break
		}
	}
	if !foundAuthCookie {
		t.Fatal("PasswordLogin: expected HttpOnly multica_auth cookie on success")
	}

	w = postPasswordLogin(username, password)
	if w.Code != http.StatusOK {
		t.Fatalf("PasswordLogin (existing user): expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSendCodeRejectedWhenPasswordAuthEnabled(t *testing.T) {
	setPasswordAuthEnv(t, "kun", "s3cret", "password-send-blocked@multica.ai")

	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{"email": "password-send-blocked@multica.ai"})
	req := httptest.NewRequest(http.MethodPost, "/auth/send-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	testHandler.SendCode(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("SendCode with password auth: expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetConfigExposesPasswordAuth(t *testing.T) {
	t.Setenv(passwordAuthEnabledEnv, "")
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
	if off.PasswordAuth {
		t.Fatal("password_auth: want omitted/false by default")
	}

	setPasswordAuthEnv(t, "kun", "s3cret", "kun@example.com")
	w = httptest.NewRecorder()
	testHandler.GetConfig(w, req)
	var on AppConfig
	if err := json.Unmarshal(w.Body.Bytes(), &on); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if !on.PasswordAuth {
		t.Fatal("password_auth: want true when fully configured")
	}
}

func postPasswordSignup(username, password, email string) *httptest.ResponseRecorder {
	return postPasswordSignupJSON(map[string]string{
		"username": username,
		"password": password,
		"email":    email,
	})
}

func postPasswordSignupJSON(body map[string]string) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest(http.MethodPost, "/auth/signup", &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	testHandler.PasswordSignup(w, req)
	return w
}

func signupErrorMessage(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode error body: %v (%s)", err, w.Body.String())
	}
	return got["error"]
}

func cleanupPasswordSignupUser(t *testing.T, email string) {
	t.Helper()
	ctx := context.Background()
	t.Cleanup(func() {
		user, err := testHandler.Queries.GetUserByEmail(ctx, email)
		if err == nil {
			workspaces, listErr := testHandler.Queries.ListWorkspaces(ctx, user.ID)
			if listErr == nil {
				for _, workspace := range workspaces {
					_ = testHandler.Queries.DeleteWorkspace(ctx, workspace.ID)
				}
			}
		}
		testPool.Exec(ctx, `DELETE FROM "user" WHERE email = $1`, email)
	})
}

func TestPasswordSignupDisabled(t *testing.T) {
	t.Setenv(passwordAuthEnabledEnv, "")
	w := postPasswordSignup("newbie", "correct-horse", "signup-disabled@multica.ai")
	if w.Code != http.StatusNotFound {
		t.Fatalf("PasswordSignup (disabled): expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPasswordSignupRejectedWhenSignupDisabled(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	setPasswordAuthEnv(t, "kun", "s3cret", "kun@example.com")
	prev := testHandler.cfg
	testHandler.cfg = Config{AllowSignup: false}
	t.Cleanup(func() { testHandler.cfg = prev })

	w := postPasswordSignup("newbie", "correct-horse", "signup-prohibited@multica.ai")
	if w.Code != http.StatusForbidden {
		t.Fatalf("PasswordSignup (allow_signup=false): expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPasswordSignupValidation(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	setPasswordAuthEnv(t, "kun", "s3cret", "kun@example.com")

	w := postPasswordSignup("a", "correct-horse", "signup-bad-user@multica.ai")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("short username: expected 400, got %d: %s", w.Code, w.Body.String())
	}

	w = postPasswordSignup("newbie", "short", "signup-bad-pass@multica.ai")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("short password: expected 400, got %d: %s", w.Code, w.Body.String())
	}

	w = postPasswordSignup("newbie", "correct-horse", "not-an-email")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid email: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPasswordSignupSuccessAndLogin(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	const (
		email    = "password-signup-test@multica.ai"
		username = "signupdemo"
		password = "correct-horse-battery"
	)
	setPasswordAuthEnv(t, "kun", "s3cret-bootstrap", "password-auth-bootstrap@multica.ai")
	cleanupPasswordSignupUser(t, email)

	w := postPasswordSignup(username, password, email)
	if w.Code != http.StatusOK {
		t.Fatalf("PasswordSignup: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp LoginResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode signup response: %v", err)
	}
	if resp.Token == "" {
		t.Fatal("PasswordSignup: expected non-empty token")
	}
	if resp.User.Email != email {
		t.Fatalf("PasswordSignup: expected email %q, got %q", email, resp.User.Email)
	}
	if resp.User.Name != username {
		t.Fatalf("PasswordSignup: expected name %q, got %q", username, resp.User.Name)
	}

	foundAuthCookie := false
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "multica_auth" && cookie.Value != "" {
			foundAuthCookie = true
			break
		}
	}
	if !foundAuthCookie {
		t.Fatal("PasswordSignup: expected HttpOnly multica_auth cookie on success")
	}

	w = postPasswordLogin(username, password)
	if w.Code != http.StatusOK {
		t.Fatalf("PasswordLogin after signup: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = postPasswordLogin(username, "wrong-password")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("PasswordLogin wrong password after signup: expected 401, got %d: %s", w.Code, w.Body.String())
	}

	w = postPasswordSignup(username, password, "password-signup-dup@multica.ai")
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate username: expected 409, got %d: %s", w.Code, w.Body.String())
	}

	w = postPasswordSignup("otherdemo", password, email)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate email: expected 409, got %d: %s", w.Code, w.Body.String())
	}

	w = postPasswordSignup("kun", password, "password-signup-env-user@multica.ai")
	if w.Code != http.StatusConflict {
		t.Fatalf("env username: expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestNormalizeSignupEmail(t *testing.T) {
	got, ok := normalizeSignupEmail("  New.User@Example.COM ")
	if !ok || got != "new.user@example.com" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
	if _, ok := normalizeSignupEmail("nodot@localhost"); ok {
		t.Fatal("domain without a dot must be rejected")
	}
	if _, ok := normalizeSignupEmail("Name <new.user@example.com>"); ok {
		t.Fatal("display-name addresses must be rejected")
	}
	if _, ok := normalizeSignupEmail(""); ok {
		t.Fatal("empty email must be rejected by normalizeSignupEmail")
	}
}

func TestResolveSignupEmail(t *testing.T) {
	got, placeholder, ok := resolveSignupEmail("", "alice")
	if !ok || !placeholder || got != "alice@signup.invalid" {
		t.Fatalf("blank email: got %q placeholder=%v ok=%v", got, placeholder, ok)
	}
	got, placeholder, ok = resolveSignupEmail("   ", "alice")
	if !ok || !placeholder || got != "alice@signup.invalid" {
		t.Fatalf("whitespace email: got %q placeholder=%v ok=%v", got, placeholder, ok)
	}
	got, placeholder, ok = resolveSignupEmail("Alice@Example.COM", "alice")
	if !ok || placeholder || got != "alice@example.com" {
		t.Fatalf("real email: got %q placeholder=%v ok=%v", got, placeholder, ok)
	}
	if _, _, ok := resolveSignupEmail("abc", "alice"); ok {
		t.Fatal("invalid non-empty email must still be rejected")
	}
}

func TestPasswordSignupOptionalEmail(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	setPasswordAuthEnv(t, "kun", "s3cret-bootstrap", "password-auth-bootstrap@multica.ai")
	const password = "correct-horse-battery"

	t.Run("omitted_email_field", func(t *testing.T) {
		const username = "noemailomit"
		cleanupPasswordSignupUser(t, placeholderSignupEmail(username))
		w := postPasswordSignupJSON(map[string]string{
			"username": username,
			"password": password,
		})
		if w.Code != http.StatusOK {
			t.Fatalf("omitted email: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp LoginResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode signup response: %v", err)
		}
		if resp.Token == "" {
			t.Fatal("expected non-empty token")
		}
		if resp.User.Email != placeholderSignupEmail(username) {
			t.Fatalf("expected placeholder email %q, got %q", placeholderSignupEmail(username), resp.User.Email)
		}
	})

	t.Run("empty_email", func(t *testing.T) {
		const username = "noemailempty"
		cleanupPasswordSignupUser(t, placeholderSignupEmail(username))
		w := postPasswordSignup(username, password, "")
		if w.Code != http.StatusOK {
			t.Fatalf("empty email: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp LoginResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode signup response: %v", err)
		}
		if resp.User.Email != placeholderSignupEmail(username) {
			t.Fatalf("expected placeholder email %q, got %q", placeholderSignupEmail(username), resp.User.Email)
		}
	})

	t.Run("invalid_nonempty_email_still_400", func(t *testing.T) {
		w := postPasswordSignup("noemailbad", password, "abc")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid email: expected 400, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("duplicate_username_is_username_taken", func(t *testing.T) {
		const username = "noemaildup"
		cleanupPasswordSignupUser(t, placeholderSignupEmail(username))
		w := postPasswordSignup(username, password, "")
		if w.Code != http.StatusOK {
			t.Fatalf("first signup: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		w = postPasswordSignup(username, password, "")
		if w.Code != http.StatusConflict {
			t.Fatalf("duplicate username: expected 409, got %d: %s", w.Code, w.Body.String())
		}
		if got := signupErrorMessage(t, w); got != "username already taken" {
			t.Fatalf("duplicate username: expected %q, got %q", "username already taken", got)
		}
	})
}
