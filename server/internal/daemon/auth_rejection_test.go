package daemon

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestDaemonAuthRejectionCredentialBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, method, token string
		status              int
		stop                bool
	}{
		{"own GET", "GET", "mul_daemon", 401, true},
		{"own POST", "POST", "mul_daemon", 401, true},
		{"task GET", "GET", "mat_task", 401, false},
		{"task POST", "POST", "mat_task", 401, false},
		{"server unavailable", "POST", "mul_daemon", 503, false},
		{"permission denied", "GET", "mul_daemon", 403, false},
		{"rate limit", "POST", "mul_daemon", 429, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status) }))
			defer srv.Close()
			d := New(Config{ServerBaseURL: srv.URL, WorkspacesRoot: t.TempDir()}, slog.Default())
			d.client.SetToken("mul_daemon")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			d.stopOnAuthRejection(ctx, cancel)
			report := terminalTaskReport{kind: terminalTaskReportComplete, taskID: "finished", output: "saved result"}
			if err := d.terminalReports.enqueue(report); err != nil {
				t.Fatal(err)
			}
			var err error
			if tc.method == "GET" {
				err = d.client.getJSONWithToken(ctx, "/test", tc.token, nil)
			} else if tc.token == "mul_daemon" {
				err = d.client.postJSON(ctx, "/test", nil, nil)
			} else {
				err = d.client.postJSONWithToken(ctx, "/test", tc.token, nil, nil)
			}
			if err == nil {
				t.Fatal("expected server error")
			}
			if (ctx.Err() != nil) != tc.stop {
				t.Fatalf("cancelled=%v, want %v", ctx.Err() != nil, tc.stop)
			}
			pending, err := d.terminalReports.list()
			if err != nil || len(pending) != 1 || pending[0].report.output != "saved result" {
				t.Fatalf("pending result lost: %v %v", pending, err)
			}
		})
	}
}

func TestDaemonAuthRejectionClosesHealthyWebSocket(t *testing.T) {
	connected := make(chan struct{})
	disconnected := make(chan struct{})
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/daemon/ws" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		close(connected)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				close(disconnected)
				return
			}
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := New(Config{ServerBaseURL: srv.URL, HeartbeatInterval: time.Hour}, slog.Default())
	d.client.SetToken("mul_daemon")
	d.stopOnAuthRejection(ctx, cancel)
	done := make(chan error, 1)
	go func() {
		_, err := d.runTaskWakeupConnection(ctx, []string{"rt"}, make(chan taskWakeup, 2), make(chan struct{}))
		done <- err
	}()
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("no connection")
	}
	_ = d.client.postJSON(ctx, "/api/daemon/tasks/task/start", nil, nil)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("revoked daemon kept websocket alive")
	}
	select {
	case <-disconnected:
	case <-time.After(3 * time.Second):
		t.Fatal("server still sees socket")
	}
}

func TestDaemonAuthRejectionOnWebSocketHandshake(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := New(Config{ServerBaseURL: srv.URL}, slog.Default())
	d.client.SetToken("mul_daemon")
	d.stopOnAuthRejection(ctx, cancel)
	_, err := d.runTaskWakeupConnection(ctx, nil, make(chan taskWakeup, 1), make(chan struct{}))
	if err == nil || ctx.Err() == nil {
		t.Fatalf("handshake rejection did not stop daemon: %v", err)
	}
}

func TestDaemonAuthRejectionRequiresIdentityConfirmation(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/me" {
					w.WriteHeader(status)
					return
				}
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"daemon token required"}`))
			}))
			defer srv.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			d := New(Config{ServerBaseURL: srv.URL}, slog.Default())
			d.client.SetToken("mul_valid")
			d.stopOnAuthRejection(ctx, cancel)
			if err := d.client.postJSON(ctx, "/api/daemon/project-resources/resource/identity", nil, nil); err == nil {
				t.Fatal("expected endpoint-specific rejection")
			}
			if ctx.Err() != nil {
				t.Fatal("stopped without identity confirmation")
			}
		})
	}
}
