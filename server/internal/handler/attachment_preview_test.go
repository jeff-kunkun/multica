package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
)

// previewTestSetup installs an in-memory seekable mockStorage (the local-disk
// shape) on testHandler and seeds an HTML attachment. It returns the storage
// and the attachment id.
func previewTestSetup(t *testing.T, filename, contentType string, body []byte) (*seekableMockStorage, string) {
	t.Helper()
	store := &seekableMockStorage{}
	origStorage := testHandler.Storage
	origCfg := testHandler.cfg
	testHandler.Storage = store
	t.Cleanup(func() {
		testHandler.Storage = origStorage
		testHandler.cfg = origCfg
	})
	id := seedPreviewAttachment(t, &store.mockStorage, "previews/"+filename, filename, contentType, body)
	return store, id
}

func newPreviewRouteRequest(t *testing.T, path string, withAuth bool) (*http.Request, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if withAuth {
		req.Header.Set("X-User-ID", testUserID)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
	}
	rctx := chi.NewRouteContext()
	if idx := strings.LastIndexByte(path, '/'); idx >= 0 {
		rctx.URLParams.Add("id", path[strings.LastIndexByte(path[:idx], '/')+1:idx])
	}
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	return req, httptest.NewRecorder()
}

func previewCapabilityQuery(t *testing.T, attachmentID string, ttl time.Duration) string {
	t.Helper()
	cap := attachmentPreviewCapabilityPath(attachmentID, time.Now(), ttl)
	u, err := url.Parse(cap)
	if err != nil {
		t.Fatalf("parse capability path %q: %v", cap, err)
	}
	return "?" + u.RawQuery
}

func requireSandboxPreviewHeaders(t *testing.T, header http.Header, wantContentType, wantDispositionPrefix string) {
	t.Helper()
	if got, want := header.Get("Content-Type"), wantContentType; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
	disposition := header.Get("Content-Disposition")
	if !strings.HasPrefix(disposition, wantDispositionPrefix) {
		t.Fatalf("Content-Disposition = %q, want it to start with %s", disposition, wantDispositionPrefix)
	}
	if got, want := header.Get("Cache-Control"), "no-store"; got != want {
		t.Fatalf("Cache-Control = %q, want %q", got, want)
	}
	if got, want := header.Get("Referrer-Policy"), "no-referrer"; got != want {
		t.Fatalf("Referrer-Policy = %q, want %q", got, want)
	}
	csp := header.Get("Content-Security-Policy")
	for _, want := range []string{
		"sandbox allow-scripts",
		"default-src 'none'",
		"connect-src 'none'",
		"object-src 'none'",
		"base-uri 'none'",
		"form-action 'none'",
		"frame-ancestors 'self'",
	} {
		if !strings.Contains(csp, want) {
			t.Fatalf("preview Content-Security-Policy missing %q; got %q", want, csp)
		}
	}
	if strings.Contains(csp, "allow-same-origin") {
		t.Fatalf("preview Content-Security-Policy must NOT grant allow-same-origin (it would expose the deployment's cookies); got %q", csp)
	}
}

func TestPreviewAttachment_CapabilityServesInlineHTML(t *testing.T) {
	body := []byte("<!doctype html><html><body><h1>weekly report</h1><script>render()</script></body></html>")
	_, id := previewTestSetup(t, "weekly report.html", "text/html", body)

	path := "/api/attachments/" + id + "/preview" + previewCapabilityQuery(t, id, 0)
	req, w := newPreviewRouteRequest(t, path, false)
	testHandler.PreviewAttachment(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("capability preview = %d, body %s", w.Code, w.Body.String())
	}
	requireSandboxPreviewHeaders(t, w.Header(), "text/html; charset=utf-8", "inline")
	if w.Body.String() != string(body) {
		t.Fatalf("preview body = %q, want the stored bytes", w.Body.String())
	}
}

