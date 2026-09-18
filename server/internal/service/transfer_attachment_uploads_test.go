package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"testing"
)

// transferAttachmentTestStorage is the smallest Storage that lets a commit
// succeed: it records what was written so a test can compare the assembled
// body against the bundle blob.
type transferAttachmentTestStorage struct {
	mu    sync.Mutex
	files map[string][]byte
}

func (s *transferAttachmentTestStorage) Upload(_ context.Context, key string, data []byte, _ string, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.files == nil {
		s.files = map[string][]byte{}
	}
	s.files[key] = append([]byte(nil), data...)
	return "https://cdn.example.test/" + key, nil
}

func (s *transferAttachmentTestStorage) stored(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.files[key]
	return data, ok
}

func (s *transferAttachmentTestStorage) Delete(context.Context, string)             {}
func (s *transferAttachmentTestStorage) DeleteObject(context.Context, string) error { return nil }
func (s *transferAttachmentTestStorage) DeleteKeys(context.Context, []string)       {}
func (s *transferAttachmentTestStorage) KeyFromURL(string) string                   { return "" }
func (s *transferAttachmentTestStorage) ObjectURL(key string) string {
	return "https://cdn.example.test/" + key
}
func (s *transferAttachmentTestStorage) CdnDomain() string { return "" }
func (s *transferAttachmentTestStorage) GetReader(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("transfer attachment test storage does not read")
}

// newTransferAttachmentFixture is newTransferOptionsFixture plus the staging
// cleanup the two new tables need — they carry no foreign key, so nothing else
// removes them when the workspace goes.
func newTransferAttachmentFixture(t *testing.T) (transferOptionsFixture, *transferAttachmentTestStorage) {
	t.Helper()
	fixture := newTransferOptionsFixture(t, nil)
	store := &transferAttachmentTestStorage{}
	fixture.env.Storage = store
	fixture.fx.Cleanup(t, `DELETE FROM transfer_attachment_upload_chunk WHERE workspace_id = $1`, fixture.workspace)
	fixture.fx.Cleanup(t, `DELETE FROM transfer_attachment_upload WHERE workspace_id = $1`, fixture.workspace)
	fixture.fx.Cleanup(t, `DELETE FROM attachment WHERE workspace_id = $1`, fixture.workspace)
	return fixture, store
}

func transferAttachmentTestBlob(seed byte, size int) []byte {
	blob := make([]byte, size)
	for i := range blob {
		blob[i] = seed + byte(i%251)
	}
	return blob
}

func transferAttachmentTestSHA(blob []byte) string {
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:])
}

func transferAttachmentTestMeta(sourceID, sha string, size int64) TransferAttachmentMeta {
	return TransferAttachmentMeta{
		SourceID:    sourceID,
		Filename:    "photo.bin",
		ContentType: "application/octet-stream",
		SizeBytes:   size,
		CreatedAt:   "2026-09-17T00:00:00Z",
		SHA256:      sha,
	}
}

func stageTransferAttachmentTestChunks(t *testing.T, env TransferImportEnv, sourceID string, blob []byte, chunkSize int, order []int) {
	t.Helper()
	stageTransferAttachmentTestChunksUnder(t, env, sourceID, transferAttachmentTestSHA(blob), blob, chunkSize, order)
}

// stageTransferAttachmentTestChunksUnder stages a body under an address that
// does not have to match it, which is how the checksum-mismatch case is built.
func stageTransferAttachmentTestChunksUnder(t *testing.T, env TransferImportEnv, sourceID, sha string, blob []byte, chunkSize int, order []int) {
	t.Helper()
	meta := transferAttachmentTestMeta(sourceID, sha, int64(len(blob)))
	for _, index := range order {
		offset := index * chunkSize
		end := offset + chunkSize
		if end > len(blob) {
			end = len(blob)
		}
		if offset >= len(blob) {
			continue
		}
		if _, err := StageTransferAttachmentChunk(context.Background(), env, TransferAttachmentChunkRequest{
			SHA256:     sha,
			Offset:     int64(offset),
			TotalBytes: int64(len(blob)),
			Meta:       meta,
			Chunk:      blob[offset:end],
		}); err != nil {
			t.Fatalf("stage chunk %d: %v", index, err)
		}
	}
}

