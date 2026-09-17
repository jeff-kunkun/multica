package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// The three chunk endpoints are one protocol: status says where to continue,
// chunk stages a slice, commit assembles and verifies. This drives the whole
// path through the real handlers so the wire contract (multipart field names,
// status codes, staging side effects) is pinned, not just the service.
func TestTransferAttachmentChunkEndpointsStageAndCommit(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	store := &mockStorage{}
	swapTransferTestStorage(t, store)

	blob := make([]byte, 5000)
	for i := range blob {
		blob[i] = byte(3 + i%251)
	}
	sum := sha256.Sum256(blob)
	sha := hex.EncodeToString(sum[:])
	sourceID := uuid.NewString()
	meta := service.TransferAttachmentMeta{
		SourceID:    sourceID,
		Filename:    "shot.png",
		ContentType: "image/png",
		SizeBytes:   int64(len(blob)),
		CreatedAt:   "2026-09-17T00:00:00Z",
		SHA256:      sha,
	}

	// Chunks arrive out of order, as a resumed upload does.
	for _, offset := range []int{2000, 0, 4000} {
		end := offset + 2000
		if end > len(blob) {
			end = len(blob)
		}
		resp := testutil.Call(t, testHandler.ImportWorkspaceTransferAttachmentChunk,
			transferChunkReq(t, dst, meta, sha, int64(offset), int64(len(blob)), blob[offset:end]))
		resp.Want(http.StatusOK)
	}

	var status service.TransferAttachmentStatusReport
	testutil.Call(t, testHandler.ImportWorkspaceTransferAttachmentStatus,
		transferReq("POST", "/api/workspaces/"+dst+"/transfer/attachments/status", dst,
			service.TransferAttachmentStatusRequest{Entries: []service.TransferAttachmentUploadQuery{{SourceID: sourceID, SHA256: sha}}})).
		Want(http.StatusOK).JSON(&status)
	if len(status.Entries) != 1 || status.Entries[0].ResumeOffset != int64(len(blob)) {
		t.Fatalf("status before commit = %+v, want a complete staged upload", status.Entries)
	}

	var report service.TransferAttachmentReport
	testutil.Call(t, testHandler.ImportWorkspaceTransferAttachmentCommit,
		transferReq("POST", "/api/workspaces/"+dst+"/transfer/attachments/commit", dst, map[string]string{"sha256": sha})).
		Want(http.StatusOK).JSON(&report)
	if !report.Created || !report.BodyStored {
		t.Fatalf("commit report = %+v, want created with the body stored", report)
	}
	key := "workspaces/" + dst + "/" + report.TargetID + ".png"
	stored, ok := store.files[key]
	if !ok {
		t.Fatalf("no object at %s; files = %v", key, store.files)
	}
	if !bytes.Equal(stored, blob) {
		t.Fatalf("stored object differs from the blob (%d vs %d bytes)", len(stored), len(blob))
	}
	// The staging rows are resume state; a committed blob must not leave them.
	if n := dbfx.Count(t, `SELECT count(*) FROM transfer_attachment_upload WHERE workspace_id = $1`, dst); n != 0 {
		t.Fatalf("commit left %d staging rows behind", n)
	}
}

// A checksum that does not match the assembled body is a refused import, not a
// stored one: the handler must answer with the machine-readable code and leave
// no attachment row.
func TestTransferAttachmentChunkCommitRejectsChecksumMismatch(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	store := &mockStorage{}
	swapTransferTestStorage(t, store)

	expected := make([]byte, 1200)
	tampered := make([]byte, 1200)
	for i := range expected {
		expected[i] = byte(i % 97)
		tampered[i] = byte(i%97 + 1)
	}
	sum := sha256.Sum256(expected)
	sha := hex.EncodeToString(sum[:])
	sourceID := uuid.NewString()
	meta := service.TransferAttachmentMeta{
		SourceID:  sourceID,
		Filename:  "bad.bin",
		SizeBytes: int64(len(tampered)),
		CreatedAt: "2026-09-17T00:00:00Z",
		SHA256:    sha,
	}
	testutil.Call(t, testHandler.ImportWorkspaceTransferAttachmentChunk,
		transferChunkReq(t, dst, meta, sha, 0, int64(len(tampered)), tampered)).
		Want(http.StatusOK)

	var body struct {
		Code string `json:"code"`
	}
	testutil.Call(t, testHandler.ImportWorkspaceTransferAttachmentCommit,
		transferReq("POST", "/api/workspaces/"+dst+"/transfer/attachments/commit", dst, map[string]string{"sha256": sha})).
		Want(http.StatusUnprocessableEntity).JSON(&body)
	if body.Code != "transfer_attachment_checksum_mismatch" {
		t.Fatalf("code = %q, want transfer_attachment_checksum_mismatch", body.Code)
	}
	if len(store.files) != 0 {
		t.Fatalf("refused commit stored %d objects", len(store.files))
	}
}