// A mislabeled upload (sniffed as a binary blob) whose filename says .html
// must still render: the mobile browser needs text/html; charset=utf-8 to
// display the document instead of offering a download.
func TestPreviewAttachment_PinsHTMLContentTypeFromFilename(t *testing.T) {
	_, id := previewTestSetup(t, "report.htm", "application/octet-stream", []byte("<html><body>ok</body></html>"))

	path := "/api/attachments/" + id + "/preview" + previewCapabilityQuery(t, id, 0)
	req, w := newPreviewRouteRequest(t, path, false)
	testHandler.PreviewAttachment(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("capability preview = %d, body %s", w.Code, w.Body.String())
	}
	requireSandboxPreviewHeaders(t, w.Header(), "text/html; charset=utf-8", "inline")
}

func TestPreviewAttachment_NonHTMLForcesAttachment(t *testing.T) {
	_, id := previewTestSetup(t, "notes.md", "text/markdown", []byte("# notes"))

	path := "/api/attachments/" + id + "/preview" + previewCapabilityQuery(t, id, 0)
	req, w := newPreviewRouteRequest(t, path, false)
	testHandler.PreviewAttachment(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("capability preview = %d, body %s", w.Code, w.Body.String())
	}
	// Non-HTML never renders inline on /preview: stored type kept, forced
	// download disposition (DENE-2225).
	requireSandboxPreviewHeaders(t, w.Header(), "text/markdown", "attachment")
}

func TestPreviewAttachment_ExpiredCapabilityFallsBackToAuth(t *testing.T) {
	_, id := previewTestSetup(t, "report.html", "text/html", []byte("<html></html>"))

	// Expired capability, no session: rejected with the same 401 shape the
	// download route uses.
	stale := attachmentPreviewCapabilityPath(id, time.Now().Add(-2*defaultAttachmentPreviewTTL), 0)
	req, w := newPreviewRouteRequest(t, stale, false)
	testHandler.PreviewAttachment(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expired capability without auth = %d, want 401; body %s", w.Code, w.Body.String())
	}

	// Expired capability WITH a member session: the bare /preview URL must
	// keep working for logged-in members.
	req, w = newPreviewRouteRequest(t, stale, true)
	testHandler.PreviewAttachment(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expired capability with member session = %d, body %s", w.Code, w.Body.String())
	}
	requireSandboxPreviewHeaders(t, w.Header(), "text/html; charset=utf-8", "inline")
}

// A signature minted for another intent (here: the forced-attachment download
// capability) must not redeem as a preview. The intents are domain-separated
// in the signed message, so replaying it fails closed to the auth fallback.
func TestPreviewAttachment_DownloadIntentSignatureCannotRedeem(t *testing.T) {
	_, id := previewTestSetup(t, "report.html", "text/html", []byte("<html></html>"))

	dl := attachmentDownloadCapabilityPath(id, time.Now())
	req, w := newPreviewRouteRequest(t, dl, false)
	testHandler.PreviewAttachment(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("download capability on /preview = %d, want 401; body %s", w.Code, w.Body.String())
	}
}

func TestPreviewAttachment_ForgedCapabilityCannotRedeem(t *testing.T) {
	_, id := previewTestSetup(t, "report.html", "text/html", []byte("<html></html>"))

	forged := "/api/attachments/" + id + "/preview?exp=9999999999&sig=deadbeef"
	req, w := newPreviewRouteRequest(t, forged, false)
	testHandler.PreviewAttachment(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("forged capability = %d, want 401; body %s", w.Code, w.Body.String())
	}
}

func TestPreviewAttachment_UnknownAttachmentIs404(t *testing.T) {
	_, _ = previewTestSetup(t, "report.html", "text/html", []byte("<html></html>"))

	path := "/api/attachments/11111111-2222-3333-4444-555555555555/preview" + previewCapabilityQuery(t, "11111111-2222-3333-4444-555555555555", 0)
	req, w := newPreviewRouteRequest(t, path, false)
	testHandler.PreviewAttachment(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown attachment = %d, want 404; body %s", w.Code, w.Body.String())
	}
}

