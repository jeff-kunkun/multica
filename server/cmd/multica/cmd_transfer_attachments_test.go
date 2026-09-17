package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
)

// transferAttachmentTestBundleBlob builds a body that is compressible-looking
// but content-checked: the tests hash it and stage slices of it.
func transferAttachmentTestBundleBlob(size int) []byte {
	blob := make([]byte, size)
	for i := range blob {
		blob[i] = byte(i%251) ^ byte(i>>8)
	}
	return blob
}

// writeTransferImportZipWithAttachment writes a V2 bundle carrying exactly one
// attachment row and its blob, which is the shape the attachments group has in
// a real export.
func writeTransferImportZipWithAttachment(t *testing.T, path string, blob []byte) service.TransferAttachmentRow {
	t.Helper()
	sum := sha256.Sum256(blob)
	sha := hex.EncodeToString(sum[:])
	row := service.TransferAttachmentRow{
		SourceID:    "att-1",
		Filename:    "shot.png",
		ContentType: "image/png",
		SizeBytes:   int64(len(blob)),
		CreatedAt:   "2026-09-17T00:00:00Z",
		SHA256:      sha,
	}
	rowJSON, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal attachment row: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(f)
	entries := map[string][]byte{
		"manifest.json":            []byte(`{"format":"multica.workspace-transfer","schema_version":1,"bundle_id":"b-1"}`),
		"config.json":              []byte(`{"format":"multica.workspace-config","schema_version":1,"bundle_id":"c-1","entities":{}}`),
		"attachments/index.jsonl":  append(rowJSON, '\n'),
		"attachments/blobs/" + sha: blob,
	}
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip entry %s: %v", name, err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}
	return row
}

// runTransferImportAgainst runs one import against a fake target, returning the
// command's stderr so a test can read the progress protocol.
func runTransferImportAgainst(t *testing.T, serverURL, inPath string) (string, error) {
	t.Helper()
	cmd := newTransferImportTestCmd()
	_ = cmd.Flags().Set("server-url", serverURL)
	_ = cmd.Flags().Set("workspace", "src")
	_ = cmd.Flags().Set("in", inPath)
	var stderr bytes.Buffer
	cmd.SetOut(io.Discard)
	cmd.SetErr(&stderr)
	err := cmd.Execute()
	return stderr.String(), err
}

