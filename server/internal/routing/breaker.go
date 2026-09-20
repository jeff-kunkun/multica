package routing

import (
	"errors"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/llm"
)

// Breaker keeps a broken model from being dialled once per ticket.
//
// Without it, a revoked key or a dead gateway turns every issue write in the
// workspace into an outbound request that will fail, on the request path, with
// the caller waiting. The breaker is per workspace: one workspace's bad model
// must not stop another's.
//
// It is in-memory on purpose. The state it holds is "this model looked broken
// N seconds ago", which is worth nothing after a restart and not worth a row.
type Breaker struct {
	mu    sync.Mutex
	state map[string]*breakerEntry
	// lastSuccess survives Succeed clearing the entry: "the model answered at
	// T" is what the settings section shows while everything is healthy, and
	// that is precisely when there is no entry left to hang it on.
	lastSuccess map[string]time.Time
	now         func() time.Time

	// failuresToTrip is how many consecutive ordinary failures (timeouts,
	// 5xx, malformed replies) open the breaker. Authentication, payment, and
	// rate-limit answers skip the count entirely — see Fail.
	failuresToTrip int
	cooldown       time.Duration
}

type breakerEntry struct {
	consecutiveFailures int
	openUntil           time.Time
	reason              string
	lastFailure         time.Time
	lastFailureReason   string
	lastSuccess         time.Time
}

// Health is the read-only view the settings section renders. It is the ONLY
// place a routing failure is visible to a person: the state table keeps
// tickets quiet, so without this report a broken model is invisible outside
// the server log (DENE-633 review, F2).
type Health struct {
	// Open reports whether the breaker is cooling down right now.
	Open bool
	// Reason is why it opened, in words meant for a human.
	Reason string
	// Retry is how long the cooldown still has to run. Zero when not open.
	Retry time.Duration
	// LastSuccess is when the routing model last answered. Zero means never
	// since this process started — the state is in-memory, like the breaker.
	LastSuccess time.Time
	// LastFailure and LastFailureReason describe the most recent failure even
	// when it did not open the breaker, so "two stumbles ago" is visible
	// before the third one takes routing out.
	LastFailure       time.Time
	LastFailureReason string
}

// Health reports the current state for a workspace without mutating it,
// except for expiring a cooldown that has already elapsed — the same
// bookkeeping Open does, because a report that still said "cooling down" a
// minute after the cooldown ended would be wrong.
func (b *Breaker) Health(workspaceID string) Health {
	if b == nil {
		return Health{}
	}
	open, retry, reason := b.Open(workspaceID)
	b.mu.Lock()
	defer b.mu.Unlock()
	out := Health{Open: open, Retry: retry, Reason: reason}
	if e := b.state[workspaceID]; e != nil {
		out.LastFailure = e.lastFailure
		out.LastFailureReason = e.lastFailureReason
		out.LastSuccess = e.lastSuccess
	}
	if ls, ok := b.lastSuccess[workspaceID]; ok && ls.After(out.LastSuccess) {
		out.LastSuccess = ls
	}
	return out
}

// DefaultFailuresToTrip and DefaultCooldown are the shipped policy: three
// ordinary failures in a row, then five minutes of silence.
const (
	DefaultFailuresToTrip = 3
	DefaultCooldown       = 5 * time.Minute
)

// NewBreaker builds a breaker with the shipped policy.
func NewBreaker() *Breaker {
	return &Breaker{
		state:          map[string]*breakerEntry{},
		lastSuccess:    map[string]time.Time{},
		now:            time.Now,
		failuresToTrip: DefaultFailuresToTrip,
		cooldown:       DefaultCooldown,
	}
}

// Open reports whether the breaker for this workspace is cooling down, and for
// how much longer. Callers must check this BEFORE building a request: the
// whole point is that no request is sent while it is open.
func (b *Breaker) Open(workspaceID string) (bool, time.Duration, string) {
	if b == nil {
		return false, 0, ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.state[workspaceID]
	if e == nil || e.openUntil.IsZero() {
		return false, 0, ""
	}
	remaining := e.openUntil.Sub(b.now())
	if remaining <= 0 {
		// Cooldown elapsed. Clear the window but keep the failure count at
		// zero so the next probe gets a full budget rather than tripping on
		// its first stumble.
		e.openUntil = time.Time{}
		e.consecutiveFailures = 0
		e.reason = ""
		return false, 0, ""
	}
	return true, remaining, e.reason
}

// Succeed records a good call and closes the breaker.
func (b *Breaker) Succeed(workspaceID string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.state, workspaceID)
	if b.lastSuccess == nil {
		b.lastSuccess = map[string]time.Time{}
	}
	b.lastSuccess[workspaceID] = b.now()
}

// Fail records a failed call and opens the breaker when warranted.
//
// Fatal answers — the upstream telling us the credential is wrong, unpaid,
// forbidden, or that we are over the rate limit — open it immediately. Those
// do not get better by being retried on the next ticket, and retrying a 429 is
// how a workspace turns a transient limit into a sustained one.
func (b *Breaker) Fail(workspaceID string, err error) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.state[workspaceID]
	if e == nil {
		e = &breakerEntry{}
		b.state[workspaceID] = e
	}
	e.consecutiveFailures++
	e.lastFailure = b.now()
	if err != nil {
		e.lastFailureReason = err.Error()
	}
	if ls, ok := b.lastSuccess[workspaceID]; ok {
		e.lastSuccess = ls
	}
	if fatal, reason := classifyFatal(err); fatal {
		e.openUntil = b.now().Add(b.cooldown)
		e.reason = reason
		e.lastFailureReason = reason
		return
	}
	if e.consecutiveFailures >= b.failuresToTrip {
		e.openUntil = b.now().Add(b.cooldown)
		e.reason = "the routing model failed " + itoa(e.consecutiveFailures) + " times in a row"
	}
}

// httpStatusError is the shape this package needs out of an upstream error.
//
// The OpenAI SDK carries the status as a struct FIELD, which no interface can
// match, so this used to be satisfied by nothing a real call produced and the
// whole 401/402/403/429 branch was dead in production (DENE-633 review, F1).
// pkg/llm now wraps every failed completion in *llm.StatusError, whose
// Status() method is this contract; TestBreakerTripsOnRealUpstreamStatus
// drives a real upstream 401 through the real client to keep it that way.
type httpStatusError interface {
	error
	Status() int
}

// FatalStatusError wraps an upstream HTTP status so callers outside this
// package can report one without importing the SDK.
type FatalStatusError struct {
	Code int
	Err  error
}

func (e *FatalStatusError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "routing: upstream returned " + itoa(e.Code)
}
func (e *FatalStatusError) Unwrap() error { return e.Err }
func (e *FatalStatusError) Status() int   { return e.Code }

func classifyFatal(err error) (bool, string) {
	if errors.Is(err, llm.ErrNotConfigured) {
		// The deployment has no internal LLM at all. Retrying once per ticket
		// is pure waste, and it is a settings-level fact, not a ticket-level
		// one — see reportUnavailable, which keeps it off the ticket.
		return true, NotConfiguredReason
	}
	var se httpStatusError
	if !errors.As(err, &se) {
		return false, ""
	}
	switch se.Status() {
	case 401:
		return true, "the routing model rejected our credentials (401)"
	case 402:
		return true, "the routing model account needs payment (402)"
	case 403:
		return true, "the routing model refused access (403)"
	case 429:
		return true, "the routing model is rate limiting us (429)"
	}
	return false, ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