// The chunk endpoint is the one place a client controls how much memory the
// server holds, so an over-sized slice has to be refused by the handler's own
// body cap.
func TestTransferAttachmentChunkEndpointRejectsOversizeChunk(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	swapTransferTestStorage(t, &mockStorage{})

	blob := bytes.Repeat([]byte{9}, service.TransferAttachmentChunkMaxBytes+1)
	sum := sha256.Sum256(blob)
	sha := hex.EncodeToString(sum[:])
	meta := service.TransferAttachmentMeta{
		SourceID:  uuid.NewString(),
		Filename:  "huge.bin",
		SizeBytes: int64(len(blob)),
		CreatedAt: "2026-09-17T00:00:00Z",
		SHA256:    sha,
	}
	resp := testutil.Call(t, testHandler.ImportWorkspaceTransferAttachmentChunk,
		transferChunkReq(t, dst, meta, sha, 0, int64(len(blob)), blob))
	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body = %s", resp.Code, resp.Text())
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM transfer_attachment_upload WHERE workspace_id = $1`, dst); n != 0 {
		t.Fatalf("oversize chunk staged %d rows", n)
	}
}

// Status is what a resumed import asks first; it must name the contiguous
// prefix, not the total including a hole.
func TestTransferAttachmentStatusReportsContiguousPrefix(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	swapTransferTestStorage(t, &mockStorage{})

	blob := bytes.Repeat([]byte{5}, 3000)
	sum := sha256.Sum256(blob)
	sha := hex.EncodeToString(sum[:])
	sourceID := uuid.NewString()
	meta := service.TransferAttachmentMeta{
		SourceID:  sourceID,
		Filename:  "resume.bin",
		SizeBytes: int64(len(blob)),
		CreatedAt: "2026-09-17T00:00:00Z",
		SHA256:    sha,
	}
	// First and last slice only: the middle one was lost with the killed run.
	for _, offset := range []int{0, 2000} {
		end := offset + 1000
		if end > len(blob) {
			end = len(blob)
		}
		testutil.Call(t, testHandler.ImportWorkspaceTransferAttachmentChunk,
			transferChunkReq(t, dst, meta, sha, int64(offset), int64(len(blob)), blob[offset:end])).
			Want(http.StatusOK)
	}

	var status service.TransferAttachmentStatusReport
	testutil.Call(t, testHandler.ImportWorkspaceTransferAttachmentStatus,
		transferReq("POST", "/api/workspaces/"+dst+"/transfer/attachments/status", dst,
			service.TransferAttachmentStatusRequest{Entries: []service.TransferAttachmentUploadQuery{{SourceID: sourceID, SHA256: sha}}})).
		Want(http.StatusOK).JSON(&status)
	if len(status.Entries) != 1 {
		t.Fatalf("status returned %d entries, want 1", len(status.Entries))
	}
	entry := status.Entries[0]
	if entry.ReceivedBytes != 2000 {
		t.Fatalf("received_bytes = %d, want 2000", entry.ReceivedBytes)
	}
	if entry.ResumeOffset != 1000 {
		t.Fatalf("resume_offset = %d, want 1000 (the hole starts at 1000)", entry.ResumeOffset)
	}
	if entry.Imported {
		t.Fatal("nothing has been committed, so nothing is imported")
	}
}

// The capability block is the negotiation channel: a target that can stage
// chunks says so on /health, and it must say the same number the handler
// enforces.
func TestTransferCapabilitiesAdvertiseChunkSize(t *testing.T) {
	caps := service.TransferCapabilitiesForCurrentBuild()
	if caps.AttachmentChunkMaxBytes != service.TransferAttachmentChunkMaxBytes {
		t.Fatalf("attachment_chunk_max_bytes = %d, want %d",
			caps.AttachmentChunkMaxBytes, service.TransferAttachmentChunkMaxBytes)
	}
	raw, err := json.Marshal(caps)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"attachment_chunk_max_bytes"`) {
		t.Fatalf("capability JSON does not carry the field: %s", raw)
	}
}

// swapTransferTestStorage gives one test a Storage without leaking it into the
// next: the handler is a package-level fixture with no storage of its own.
func swapTransferTestStorage(t *testing.T, store *mockStorage) {
	t.Helper()
	prev := testHandler.Storage
	testHandler.Storage = store
	t.Cleanup(func() { testHandler.Storage = prev })
}

func transferChunkReq(t *testing.T, wsID string, meta service.TransferAttachmentMeta, sha string, offset, total int64, chunk []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	for name, value := range map[string]string{
		"meta":        string(metaJSON),
		"sha256":      sha,
		"offset":      strconv.FormatInt(offset, 10),
		"total_bytes": strconv.FormatInt(total, 10),
	} {
		if err := mw.WriteField(name, value); err != nil {
			t.Fatalf("write field %s: %v", name, err)
		}
	}
	part, err := mw.CreateFormFile("file", meta.Filename)
	if err != nil {
		t.Fatalf("create file part: %v", err)
	}
	if _, err := part.Write(chunk); err != nil {
		t.Fatalf("write chunk: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces/"+wsID+"/transfer/attachments/chunk", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req = testutil.WithHeaders(req, "X-User-ID", testUserID)
	return testutil.WithURLParams(req, "id", wsID)
}