// The whole point of content-addressed staging: chunks that arrive out of order
// still assemble into the blob the sha256 names.
func TestTransferAttachmentUploadAssemblesOutOfOrderAndCommits(t *testing.T) {
	fixture, store := newTransferAttachmentFixture(t)
	env := fixture.env
	sourceID := "att-source-out-of-order"
	blob := transferAttachmentTestBlob(7, 5000)

	stageTransferAttachmentTestChunks(t, env, sourceID, blob, 2000, []int{2, 0, 1})

	sha := transferAttachmentTestSHA(blob)
	report, err := CommitTransferAttachmentUpload(context.Background(), env, sha)
	if err != nil {
		t.Fatalf("commit staged upload: %v", err)
	}
	if !report.Created || !report.BodyStored {
		t.Fatalf("commit report = %+v, want created with the body stored", report)
	}
	stored, ok := store.stored("workspaces/" + fixture.workspace + "/" + report.TargetID + ".bin")
	if !ok {
		t.Fatalf("no object stored under the attachment key; files = %v", store.files)
	}
	if string(stored) != string(blob) {
		t.Fatalf("stored %d bytes that do not match the %d-byte blob", len(stored), len(blob))
	}
	// The staging rows are the resume state, not a ledger: a committed blob
	// must not leave them behind for the next import to find.
	if n := fixture.fx.Count(t, `SELECT count(*) FROM transfer_attachment_upload WHERE workspace_id = $1`, fixture.workspace); n != 0 {
		t.Fatalf("commit left %d staging rows", n)
	}
	if n := fixture.fx.Count(t, `SELECT count(*) FROM transfer_attachment_upload_chunk WHERE workspace_id = $1`, fixture.workspace); n != 0 {
		t.Fatalf("commit left %d staging chunks", n)
	}
}

// A body that does not hash to its address is the wrong body. Keeping it would
// make the next resume assemble the same wrong blob, so the whole staging row
// goes.
func TestTransferAttachmentUploadDiscardsChecksumMismatch(t *testing.T) {
	fixture, store := newTransferAttachmentFixture(t)
	env := fixture.env
	sourceID := "att-source-tampered"
	expected := transferAttachmentTestBlob(11, 4000)
	tampered := transferAttachmentTestBlob(12, 4000)
	sha := transferAttachmentTestSHA(expected)

	stageTransferAttachmentTestChunksUnder(t, env, sourceID, sha, tampered, 1500, []int{0, 1, 2})

	_, err := CommitTransferAttachmentUpload(context.Background(), env, sha)
	var ie *ImportError
	if !errors.As(err, &ie) || ie.Code != "transfer_attachment_checksum_mismatch" {
		t.Fatalf("commit error = %v, want transfer_attachment_checksum_mismatch", err)
	}
	if n := fixture.fx.Count(t, `SELECT count(*) FROM transfer_attachment_upload WHERE workspace_id = $1`, fixture.workspace); n != 0 {
		t.Fatalf("rejected commit left %d staging rows", n)
	}
	if n := fixture.fx.Count(t, `SELECT count(*) FROM transfer_attachment_upload_chunk WHERE workspace_id = $1`, fixture.workspace); n != 0 {
		t.Fatalf("rejected commit left %d staging chunks", n)
	}
	if n := fixture.fx.Count(t, `SELECT count(*) FROM attachment WHERE workspace_id = $1`, fixture.workspace); n != 0 {
		t.Fatalf("rejected commit wrote %d attachment rows", n)
	}
	if len(store.files) != 0 {
		t.Fatalf("rejected commit stored %d objects", len(store.files))
	}
}

// Resume: what the target says it holds decides where the next run starts, and
// nothing before that offset is sent again.
func TestTransferAttachmentUploadResumesOnlyMissingBytes(t *testing.T) {
	fixture, _ := newTransferAttachmentFixture(t)
	env := fixture.env
	ctx := context.Background()
	sourceID := "att-source-resume"
	blob := transferAttachmentTestBlob(21, 6000)
	sha := transferAttachmentTestSHA(blob)

	// Chunks 0 and 2 of three arrive; 1 is lost with the killed run.
	stageTransferAttachmentTestChunks(t, env, sourceID, blob, 2000, []int{0, 2})

	status, err := TransferAttachmentUploadStatuses(ctx, env, []TransferAttachmentUploadQuery{{SourceID: sourceID, SHA256: sha}})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(status.Entries) != 1 {
		t.Fatalf("status returned %d entries, want 1", len(status.Entries))
	}
	entry := status.Entries[0]
	if entry.Imported {
		t.Fatal("nothing has been committed yet")
	}
	if entry.ReceivedBytes != 4000 {
		t.Fatalf("received_bytes = %d, want 4000 (chunks 0 and 2)", entry.ReceivedBytes)
	}
	// The hole after chunk 0 is what the uploader must respect: bytes beyond
	// it cannot be assembled yet.
	if entry.ResumeOffset != 2000 {
		t.Fatalf("resume_offset = %d, want 2000", entry.ResumeOffset)
	}

	// Only the missing slice is sent.
	stageTransferAttachmentTestChunks(t, env, sourceID, blob, 2000, []int{1})

	report, err := CommitTransferAttachmentUpload(ctx, env, sha)
	if err != nil {
		t.Fatalf("commit after resume: %v", err)
	}
	if !report.BodyStored {
		t.Fatalf("report = %+v, want the body stored", report)
	}
}

