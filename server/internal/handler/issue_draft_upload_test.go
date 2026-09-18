package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/middleware"
)

// An alignment carrier's task token is read-only except for one write: the file
// it made for its own reply (DENE-590, middleware/issue_draft_scope.go). The
// middleware decides WHETHER that POST arrives; these tests pin what the
// handler does once it has, because the bindings that would send the row
// somewhere the user never confirmed — an issue, a comment, another session —
// are form fields no middleware can see.

// uploadAsCarrier posts a file with the carrier scope the middleware stamps,
// plus whichever binding fields the case is about.
func uploadAsCarrier(t *testing.T, agentID, actorTaskID string, fields map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "mock.html")
	if err != nil {
		t.Fatal(err)
	}
	part.Write([]byte("<!doctype html><title>mock</title>"))
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	writer.Close()

	req := httptest.NewRequest("POST", "/api/upload-file", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", actorTaskID)
	req.Header.Set(middleware.IssueDraftScopeHeader, middleware.IssueDraftScopeValue)
	w := httptest.NewRecorder()
	testHandler.UploadFile(w, req)
	return w
}

func TestUploadFile_IssueDraftCarrierScope(t *testing.T) {
	if testPool == nil {
		t.Skip("test database not available")
	}
	origStorage := testHandler.Storage
	testHandler.Storage = &mockStorage{}
	defer func() { testHandler.Storage = origStorage }()

	agentID := createHandlerTestAgent(t, "AlignmentCarrier", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	taskID := seedRunningChatTask(t, agentID, sessionID)

	// The point of the whole change: the carrier's prototype reaches its reply.
	t.Run("carrier uploads for its own reply", func(t *testing.T) {
		w := uploadAsCarrier(t, agentID, taskID, map[string]string{"task_id": taskID})
		if w.Code != http.StatusOK {
			t.Fatalf("carrier upload: status = %d, want 200: %s", w.Code, w.Body.String())
		}
		var resp AttachmentResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v; body: %s", err, w.Body.String())
		}
		t.Cleanup(func() {
			testPool.Exec(context.Background(), `DELETE FROM attachment WHERE id = $1`, resp.ID)
		})
		if resp.ChatSessionID == nil || *resp.ChatSessionID != sessionID {
			t.Fatalf("chat_session_id = %v, want the carrier's own session %s", resp.ChatSessionID, sessionID)
		}
	})

	// No task binding is a loose row: a file uploaded into the workspace that
	// belongs to no conversation, which is exactly the write the read-only
	// scope exists to refuse.
	t.Run("carrier upload without a task binding is refused", func(t *testing.T) {
		w := uploadAsCarrier(t, agentID, taskID, nil)
		if w.Code != http.StatusForbidden {
			t.Fatalf("unbound carrier upload: status = %d, want 403: %s", w.Code, w.Body.String())
		}
	})

	// The bindings that would put the row anywhere but the carrier's reply.
	for field, value := range map[string]string{
		"issue_id":        dbfx.Issue(t, "carrier upload target"),
		"chat_session_id": sessionID,
	} {
		t.Run("carrier upload bound to "+field+" is refused", func(t *testing.T) {
			w := uploadAsCarrier(t, agentID, taskID, map[string]string{
				"task_id": taskID,
				field:     value,
			})
			if w.Code != http.StatusForbidden {
				t.Fatalf("carrier upload with %s: status = %d, want 403: %s", field, w.Code, w.Body.String())
			}
		})
	}
}
