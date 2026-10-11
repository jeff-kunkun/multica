package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

const urgentTestSession = "019ec09d-6222-722b-bdfa-427b105d80be"
const urgentTestTask = "019ec09d-7777-722b-bdfa-427b105d80be"

// urgentSendCommand builds the flag set runChatSend reads, with --urgent set
// and no --wait: the pickup wait must come from --urgent alone (DENE-1884).
func urgentSendCommand(t *testing.T, srv *httptest.Server) *cobra.Command {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Setenv("MULTICA_TASK_ID", "")
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "test-token")
	cmd := &cobra.Command{}
	cmd.Flags().String("session", urgentTestSession, "")
	cmd.Flags().String("mode", "", "")
	cmd.Flags().String("output", "json", "")
	cmd.Flags().String("content-file", "", "")
	cmd.Flags().Bool("allow-external-file", false, "")
	cmd.Flags().StringSlice("attachment", nil, "")
	cmd.Flags().Bool("wait", false, "")
	cmd.Flags().Duration("wait-timeout", 10*time.Minute, "")
	cmd.Flags().String("per-quote", "", "")
	cmd.Flags().Bool("urgent", true, "")
	return cmd
}

// urgentTestServer answers the send with a pickup deadline `deadlineIn` from
// now and lets `progress` decide what each poll of the turn reports.
func urgentTestServer(t *testing.T, deadlineIn time.Duration, fallback bool, progress func(poll int, pastDeadline bool) map[string]any) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var polls atomic.Int32
	var deadline time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions/"+urgentTestSession+"/messages":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["urgent"] != true {
				t.Errorf("send body must carry urgent=true: %v", body)
			}
			deadline = time.Now().Add(deadlineIn)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"mode": "restart", "urgent": true, "message_id": "msg-1", "task_id": urgentTestTask,
				"pickup_deadline_at": deadline.UTC().Format(time.RFC3339Nano),
				"fallback_runtime":   fallback,
				"runtime":            map[string]any{"status": "online", "name": "mac"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/chat/sessions/"+urgentTestSession+"/progress":
			if r.URL.Query().Get("task_id") != urgentTestTask {
				t.Errorf("progress must poll the sent task: %s", r.URL.RawQuery)
			}
			n := int(polls.Add(1))
			_ = json.NewEncoder(w).Encode(progress(n, time.Now().After(deadline)))
		default:
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &polls
}

func fastPickupPolling(t *testing.T) {
	t.Helper()
	oldPoll := chatPickupPollInterval
	chatPickupPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { chatPickupPollInterval = oldPoll })
}

// deadlineTolerance is the slack a wait may run past pickup_deadline_at for
// the command to shut down and print: the bound the review (DENE-1886) asked
// for is deadline + 1 s, never deadline + a grace of its own.
const deadlineTolerance = time.Second

// slowUntilCancelled blocks a progress handler for `d` or until the client
// hangs up, whichever comes first, standing in for a server that answers a
// progress request late.
func slowUntilCancelled(r *http.Request, d time.Duration) {
	select {
	case <-time.After(d):
	case <-r.Context().Done():
	}
}

func decodeReceipt(t *testing.T, output string) map[string]any {
	t.Helper()
	var receipt map[string]any
	if err := json.Unmarshal([]byte(output), &receipt); err != nil {
		t.Fatalf("receipt is not JSON: %q (%v)", output, err)
	}
	return receipt
}

