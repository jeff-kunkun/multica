package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/mcpaccess"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// A remote grant is narrower than its owner's membership: a person can belong
// to both workspaces, while their plugin can only read files in the grant's one
// workspace. Exercise the real mmc_ authentication branch and internal transport,
// including the attachment-self-resolving download path and warm member cache.
func TestMCPAttachmentGrantRejectsForeignWorkspace(t *testing.T) {
	f := newMCPFixture(t)
	_, code := f.create(t)
	token := f.exchange(t, code)
	foreign := dbfx.Workspace(t, "MCP foreign files", "mcp-files-"+randomID(), testutil.Cols{"issue_prefix": "MCF"})
	dbfx.Member(t, foreign, testUserID, "owner")
	oldStorage, oldConfig, oldSigner := testHandler.Storage, testHandler.cfg, testHandler.CFSigner
	storage := &mockStorage{}
	testHandler.Storage = storage
	testHandler.cfg.AttachmentDownloadMode = "proxy"
	testHandler.CFSigner = nil
	t.Cleanup(func() { testHandler.Storage, testHandler.cfg, testHandler.CFSigner = oldStorage, oldConfig, oldSigner })
	fixture := func(ws, key, body string) string {
		storage.put(key, []byte(body))
		return dbfx.Insert(t, "attachment", testutil.Cols{"workspace_id": ws, "uploader_type": "member", "uploader_id": testUserID, "filename": key, "url": "https://s3.example.com/test-bucket/" + key, "content_type": "text/plain", "size_bytes": len(body)})
	}
	foreignAttachment := fixture(foreign, "foreign-secret.txt", "foreign-workspace-file-secret")
	localAttachment := fixture(testWorkspaceID, "permitted.txt", "bound-workspace-file")
	router := chi.NewRouter()
	router.Use(middleware.Auth(testHandler.Queries, nil, nil, nil))
	router.Get("/api/attachments/{id}/download", testHandler.DownloadAttachment)
	router.With(middleware.RequireWorkspaceMember(testHandler.Queries)).Get("/api/attachments/{id}", testHandler.GetAttachmentByID)
	internal := &http.Client{Transport: mcpaccess.Transport{Handler: router}}
	read := func(path, credential string, inside bool) (int, string) {
		req := httptest.NewRequest("GET", "http://multica.internal"+path, nil)
		req.Header.Set("Authorization", "Bearer "+credential)
		// Deliberately forge every request hint; Auth must overwrite the binding.
		req.Header.Set("X-Workspace-ID", foreign)
		req.Header.Set("X-Workspace-Slug", "forged-foreign")
		req.Header.Set("X-Actor-Source", "personal_token")
		if inside {
			req.RequestURI = ""
			response, err := internal.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			return response.StatusCode, string(body)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response.Code, response.Body.String()
	}
	// Prove the foreign file exists and this person can read it, also warming
	// MembershipCache so the plugin cannot slip through its cache fast path.
	if status, body := read("/api/attachments/"+foreignAttachment+"/download?raw=1", f.human, false); status != 200 || body != "foreign-workspace-file-secret" {
		t.Fatalf("person control status=%d body=%s", status, body)
	}
	for _, suffix := range []string{"", "/download?raw=1"} {
		if status, body := read("/api/attachments/"+foreignAttachment+suffix, token, true); status != 404 || strings.Contains(body, "foreign-workspace-file-secret") || strings.Contains(body, "foreign-secret.txt") {
			t.Fatalf("foreign attachment escaped grant status=%d body=%s", status, body)
		}
		// A raw network REST request cannot reuse the plugin credential at all.
		if status, _ := read("/api/attachments/"+localAttachment+suffix, token, false); status != 403 {
			t.Fatalf("network REST audience accepted status=%d", status)
		}
	}
	if status, body := read("/api/attachments/"+localAttachment+"/download?raw=1", token, true); status != 200 || body != "bound-workspace-file" {
		t.Fatalf("bound download status=%d body=%s", status, body)
	}
	if status, body := read("/api/attachments/"+localAttachment, token, true); status != 200 || !strings.Contains(body, localAttachment) {
		t.Fatalf("bound metadata status=%d body=%s", status, body)
	}
}
