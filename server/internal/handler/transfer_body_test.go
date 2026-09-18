package handler

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// DENE-442: a transfer request body may arrive gzip-compressed. The cap that
// makes the endpoints safe has to apply to the DECODED body, or a small gzip
// body would walk straight past it.

func gzipBody(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func transferBodyRequest(t *testing.T, body []byte, encoding string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces/ws/transfer/issues", bytes.NewReader(body))
	if encoding != "" {
		req.Header.Set("Content-Encoding", encoding)
	}
	return req
}

func TestOpenTransferBody_DecodesGzip(t *testing.T) {
	raw := []byte(strings.Repeat(`{"issues":[]}`, 64))
	req := transferBodyRequest(t, gzipBody(t, raw), "gzip")
	rec := httptest.NewRecorder()

	body, err := openTransferBody(rec, req, 1<<20)
	if err != nil {
		t.Fatalf("openTransferBody: %v", err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("decoded %d bytes, want the %d original ones", len(got), len(raw))
	}
}

// An instance that predates the handshake never sends the header, and its body
// must keep working untouched.
func TestOpenTransferBody_LeavesAnUncompressedBodyAlone(t *testing.T) {
	raw := []byte(`{"issues":[]}`)
	req := transferBodyRequest(t, raw, "")
	rec := httptest.NewRecorder()

	body, err := openTransferBody(rec, req, 1<<20)
	if err != nil {
		t.Fatalf("openTransferBody: %v", err)
	}
	defer body.Close()
	got, _ := io.ReadAll(body)
	if !bytes.Equal(got, raw) {
		t.Fatalf("decoded %q, want %q", got, raw)
	}
}

// The header is a statement the client makes about its own body; a body that
// does not honor it is a client bug, and saying so is better than feeding the
// compressed bytes to the JSON decoder.
func TestOpenTransferBody_RejectsAHeaderThatDoesNotMatchTheBody(t *testing.T) {
	req := transferBodyRequest(t, []byte("this is not gzip"), "gzip")
	rec := httptest.NewRecorder()

	_, err := openTransferBody(rec, req, 1<<20)
	if err == nil {
		t.Fatal("want an error for a body that is not the gzip it claims to be")
	}
	status, code, _, ok := transferBodyErrorStatus(err)
	if !ok || status != http.StatusBadRequest || code != "transfer_bundle_invalid" {
		t.Fatalf("error maps to status=%d code=%q ok=%v", status, code, ok)
	}
}

// The cap is on the decoded body, which is the only number that bounds what the
// handler allocates. A 21 MiB body of zeros compresses to a few KiB, so a cap
// on the wire bytes would let this through.
func TestOpenTransferBody_CapsTheDecodedBodyNotTheWireBytes(t *testing.T) {
	const limit = 1 << 20
	raw := bytes.Repeat([]byte{'a'}, limit+1024)
	encoded := gzipBody(t, raw)
	if len(encoded) >= limit {
		t.Fatalf("fixture is not a compression bomb: %d encoded bytes", len(encoded))
	}
	req := transferBodyRequest(t, encoded, "gzip")
	rec := httptest.NewRecorder()

	body, err := openTransferBody(rec, req, limit)
	if err != nil {
		t.Fatalf("openTransferBody: %v", err)
	}
	defer body.Close()
	if _, err := io.ReadAll(body); err == nil {
		t.Fatal("want the read to fail once the decoded body passes the cap")
	}
}

// The endpoint-level version of the same rule, through the real handler: a
// compressed payload whose decoded size is over the config cap is refused with
// a readable 413 rather than being parsed.
func TestImportWorkspaceTransferConfig_RejectsAnOversizedDecodedBody(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	// Pads the decoded body past the 20 MiB cap while staying tiny on the wire.
	raw, err := json.Marshal(map[string]any{
		"dry_run":     true,
		"on_conflict": "skip",
		"config": map[string]any{
			"format":         service.ConfigBundleFormat,
			"schema_version": 1,
			"bundle_id":      uuid.NewString(),
			"exported_at":    "2026-09-15T00:00:00Z",
			"source":         map[string]any{"workspace_id": uuid.NewString(), "slug": "x", "name": "x", "exported_by": testUserID},
			"entities":       map[string]any{},
			"padding":        strings.Repeat("a", service.ConfigBundleMaxBytes+1024),
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	encoded := gzipBody(t, raw)
	if len(encoded) > 1<<20 {
		t.Fatalf("fixture should be small on the wire, got %d bytes", len(encoded))
	}

	req := testutil.WithURLParams(
		testutil.WithHeaders(transferBodyRequest(t, encoded, "gzip"), "X-User-ID", testUserID),
		"id", dst,
	)
	resp := testutil.Call(t, testHandler.ImportWorkspaceTransferConfig, req)
	resp.Want(http.StatusRequestEntityTooLarge)
	if code := resp.Map()["code"]; code != "config_bundle_too_large" {
		t.Fatalf("code=%v body=%s", code, resp.Text())
	}
}

// The compressed path has to be the same import as the plain one, not a second
// code path that happens to parse: a gzip'd shard writes its rows and answers
// with the same report shape.
func TestImportWorkspaceTransferIssues_AcceptsAGzipBody(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	srcIssue := uuid.NewString()
	row := transferIssueRow(srcIssue, 1, "carried through a gzip body", testUserID, map[string]any{
		"description": strings.Repeat("compressible text ", 200),
	})
	body := transferIssueBody(
		map[string]any{"issues": map[string]any{srcIssue: map[string]any{"number": 1, "identifier": "GZ-1"}}},
		[]map[string]any{row}, nil, nil, false,
	)
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	req := testutil.WithURLParams(
		testutil.WithHeaders(transferBodyRequest(t, gzipBody(t, raw), "gzip"), "X-User-ID", testUserID),
		"id", dst,
	)
	resp := testutil.Call(t, testHandler.ImportWorkspaceTransferIssues, req)
	resp.Want(http.StatusOK)
	var report service.TransferIssuesReport
	resp.JSON(&report)
	if report.IssuesCreated != 1 {
		t.Fatalf("report says %d issue(s) created: %s", report.IssuesCreated, resp.Text())
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id = $1`, dst); n != 1 {
		t.Fatalf("workspace holds %d issue(s), want 1", n)
	}
}

// transferAttachmentMultipartBody builds the multipart body the CLI sends for
// one attachment, and the Content-Type that names its boundary.
func transferAttachmentMultipartBody(t *testing.T, meta service.TransferAttachmentMeta, blob []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := mw.WriteField("meta", string(metaJSON)); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	part, err := mw.CreateFormFile("file", meta.Filename)
	if err != nil {
		t.Fatalf("create file part: %v", err)
	}
	if _, err := part.Write(blob); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}

// The attachment request is the one transfer body that is not JSON: it is
// multipart, and the gzip decoder has to be installed before ParseMultipartForm
// reads r.Body. If it is not, the compressed bytes are parsed as a form, the
// file part comes back empty or the parse fails outright, and the row the
// importer writes is wrong even though the response looks like a success.
func TestImportWorkspaceTransferAttachment_AcceptsAGzipMultipartBody(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	orig := testHandler.Storage
	store := &mockStorage{}
	testHandler.Storage = store
	defer func() { testHandler.Storage = orig }()

	srcAtt := uuid.NewString()
	blob := []byte(strings.Repeat("gzip me through the multipart parser ", 256))
	meta := service.TransferAttachmentMeta{
		SourceID:    srcAtt,
		Filename:    "archive.txt",
		ContentType: "text/plain",
		SizeBytes:   int64(len(blob)),
		CreatedAt:   "2026-09-01T00:00:01Z",
	}
	raw, contentType := transferAttachmentMultipartBody(t, meta, blob)
	encoded := gzipBody(t, raw)
	if len(encoded) >= len(raw) {
		t.Fatalf("fixture does not compress: %d encoded vs %d raw bytes", len(encoded), len(raw))
	}

	req := httptest.NewRequest(http.MethodPost, "/api/workspaces/"+dst+"/transfer/attachments", bytes.NewReader(encoded))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Content-Encoding", "gzip")
	testutil.Call(t, testHandler.ImportWorkspaceTransferAttachment,
		testutil.WithURLParams(testutil.WithHeaders(req, "X-User-ID", testUserID), "id", dst)).Want(http.StatusOK)

	target := service.TransferAttachmentID(dst, srcAtt).String()
	if n := dbfx.Count(t, `SELECT count(*) FROM attachment WHERE id = $1`, target); n != 1 {
		t.Fatalf("attachments=%d want 1", n)
	}
	if got := transferScanString(t, `SELECT filename FROM attachment WHERE id = $1`, target); got != meta.Filename {
		t.Fatalf("filename=%q want %q", got, meta.Filename)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.files) != 1 {
		t.Fatalf("storage holds %d file(s), want the one attachment", len(store.files))
	}
	for key, got := range store.files {
		if !bytes.Equal(got, blob) {
			t.Fatalf("stored %d bytes under %s, want the %d decoded ones", len(got), key, len(blob))
		}
	}
}

// A multipart body that claims gzip and is not is a client bug; naming that is
// better than feeding the compressed bytes to the form parser and reporting the
// multipart error they produce.
func TestImportWorkspaceTransferAttachment_RejectsAGzipHeaderThatDoesNotMatchTheBody(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	raw, contentType := transferAttachmentMultipartBody(t, service.TransferAttachmentMeta{
		SourceID: uuid.NewString(), Filename: "not-gzipped.txt", ContentType: "text/plain",
		CreatedAt: "2026-09-01T00:00:01Z",
	}, []byte("hello"))

	req := httptest.NewRequest(http.MethodPost, "/api/workspaces/"+dst+"/transfer/attachments", bytes.NewReader(raw))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Content-Encoding", "gzip")
	resp := testutil.Call(t, testHandler.ImportWorkspaceTransferAttachment,
		testutil.WithURLParams(testutil.WithHeaders(req, "X-User-ID", testUserID), "id", dst)).Want(http.StatusBadRequest)
	if code := resp.Map()["code"]; code != "transfer_bundle_invalid" {
		t.Fatalf("code=%v body=%s", code, resp.Text())
	}
}

// The multipart path gets the same decoded-size cap as the JSON ones: the
// gzip'd form is small on the wire, and only the decoded bytes bound what the
// parser is allowed to materialize.
func TestImportWorkspaceTransferAttachment_RejectsAnOversizedDecodedBody(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	const limit = service.TransferAttachmentMaxBytes + 1<<20

	// Pads the decoded multipart past the cap while staying tiny on the wire.
	raw, contentType := transferAttachmentMultipartBody(t, service.TransferAttachmentMeta{
		SourceID: uuid.NewString(), Filename: "bomb.bin", ContentType: "application/octet-stream",
		CreatedAt: "2026-09-01T00:00:01Z",
	}, bytes.Repeat([]byte{'a'}, limit))
	encoded := gzipBody(t, raw)
	if len(encoded) > 1<<20 {
		t.Fatalf("fixture should be small on the wire, got %d bytes", len(encoded))
	}

	req := httptest.NewRequest(http.MethodPost, "/api/workspaces/"+dst+"/transfer/attachments", bytes.NewReader(encoded))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Content-Encoding", "gzip")
	resp := testutil.Call(t, testHandler.ImportWorkspaceTransferAttachment,
		testutil.WithURLParams(testutil.WithHeaders(req, "X-User-ID", testUserID), "id", dst)).Want(http.StatusRequestEntityTooLarge)
	if code := resp.Map()["code"]; code != "transfer_bundle_too_large" {
		t.Fatalf("code=%v body=%s", code, resp.Text())
	}
}