// A resume that re-sends from a boundary the target already passed is the
// normal case, not an error: the offset is part of the key.
func TestTransferAttachmentUploadDuplicateChunkIsIdempotent(t *testing.T) {
	fixture, _ := newTransferAttachmentFixture(t)
	env := fixture.env
	ctx := context.Background()
	blob := transferAttachmentTestBlob(31, 3000)
	sha := transferAttachmentTestSHA(blob)
	meta := transferAttachmentTestMeta("att-source-duplicate", sha, int64(len(blob)))
	req := TransferAttachmentChunkRequest{
		SHA256:     sha,
		Offset:     0,
		TotalBytes: int64(len(blob)),
		Meta:       meta,
		Chunk:      blob[:1000],
	}
	first, err := StageTransferAttachmentChunk(ctx, env, req)
	if err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	if first.Duplicate {
		t.Fatalf("first chunk reported as duplicate: %+v", first)
	}
	second, err := StageTransferAttachmentChunk(ctx, env, req)
	if err != nil {
		t.Fatalf("repeated chunk: %v", err)
	}
	if !second.Duplicate {
		t.Fatalf("repeated chunk was not reported as duplicate: %+v", second)
	}
	if second.ReceivedBytes != first.ReceivedBytes {
		t.Fatalf("repeat changed received_bytes from %d to %d", first.ReceivedBytes, second.ReceivedBytes)
	}
	if n := fixture.fx.Count(t, `SELECT count(*) FROM transfer_attachment_upload_chunk WHERE workspace_id = $1`, fixture.workspace); n != 1 {
		t.Fatalf("staged %d rows for one repeated chunk, want 1", n)
	}
}

// A resume re-sends from whatever boundary the target reported, which need not
// be a boundary the killed run used. The two passes' slices then overlap, and
// the overlapping slice still carries bytes the other one does not — dropping
// it wholesale is what turned a resumable upload into "staged chunks have a
// gap" at commit (DENE-443).
func TestTransferAttachmentUploadAssemblesOverlappingPasses(t *testing.T) {
	fixture, store := newTransferAttachmentFixture(t)
	env := fixture.env
	ctx := context.Background()
	sourceID := "att-source-overlap"
	blob := transferAttachmentTestBlob(91, 6000)
	sha := transferAttachmentTestSHA(blob)
	meta := transferAttachmentTestMeta(sourceID, sha, int64(len(blob)))

	// First pass staged [0, 3000) before it was killed.
	if _, err := StageTransferAttachmentChunk(ctx, env, TransferAttachmentChunkRequest{
		SHA256: sha, Offset: 0, TotalBytes: int64(len(blob)), Meta: meta, Chunk: blob[0:3000],
	}); err != nil {
		t.Fatalf("first pass chunk: %v", err)
	}
	// The resume restarts below that boundary and tiles forward with a
	// different slice size, so [1000, 4000) overlaps the staged [0, 3000).
	for _, span := range [][2]int{{1000, 4000}, {4000, 6000}} {
		if _, err := StageTransferAttachmentChunk(ctx, env, TransferAttachmentChunkRequest{
			SHA256: sha, Offset: int64(span[0]), TotalBytes: int64(len(blob)), Meta: meta, Chunk: blob[span[0]:span[1]],
		}); err != nil {
			t.Fatalf("resume chunk %v: %v", span, err)
		}
	}

	report, err := CommitTransferAttachmentUpload(ctx, env, sha)
	if err != nil {
		t.Fatalf("commit across overlapping passes: %v", err)
	}
	if !report.BodyStored {
		t.Fatalf("report = %+v, want the body stored", report)
	}
	stored, ok := store.stored("workspaces/" + fixture.workspace + "/" + report.TargetID + ".bin")
	if !ok {
		t.Fatal("no object stored")
	}
	if !bytes.Equal(stored, blob) {
		t.Fatalf("assembled %d bytes that do not match the %d-byte blob", len(stored), len(blob))
	}
}

