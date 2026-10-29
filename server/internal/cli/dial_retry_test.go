package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// closedAddr returns a local address nothing listens on, so dialing it fails
// with a real connection-refused error from the kernel.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// flakyDialTransport is a real http.Transport whose first failures dials go to
// a closed port; later dials reach the requested address.
func flakyDialTransport(t *testing.T, failures int32, dials *atomic.Int32) *http.Transport {
	dead := closedAddr(t)
	return &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if dials.Add(1) <= failures {
				addr = dead
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}
}

func testClient(srvURL string, base http.RoundTripper) *APIClient {
	c := NewAPIClient(srvURL, "ws-1", "tok")
	c.HTTPClient.Transport = &dialRetryTransport{base: base, retries: dialRetries, backoff: time.Millisecond}
	return c
}

func TestNewAPIClientRetriesFailedDials(t *testing.T) {
	if _, ok := NewAPIClient("http://x", "", "").HTTPClient.Transport.(*dialRetryTransport); !ok {
		t.Fatal("NewAPIClient should wrap its transport in dialRetryTransport")
	}
}

func TestDialRetryResendsPostBodyOnceConnected(t *testing.T) {
	var hits atomic.Int32
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var dials atomic.Int32
	c := testClient(srv.URL, flakyDialTransport(t, 2, &dials))
	var out map[string]any
	if err := c.PostJSON(context.Background(), "/api/issues/x/comments", map[string]string{"content": "hello"}, &out); err != nil {
		t.Fatalf("PostJSON after two failed dials: %v", err)
	}
	if dials.Load() != 3 || hits.Load() != 1 {
		t.Fatalf("dials=%d hits=%d, want 3 dials and exactly 1 request at the server", dials.Load(), hits.Load())
	}
	if gotBody != `{"content":"hello"}` {
		t.Fatalf("server got body %q, want the original payload", gotBody)
	}
}

func TestDialRetryGivesUpAfterRetries(t *testing.T) {
	var dials atomic.Int32
	c := testClient("http://"+closedAddr(t), flakyDialTransport(t, 100, &dials))
	err := c.GetJSON(context.Background(), "/api/me", nil)
	if err == nil {
		t.Fatal("want an error when every dial fails")
	}
	if dials.Load() != 1+dialRetries {
		t.Fatalf("dials=%d, want %d", dials.Load(), 1+dialRetries)
	}
	if got := classifyNetworkError(err); got != KindNetworkRefused {
		t.Fatalf("classifyNetworkError = %v, want KindNetworkRefused (retries must not change the error)", got)
	}
}

// stubTransport returns a fixed error and counts calls.
type stubTransport struct {
	err   error
	calls atomic.Int32
}

func (s *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.calls.Add(1)
	if req.Body != nil {
		req.Body.Close()
	}
	return nil, s.err
}

func TestDialRetrySkipsErrorsAfterConnect(t *testing.T) {
	for name, err := range map[string]error{
		"read reset":   &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")},
		"eof":          io.ErrUnexpectedEOF,
		"unknown host": &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "nope.invalid", IsNotFound: true}},
	} {
		t.Run(name, func(t *testing.T) {
			stub := &stubTransport{err: err}
			rt := &dialRetryTransport{base: stub, retries: dialRetries, backoff: time.Millisecond}
			req, _ := http.NewRequest(http.MethodPost, "http://example.test/api/x", strings.NewReader("{}"))
			if _, got := rt.RoundTrip(req); got == nil {
				t.Fatal("want the error back")
			}
			if stub.calls.Load() != 1 {
				t.Fatalf("calls=%d, want 1 (no retry once the request may have been sent)", stub.calls.Load())
			}
		})
	}
}

func TestDialRetrySkipsBodyThatCannotRewind(t *testing.T) {
	stub := &stubTransport{err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}}
	rt := &dialRetryTransport{base: stub, retries: dialRetries, backoff: time.Millisecond}
	req, _ := http.NewRequest(http.MethodPost, "http://example.test/api/x", io.NopCloser(bytes.NewReader([]byte("{}"))))
	req.GetBody = nil
	rt.RoundTrip(req)
	if stub.calls.Load() != 1 {
		t.Fatalf("calls=%d, want 1 for a streamed body", stub.calls.Load())
	}
}

// countingBody records how many replay bodies were made and closed.
type countingBody struct {
	io.Reader
	closed *atomic.Int32
}

func (b countingBody) Close() error { b.closed.Add(1); return nil }

func TestDialRetryStopsWhenContextEnds(t *testing.T) {
	var dials, opened, closed atomic.Int32
	client := &http.Client{Transport: &dialRetryTransport{
		base:    flakyDialTransport(t, 100, &dials),
		retries: dialRetries,
		backoff: time.Second,
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+closedAddr(t)+"/api/x", strings.NewReader("{}"))
	req.GetBody = func() (io.ReadCloser, error) {
		opened.Add(1)
		return countingBody{Reader: strings.NewReader("{}"), closed: &closed}, nil
	}
	start := time.Now()
	_, err := client.Do(req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded rather than the earlier dial failure", err)
	}
	if got := classifyNetworkError(err); got != KindNetworkTimeout {
		t.Fatalf("classifyNetworkError = %v, want KindNetworkTimeout", got)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond || dials.Load() != 1 {
		t.Fatalf("dials=%d after %s; want 1 dial and a return at the deadline", dials.Load(), elapsed)
	}
	if opened.Load() != closed.Load() {
		t.Fatalf("replay bodies opened=%d closed=%d; every body made must be closed", opened.Load(), closed.Load())
	}
}
