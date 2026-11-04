package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Exercise handleTask's real disposition and HTTP delivery without launching a
// provider or stopping a daemon. Cancellation happens while the runner is active.
// A daemon-side cancel that the server has not finalized is reported as
// "cancelled", which the server retries as a platform interrupt (DENE-813);
// the final usage, status check and acknowledgement must still reach the
// server after the daemon context is gone.
func TestHandleTask_ShutdownDisposition(t *testing.T) {
	for _, tc := range []struct {
		name, status, reason, serverStatus, wantEndpoint, wantReason string
		shutdown                                                     bool
	}{
		{"shutdown", "cancelled", "", "running", "/fail", "cancelled", true},
		{"explicit agent cancellation", "cancelled", "", "running", "/fail", "cancelled", false},
		{"server cancellation during shutdown", "cancelled", "", "cancelled", "/cancel-ack", "", true},
		{"timeout during shutdown", "blocked", "timeout", "running", "/fail", "timeout", true},
		{"completed before shutdown", "completed", "", "running", "/complete", "", true},
		{"cleanup uncertainty", "cancelled", "process_cleanup_unconfirmed", "running", "/fail", "process_cleanup_unconfirmed", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var mu sync.Mutex
			var endpoint string
			var terminal, usage map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch {
				case strings.HasSuffix(r.URL.Path, "/status"):
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]string{"status": tc.serverStatus})
				case strings.HasSuffix(r.URL.Path, "/usage"):
					if err := json.NewDecoder(r.Body).Decode(&usage); err != nil {
						t.Error(err)
					}
				case strings.HasSuffix(r.URL.Path, "/progress"):
				default:
					endpoint = r.URL.Path
					if err := json.NewDecoder(r.Body).Decode(&terminal); err != nil {
						t.Error(err)
					}
				}
			}))
			defer srv.Close()
			d := &Daemon{
				client: NewClient(srv.URL), logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
				runtimeIndex:       map[string]Runtime{"rt": {ID: "rt", Provider: "codex"}},
				cancelPollInterval: time.Hour,
			}
			d.runner = taskRunnerFunc(func(runCtx context.Context, _ Task, _ string, _ int, _ *slog.Logger) (TaskResult, error) {
				if runCtx.Err() != nil {
					t.Fatal("run started with cancelled context")
				}
				if tc.shutdown {
					cancel()
					<-runCtx.Done()
				}
				return TaskResult{
					Status: tc.status, FailureReason: tc.reason,
					SessionID: "session", WorkDir: "/workdir", BranchName: "agent/work",
					Usage: []TaskUsageEntry{{Provider: "openai", Model: "test", InputTokens: 100, OutputTokens: 50}},
				}, nil
			})
			d.handleTask(ctx, Task{ID: "task", RuntimeID: "rt"}, 0)
			mu.Lock()
			defer mu.Unlock()
			if !strings.HasSuffix(endpoint, tc.wantEndpoint) {
				t.Fatalf("endpoint = %q, want %q", endpoint, tc.wantEndpoint)
			}
			if tc.wantReason != "" && terminal["failure_reason"] != tc.wantReason {
				t.Errorf("reason = %v, want %s", terminal["failure_reason"], tc.wantReason)
			}
			if tc.wantEndpoint != "/cancel-ack" {
				for k, want := range map[string]string{"session_id": "session", "work_dir": "/workdir", "branch_name": "agent/work"} {
					if terminal[k] != want {
						t.Errorf("%s = %v, want %s", k, terminal[k], want)
					}
				}
			}
			entries, ok := usage["usage"].([]any)
			if !ok || len(entries) != 1 {
				t.Fatalf("usage lost: %v", usage)
			}
			entry := entries[0].(map[string]any)
			if entry["input_tokens"] != float64(100) || entry["output_tokens"] != float64(50) {
				t.Errorf("usage changed: %v", entry)
			}
		})
	}
}

// A server that accepts the connection and never answers must not hold the
// shutdown report hostage: usage, progress and status share one short budget,
// then the terminal report goes out.
func TestHandleTask_ShutdownHungServerStillReportsTerminal(t *testing.T) {
	prev := shutdownFinalReportBudget
	shutdownFinalReportBudget = 200 * time.Millisecond
	t.Cleanup(func() { shutdownFinalReportBudget = prev })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failed := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/fail"):
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			failed <- body
		default:
			select {
			case <-r.Context().Done():
			case <-time.After(3 * time.Second):
			}
		}
	}))
	defer srv.Close()
	d := &Daemon{
		client: NewClient(srv.URL), logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtimeIndex:       map[string]Runtime{"rt": {ID: "rt", Provider: "codex"}},
		cancelPollInterval: time.Hour,
	}
	d.runner = taskRunnerFunc(func(runCtx context.Context, _ Task, _ string, _ int, _ *slog.Logger) (TaskResult, error) {
		cancel()
		<-runCtx.Done()
		return TaskResult{
			Status: "cancelled", SessionID: "session", WorkDir: "/workdir",
			Usage: []TaskUsageEntry{{Provider: "openai", Model: "test", InputTokens: 1, OutputTokens: 1}},
		}, nil
	})
	done := make(chan struct{})
	start := time.Now()
	go func() { defer close(done); d.handleTask(ctx, Task{ID: "task", RuntimeID: "rt"}, 0) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("handleTask stayed on the hung server past the shutdown budget")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("handleTask took %v with a 200ms shutdown budget", elapsed)
	}
	select {
	case body := <-failed:
		if body["failure_reason"] != "cancelled" {
			t.Errorf("terminal report = %v, want failure_reason cancelled", body)
		}
	default:
		t.Fatal("terminal /fail report never sent")
	}
}