// A committed attachment answers as imported, which is what lets a resume skip
// a blob that is already done instead of sending it again.
func TestTransferAttachmentUploadStatusReportsImported(t *testing.T) {
	fixture, _ := newTransferAttachmentFixture(t)
	env := fixture.env
	ctx := context.Background()
	sourceID := "att-source-imported"
	blob := transferAttachmentTestBlob(41, 2000)
	sha := transferAttachmentTestSHA(blob)
	stageTransferAttachmentTestChunks(t, env, sourceID, blob, 1000, []int{0, 1})
	if _, err := CommitTransferAttachmentUpload(ctx, env, sha); err != nil {
		t.Fatalf("commit: %v", err)
	}
	status, err := TransferAttachmentUploadStatuses(ctx, env, []TransferAttachmentUploadQuery{{SourceID: sourceID, SHA256: sha}})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.Entries[0].Imported {
		t.Fatalf("entry = %+v, want imported", status.Entries[0])
	}
}

// Half-finished staging must not accumulate: past its TTL it is garbage, and
// past the workspace cap the oldest uploads go first.
func TestTransferAttachmentUploadCleanupReapsExpiredAndOverCap(t *testing.T) {
	fixture, _ := newTransferAttachmentFixture(t)
	env := fixture.env
	ctx := context.Background()

	expiredBlob := transferAttachmentTestBlob(51, 2000)
	expiredSHA := transferAttachmentTestSHA(expiredBlob)
	stageTransferAttachmentTestChunks(t, env, "att-source-expired", expiredBlob, 1000, []int{0, 1})
	fixture.fx.Exec(t, `UPDATE transfer_attachment_upload SET created_at = now() - interval '48 hours' WHERE workspace_id = $1 AND sha256 = $2`,
		fixture.workspace, expiredSHA)

	freshBlob := transferAttachmentTestBlob(52, 2000)
	freshSHA := transferAttachmentTestSHA(freshBlob)
	stageTransferAttachmentTestChunks(t, env, "att-source-fresh", freshBlob, 1000, []int{0})

	// Any staging request runs the reaper; the status call is the cheapest one.
	if _, err := TransferAttachmentUploadStatuses(ctx, env, []TransferAttachmentUploadQuery{{SourceID: "att-source-fresh", SHA256: freshSHA}}); err != nil {
		t.Fatalf("status: %v", err)
	}
	if n := fixture.fx.Count(t, `SELECT count(*) FROM transfer_attachment_upload WHERE workspace_id = $1 AND sha256 = $2`, fixture.workspace, expiredSHA); n != 0 {
		t.Fatalf("expired upload survived cleanup")
	}
	if n := fixture.fx.Count(t, `SELECT count(*) FROM transfer_attachment_upload_chunk WHERE workspace_id = $1 AND sha256 = $2`, fixture.workspace, expiredSHA); n != 0 {
		t.Fatalf("expired upload's chunks survived cleanup")
	}
	if n := fixture.fx.Count(t, `SELECT count(*) FROM transfer_attachment_upload WHERE workspace_id = $1 AND sha256 = $2`, fixture.workspace, freshSHA); n != 1 {
		t.Fatalf("fresh upload was reaped by the TTL pass")
	}

	// Cap pass: make the older row look like it already holds more than the
	// workspace budget, then run the reaper again. received_bytes is the cache
	// the cap is computed from, so this is the input the reaper really reads.
	fixture.fx.Exec(t, `UPDATE transfer_attachment_upload SET received_bytes = $3, created_at = now() - interval '1 hour' WHERE workspace_id = $1 AND sha256 = $2`,
		fixture.workspace, freshSHA, TransferAttachmentWorkspaceStagingMaxBytes+1)
	if _, err := TransferAttachmentUploadStatuses(ctx, env, nil); err != nil {
		t.Fatalf("status after cap setup: %v", err)
	}
	if n := fixture.fx.Count(t, `SELECT count(*) FROM transfer_attachment_upload WHERE workspace_id = $1`, fixture.workspace); n != 0 {
		t.Fatalf("over-cap workspace still holds %d staged uploads", n)
	}
}