// Only --urgent: the command returns 0 as soon as a daemon starts the turn,
// well before the deadline, and the receipt says so.
func TestChatSendUrgentAloneWaitsForPickupAndSucceeds(t *testing.T) {
	fastPickupPolling(t)
	srv, polls := urgentTestServer(t, 2*time.Second, false, func(poll int, _ bool) map[string]any {
		if poll < 3 {
			return map[string]any{"task_id": urgentTestTask, "task_status": "dispatched", "reason": "dispatched"}
		}
		return map[string]any{"task_id": urgentTestTask, "task_status": "running", "reason": "running_no_logs"}
	})
	cmd := urgentSendCommand(t, srv)
	started := time.Now()
	output, err := captureStdout(t, func() error { return runChatSend(cmd, []string{"验证码 482913"}) })
	elapsed := time.Since(started)
	if err != nil || cli.ExitCodeFor(err) != 0 {
		t.Fatalf("urgent send picked up in time must exit 0: %v", err)
	}
	receipt := decodeReceipt(t, output)
	if receipt["task_status"] != "running" || receipt["urgent"] != true || receipt["fallback_runtime"] != false {
		t.Fatalf("receipt = %v", receipt)
	}
	if polls.Load() != 3 {
		t.Fatalf("polled %d times, want 3 (returned on the first running report)", polls.Load())
	}
	if elapsed > time.Second {
		t.Fatalf("took %s; must return on pickup, not wait for the deadline", elapsed)
	}
	if ms, _ := receipt["pickup_elapsed_ms"].(float64); ms < 0 || time.Duration(ms)*time.Millisecond > elapsed {
		t.Fatalf("pickup_elapsed_ms = %v, total %s", receipt["pickup_elapsed_ms"], elapsed)
	}
}

// Online but nobody starts the turn: the command waits until the server's
// deadline, reports the server's pickup_timeout verdict, and exits nonzero
// within a second of pickup_deadline_at.
func TestChatSendUrgentOnlineButNotPickedUpFailsAtServerDeadline(t *testing.T) {
	fastPickupPolling(t)
	const deadlineIn = 400 * time.Millisecond
	srv, _ := urgentTestServer(t, deadlineIn, false, func(_ int, pastDeadline bool) map[string]any {
		if !pastDeadline {
			return map[string]any{"task_id": urgentTestTask, "task_status": "dispatched", "reason": "dispatched", "runtime": map[string]any{"status": "online", "name": "mac"}}
		}
		// The server's sweep failed it.
		return map[string]any{"task_id": urgentTestTask, "task_status": "failed", "reason": "pickup_timeout", "runtime": map[string]any{"status": "online", "name": "mac"}}
	})
	cmd := urgentSendCommand(t, srv)
	started := time.Now()
	output, err := captureStdout(t, func() error { return runChatSend(cmd, []string{"验证码 482913"}) })
	elapsed := time.Since(started)
	if err == nil || cli.ExitCodeFor(err) == 0 || !strings.Contains(err.Error(), "pickup_timeout") {
		t.Fatalf("unstarted urgent turn must exit nonzero with pickup_timeout: %v", err)
	}
	receipt := decodeReceipt(t, output)
	if receipt["reason"] != "pickup_timeout" || receipt["message_id"] != "msg-1" {
		t.Fatalf("receipt = %v", receipt)
	}
	// Depending on whether the last poll landed before or after the deadline,
	// the verdict is the server's (failed) or the sender's own give-up; both
	// are honest about task_status and neither waits past the deadline.
	switch receipt["reason_source"] {
	case "server":
		if receipt["task_status"] != "failed" {
			t.Fatalf("server verdict must carry its task_status: %v", receipt)
		}
	case "sender":
		if receipt["task_status"] != "dispatched" {
			t.Fatalf("sender give-up must keep the last reported task_status: %v", receipt)
		}
	default:
		t.Fatalf("reason_source = %v", receipt["reason_source"])
	}
	if elapsed < deadlineIn-50*time.Millisecond || elapsed > deadlineIn+deadlineTolerance {
		t.Fatalf("waited %s; must hold until the server deadline (%s) and stop within %s of it", elapsed, deadlineIn, deadlineTolerance)
	}
}

