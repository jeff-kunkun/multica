package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/auth"
)

// optionalMiddleware returns OptionalAuth with nil queries (JWT-only tests).
func optionalMiddleware(next http.Handler) http.Handler {
	return OptionalAuth(nil, nil, nil, nil)(next)
}

// identityHeaders is the full set OptionalAuth treats as server-set only.
var identityHeaders = []string{"X-User-ID", "X-User-Email", "X-Actor-Source", "X-Agent-ID", "X-Task-ID"}

func TestOptionalAuth_NoCredentialContinuesAnonymous(t *testing.T) {
	var called bool
	var got *http.Request
	handler := optionalMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		got = r
	}))

	req := httptest.NewRequest("GET", "/api/attachments/x/preview", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Fatal("next handler must run when there is no credential")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("no credential must not write a rejection, got %d", w.Code)
	}
	for _, h := range identityHeaders {
		if v := got.Header.Get(h); v != "" {
			t.Fatalf("%s must stay empty without a credential, got %q", h, v)
		}
	}
}

func TestOptionalAuth_ForgedIdentityHeadersWithoutCredentialAreStripped(t *testing.T) {
	var got *http.Request
	handler := optionalMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r
	}))

	req := httptest.NewRequest("GET", "/api/attachments/x/preview", nil)
	// The pre-fix bug: the public route trusted these as real identity.
	for _, h := range identityHeaders {
		req.Header.Set(h, "forged-"+h)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected anonymous continuation, got %d", w.Code)
	}
	for _, h := range identityHeaders {
		if v := got.Header.Get(h); v != "" {
			t.Fatalf("forged %s must be stripped, got %q", h, v)
		}
	}
}

func TestOptionalAuth_InvalidTokenIsAnonymousAndDeadensSpoofing(t *testing.T) {
	var called bool
	var got *http.Request
	handler := optionalMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		got = r
	}))

	req := httptest.NewRequest("GET", "/api/attachments/x/preview", nil)
	req.Header.Set("Authorization", "Bearer not-a-valid-jwt")
	req.Header.Set("X-User-ID", "forged-user-id")
	req.Header.Set("X-User-Email", "forged@example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Fatal("invalid credential must fall through, not reject")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("Auth's rejection must be discarded, got %d: %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); body != "" {
		t.Fatalf("rejection body must not reach the client, got %q", body)
	}
	if v := got.Header.Get("X-User-ID"); v != "" {
		t.Fatalf("forged X-User-ID must not survive an invalid token, got %q", v)
	}
	if v := got.Header.Get("X-User-Email"); v != "" {
		t.Fatalf("forged X-User-Email must not survive an invalid token, got %q", v)
	}
}

func TestOptionalAuth_ExpiredTokenIsAnonymous(t *testing.T) {
	claims := validClaims()
	claims["exp"] = time.Now().Add(-time.Hour).Unix()
	token := generateToken(claims, auth.JWTSecret())

	var called bool
	var got *http.Request
	handler := optionalMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		got = r
	}))

	req := httptest.NewRequest("GET", "/api/attachments/x/preview", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-User-ID", "forged-user-id")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Fatal("expired credential must fall through, not reject")
	}
	if v := got.Header.Get("X-User-ID"); v != "" {
		t.Fatalf("forged X-User-ID must not survive an expired token, got %q", v)
	}
}

func TestOptionalAuth_ValidJWTStampsRealIdentityOverForgery(t *testing.T) {
	var got *http.Request
	handler := optionalMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r
	}))

	token := generateToken(validClaims(), auth.JWTSecret())
	req := httptest.NewRequest("GET", "/api/attachments/x/preview", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	// Client-supplied identity must be replaced, not merged.
	req.Header.Set("X-User-ID", "forged-user-id")
	req.Header.Set("X-User-Email", "forged@example.com")
	req.Header.Set("X-Actor-Source", "forged-actor")
	req.Header.Set("X-Agent-ID", "forged-agent")
	req.Header.Set("X-Task-ID", "forged-task")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got == nil {
		t.Fatal("valid credential must reach the next handler")
	}
	if v := got.Header.Get("X-User-ID"); v != "test-user-id" {
		t.Fatalf("X-User-ID must be the token subject, got %q", v)
	}
	if v := got.Header.Get("X-User-Email"); v != "test@multica.ai" {
		t.Fatalf("X-User-Email must be the token email, got %q", v)
	}
	// These three are mat_-token fields: never stamped for a JWT, so the
	// forgery must be gone rather than replaced.
	for _, h := range []string{"X-Actor-Source", "X-Agent-ID", "X-Task-ID"} {
		if v := got.Header.Get(h); v != "" {
			t.Fatalf("%s must not survive a JWT credential, got %q", h, v)
		}
	}
}

func TestOptionalAuth_UnknownAuthorizationSchemeIsAnonymous(t *testing.T) {
	var called bool
	var got *http.Request
	handler := optionalMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		got = r
	}))

	req := httptest.NewRequest("GET", "/api/attachments/x/preview", nil)
	req.Header.Set("Authorization", "Token some-token")
	req.Header.Set("X-User-ID", "forged-user-id")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Fatal("unusable credential must fall through")
	}
	if v := got.Header.Get("X-User-ID"); v != "" {
		t.Fatalf("forged X-User-ID must be stripped, got %q", v)
	}
}

func TestAuthProbeWriter_KeepsHeadersAndDropsOutput(t *testing.T) {
	p := &authProbeWriter{header: http.Header{}}
	p.Header().Set("Set-Cookie", "a=b")
	p.WriteHeader(http.StatusUnauthorized)
	if n, err := p.Write([]byte(`{"error":"missing authorization"}`)); err != nil || n != len(`{"error":"missing authorization"}`) {
		t.Fatalf("probe Write must absorb output, got n=%d err=%v", n, err)
	}
	if got := p.header.Get("Set-Cookie"); got != "a=b" {
		t.Fatalf("headers must be retained for commit-on-success, got %q", got)
	}
}
