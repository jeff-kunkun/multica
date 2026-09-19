package routing

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestBreakerTripsAfterConsecutiveFailures(t *testing.T) {
	b := NewBreaker()
	for i := 0; i < DefaultFailuresToTrip-1; i++ {
		b.Fail("ws", errors.New("timeout"))
		if open, _, _ := b.Open("ws"); open {
			t.Fatalf("opened after %d failures, want %d", i+1, DefaultFailuresToTrip)
		}
	}
	b.Fail("ws", errors.New("timeout"))
	if open, _, _ := b.Open("ws"); !open {
		t.Fatal("did not open after the failure budget was spent")
	}
}

func TestBreakerOpensImmediatelyOnFatalStatuses(t *testing.T) {
	// These do not get better by being retried on the next ticket, and
	// retrying a 429 is how a transient limit becomes a sustained one.
	for _, code := range []int{401, 402, 403, 429} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			b := NewBreaker()
			b.Fail("ws", fmt.Errorf("wrapped: %w", &FatalStatusError{Code: code}))
			open, _, reason := b.Open("ws")
			if !open {
				t.Fatalf("%d did not open the breaker", code)
			}
			if reason == "" {
				t.Error("opened without a reason to show in settings")
			}
		})
	}
}

func TestBreakerIsPerWorkspace(t *testing.T) {
	b := NewBreaker()
	b.Fail("ws-a", &FatalStatusError{Code: 401})
	if open, _, _ := b.Open("ws-b"); open {
		t.Error("one workspace's broken model stopped another's")
	}
}

func TestBreakerClosesAfterCooldownWithAFullBudget(t *testing.T) {
	b := NewBreaker()
	now := time.Now()
	b.now = func() time.Time { return now }
	b.Fail("ws", &FatalStatusError{Code: 403})
	if open, _, _ := b.Open("ws"); !open {
		t.Fatal("expected open")
	}
	now = now.Add(DefaultCooldown + time.Second)
	if open, _, _ := b.Open("ws"); open {
		t.Fatal("still open past the cooldown")
	}
	// The next probe gets the whole budget back rather than tripping on its
	// first stumble.
	b.Fail("ws", errors.New("timeout"))
	if open, _, _ := b.Open("ws"); open {
		t.Error("re-opened on a single failure after cooling down")
	}
}

func TestSuccessClearsTheFailureRun(t *testing.T) {
	b := NewBreaker()
	b.Fail("ws", errors.New("timeout"))
	b.Fail("ws", errors.New("timeout"))
	b.Succeed("ws")
	b.Fail("ws", errors.New("timeout"))
	if open, _, _ := b.Open("ws"); open {
		t.Error("counted failures across a success; the run must be consecutive")
	}
}

func TestNilBreakerIsInert(t *testing.T) {
	var b *Breaker
	b.Fail("ws", errors.New("x"))
	b.Succeed("ws")
	if open, _, _ := b.Open("ws"); open {
		t.Error("a nil breaker reported open")
	}
}