// The original download route keeps forcing a file download for HTML — the
// preview link is additive, it does not silently change /download. Seeded on a
// private object host so the download proxies (the office deployment's mode).
func TestDownloadAttachment_HTMLStillForcesAttachment(t *testing.T) {
	store := &mockStorage{}
	origStorage := testHandler.Storage
	origCfg := testHandler.cfg
	testHandler.Storage = store
	t.Cleanup(func() {
		testHandler.Storage = origStorage
		testHandler.cfg = origCfg
	})

	key := "previews/report.html"
	body := []byte("<html><body>hi</body></html>")
	store.put(key, body)
	id := seedAttachmentURL(t, "http://rustfs:9000/test-bucket/"+key, "report.html", "text/html", int64(len(body)))

	req, w := newDownloadRequest(t, id, testWorkspaceID)
	testHandler.DownloadAttachment(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("download = %d, body %s", w.Code, w.Body.String())
	}
	disposition := w.Header().Get("Content-Disposition")
	if !strings.HasPrefix(disposition, "attachment") {
		t.Fatalf("Content-Disposition = %q, want it to force attachment", disposition)
	}
	if got := w.Header().Get("Content-Type"); got != "text/html" {
		t.Fatalf("Content-Type = %q, want stored text/html", got)
	}
	csp := w.Header().Get("Content-Security-Policy")
	if strings.Contains(csp, "sandbox") {
		t.Fatalf("/download must not carry the preview sandbox CSP; got %q", csp)
	}
}

