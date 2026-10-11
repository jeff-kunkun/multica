package main

import (
	"encoding/json"
	"github.com/multica-ai/multica/server/internal/cli"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunChatNewCreatesSendsAndWaits(t *testing.T) {
	t.Chdir(t.TempDir())
	const agentID = "5a0c459c-483e-4353-b03c-c3d3f260b6be"
	var created map[string]any
	var sentContent string
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/agents":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": agentID, "name": "小助手"}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions":
			_ = json.NewDecoder(r.Body).Decode(&created)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "s-1", "title": "算术"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions/s-1/messages":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			sentContent = body["content"]
			_ = json.NewEncoder(w).Encode(map[string]any{"message_id": "m-1", "task_id": "t-1"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/chat/sessions/s-1/messages":
			msgs := []map[string]any{{"id": "m-1", "role": "user", "content": "1+1?"}}
			if polls.Add(1) > 1 {
				msgs = append(msgs,
					map[string]any{"id": "m-0", "role": "assistant", "content": "old", "task_id": "t-0"},
					map[string]any{"id": "m-2", "role": "assistant", "content": "等于 2", "task_id": "t-1"})
			}
			_ = json.NewEncoder(w).Encode(msgs)
		default:
			http.Error(w, r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	clearAgentContext(t)
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "mul_test")
	prev := chatReplyPollInterval
	chatReplyPollInterval = time.Millisecond
	defer func() { chatReplyPollInterval = prev }()

	cmd := chatNewCmd
	for k, v := range map[string]string{"agent": "小助手", "title": "算术", "wait": "true", "output": "json", "reference": referenceLink + ":" + referenceProject} {
		if err := cmd.Flags().Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		_ = cmd.Flags().Set("reference", "")
		cmd.Flags().Lookup("reference").Changed = false
		_ = cmd.Flags().Set("agent", "")
		_ = cmd.Flags().Set("title", "")
		_ = cmd.Flags().Set("wait", "false")
	}()
	if err := runChatNew(cmd, []string{"1+1?"}); err != nil {
		t.Fatalf("chat new: %v", err)
	}
	if created["agent_id"] != agentID || created["title"] != "算术" {
		t.Fatalf("create body = %v", created)
	}
	refs, _ := created["linked_projects"].([]any)
	if len(refs) != 1 {
		t.Fatalf("references: %v", created)
	}
	ref := refs[0].(map[string]any)
	if ref["link_id"] != referenceLink || ref["project_id"] != referenceProject {
		t.Fatalf("reference: %v", ref)
	}
	if sentContent != "1+1?" {
		t.Fatalf("sent %q", sentContent)
	}
	if polls.Load() < 2 {
		t.Fatalf("expected to poll until the reply arrived, polled %d", polls.Load())
	}
}

func TestRunChatNewRefusesAgentRuns(t *testing.T) {
	t.Setenv("MULTICA_AGENT_ID", "a-1")
	t.Setenv("MULTICA_TASK_ID", "t-1")
	err := runChatNew(chatNewCmd, []string{"hi"})
	if err == nil || !strings.Contains(err.Error(), "chat open") {
		t.Fatalf("err = %v, want refusal pointing at chat open", err)
	}
}

func clearAgentContext(t *testing.T) {
	t.Helper()
	for _, k := range []string{"MULTICA_AGENT_ID", "MULTICA_TASK_ID"} {
		t.Setenv(k, "")
	}
}

// A timed-out send must diagnose its own task even when a newer turn exists.
func TestWaitChatReplyTimeoutReadsTaskDiagnostics(t *testing.T) {
	var queriedTask string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/messages") {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/progress") {
			queriedTask = r.URL.Query().Get("task_id")
			_, _ = w.Write([]byte(`{"progress":[],"task_id":"t-1","task_status":"queued","reason":"runtime_offline","queue_ahead":0,"runtime":{"status":"offline","name":"Claude (test)","last_seen_at":"2026-10-11T09:00:10Z"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	client := cli.NewAPIClient(srv.URL, "ws-1", "test")
	out, err := waitChatReply(client, "s-1", "t-1", 0)
	if err == nil || !strings.Contains(err.Error(), "task_status=queued reason=runtime_offline") {
		t.Fatalf("timeout error: %v", err)
	}
	if queriedTask != "t-1" || out["task_status"] != "queued" || out["reason"] != "runtime_offline" || out["runtime"] == nil {
		t.Fatalf("task=%s output=%v", queriedTask, out)
	}
	if _, ok := out["progress"]; ok {
		t.Fatal("timeout output must not include history")
	}
}
