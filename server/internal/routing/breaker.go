package routing

import (
	"errors"
	"sync"
	"time"
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
	now   func() time.Time

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
	if fatal, reason := classifyFatal(err); fatal {
		e.openUntil = b.now().Add(b.cooldown)
		e.reason = reason
		return
	}
	if e.consecutiveFailures >= b.failuresToTrip {
		e.openUntil = b.now().Add(b.cooldown)
		e.reason = "the routing model failed " + itoa(e.consecutiveFailures) + " times in a row"
	}
}

// httpStatusError is the shape this package needs out of an upstream error.
type httpStatusError interface {
	error
	// StatusCode mirrors the openai-go *Error field name.
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