// The reviewer's counterexample (DENE-1886): the server never judges the
// turn — every poll says dispatched, forever. The old grace made the command
// return at deadline + 10 s (40.04 s measured). It must stop at the deadline,
// say the server still has it dispatched, and mark that the sender gave up.
func TestChatSendUrgentServerNeverDecidesStopsAtDeadline(t *testing.T) {
	fastPickupPolling(t)
	const deadlineIn = 300 * time.Millisecond
	srv, polls := urgentTestServer(t, deadlineIn, false, func(_ int, _ bool) map[string]any {
		return map[string]any{"task_id": urgentTestTask, "task_status": "dispatched", "reason": "dispatched", "runtime": map[string]any{"status": "online", "name": "mac"}}
	})
	cmd := urgentSendCommand(t, srv)
	started := time.Now()
	output, err := captureStdout(t, func() error { return runChatSend(cmd, []string{"验证码 482913"}) })
	elapsed := time.Since(started)
	if err == nil || cli.ExitCodeFor(err) == 0 || !strings.Contains(err.Error(), "pickup_timeout") || !strings.Contains(err.Error(), "task_status=dispatched") {
		t.Fatalf("error = %v", err)
	}
	receipt := decodeReceipt(t, output)
	if receipt["reason"] != "pickup_timeout" || receipt["reason_source"] != "sender" || receipt["task_status"] != "dispatched" {
		t.Fatalf("receipt must not forge a failed task: %v", receipt)
	}
	if polls.Load() < 2 {
		t.Fatalf("polled %d times; must keep polling up to the deadline", polls.Load())
	}
	if elapsed < deadlineIn-50*time.Millisecond || elapsed > deadlineIn+deadlineTolerance {
		t.Fatalf("waited %s, want pickup_deadline_at (%s) within %s, not deadline + grace", elapsed, deadlineIn, deadlineTolerance)
	}
}

// A progress request that stalls near the deadline cannot drag the wait past
// it: the request runs under the deadline context, is cut at the deadline,
// and the receipt keeps the last status the server did report.
func TestChatSendUrgentSlowProgressNearDeadlineStillStopsAtDeadline(t *testing.T) {
	fastPickupPolling(t)
	const deadlineIn = 300 * time.Millisecond
	var req atomic.Pointer[http.Request]
	srv, polls := urgentTestServer(t, deadlineIn, false, func(poll int, _ bool) map[string]any {
		if poll >= 3 {
			// Far slower than the time left before the deadline.
			slowUntilCancelled(req.Load(), 5*time.Second)
		}
		return map[string]any{"task_id": urgentTestTask, "task_status": "waiting_local_directory", "reason": "waiting_local_directory"}
	})
	// urgentTestServer hides the request from the progress callback; wrap the
	// handler so the slow poll can watch for the client hanging up.
	inner := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req.Store(r)
		inner.ServeHTTP(w, r)
	})
	cmd := urgentSendCommand(t, srv)
	started := time.Now()
	output, err := captureStdout(t, func() error { return runChatSend(cmd, []string{"验证码 482913"}) })
	elapsed := time.Since(started)
	if err == nil || !strings.Contains(err.Error(), "pickup_timeout") {
		t.Fatalf("error = %v", err)
	}
	receipt := decodeReceipt(t, output)
	if receipt["reason"] != "pickup_timeout" || receipt["reason_source"] != "sender" || receipt["task_status"] != "waiting_local_directory" {
		t.Fatalf("receipt = %v", receipt)
	}
	if polls.Load() < 3 {
		t.Fatalf("polled %d times; the slow poll never happened", polls.Load())
	}
	if elapsed < deadlineIn-50*time.Millisecond || elapsed > deadlineIn+deadlineTolerance {
		t.Fatalf("waited %s; a slow progress request must be cut at the deadline (%s + %s)", elapsed, deadlineIn, deadlineTolerance)
	}
}