// The staged meta is what a later chunk or a commit trusts about which row the
// blob belongs to; a chunk that contradicts its own address is refused.
func TestTransferAttachmentUploadRejectsContradictoryChunk(t *testing.T) {
	fixture, _ := newTransferAttachmentFixture(t)
	env := fixture.env
	blob := transferAttachmentTestBlob(61, 1000)
	sha := transferAttachmentTestSHA(blob)
	ctx := context.Background()

	_, err := StageTransferAttachmentChunk(ctx, env, TransferAttachmentChunkRequest{
		SHA256:     sha,
		Offset:     0,
		TotalBytes: int64(len(blob)),
		Meta:       transferAttachmentTestMeta("att-source-bad-offset", sha, int64(len(blob))),
		Chunk:      blob[:600],
	})
	if err != nil {
		t.Fatalf("valid chunk: %v", err)
	}
	_, err = StageTransferAttachmentChunk(ctx, env, TransferAttachmentChunkRequest{
		SHA256:     sha,
		Offset:     900,
		TotalBytes: int64(len(blob)),
		Meta:       transferAttachmentTestMeta("att-source-bad-offset", sha, int64(len(blob))),
		Chunk:      blob[:600],
	})
	var ie *ImportError
	if !errors.As(err, &ie) || ie.Code != "transfer_attachment_offset_invalid" {
		t.Fatalf("out-of-range chunk error = %v, want transfer_attachment_offset_invalid", err)
	}
	if n := fixture.fx.Count(t, `SELECT count(*) FROM transfer_attachment_upload_chunk WHERE workspace_id = $1`, fixture.workspace); n != 1 {
		t.Fatalf("rejected chunk changed the staged rows: %d", n)
	}
}

// An incomplete staging row is not assembled silently short: the commit says
// how far it got, and the attachment row is not written.
func TestTransferAttachmentUploadRefusesIncompleteAssembly(t *testing.T) {
	fixture, _ := newTransferAttachmentFixture(t)
	env := fixture.env
	blob := transferAttachmentTestBlob(71, 3000)
	sha := transferAttachmentTestSHA(blob)
	stageTransferAttachmentTestChunks(t, env, "att-source-incomplete", blob, 1000, []int{0, 2})

	_, err := CommitTransferAttachmentUpload(context.Background(), env, sha)
	var ie *ImportError
	if !errors.As(err, &ie) || ie.Code != "transfer_attachment_upload_incomplete" {
		t.Fatalf("commit error = %v, want transfer_attachment_upload_incomplete", err)
	}
	if n := fixture.fx.Count(t, `SELECT count(*) FROM attachment WHERE workspace_id = $1`, fixture.workspace); n != 0 {
		t.Fatalf("incomplete commit wrote %d attachment rows", n)
	}
	// The staged bytes stay: the resume depends on them.
	if n := fixture.fx.Count(t, `SELECT count(*) FROM transfer_attachment_upload_chunk WHERE workspace_id = $1`, fixture.workspace); n != 2 {
		t.Fatalf("incomplete commit discarded staging: %d chunks left", n)
	}
}

// The staging tables are workspace-keyed diagnostics with no foreign key, so a
// workspace teardown has to name them itself.
func TestDeleteWorkspaceReapsStagedAttachmentUploads(t *testing.T) {
	fixture, _ := newTransferAttachmentFixture(t)
	env := fixture.env
	blob := transferAttachmentTestBlob(81, 1000)
	sha := transferAttachmentTestSHA(blob)
	stageTransferAttachmentTestChunks(t, env, "att-source-teardown", blob, 500, []int{0})

	if err := fixture.env.Queries.DeleteWorkspaceLeafData(context.Background(), fixture.env.TargetID); err != nil {
		t.Fatalf("DeleteWorkspaceLeafData: %v", err)
	}
	if n := fixture.fx.Count(t, `SELECT count(*) FROM transfer_attachment_upload WHERE sha256 = $1`, sha); n != 0 {
		t.Fatalf("%d staging rows survived the workspace teardown", n)
	}
	if n := fixture.fx.Count(t, `SELECT count(*) FROM transfer_attachment_upload_chunk WHERE sha256 = $1`, sha); n != 0 {
		t.Fatalf("%d staging chunks survived the workspace teardown", n)
	}
	_ = fixture
}
