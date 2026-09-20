package routing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/pkg/llm"
)

// newUpstream returns an LLMJudge wired to a real client pointed at a fake
// OpenAI-compatible endpoint that always answers with status `code`.
func newUpstream(t *testing.T, code int) LLMJudge {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"error":{"message":"nope","type":"invalid_request_error"}}`))
	}))
	t.Cleanup(srv.Close)
	budget, err := llm.Retries(0)
	if err != nil {
		t.Fatalf("Retries(0): %v", err)
	}
	return LLMJudge{Gen: llm.New(llm.Config{APIKey: "test-key", BaseURL: srv.URL, MaxRetries: budget})}
}

// TestBreakerTripsOnRealUpstreamStatus is the end-to-end form of the
// 401/402/403/429 rule: a real rejection, through the real LLM client, through
// the real judge, must open the breaker on the FIRST failure.
//
// The unit tests for classifyFatal fed it errors this package constructed, and
// those passed while the production path was dead — the SDK carries the status
// as a struct field, which no interface can match (DENE-633 review, F1). This
// test is the one that would have caught it, so it deliberately owns no error
// of its own: everything below comes from a real HTTP response.
func TestBreakerTripsOnRealUpstreamStatus(t *testing.T) {
	for _, code := range []int{401, 402, 403, 429} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			judge := newUpstream(t, code)
			b := NewBreaker()

			_, err := judge.Assign(context.Background(), "gpt-5.6-luna", JudgeState{Title: "t", Status: "todo"})
			if err == nil {
				t.Fatal("Assign succeeded against an error-only upstream")
			}
			b.Fail("ws", err)

			open, _, reason := b.Open("ws")
			if !open {
				t.Fatalf("a %d did not open the breaker on the first failure; "+
					"routing will keep dialling a dead model once per ticket", code)
			}
			if reason == "" {
				t.Fatal("the breaker opened with no reason; the settings section has nothing to show")
			}
		})
	}
}

// TestBreakerCountsOrdinaryUpstreamFailures is the other half: a 500 is not
// fatal, so it must take the full failure budget rather than cooling down on
// the first stumble.
func TestBreakerCountsOrdinaryUpstreamFailures(t *testing.T) {
	judge := newUpstream(t, http.StatusInternalServerError)
	b := NewBreaker()
	for i := 1; i <= DefaultFailuresToTrip; i++ {
		_, err := judge.Assign(context.Background(), "gpt-5.6-luna", JudgeState{Title: "t", Status: "todo"})
		if err == nil {
			t.Fatal("Assign succeeded against an error-only upstream")
		}
		b.Fail("ws", err)
		open, _, _ := b.Open("ws")
		if want := i >= DefaultFailuresToTrip; open != want {
			t.Fatalf("after %d ordinary failures open = %v, want %v", i, open, want)
		}
	}
}

// TestUnconfiguredDeploymentTripsTheBreaker covers the deployment that never
// configured an internal LLM. Retrying that once per ticket is pure waste, and
// it is a settings-level fact — Route keeps it off the ticket entirely.
func TestUnconfiguredDeploymentTripsTheBreaker(t *testing.T) {
	judge := LLMJudge{Gen: llm.New(llm.Config{})}
	_, err := judge.Assign(context.Background(), "some-model", JudgeState{Title: "t", Status: "todo"})
	if err == nil {
		t.Fatal("Assign succeeded with no LLM configured")
	}
	b := NewBreaker()
	b.Fail("ws", err)
	if open, _, _ := b.Open("ws"); !open {
		t.Fatal("an unconfigured deployment did not open the breaker; every ticket would dial")
	}
}