func TestGetAttachmentByID_MintsPreviewURL(t *testing.T) {
	body := []byte("<html><body>ok</body></html>")
	_, id := previewTestSetup(t, "report.html", "text/html", body)

	req := httptest.NewRequest(http.MethodGet, "/api/attachments/"+id, nil)
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	testHandler.GetAttachmentByID(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/attachments/{id} = %d, body %s", w.Code, w.Body.String())
	}
	var resp AttachmentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.PreviewURL == "" {
		t.Fatal("single-attachment response must mint preview_url")
	}
	u, err := url.Parse(resp.PreviewURL)
	if err != nil || u.Path != "/api/attachments/"+id+"/preview" {
		t.Fatalf("preview_url = %q, want the /preview capability path", resp.PreviewURL)
	}

	// The minted link must actually redeem, without any session.
	redemption := httptest.NewRequest("GET", resp.PreviewURL, nil)
	rctx = chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	redemption = redemption.WithContext(context.WithValue(redemption.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	testHandler.PreviewAttachment(rec, redemption)
	if rec.Code != http.StatusOK {
		t.Fatalf("minted preview_url = %d, body %s", rec.Code, rec.Body.String())
	}
	requireSandboxPreviewHeaders(t, rec.Header(), "text/html; charset=utf-8", "inline")

	// List-path responses never mint it: preview_url is a bearer link.
	att := loadTestAttachment(t, id)
	if got := testHandler.attachmentToResponse(att, attachmentURLModeSigned).PreviewURL; got != "" {
		t.Fatalf("attachmentToResponse must not embed a preview_url; got %q", got)
	}
}

func TestAttachmentSandboxPreviewCSPHeader_ConfiguredAncestors(t *testing.T) {
	csp := attachmentSandboxPreviewCSPHeader([]string{"https://app.example.test", "*", "javascript:alert(1)"})
	if !strings.Contains(csp, "frame-ancestors 'self' https://app.example.test") {
		t.Fatalf("sandbox CSP frame-ancestors = %q, want 'self' plus the configured origin", csp)
	}
	if strings.Contains(csp, "*") || strings.Contains(csp, "javascript:") {
		t.Fatalf("sandbox CSP must not admit wildcard or javascript sources; got %q", csp)
	}
}

func TestAttachmentPreviewTTL_DefaultsAndOverride(t *testing.T) {
	if got := attachmentPreviewTTL(0); got != defaultAttachmentPreviewTTL {
		t.Fatalf("attachmentPreviewTTL(0) = %v, want the default", got)
	}
	if got := attachmentPreviewTTL(-time.Hour); got != defaultAttachmentPreviewTTL {
		t.Fatalf("attachmentPreviewTTL(-1h) = %v, want the default", got)
	}
	if got, want := attachmentPreviewTTL(72*time.Hour), 72*time.Hour; got != want {
		t.Fatalf("attachmentPreviewTTL(72h) = %v, want %v", got, want)
	}
}

func TestAttachmentPreviewContentType(t *testing.T) {
	cases := []struct {
		contentType, filename, want string
	}{
		{"text/html", "report.html", "text/html; charset=utf-8"},
		{"", "report.html", "text/html; charset=utf-8"},
		{"application/octet-stream", "report.htm", "text/html; charset=utf-8"},
		{"text/html; charset=gbk", "x.html", "text/html; charset=utf-8"},
		{"text/markdown", "notes.md", "text/markdown"},
		{"", "archive.zip", "application/octet-stream"},
		{"application/pdf", "deck.pdf", "application/pdf"},
	}
	for _, tc := range cases {
		if got := attachmentPreviewContentType(tc.contentType, tc.filename); got != tc.want {
			t.Fatalf("attachmentPreviewContentType(%q, %q) = %q, want %q", tc.contentType, tc.filename, got, tc.want)
		}
	}
}

// servePreviewThroughRoute runs the composed public /preview chain — the real
// OptionalAuth middleware in front of the handler, as router.go mounts it —
// instead of the handler in isolation. The tests below are about the chain:
// identity must come from a validated credential, never from the request.
func servePreviewThroughRoute(w http.ResponseWriter, r *http.Request) {
	middleware.OptionalAuth(nil, nil, nil, nil)(http.HandlerFunc(testHandler.PreviewAttachment)).ServeHTTP(w, r)
}

// The regression test for the auth bypass: a client-supplied X-User-ID naming a
// real workspace member must not read the attachment. Before the fix the public
// route trusted that header outright; now OptionalAuth strips it and the handler
// sees no identity, so the member can neither be impersonated nor read.
func TestPreviewAttachment_ForgedUserHeaderCannotRead(t *testing.T) {
	_, id := previewTestSetup(t, "report.html", "text/html", []byte("<html></html>"))

	req, w := newPreviewRouteRequest(t, "/api/attachments/"+id+"/preview", false)
	// testUserID is the owner of the attachment's workspace, so this header
	// is exactly what used to pass the membership check.
	req.Header.Set("X-User-ID", testUserID)
	servePreviewThroughRoute(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("forged X-User-ID read the attachment: %d, body %s", w.Code, w.Body.String())
	}
}

// A bare /preview URL with a valid session must keep working — the route falls
// back to session auth when there is no capability. The session is honoured
// only because it validates through the same code /download uses.
func TestPreviewAttachment_MemberSessionReadsBareURL(t *testing.T) {
	_, id := previewTestSetup(t, "report.html", "text/html", []byte("<html></html>"))

	req, w := newPreviewRouteRequest(t, "/api/attachments/"+id+"/preview", false)
	req.Header.Set("Authorization", "Bearer "+refreshSessionToken(t, time.Hour, ""))
	// A forged header alongside a real credential must lose to the token.
	req.Header.Set("X-User-ID", "00000000-0000-0000-0000-000000000000")
	servePreviewThroughRoute(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("bare /preview with a member session = %d, body %s", w.Code, w.Body.String())
	}
	requireSandboxPreviewHeaders(t, w.Header(), "text/html; charset=utf-8", "inline")
}

// A signed capability must still redeem with no session at all — the phone
// share-link path. This is why a missing credential is anonymous rather than
// rejected on this route.
func TestPreviewAttachment_CapabilityRedeemsThroughMiddleware(t *testing.T) {
	body := []byte("<html><body>shared</body></html>")
	_, id := previewTestSetup(t, "report.html", "text/html", body)

	req, w := newPreviewRouteRequest(t, "/api/attachments/"+id+"/preview"+previewCapabilityQuery(t, id, 0), false)
	// Even with a bogus identity header attached, the capability is what grants.
	req.Header.Set("X-User-ID", "00000000-0000-0000-0000-000000000000")
	servePreviewThroughRoute(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("capability preview through OptionalAuth = %d, body %s", w.Code, w.Body.String())
	}
	requireSandboxPreviewHeaders(t, w.Header(), "text/html; charset=utf-8", "inline")
	if w.Body.String() != string(body) {
		t.Fatalf("preview body = %q, want the stored bytes", w.Body.String())
	}
}