// A target that advertises chunk staging gets the resumable protocol: one
// status question, small slices, one commit.
func TestTransferImportStagesAttachmentChunksWhenTargetAdvertisesThem(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "")

	blob := transferAttachmentTestBundleBlob(70_000)
	sum := sha256.Sum256(blob)
	sha := hex.EncodeToString(sum[:])

	var (
		mu          sync.Mutex
		statusCalls int
		chunkShas   []string
		chunkOffset []int64
		chunkLens   []int
		staged      []byte
		commitCalls int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/health":
			_, _ = io.WriteString(w, `{"transfer":{"max_schema_version":1,"groups":["config","conversations","attachments"],"attachment_chunk_max_bytes":4194304}}`)
		case r.URL.Path == "/api/workspaces":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "ws-1", "slug": "src", "name": "Src"}})
		case strings.HasSuffix(r.URL.Path, "/transfer/config"):
			_, _ = io.WriteString(w, `{"config_report":{"stats":{}}}`)
		case strings.HasSuffix(r.URL.Path, "/transfer/attachments/status"):
			mu.Lock()
			statusCalls++
			mu.Unlock()
			var req service.TransferAttachmentStatusRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			entries := make([]service.TransferAttachmentUploadStatus, 0, len(req.Entries))
			for _, e := range req.Entries {
				entries = append(entries, service.TransferAttachmentUploadStatus{
					SourceID: e.SourceID,
					SHA256:   e.SHA256,
				})
			}
			_ = json.NewEncoder(w).Encode(service.TransferAttachmentStatusReport{Entries: entries})
		case strings.HasSuffix(r.URL.Path, "/transfer/attachments/chunk"):
			mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
				t.Errorf("chunk request content-type = %q", r.Header.Get("Content-Type"))
				http.Error(w, "bad content type", http.StatusBadRequest)
				return
			}
			mr := multipart.NewReader(r.Body, params["boundary"])
			form, err := mr.ReadForm(int64(service.TransferAttachmentChunkMaxBytes) + 1<<20)
			if err != nil {
				t.Errorf("read chunk form: %v", err)
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			defer form.RemoveAll()
			offset, _ := strconv.ParseInt(form.Value["offset"][0], 10, 64)
			total, _ := strconv.ParseInt(form.Value["total_bytes"][0], 10, 64)
			files := form.File["file"]
			if len(files) != 1 {
				t.Errorf("chunk request carried %d file parts, want 1", len(files))
				http.Error(w, "bad file", http.StatusBadRequest)
				return
			}
			fh, err := files[0].Open()
			if err != nil {
				t.Errorf("open chunk: %v", err)
				http.Error(w, "bad file", http.StatusBadRequest)
				return
			}
			data, _ := io.ReadAll(fh)
			fh.Close()
			mu.Lock()
			chunkShas = append(chunkShas, form.Value["sha256"][0])
			chunkOffset = append(chunkOffset, offset)
			chunkLens = append(chunkLens, len(data))
			if int64(len(staged)) == offset {
				staged = append(staged, data...)
			}
			resume := int64(len(staged))
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(service.TransferAttachmentChunkReport{
				SHA256:        form.Value["sha256"][0],
				ReceivedBytes: resume,
				ResumeOffset:  resume,
				TotalBytes:    total,
				Complete:      resume >= total,
			})
		case strings.HasSuffix(r.URL.Path, "/transfer/attachments/commit"):
			var req struct {
				SHA256 string `json:"sha256"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			commitCalls++
			mu.Unlock()
			if req.SHA256 != sha {
				t.Errorf("commit sha = %q, want %q", req.SHA256, sha)
			}
			_ = json.NewEncoder(w).Encode(service.TransferAttachmentReport{Applied: true, Created: true, BodyStored: true})
		case strings.HasSuffix(r.URL.Path, "/transfer/attachments"):
			t.Errorf("the whole-blob endpoint must not be used when the target advertises chunk staging")
			_, _ = io.WriteString(w, `{}`)
		default:
			_ = json.NewEncoder(w).Encode([]any{})
		}
	}))
	defer srv.Close()

	inPath := filepath.Join(t.TempDir(), "bundle.zip")
	writeTransferImportZipWithAttachment(t, inPath, blob)

	stderr, err := runTransferImportAgainst(t, srv.URL, inPath)
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if statusCalls != 1 {
		t.Fatalf("status calls = %d, want 1", statusCalls)
	}
	if commitCalls != 1 {
		t.Fatalf("commit calls = %d, want 1", commitCalls)
	}
	if len(chunkOffset) == 0 {
		t.Fatal("no chunks were staged")
	}
	// Slices must tile the blob from 0 without gaps, and every one carries the
	// blob's own address.
	var covered int64
	for i, offset := range chunkOffset {
		if offset != covered {
			t.Fatalf("chunk %d starts at %d, want %d (offsets %v)", i, offset, covered, chunkOffset)
		}
		if chunkShas[i] != sha {
			t.Fatalf("chunk %d sha = %q, want %q", i, chunkShas[i], sha)
		}
		covered = offset + int64(chunkLens[i])
	}
	if covered != int64(len(blob)) {
		t.Fatalf("chunks covered %d of %d bytes", covered, len(blob))
	}
	if !bytes.Equal(staged, blob) {
		t.Fatalf("target assembled %d bytes that do not match the %d-byte blob", len(staged), len(blob))
	}
	// Progress is reported in bytes, not attachment counts.
	if !strings.Contains(stderr, fmt.Sprintf(`"attachments_bytes_total":%d`, len(blob))) {
		t.Fatalf("progress did not carry the byte total; stderr = %s", stderr)
	}
	if !strings.Contains(stderr, fmt.Sprintf(`"attachments_bytes_uploaded":%d`, len(blob))) {
		t.Fatalf("progress never reached the byte total; stderr = %s", stderr)
	}
}

// Resume: the status answer decides where the upload starts. Nothing before
// that offset is sent again.
func TestTransferImportResumesAttachmentFromTargetOffset(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "")

	blob := transferAttachmentTestBundleBlob(40_000)
	sum := sha256.Sum256(blob)
	sha := hex.EncodeToString(sum[:])
	const alreadyStaged = 12_000

	var firstOffset int64 = -1
	var staged []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/health":
			_, _ = io.WriteString(w, `{"transfer":{"max_schema_version":1,"groups":["config","conversations","attachments"],"attachment_chunk_max_bytes":4194304}}`)
		case r.URL.Path == "/api/workspaces":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "ws-1", "slug": "src", "name": "Src"}})
		case strings.HasSuffix(r.URL.Path, "/transfer/config"):
			_, _ = io.WriteString(w, `{"config_report":{"stats":{}}}`)
		case strings.HasSuffix(r.URL.Path, "/transfer/attachments/status"):
			_ = json.NewEncoder(w).Encode(service.TransferAttachmentStatusReport{Entries: []service.TransferAttachmentUploadStatus{{
				SourceID:      "att-1",
				SHA256:        sha,
				ReceivedBytes: alreadyStaged,
				TotalBytes:    int64(len(blob)),
				ResumeOffset:  alreadyStaged,
			}}})
		case strings.HasSuffix(r.URL.Path, "/transfer/attachments/chunk"):
			mediaType, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if !strings.HasPrefix(mediaType, "multipart/") {
				t.Errorf("chunk content-type = %q", r.Header.Get("Content-Type"))
			}
			mr := multipart.NewReader(r.Body, params["boundary"])
			form, err := mr.ReadForm(int64(service.TransferAttachmentChunkMaxBytes) + 1<<20)
			if err != nil {
				t.Errorf("read chunk form: %v", err)
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			defer form.RemoveAll()
			offset, _ := strconv.ParseInt(form.Value["offset"][0], 10, 64)
			fh, _ := form.File["file"][0].Open()
			data, _ := io.ReadAll(fh)
			fh.Close()
			if firstOffset < 0 {
				firstOffset = offset
			}
			// The target already holds the prefix it reported; the uploader
			// only sends the rest.
			base := alreadyStaged
			if int64(len(staged)) == 0 {
				staged = append([]byte(nil), blob[:base]...)
			}
			if int64(len(staged)) == offset {
				staged = append(staged, data...)
			}
			resume := int64(len(staged))
			if resume > int64(len(blob)) {
				resume = int64(len(blob))
			}
			_ = json.NewEncoder(w).Encode(service.TransferAttachmentChunkReport{
				SHA256:        sha,
				ReceivedBytes: resume,
				ResumeOffset:  resume,
				TotalBytes:    int64(len(blob)),
				Complete:      resume >= int64(len(blob)),
			})
		case strings.HasSuffix(r.URL.Path, "/transfer/attachments/commit"):
			_ = json.NewEncoder(w).Encode(service.TransferAttachmentReport{Applied: true, Created: true, BodyStored: true})
		default:
			_ = json.NewEncoder(w).Encode([]any{})
		}
	}))
	defer srv.Close()

	inPath := filepath.Join(t.TempDir(), "bundle.zip")
	writeTransferImportZipWithAttachment(t, inPath, blob)

	if _, err := runTransferImportAgainst(t, srv.URL, inPath); err != nil {
		t.Fatalf("import: %v", err)
	}
	if firstOffset != alreadyStaged {
		t.Fatalf("first chunk offset = %d, want %d (the target's resume point)", firstOffset, alreadyStaged)
	}
}

// A blob the target already holds is not sent at all.
func TestTransferImportSkipsImportedAttachment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "")

	blob := transferAttachmentTestBundleBlob(9_000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/health":
			_, _ = io.WriteString(w, `{"transfer":{"max_schema_version":1,"groups":["config","conversations","attachments"],"attachment_chunk_max_bytes":4194304}}`)
		case r.URL.Path == "/api/workspaces":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "ws-1", "slug": "src", "name": "Src"}})
		case strings.HasSuffix(r.URL.Path, "/transfer/config"):
			_, _ = io.WriteString(w, `{"config_report":{"stats":{}}}`)
		case strings.HasSuffix(r.URL.Path, "/transfer/attachments/status"):
			_ = json.NewEncoder(w).Encode(service.TransferAttachmentStatusReport{Entries: []service.TransferAttachmentUploadStatus{{
				SourceID: "att-1",
				Imported: true,
			}}})
		case strings.HasSuffix(r.URL.Path, "/transfer/attachments/chunk"):
			t.Error("an imported attachment must not be staged again")
			_, _ = io.WriteString(w, `{}`)
		case strings.HasSuffix(r.URL.Path, "/transfer/attachments/commit"):
			t.Error("an imported attachment must not be committed again")
			_, _ = io.WriteString(w, `{}`)
		default:
			_ = json.NewEncoder(w).Encode([]any{})
		}
	}))
	defer srv.Close()

	inPath := filepath.Join(t.TempDir(), "bundle.zip")
	writeTransferImportZipWithAttachment(t, inPath, blob)

	stderr, err := runTransferImportAgainst(t, srv.URL, inPath)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	// Progress still reaches the total: the bytes are on the target either way.
	if !strings.Contains(stderr, fmt.Sprintf(`"attachments_bytes_uploaded":%d`, len(blob))) {
		t.Fatalf("skipped attachment never counted as done; stderr = %s", stderr)
	}
}

// Fallback: a target that does not advertise chunk staging gets one request per
// blob when that can work, and a readable refusal when it cannot.
func TestTransferImportFallsBackToWholeBlobWhenTargetCannotChunk(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "")

	blob := transferAttachmentTestBundleBlob(3_000)
	var wholeBlobCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/health":
			// An instance from before resumable staging: the field is absent.
			_, _ = io.WriteString(w, `{"transfer":{"max_schema_version":1,"groups":["config","conversations","attachments"]}}`)
		case r.URL.Path == "/api/workspaces":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "ws-1", "slug": "src", "name": "Src"}})
		case strings.HasSuffix(r.URL.Path, "/transfer/config"):
			_, _ = io.WriteString(w, `{"config_report":{"stats":{}}}`)
		case strings.HasSuffix(r.URL.Path, "/transfer/attachments"):
			wholeBlobCalls++
			_, _ = io.WriteString(w, `{"applied":true,"created":true,"body_stored":true}`)
		default:
			_ = json.NewEncoder(w).Encode([]any{})
		}
	}))
	defer srv.Close()

	inPath := filepath.Join(t.TempDir(), "bundle.zip")
	writeTransferImportZipWithAttachment(t, inPath, blob)

	if _, err := runTransferImportAgainst(t, srv.URL, inPath); err != nil {
		t.Fatalf("import: %v", err)
	}
	if wholeBlobCalls != 1 {
		t.Fatalf("whole-blob calls = %d, want 1", wholeBlobCalls)
	}
}

// A blob too large for one request on a target that cannot chunk used to be
// uploaded until the proxy cut the connection. It must be refused with the
// reason instead of a bare 5xx.
func TestTransferImportRefusesLargeBlobWhenTargetCannotChunk(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "")

	blob := transferAttachmentTestBundleBlob(transferAttachmentWholeBlobLimitBytes + 1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/health":
			_, _ = io.WriteString(w, `{"transfer":{"max_schema_version":1,"groups":["config","conversations","attachments"]}}`)
		case r.URL.Path == "/api/workspaces":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "ws-1", "slug": "src", "name": "Src"}})
		case strings.HasSuffix(r.URL.Path, "/transfer/config"):
			_, _ = io.WriteString(w, `{"config_report":{"stats":{}}}`)
		case strings.HasSuffix(r.URL.Path, "/transfer/attachments"):
			t.Error("an over-limit blob must not be sent to the whole-blob endpoint")
			_, _ = io.WriteString(w, `{}`)
		default:
			_ = json.NewEncoder(w).Encode([]any{})
		}
	}))
	defer srv.Close()

	inPath := filepath.Join(t.TempDir(), "bundle.zip")
	writeTransferImportZipWithAttachment(t, inPath, blob)

	_, err := runTransferImportAgainst(t, srv.URL, inPath)
	if err == nil {
		t.Fatal("importing a blob too large for one request must fail")
	}
	if !strings.Contains(err.Error(), "target_unsupported") {
		t.Fatalf("error = %v, want the target_unsupported explanation", err)
	}
}

// A killed run can leave a request in flight that lands after the next run has
// already asked where to continue. That makes the target's staged prefix move
// BACKWARDS under the resuming client; the target is the authority, so the
// uploader rewinds to it instead of failing a resume that is still progressing.
func TestTransferImportRewindsWhenTargetResumePointMovesBackwards(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "")

	blob := transferAttachmentTestBundleBlob(50_000)
	sum := sha256.Sum256(blob)
	sha := hex.EncodeToString(sum[:])

	var (
		mu       sync.Mutex
		attempts int
		staged   []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/health":
			_, _ = io.WriteString(w, `{"transfer":{"max_schema_version":1,"groups":["config","conversations","attachments"],"attachment_chunk_max_bytes":4194304}}`)
		case r.URL.Path == "/api/workspaces":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "ws-1", "slug": "src", "name": "Src"}})
		case strings.HasSuffix(r.URL.Path, "/transfer/config"):
			_, _ = io.WriteString(w, `{"config_report":{"stats":{}}}`)
		case strings.HasSuffix(r.URL.Path, "/transfer/attachments/status"):
			// The preflight saw a partial staging row that was reaped before
			// the first chunk arrived, so the client starts from its offset.
			_ = json.NewEncoder(w).Encode(service.TransferAttachmentStatusReport{Entries: []service.TransferAttachmentUploadStatus{{
				SourceID: "att-1", SHA256: sha, ReceivedBytes: 10_000, TotalBytes: int64(len(blob)), ResumeOffset: 10_000,
			}}})
		case strings.HasSuffix(r.URL.Path, "/transfer/attachments/chunk"):
			mediaType, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if !strings.HasPrefix(mediaType, "multipart/") {
				t.Errorf("chunk content-type = %q", r.Header.Get("Content-Type"))
			}
			mr := multipart.NewReader(r.Body, params["boundary"])
			form, _ := mr.ReadForm(int64(service.TransferAttachmentChunkMaxBytes) + 1<<20)
			defer form.RemoveAll()
			fh, _ := form.File["file"][0].Open()
			data, _ := io.ReadAll(fh)
			fh.Close()
			mu.Lock()
			attempts++
			first := attempts == 1
			if first {
				// The staging row the preflight promised is gone: the target
				// reports a prefix of zero even though it accepted the slice.
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(service.TransferAttachmentChunkReport{
					SHA256:        sha,
					ReceivedBytes: int64(len(data)),
					ResumeOffset:  0,
					TotalBytes:    int64(len(blob)),
				})
				return
			}
			staged = append(staged, data...)
			resume := int64(len(staged))
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(service.TransferAttachmentChunkReport{
				SHA256:        sha,
				ReceivedBytes: resume,
				ResumeOffset:  resume,
				TotalBytes:    int64(len(blob)),
				Complete:      resume >= int64(len(blob)),
			})
		case strings.HasSuffix(r.URL.Path, "/transfer/attachments/commit"):
			_ = json.NewEncoder(w).Encode(service.TransferAttachmentReport{Applied: true, Created: true, BodyStored: true})
		default:
			_ = json.NewEncoder(w).Encode([]any{})
		}
	}))
	defer srv.Close()

	inPath := filepath.Join(t.TempDir(), "bundle.zip")
	writeTransferImportZipWithAttachment(t, inPath, blob)

	if _, err := runTransferImportAgainst(t, srv.URL, inPath); err != nil {
		t.Fatalf("import: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts < 2 {
		t.Fatalf("chunk attempts = %d, want the rewind to re-send from zero", attempts)
	}
	if !bytes.Equal(staged, blob) {
		t.Fatalf("target assembled %d bytes that do not match the %d-byte blob", len(staged), len(blob))
	}
}

// A retryable chunk failure is retried at the same offset with a smaller slice
// rather than failing the import.
func TestTransferImportSplitsChunkOnRetryableFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "")

	blob := transferAttachmentTestBundleBlob(60_000)
	sum := sha256.Sum256(blob)
	sha := hex.EncodeToString(sum[:])

	var (
		mu       sync.Mutex
		sizes    []int
		staged   []byte
		attempts int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/health":
			_, _ = io.WriteString(w, `{"transfer":{"max_schema_version":1,"groups":["config","conversations","attachments"],"attachment_chunk_max_bytes":4194304}}`)
		case r.URL.Path == "/api/workspaces":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "ws-1", "slug": "src", "name": "Src"}})
		case strings.HasSuffix(r.URL.Path, "/transfer/config"):
			_, _ = io.WriteString(w, `{"config_report":{"stats":{}}}`)
		case strings.HasSuffix(r.URL.Path, "/transfer/attachments/status"):
			_ = json.NewEncoder(w).Encode(service.TransferAttachmentStatusReport{Entries: []service.TransferAttachmentUploadStatus{{
				SourceID: "att-1", SHA256: sha, TotalBytes: int64(len(blob)),
			}}})
		case strings.HasSuffix(r.URL.Path, "/transfer/attachments/chunk"):
			mediaType, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if !strings.HasPrefix(mediaType, "multipart/") {
				t.Errorf("chunk content-type = %q", r.Header.Get("Content-Type"))
			}
			mr := multipart.NewReader(r.Body, params["boundary"])
			form, _ := mr.ReadForm(int64(service.TransferAttachmentChunkMaxBytes) + 1<<20)
			defer form.RemoveAll()
			fh, _ := form.File["file"][0].Open()
			data, _ := io.ReadAll(fh)
			fh.Close()
			mu.Lock()
			attempts++
			fail := attempts == 1
			if !fail {
				sizes = append(sizes, len(data))
			}
			mu.Unlock()
			if fail {
				// What an edge proxy looks like mid-body: no response the
				// client can use, so it retries.
				w.WriteHeader(524)
				_, _ = io.WriteString(w, `{"error":"edge timeout"}`)
				return
			}
			mu.Lock()
			staged = append(staged, data...)
			resume := int64(len(staged))
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(service.TransferAttachmentChunkReport{
				SHA256:        sha,
				ReceivedBytes: resume,
				ResumeOffset:  resume,
				TotalBytes:    int64(len(blob)),
				Complete:      resume >= int64(len(blob)),
			})
		case strings.HasSuffix(r.URL.Path, "/transfer/attachments/commit"):
			_ = json.NewEncoder(w).Encode(service.TransferAttachmentReport{Applied: true, Created: true, BodyStored: true})
		default:
			_ = json.NewEncoder(w).Encode([]any{})
		}
	}))
	defer srv.Close()

	inPath := filepath.Join(t.TempDir(), "bundle.zip")
	writeTransferImportZipWithAttachment(t, inPath, blob)

	if _, err := runTransferImportAgainst(t, srv.URL, inPath); err != nil {
		t.Fatalf("import: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts < 2 {
		t.Fatalf("chunk attempts = %d, want the 524 retried", attempts)
	}
	if len(sizes) == 0 {
		t.Fatal("no chunk succeeded after the retry")
	}
	if sizes[0] >= service.TransferAttachmentChunkDefaultBytes {
		t.Fatalf("retried slice = %d bytes, want it halved below the default %d", sizes[0], service.TransferAttachmentChunkDefaultBytes)
	}
}
