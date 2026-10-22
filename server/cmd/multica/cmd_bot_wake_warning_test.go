package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

func TestChatSendBlockedPreservesReceiptAndStopsWait(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("MULTICA_TASK_ID", "")
	const session = "019ec09d-6222-722b-bdfa-427b105d80be"
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost {
			t.Errorf("blocked send must not wait: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["per_quote"] != "Please retry" {
			t.Errorf("per_quote = %v", body["per_quote"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"mode": "blocked", "message_id": "saved-message", "warning": "bot wake chain limit (4): target agent-a"})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "test-token")
	cmd := &cobra.Command{}
	cmd.Flags().String("session", session, "")
	cmd.Flags().String("mode", "queue", "")
	cmd.Flags().String("output", "json", "")
	cmd.Flags().String("content-file", "", "")
	cmd.Flags().Bool("allow-external-file", false, "")
	cmd.Flags().StringSlice("attachment", nil, "")
	cmd.Flags().Bool("wait", true, "")
	cmd.Flags().String("per-quote", "Please retry", "")
	output, err := captureStdout(t, func() error { return runChatSend(cmd, []string{"Try again"}) })
	if err == nil || !strings.Contains(err.Error(), "bot wake chain limit (4)") || !strings.Contains(err.Error(), "saved-message") {
		t.Fatalf("error = %v", err)
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(output), &receipt); err != nil || receipt["message_id"] != "saved-message" {
		t.Fatalf("receipt = %s; %v", output, err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestCommentBotWakeWarningNamesBlockedTarget(t *testing.T) {
	var out bytes.Buffer
	printBotWakeBlockedWarnings(&out, []any{
		map[string]any{"reason_code": "queued", "target_id": "active"},
		map[string]any{"reason_code": "bot_wake_chain_limit", "target_type": "agent", "target_id": "blocked-agent"},
	})
	if !strings.Contains(out.String(), "blocked-agent") || !strings.Contains(out.String(), "comment saved") || !strings.Contains(out.String(), "Do not repost") || strings.Contains(out.String(), "active") {
		t.Fatalf("warning = %q", out.String())
	}
	var jsonOut bytes.Buffer
	if err := cli.PrintJSON(&jsonOut, map[string]any{"saved": true}); err != nil || !json.Valid(jsonOut.Bytes()) {
		t.Fatal("warning affected JSON receipt")
	}
}

func TestChatNewBlockedPreservesReceiptWithoutWaiting(t *testing.T) {
	for _, wait := range []bool{false, true} {
		t.Run(fmt.Sprintf("wait_%v", wait), func(t *testing.T) {
			t.Chdir(t.TempDir())
			clearAgentContext(t)
			const agent = "5a0c459c-483e-4353-b03c-c3d3f260b6be"
			polls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/api/agents":
					_ = json.NewEncoder(w).Encode([]map[string]any{{"id": agent, "name": "target"}})
				case r.Method == "POST" && r.URL.Path == "/api/chat/sessions":
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "new-chat", "title": "New chat"})
				case r.Method == "POST" && r.URL.Path == "/api/chat/sessions/new-chat/messages":
					_ = json.NewEncoder(w).Encode(map[string]any{"mode": "blocked", "message_id": "saved-message", "task_id": "blocked-task", "warning": "bot wake chain limit (4)", "reason_code": "bot_wake_chain_limit"})
				default:
					polls++
					http.Error(w, "blocked send must not poll", 500)
				}
			}))
			defer srv.Close()
			t.Setenv("MULTICA_SERVER_URL", srv.URL)
			t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
			t.Setenv("MULTICA_TOKEN", "test-token")
			cmd := &cobra.Command{}
			cmd.Flags().String("agent", "target", "")
			cmd.Flags().String("content-file", "", "")
			cmd.Flags().StringSlice("reference", nil, "")
			cmd.Flags().Bool("wait", wait, "")
			cmd.Flags().String("output", "json", "")
			out, err := captureStdout(t, func() error { return runChatNew(cmd, []string{"Try again"}) })
			if err == nil || !strings.Contains(err.Error(), "bot wake chain limit (4)") || !strings.Contains(err.Error(), "new-chat") {
				t.Fatalf("refusal = %v", err)
			}
			var receipt map[string]any
			if err := json.Unmarshal([]byte(out), &receipt); err != nil || receipt["session_id"] != "new-chat" || receipt["message_id"] != "saved-message" || receipt["reason_code"] != "bot_wake_chain_limit" || receipt["mode"] != "blocked" {
				t.Fatalf("receipt = %s; %v", out, err)
			}
			if polls != 0 {
				t.Fatalf("blocked send polled %d times", polls)
			}
		})
	}
}
