package cli

import (
	"errors"
	"net"
	"net/http"
	"time"
)

// dialRetryTransport retries a request whose TCP connection never opened.
//
// Some links drop a share of connection attempts outright: from mainland
// China about 5% of direct connects to the office server died after ~7.8s
// without ever reaching it (DENE-2765), which agents saw as a failed comment
// or status change. When the dial itself fails nothing has been sent, so a
// retry is safe even for POST — the server cannot have seen the request.
// Anything after the connection opens (TLS, a reset mid-request, a slow
// reply) is not retried, because the request may already have landed.
type dialRetryTransport struct {
	base    http.RoundTripper
	retries int
	backoff time.Duration // multiplied by the attempt number
}

const (
	dialRetries      = 2
	dialRetryBackoff = 500 * time.Millisecond
)

func newDialRetryTransport(base http.RoundTripper) *dialRetryTransport {
	return &dialRetryTransport{base: base, retries: dialRetries, backoff: dialRetryBackoff}
}

func (t *dialRetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	for attempt := 1; attempt <= t.retries && err != nil && isDialFailure(err) && canRewind(req); attempt++ {
		timer := time.NewTimer(t.backoff * time.Duration(attempt))
		select {
		case <-req.Context().Done():
			// The caller gave up while we waited: report that, not the dial
			// failure, so a deadline still reads as a timeout.
			timer.Stop()
			return nil, req.Context().Err()
		case <-timer.C:
		}
		// The fresh body is made only now, right before the transport takes
		// ownership of it, so no exit path can leave it unclosed.
		retry, ok := rewindRequest(req)
		if !ok {
			break
		}
		resp, err = t.base.RoundTrip(retry)
	}
	return resp, err
}

// canRewind reports whether req's body can be re-created for another attempt.
// The transport has already closed the original body.
func canRewind(req *http.Request) bool {
	return req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
}

// isDialFailure reports whether err is a TCP connect that never completed.
// A host that does not exist is left alone: asking again cannot help.
func isDialFailure(err error) bool {
	var opErr *net.OpError
	if !errors.As(err, &opErr) || opErr.Op != "dial" {
		return false
	}
	var dnsErr *net.DNSError
	return !errors.As(err, &dnsErr) || !dnsErr.IsNotFound
}

// rewindRequest copies req for another attempt with a fresh body; the caller
// must hand the copy to a transport, which then owns closing that body.
func rewindRequest(req *http.Request) (*http.Request, bool) {
	retry := req.Clone(req.Context())
	if req.Body == nil || req.Body == http.NoBody {
		return retry, true
	}
	if req.GetBody == nil {
		return nil, false
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, false
	}
	retry.Body = body
	return retry, true
}
