package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestChatTaskReason(t *testing.T) {
	offline := &ChatRuntimeResponse{Status: "offline"}
	online := &ChatRuntimeResponse{Status: "online"}
	for _, tc := range []struct {
		status  string
		runtime *ChatRuntimeResponse
		logs    bool
		ahead   int64
		failure string
		want    string
	}{
		{"queued", offline, false, 3, "", "runtime_offline"},
		{"running", online, false, 0, "", "running_no_logs"},
		{"running", online, true, 0, "", "running"},
		{"queued", online, false, 2, "", "queued_behind_2"},
		{"queued", online, false, 0, "", "awaiting_dispatch"},
		{"dispatched", online, false, 0, "", "dispatched"},
		{"failed", offline, false, 0, "provider_auth", "provider_auth"},
		{"completed", offline, false, 0, "", "completed"},
	} {
		task := db.AgentTaskQueue{Status: tc.status, FailureReason: pgtype.Text{String: tc.failure, Valid: tc.failure != ""}}
		if got := chatTaskReason(task, tc.runtime, tc.logs, tc.ahead); got != tc.want {
			t.Errorf("%s: got %s want %s", tc.status, got, tc.want)
		}
	}
}

func TestChatOfflineSendAndProgressDiagnostics(t *testing.T) {
	agentID := createHandlerTestAgent(t, "offline-chat-diagnostics", nil)
	sessionID := createHandlerTestChatSession(t, agentID)
	seen := time.Date(2026, 10, 11, 9, 0, 10, 0, time.UTC)
	runtimeID := dbfx.Runtime(t, "Claude (offline-test.local)", map[string]any{"status": "offline", "last_seen_at": seen})
	dbfx.Exec(t, `UPDATE agent SET runtime_id=$1 WHERE id=$2`, runtimeID, agentID)
	send := func() SendChatMessageResponse {
		req := withChatTestWorkspaceCtx(t, withURLParam(newRequest("POST", "/messages", map[string]any{"content": "hello"}), "sessionId", sessionID))
		w := httptest.NewRecorder()
		testHandler.SendChatMessage(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("send: %d %s", w.Code, w.Body.String())
		}
		var out SendChatMessageResponse
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.Runtime == nil || out.Runtime.Status != "offline" || out.Runtime.LastSeenAt == nil || !sameDiagnosticInstant(out.Runtime.LastSeenAt, seen) || !strings.Contains(out.Warning, "runtime_offline") {
			t.Fatalf("missing offline diagnostic: %+v", out)
		}
		return out
	}
	first := send()
	second := send()
	read := func(taskID string, wantCode int) ChatDiagnostics {
		req := withChatTestWorkspaceCtx(t, withURLParam(newRequest("GET", "/progress?task_id="+taskID, nil), "sessionId", sessionID))
		w := httptest.NewRecorder()
		testHandler.ListChatProgress(w, req)
		if w.Code != wantCode {
			t.Fatalf("progress: %d %s", w.Code, w.Body.String())
		}
		var out ChatDiagnostics
		if wantCode == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	if out := read(first.TaskID, http.StatusOK); out.TaskID != first.TaskID || out.TaskStatus != "queued" || out.Reason != "runtime_offline" {
		t.Fatalf("explicit task diagnostics: %+v", out)
	}
	dbfx.Exec(t, `UPDATE agent_runtime SET status='online' WHERE id=$1`, runtimeID)
	if out := read(second.TaskID, http.StatusOK); out.QueueAhead != 1 || out.Reason != "queued_behind_1" {
		t.Fatalf("queue: %+v", out)
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='running' WHERE id=$1`, first.TaskID)
	if out := read(first.TaskID, http.StatusOK); out.Reason != "running_no_logs" {
		t.Fatalf("running: %+v", out)
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='failed', failure_reason='provider_auth' WHERE id=$1`, second.TaskID)
	if out := read("", http.StatusOK); out.TaskID != second.TaskID || out.TaskStatus != "failed" || out.Reason != "provider_auth" {
		t.Fatalf("latest terminal: %+v", out)
	}
	other := createHandlerTestTaskForAgent(t, agentID)
	read(other, http.StatusNotFound)
	read("invalid", http.StatusBadRequest)
}

func sameDiagnosticInstant(value *string, want time.Time) bool {
	if value == nil {
		return false
	}
	parsed, err := time.Parse(time.RFC3339, *value)
	return err == nil && parsed.Equal(want)
}
