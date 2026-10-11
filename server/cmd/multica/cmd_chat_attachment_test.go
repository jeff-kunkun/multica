package main

import (
	"encoding/json"
	"github.com/spf13/cobra"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestChatSendAttachmentsBindAndPreflight(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("report.log", []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	uploads, sends := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/upload-file" {
			uploads++
			if err := r.ParseMultipartForm(1024); err != nil {
				t.Error(err)
			}
			if r.FormValue("task_id") != "" {
				t.Error("personal upload must not fabricate task identity")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "attachment-1", "url": "/file", "markdown_url": "/file"})
			return
		}
		sends++
		var body struct {
			AttachmentIDs []string `json:"attachment_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.AttachmentIDs) != 1 || body.AttachmentIDs[0] != "attachment-1" {
			t.Errorf("bound ids: %v", body.AttachmentIDs)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"message_id": "message-1"})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_TASK_ID", "")
	cmd := &cobra.Command{}
	cmd.Flags().String("session", "019ec09d-6222-722b-bdfa-427b105d80be", "")
	cmd.Flags().String("mode", "queue", "")
	cmd.Flags().String("output", "json", "")
	cmd.Flags().String("content-file", "", "")
	cmd.Flags().Bool("allow-external-file", false, "")
	cmd.Flags().StringSlice("attachment", []string{"report.log", "missing.log"}, "")
	if err := runChatSend(cmd, []string{"hello"}); err == nil {
		t.Fatal("missing second file accepted")
	}
	if uploads != 0 || sends != 0 {
		t.Fatal("preflight failure wrote data")
	}
	_ = cmd.Flags().Set("attachment", "report.log")
	if err := runChatSend(cmd, []string{"hello"}); err != nil {
		t.Fatal(err)
	}
	if uploads != 1 || sends != 1 {
		t.Fatalf("uploads=%d sends=%d", uploads, sends)
	}
}
