package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	openai "github.com/openai/openai-go/v3"
)

// TestUpstreamStatusSurvivesAsBehaviour drives a REAL upstream rejection
// through the REAL client and asserts the status is readable without touching
// the SDK.
//
// Constructing the error by hand would prove nothing here: the bug this test
// exists for (DENE-633 review, F1) was precisely that a hand-built error
// satisfied a caller's interface while the SDK's own error — which carries the
// status as a struct field, not a method — never could. The upstream has to be
// real for the assertion to mean anything.
func TestUpstreamStatusSurvivesAsBehaviour(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusPaymentRequired,
		http.StatusForbidden, http.StatusTooManyRequests} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(code)
				_, _ = w.Write([]byte(`{"error":{"message":"nope","type":"invalid_request_error"}}`))
			}))
			defer srv.Close()

			// No retries: a 429 would otherwise be retried by the SDK and the
			// test would pay the backoff for a result it already has.
			budget, err := Retries(0)
			if err != nil {
				t.Fatalf("Retries(0): %v", err)
			}
			c := New(Config{APIKey: "test-key", BaseURL: srv.URL, MaxRetries: budget})

			_, err = c.GenerateJSON(context.Background(), "gpt-5.6-luna", "Answer in JSON.", "{}", 0, 16)
			if err == nil {
				t.Fatal("GenerateJSON succeeded against an error-only upstream")
			}

			// The contract other packages branch on: a method, reachable
			// through errors.As, with no SDK import on their side.
			var statusErr *StatusError
			if !errors.As(err, &statusErr) {
				t.Fatalf("a %d from upstream did not surface as *StatusError: %v", code, err)
			}
			if statusErr.Status() != code {
				t.Fatalf("Status() = %d, want %d", statusErr.Status(), code)
			}
			if got, ok := HTTPStatus(err); !ok || got != code {
				t.Fatalf("HTTPStatus() = (%d, %v), want (%d, true)", got, ok, code)
			}

			// Wrapping must not hide the SDK error from callers inside this
			// package that still inspect it (isUnsupportedParameter does).
			var apiErr *openai.Error
			if !errors.As(err, &apiErr) {
				t.Fatalf("wrapping hid the SDK error from errors.As: %v", err)
			}
		})
	}
}

// TestHTTPStatusIgnoresErrorsWithoutAStatus keeps a transport failure from
// being reported as some HTTP status nobody returned — a breaker that read a
// dial timeout as 401 would cool down for the wrong reason and say so to a
// person.
func TestHTTPStatusIgnoresErrorsWithoutAStatus(t *testing.T) {
	for _, err := range []error{nil, errors.New("dial tcp: connection refused"), ErrNotConfigured} {
		if code, ok := HTTPStatus(err); ok {
			t.Fatalf("HTTPStatus(%v) reported status %d; want no status", err, code)
		}
	}
}
