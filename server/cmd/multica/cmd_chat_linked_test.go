package main

import (
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManagedChatListAndSendUseSourceHeaders(t *testing.T) {
	session := "019abcde-1234-7000-8000-000000000001"
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/workspace-links" {
			_, _ = w.Write([]byte(`{"links":[{"id":"link-1","side":"viewer","status":"active","managed":true,"source":{"id":"src-1","slug":"studio"}}]}`))
			return
		}
		calls++
		if r.Header.Get("X-Workspace-ID") != "src-1" || r.Header.Get("X-Linked-Workspace") != "src-1" {
			t.Errorf("wrong headers: %v", r.Header)
		}
		switch r.URL.Path {
		case "/api/chat/directory":
			_, _ = w.Write([]byte(`[{"id":"chat-1","title":"My chat"}]`))
		case "/api/chat/sessions/" + session + "/messages":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["content"] != "hello source" || r.Method != http.MethodPost {
				t.Errorf("send %s %v", r.Method, body)
			}
			_, _ = w.Write([]byte(`{"message_id":"msg-1","chat_session_id":"chat-1","linked_delivery":{"viewer_workspace_id":"viewer","agent_id":"agent-1","source_user_id":"owner"}}`))
		default:
			t.Errorf("unexpected %s", r.URL)
		}
	}))
	defer srv.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "viewer")
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	for _, send := range []bool{false, true} {
		cmd := &cobra.Command{}
		cmd.Flags().String("linked", "studio", "")
		cmd.Flags().String("output", "json", "")
		cmd.Flags().String("session", session, "")
		cmd.Flags().String("mode", "queue", "")
		out, err := captureStdout(t, func() error {
			if send {
				return runChatSend(cmd, []string{"hello source"})
			}
			return runChatDirectoryList(cmd, nil)
		})
		if err != nil {
			t.Fatal(err)
		}
		if send && (!strings.Contains(out, `"linked_delivery"`) || !strings.Contains(out, `"message_id": "msg-1"`) || !strings.Contains(out, `"chat_session_id": "chat-1"`)) {
			t.Errorf("lost receipt: %s", out)
		}
	}
	if calls != 2 {
		t.Errorf("calls=%d", calls)
	}
}

func TestManagedChatSendRejectsUnsupportedExtraRoutes(t *testing.T) {
	for _, flag := range []string{"attachment", "wait"} {
		cmd := &cobra.Command{}
		cmd.Flags().String("linked", "studio", "")
		cmd.Flags().StringSlice("attachment", nil, "")
		cmd.Flags().Bool("wait", false, "")
		value := "true"
		if flag == "attachment" {
			value = "secret.txt"
		}
		_ = cmd.Flags().Set(flag, value)
		if err := runChatSend(cmd, []string{"hello"}); err == nil || !strings.Contains(err.Error(), "text only") {
			t.Fatalf("%s: %v", flag, err)
		}
	}
}

func TestCreateLinkManagedFlagDefaultAndOptOut(t *testing.T) {
	for _, managed := range []bool{true, false} {
		t.Run(fmt.Sprint(managed), func(t *testing.T) {
			called := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/projects" {
					_, _ = w.Write([]byte(`{"projects":[{"id":"019abcde-1234-7000-8000-000000000001","title":"Game"}]}`))
					return
				}
				if r.Method != http.MethodPost || r.URL.Path != "/api/workspace-links" {
					t.Errorf("unexpected %s %s", r.Method, r.URL)
				}
				called = true
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body["managed"] != managed {
					t.Errorf("managed=%v want %v", body["managed"], managed)
				}
				_, _ = w.Write([]byte(`{"id":"link-1","status":"pending"}`))
			}))
			defer srv.Close()
			t.Setenv("HOME", t.TempDir())
			t.Setenv("MULTICA_TOKEN", "mat_test-token")
			t.Setenv("MULTICA_WORKSPACE_ID", "source")
			t.Setenv("MULTICA_SERVER_URL", srv.URL)
			cmd := &cobra.Command{}
			cmd.Flags().Bool("managed", true, "")
			cmd.Flags().String("to", "studio", "")
			cmd.Flags().String("from", "", "")
			cmd.Flags().StringArray("project", []string{"019abcde-1234-7000-8000-000000000001"}, "")
			cmd.Flags().String("output", "json", "")
			if !managed {
				_ = cmd.Flags().Set("managed", "false")
			}
			if _, err := captureStdout(t, func() error { return runWorkspaceLinkCreate(cmd, nil) }); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("missing create request")
			}
		})
	}
	if workspaceLinkCreateCmd.Flags().Lookup("managed").DefValue != "true" {
		t.Fatal("create must default managed on")
	}
}

func TestManagedChatSendRejectsSteeringBeforeRequest(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("linked", "studio", "")
	cmd.Flags().String("session", "019abcde-1234-7000-8000-000000000001", "")
	cmd.Flags().String("mode", "restart", "")
	if err := runChatSend(cmd, []string{"hello"}); err == nil || !strings.Contains(err.Error(), "queue only") {
		t.Fatalf("restart: %v", err)
	}
}
