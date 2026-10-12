package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestAttachmentRawUploadDownload(t *testing.T) {
	requireDB(t)
	t.Setenv("LOCAL_UPLOAD_DIR", t.TempDir())
	t.Setenv("LOCAL_UPLOAD_BASE_URL", "")
	local := storage.NewLocalStorageFromEnv()
	h := *testHandler
	h.Storage = local
	// A normal request would choose a CDN redirect; raw must read storage.
	h.cfg.AttachmentDownloadMode = "cloudfront"
	h.CFSigner = nil
	for _, f := range []struct{ name, contentType, body string }{
		{"report.html", "text/html; charset=utf-8", "<!doctype html><p>字节</p>"},
		{"report.pdf", "application/pdf", "%PDF-1.7\x00\xff\r\n"},
		{"shot.png", "image/png", "\x89PNG\r\n\x1a\n\x00\xff"},
		{"report.txt", "text/plain; charset=utf-8", "文本\r\n"},
		// DENE-1946: over 1 MiB, the size where CDN rewriting of previews
		// (injected scripts, re-encoding) first showed up as size/sha256 drift.
		{"large.html", "text/html; charset=utf-8", strings.Repeat("<!doctype html><p>字节</p>", 60000)},
		{"large.pdf", "application/pdf", strings.Repeat("%PDF-1.7\x00\xff\r\n", 150000)},
	} {
		t.Run(f.name, func(t *testing.T) {
			data := []byte(f.body)
			digest := fmt.Sprintf("%x", sha256.Sum256(data))
			var body bytes.Buffer
			mw := multipart.NewWriter(&body)
			part, err := mw.CreateFormFile("file", f.name)
			if err != nil {
				t.Fatal(err)
			}
			part.Write(data)
			mw.WriteField("sha256", digest)
			mw.Close()
			req := httptest.NewRequest("POST", "/api/upload-file", &body)
			req.Header.Set("Content-Type", mw.FormDataContentType())
			req.Header.Set("X-User-ID", testUserID)
			req.Header.Set("X-Workspace-ID", testWorkspaceID)
			w := httptest.NewRecorder()
			h.UploadFile(w, req)
			if w.Code != 200 {
				t.Fatalf("upload=%d %s", w.Code, w.Body.String())
			}
			var att AttachmentResponse
			if err := json.Unmarshal(w.Body.Bytes(), &att); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM attachment WHERE id=$1`, att.ID) })
			if att.SHA256 != digest || att.SizeBytes != int64(len(data)) || att.ContentType != f.contentType {
				t.Fatalf("metadata=%+v", att)
			}
			stored, err := local.GetReader(context.Background(), local.KeyFromURL(att.URL))
			if err != nil {
				t.Fatal(err)
			}
			actual, err := io.ReadAll(stored)
			stored.Close()
			if err != nil || !bytes.Equal(actual, data) {
				t.Fatalf("stored object=%q error=%v", actual, err)
			}
			req, w = newDownloadRequest(t, att.ID, testWorkspaceID)
			h.GetAttachmentByID(w, req)
			if w.Code != 200 {
				t.Fatalf("metadata=%d %s", w.Code, w.Body.String())
			}
			if err := json.Unmarshal(w.Body.Bytes(), &att); err != nil {
				t.Fatal(err)
			}
			if att.RawDownloadURL != "/api/attachments/"+att.ID+"/download?raw=1" {
				t.Fatalf("raw URL=%s", att.RawDownloadURL)
			}
			req, w = newDownloadRequest(t, att.ID, testWorkspaceID)
			req.URL.RawQuery = "raw=1"
			h.DownloadAttachment(w, req)
			if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) {
				t.Fatalf("raw=%d %q", w.Code, w.Body.Bytes())
			}
			if w.Header().Get("Content-Type") != "application/octet-stream" || w.Header().Get("Cache-Control") != "no-store, no-transform" || w.Header().Get("Location") != "" {
				t.Fatalf("raw headers=%v", w.Header())
			}
		})
	}
}

func TestListCommentAttachmentsVisibility(t *testing.T) {
	requireDB(t)
	author := visibilityTestMember(t, "attachment author", "attachment-author@multica.ai")
	stranger := visibilityTestMember(t, "attachment stranger", "attachment-stranger@multica.ai")
	issueID := dbfx.Issue(t, "private attachments", testutil.Cols{"creator_type": "member", "creator_id": author, "visibility": "private"})
	commentID := seedCommentRow(t, issueID, time.Now(), "files", nil, nil)
	attachmentID := dbfx.Insert(t, "attachment", testutil.Cols{"workspace_id": testWorkspaceID, "issue_id": issueID, "comment_id": commentID, "uploader_type": "member", "uploader_id": author, "filename": "report.html", "url": "https://example.test/report.html", "content_type": "text/html; charset=utf-8", "size_bytes": 12})
	var attachments []AttachmentResponse
	testutil.Call(t, testHandler.ListCommentAttachments, withURLParam(newRequestAs(author, "GET", "/api/comments/"+commentID+"/attachments", nil), "commentId", commentID)).Want(200).JSON(&attachments)
	if len(attachments) != 1 || attachments[0].ID != attachmentID {
		t.Fatalf("attachments=%+v", attachments)
	}
	testutil.Call(t, testHandler.ListCommentAttachments, withURLParam(newRequestAs(stranger, "GET", "/api/comments/"+commentID+"/attachments", nil), "commentId", commentID)).Want(404)
	req, w := newDownloadRequest(t, attachmentID, testWorkspaceID)
	req.Header.Set("X-User-ID", stranger)
	req.URL.RawQuery = "raw=1"
	testHandler.DownloadAttachment(w, req)
	if w.Code != 404 {
		t.Fatalf("raw private attachment leaked: %d", w.Code)
	}
	dbfx.Exec(t, `UPDATE comment SET deleted_at=now() WHERE id=$1`, commentID)
	testutil.Call(t, testHandler.ListCommentAttachments, withURLParam(newRequestAs(author, "GET", "/api/comments/"+commentID+"/attachments", nil), "commentId", commentID)).Want(404)
}