// The very first progress request stalls past the deadline: the command
// still stops at the deadline and, knowing nothing newer, reports the send
// receipt's status rather than inventing one.
func TestChatSendUrgentFirstProgressRequestStallsStopsAtDeadline(t *testing.T) {
	fastPickupPolling(t)
	const deadlineIn = 300 * time.Millisecond
	var req atomic.Pointer[http.Request]
	srv, _ := urgentTestServer(t, deadlineIn, false, func(_ int, _ bool) map[string]any {
		slowUntilCancelled(req.Load(), 5*time.Second)
		return map[string]any{"task_id": urgentTestTask, "task_status": "running"}
	})
	inner := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req.Store(r)
		inner.ServeHTTP(w, r)
	})
	cmd := urgentSendCommand(t, srv)
	started := time.Now()
	output, err := captureStdout(t, func() error { return runChatSend(cmd, []string{"验证码 482913"}) })
	elapsed := time.Since(started)
	if err == nil || !strings.Contains(err.Error(), "pickup_timeout") || !strings.Contains(err.Error(), "task_status=unknown") {
		t.Fatalf("error = %v", err)
	}
	receipt := decodeReceipt(t, output)
	if receipt["reason"] != "pickup_timeout" || receipt["reason_source"] != "sender" || receipt["task_id"] != urgentTestTask || receipt["message_id"] != "msg-1" {
		t.Fatalf("receipt = %v", receipt)
	}
	if _, forged := receipt["task_status"]; forged {
		t.Fatalf("no progress ever answered; task_status must not be invented: %v", receipt)
	}
	if elapsed > deadlineIn+deadlineTolerance {
		t.Fatalf("waited %s; must stop at the deadline (%s + %s)", elapsed, deadlineIn, deadlineTolerance)
	}
}

// Offline primary, no standby: refused on the spot, no polling, nonzero exit
// with the server's reason and next_steps.
func TestChatSendUrgentOfflineWithoutFallbackIsRefusedImmediately(t *testing.T) {
	fastPickupPolling(t)
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			polls++
			t.Errorf("a refused urgent send must not poll: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": "runtime_offline", "reason": "runtime_offline: mac last_seen 2026-10-11T09:00:10Z",
			"next_steps": []string{"start the daemon on mac", "configure a fallback runtime"},
			"runtime":    map[string]any{"status": "offline", "name": "mac"},
		})
	}))
	defer srv.Close()
	cmd := urgentSendCommand(t, srv)
	started := time.Now()
	output, err := captureStdout(t, func() error { return runChatSend(cmd, []string{"验证码 482913"}) })
	elapsed := time.Since(started)
	if err == nil || cli.ExitCodeFor(err) == 0 || !strings.Contains(err.Error(), "runtime_offline") || !strings.Contains(err.Error(), "nothing was sent") {
		t.Fatalf("error = %v", err)
	}
	receipt := decodeReceipt(t, output)
	if receipt["code"] != "runtime_offline" || !strings.HasPrefix(receipt["reason"].(string), "runtime_offline") {
		t.Fatalf("receipt = %v", receipt)
	}
	if polls != 0 || elapsed > 500*time.Millisecond {
		t.Fatalf("refusal must be immediate: polls=%d elapsed=%s", polls, elapsed)
	}
}

// Offline primary with an online standby: the turn runs there; the receipt
// says fallback_runtime=true and the pickup is awaited the same way.
func TestChatSendUrgentOfflineWithFallbackStartsOnStandby(t *testing.T) {
	fastPickupPolling(t)
	srv, polls := urgentTestServer(t, 2*time.Second, true, func(poll int, _ bool) map[string]any {
		if poll == 1 {
			return map[string]any{"task_id": urgentTestTask, "task_status": "dispatched", "reason": "dispatched", "runtime": map[string]any{"status": "online", "name": "standby"}}
		}
		return map[string]any{"task_id": urgentTestTask, "task_status": "running", "reason": "running_no_logs", "runtime": map[string]any{"status": "online", "name": "standby"}}
	})
	cmd := urgentSendCommand(t, srv)
	started := time.Now()
	output, err := captureStdout(t, func() error { return runChatSend(cmd, []string{"验证码 482913"}) })
	if err != nil {
		t.Fatalf("urgent send on the standby must succeed: %v", err)
	}
	receipt := decodeReceipt(t, output)
	runtime, _ := receipt["runtime"].(map[string]any)
	if receipt["fallback_runtime"] != true || receipt["task_status"] != "running" || runtime["name"] != "standby" {
		t.Fatalf("receipt = %v", receipt)
	}
	if polls.Load() != 2 || time.Since(started) > time.Second {
		t.Fatalf("polls=%d elapsed=%s; must return on the first running report", polls.Load(), time.Since(started))
	}
}
