package routing

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/pkg/llm"
)

// TestHealthReportsTheFourStates pins what the settings section renders.
//
// This report is the only place a routing failure is visible to a person: the
// state table keeps tickets quiet on purpose, so a broken model that does not
// show up here shows up nowhere but the server log (DENE-633 review, F2).
func TestHealthReportsTheFourStates(t *testing.T) {
	t.Run("off", func(t *testing.T) {
		store := newFakeStore()
		store.settings = Settings{}
		rep, err := newRouter(store, &fakeJudge{}).Health(context.Background(), "ws")
		if err != nil {
			t.Fatalf("Health: %v", err)
		}
		if rep.State != StateOff || rep.Usable {
			t.Fatalf("got %+v, want state=off usable=false", rep)
		}
	})

	t.Run("incomplete", func(t *testing.T) {
		store := newFakeStore()
		store.settings = Settings{Enabled: true}
		rep, _ := newRouter(store, &fakeJudge{}).Health(context.Background(), "ws")
		if rep.State != StateIncomplete || rep.Usable {
			t.Fatalf("got %+v, want state=incomplete usable=false", rep)
		}
	})

	t.Run("enabled", func(t *testing.T) {
		store := newFakeStore()
		r := newRouter(store, &fakeJudge{})
		r.Breaker.Succeed("ws")
		rep, _ := r.Health(context.Background(), "ws")
		if rep.State != StateEnabled || !rep.Usable {
			t.Fatalf("got %+v, want state=enabled usable=true", rep)
		}
		if rep.LastSuccessAt == 0 {
			t.Fatal("a successful call left no self-check timestamp; the chip has nothing to date")
		}
		if rep.Reason != "" {
			t.Fatalf("a healthy report carried a reason %q; that reads as a current fault", rep.Reason)
		}
	})

	t.Run("ineffective while the breaker is open", func(t *testing.T) {
		store := newFakeStore()
		r := newRouter(store, &fakeJudge{})
		r.Breaker.Fail("ws", &FatalStatusError{Code: http.StatusUnauthorized})
		rep, _ := r.Health(context.Background(), "ws")
		if rep.State != StateIneffective || rep.Usable {
			t.Fatalf("got %+v, want state=ineffective usable=false", rep)
		}
		if rep.Reason == "" {
			t.Fatal("ineffective with no reason: the settings section can say something is wrong but not what")
		}
		if rep.RetryAfterSeconds <= 0 {
			t.Fatalf("RetryAfterSeconds = %d, want the remaining cooldown", rep.RetryAfterSeconds)
		}
		if rep.LastFailureAt == 0 {
			t.Fatal("no failure timestamp on an ineffective report")
		}
	})
}

// TestHealthNeverDialsTheModel keeps an open settings tab from becoming an
// outbound request loop — and from defeating the cooldown it is reporting on.
func TestHealthNeverDialsTheModel(t *testing.T) {
	judge := &fakeJudge{verdict: confidentVerdict()}
	r := newRouter(newFakeStore(), judge)
	for i := 0; i < 5; i++ {
		if _, err := r.Health(context.Background(), "ws"); err != nil {
			t.Fatalf("Health: %v", err)
		}
	}
	if judge.callCount() != 0 {
		t.Fatalf("Health made %d model calls; it must only read", judge.callCount())
	}
}

// TestProbeClearsACooldown covers the "re-check" button: one deliberate dial,
// and a success ends the cooldown rather than making the person wait it out.
func TestProbeClearsACooldown(t *testing.T) {
	judge := &fakeJudge{verdict: confidentVerdict()}
	r := newRouter(newFakeStore(), judge)
	r.Breaker.Fail("ws", &FatalStatusError{Code: http.StatusUnauthorized})

	rep, err := r.Probe(context.Background(), "ws")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if judge.callCount() != 1 {
		t.Fatalf("Probe made %d model calls, want exactly 1", judge.callCount())
	}
	if rep.State != StateEnabled || !rep.Usable {
		t.Fatalf("after a successful probe got %+v, want enabled", rep)
	}
	if rep.LastSuccessAt == 0 {
		t.Fatal("a successful probe left no self-check timestamp")
	}
}

// TestProbeDoesNotDialWhenRoutingIsNotEnabled: off and incomplete are the
// pre-routing product to the letter, and that includes the button.
func TestProbeDoesNotDialWhenRoutingIsNotEnabled(t *testing.T) {
	for name, settings := range map[string]Settings{
		"off":        {},
		"incomplete": {Enabled: true},
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakeStore()
			store.settings = settings
			judge := &fakeJudge{verdict: confidentVerdict()}
			if _, err := newRouter(store, judge).Probe(context.Background(), "ws"); err != nil {
				t.Fatalf("Probe: %v", err)
			}
			if judge.callCount() != 0 {
				t.Fatalf("Probe dialled %d times while routing was %s", judge.callCount(), name)
			}
		})
	}
}

// TestUnconfiguredLLMLeavesTheTicketAlone. A deployment with no internal LLM
// is a settings problem, not a ticket problem: commenting and @-ing on every
// issue would punish a workspace for an admin's omission, and the spec puts
// diagnosis in exactly one place.
func TestUnconfiguredLLMLeavesTheTicketAlone(t *testing.T) {
	store := newFakeStore()
	r := newRouter(store, LLMJudge{Gen: llm.New(llm.Config{})})

	out, err := r.Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if out.State != StateIneffective {
		t.Fatalf("state = %s, want ineffective", out.State)
	}
	if store.commentCount() != 0 {
		t.Fatalf("posted %d comments about a deployment-level omission", store.commentCount())
	}
	if out.Mentioned || len(store.subs) != 0 {
		t.Fatal("notified somebody about a deployment-level omission")
	}
	if store.wrote() {
		t.Fatal("wrote a value while the model was unavailable")
	}
	// And it must be visible where it belongs.
	rep, _ := r.Health(context.Background(), "ws")
	if rep.State != StateIneffective || rep.Reason == "" {
		t.Fatalf("settings health = %+v, want ineffective with a reason", rep)
	}
}
