package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Content-addressed, resumable attachment staging.
//
// A transfer attachment is a single blob in a single request, and the blob can
// be far larger than one edge-timed request can carry: the reference bundle has
// a 6.2 MB blob on a ~45 KB/s uplink, which is past Cloudflare's 100 s proxy
// timeout. Cutting the blob into small chunks makes each request survivable;
// staging those chunks on the target is what makes a killed upload resumable
// instead of a restart. `sha256` is both the address of the blob and the row
// key, so "do you already have it" and "how many bytes do you have" are the
// same question.
const (
	// TransferAttachmentChunkMaxBytes caps one staged chunk. It bounds the
	// decompressed size of a single request and therefore the memory a chunk
	// request can make the server hold.
	TransferAttachmentChunkMaxBytes = 4 << 20
	// TransferAttachmentChunkDefaultBytes is the size the uploader starts at.
	// At ~45 KB/s this is ~45 s, which leaves the 100 s edge timeout enough
	// headroom to finish even when the measured rate was optimistic.
	TransferAttachmentChunkDefaultBytes = 2 << 20
	// TransferAttachmentChunkMinBytes is the floor an adaptive uploader may
	// shrink to before it gives up and reports the failure.
	TransferAttachmentChunkMinBytes = 128 << 10
	// TransferAttachmentUploadTTL is how long a half-finished upload is kept
	// before the next request for that workspace reaps it. A resume that waits
	// longer than this starts over — the staging rows are a cache, not a
	// ledger.
	TransferAttachmentUploadTTL = 24 * time.Hour
	// TransferAttachmentWorkspaceStagingMaxBytes bounds one workspace's staged
	// bytes. An aborted import must not be able to fill the target's disk with
	// half-uploaded blobs; the oldest uploads are evicted first.
	TransferAttachmentWorkspaceStagingMaxBytes int64 = 512 << 20
)

// TransferAttachmentUploadQuery is one "do you have this blob?" question. The
// source id travels with it because whether the work is already done is a
// question about the target attachment row (deterministic id), not about the
// staging rows.
type TransferAttachmentUploadQuery struct {
	SourceID string `json:"source_id"`
	SHA256   string `json:"sha256"`
}

// TransferAttachmentUploadStatus answers that question.
type TransferAttachmentUploadStatus struct {
	SourceID string `json:"source_id"`
	SHA256   string `json:"sha256"`
	// Imported means the attachment row already exists on the target, so the
	// uploader must not send the blob again at all.
	Imported bool `json:"imported"`
	// ReceivedBytes is every staged byte, including chunks after a hole.
	ReceivedBytes int64 `json:"received_bytes"`
	// TotalBytes is what the staging row recorded, or 0 when nothing is staged.
	TotalBytes int64 `json:"total_bytes"`
	// ResumeOffset is the length of the contiguous run starting at 0. It is
	// the only offset an uploader may trust: bytes after a hole cannot be
	// assembled yet.
	ResumeOffset int64 `json:"resume_offset"`
}

// TransferAttachmentStatusRequest is the batched form: one round trip answers
// the whole bundle, because asking per attachment would itself be hundreds of
// requests over the same slow link.
type TransferAttachmentStatusRequest struct {
	Entries []TransferAttachmentUploadQuery `json:"entries"`
}

type TransferAttachmentStatusReport struct {
	Entries []TransferAttachmentUploadStatus `json:"entries"`
}

// TransferAttachmentChunkRequest is one staged slice of a blob.
type TransferAttachmentChunkRequest struct {
	SHA256     string
	Offset     int64
	TotalBytes int64
	Meta       TransferAttachmentMeta
	Chunk      []byte
}

type TransferAttachmentChunkReport struct {
	SHA256        string `json:"sha256"`
	ReceivedBytes int64  `json:"received_bytes"`
	ResumeOffset  int64  `json:"resume_offset"`
	TotalBytes    int64  `json:"total_bytes"`
	Complete      bool   `json:"complete"`
	// Duplicate reports that this exact chunk was already staged, which is the
	// normal outcome of a resume re-sending from a boundary the server had
	// already passed.
	Duplicate bool `json:"duplicate"`
}

// ValidTransferAttachmentSHA256 is the shape check for the address. It is
// deliberately only a shape check: the content is verified at commit, which is
// the only point where the bytes exist as a whole.
func ValidTransferAttachmentSHA256(raw string) bool {
	if len(raw) != 64 {
		return false
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// TransferAttachmentUploadStatuses answers a batch of "do you have this blob?"
// questions.
func TransferAttachmentUploadStatuses(ctx context.Context, env TransferImportEnv, entries []TransferAttachmentUploadQuery) (*TransferAttachmentStatusReport, error) {
	if env.Queries == nil {
		return nil, fmt.Errorf("transfer import has no queries")
	}
	if err := cleanupTransferAttachmentUploads(ctx, env, time.Now().UTC()); err != nil {
		return nil, err
	}
	report := &TransferAttachmentStatusReport{Entries: make([]TransferAttachmentUploadStatus, 0, len(entries))}
	for _, entry := range entries {
		status := TransferAttachmentUploadStatus{
			SourceID: strings.TrimSpace(entry.SourceID),
			SHA256:   strings.ToLower(strings.TrimSpace(entry.SHA256)),
		}
		if status.SourceID != "" {
			existing, err := env.Queries.GetAttachment(ctx, db.GetAttachmentParams{
				ID:          pgUUID(TransferAttachmentID(uuidString(env.TargetID), status.SourceID)),
				WorkspaceID: env.TargetID,
			})
			if err == nil && uuidString(existing.ID) != "" {
				status.Imported = true
				report.Entries = append(report.Entries, status)
				continue
			}
		}
		if !ValidTransferAttachmentSHA256(status.SHA256) {
			report.Entries = append(report.Entries, status)
			continue
		}
		upload, err := env.Queries.GetTransferAttachmentUpload(ctx, db.GetTransferAttachmentUploadParams{
			WorkspaceID: env.TargetID,
			Sha256:      status.SHA256,
		})
		if err != nil {
			report.Entries = append(report.Entries, status)
			continue
		}
		ranges, err := env.Queries.ListTransferAttachmentUploadChunkRanges(ctx, db.ListTransferAttachmentUploadChunkRangesParams{
			WorkspaceID: env.TargetID,
			Sha256:      status.SHA256,
		})
		if err != nil {
			return nil, fmt.Errorf("read staged chunks: %w", err)
		}
		status.TotalBytes = upload.TotalBytes
		status.ResumeOffset = contiguousTransferChunkPrefix(ranges)
		for _, r := range ranges {
			status.ReceivedBytes += r.SizeBytes
		}
		report.Entries = append(report.Entries, status)
	}
	return report, nil
}

// StageTransferAttachmentChunk stores one slice. It is idempotent on
// (sha256, offset): a resume that re-sends a chunk the server already holds
// leaves the staged bytes untouched.
func StageTransferAttachmentChunk(ctx context.Context, env TransferImportEnv, req TransferAttachmentChunkRequest) (*TransferAttachmentChunkReport, error) {
	if env.Queries == nil {
		return nil, fmt.Errorf("transfer import has no queries")
	}
	sha := strings.ToLower(strings.TrimSpace(req.SHA256))
	if !ValidTransferAttachmentSHA256(sha) {
		return nil, &ImportError{Status: 400, Code: "transfer_attachment_sha256_invalid", Msg: "sha256 must be 64 lowercase hex characters"}
	}
	if req.TotalBytes <= 0 {
		return nil, &ImportError{Status: 400, Code: "transfer_attachment_total_invalid", Msg: "total_bytes must be positive"}
	}
	if req.Offset < 0 || req.Offset+int64(len(req.Chunk)) > req.TotalBytes {
		return nil, &ImportError{Status: 400, Code: "transfer_attachment_offset_invalid", Msg: "chunk range falls outside the blob"}
	}
	if len(req.Chunk) == 0 {
		return nil, &ImportError{Status: 400, Code: "transfer_attachment_chunk_empty", Msg: "chunk carries no bytes"}
	}
	if len(req.Chunk) > TransferAttachmentChunkMaxBytes {
		return nil, &ImportError{Status: 413, Code: "transfer_bundle_too_large", Msg: fmt.Sprintf("chunk exceeds %d bytes", TransferAttachmentChunkMaxBytes)}
	}
	sourceID := strings.TrimSpace(req.Meta.SourceID)
	if sourceID == "" {
		return nil, &ImportError{Status: 400, Code: "transfer_bundle_invalid", Msg: "attachment meta has no source_id"}
	}
	if req.Meta.SHA256 != "" && !strings.EqualFold(req.Meta.SHA256, sha) {
		return nil, &ImportError{Status: 400, Code: "transfer_attachment_sha256_mismatch", Msg: "meta.sha256 does not match the staged chunk's sha256"}
	}
	if req.Meta.SizeBytes != 0 && req.Meta.SizeBytes != req.TotalBytes {
		return nil, &ImportError{Status: 400, Code: "transfer_attachment_total_invalid", Msg: "meta.size_bytes does not match total_bytes"}
	}
	metaJSON, err := marshalTransferAttachmentMeta(req.Meta)
	if err != nil {
		return nil, err
	}
	if err := cleanupTransferAttachmentUploads(ctx, env, time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := env.Queries.UpsertTransferAttachmentUpload(ctx, db.UpsertTransferAttachmentUploadParams{
		WorkspaceID: env.TargetID,
		Sha256:      sha,
		SourceID:    sourceID,
		UploaderID:  env.ImporterID,
		TotalBytes:  req.TotalBytes,
		Meta:        metaJSON,
	}); err != nil {
		return nil, fmt.Errorf("stage attachment upload: %w", err)
	}
	rows, err := env.Queries.InsertTransferAttachmentUploadChunk(ctx, db.InsertTransferAttachmentUploadChunkParams{
		WorkspaceID: env.TargetID,
		Sha256:      sha,
		OffsetBytes: req.Offset,
		SizeBytes:   int64(len(req.Chunk)),
		Data:        req.Chunk,
	})
	if err != nil {
		return nil, fmt.Errorf("stage attachment chunk: %w", err)
	}
	summary, err := env.Queries.SummarizeTransferAttachmentUploadChunks(ctx, db.SummarizeTransferAttachmentUploadChunksParams{
		WorkspaceID: env.TargetID,
		Sha256:      sha,
	})
	if err != nil {
		return nil, fmt.Errorf("summarize staged chunks: %w", err)
	}
	if err := env.Queries.SetTransferAttachmentUploadReceived(ctx, db.SetTransferAttachmentUploadReceivedParams{
		WorkspaceID:   env.TargetID,
		Sha256:        sha,
		ReceivedBytes: summary.ReceivedBytes,
	}); err != nil {
		return nil, fmt.Errorf("record staged bytes: %w", err)
	}
	ranges, err := env.Queries.ListTransferAttachmentUploadChunkRanges(ctx, db.ListTransferAttachmentUploadChunkRangesParams{
		WorkspaceID: env.TargetID,
		Sha256:      sha,
	})
	if err != nil {
		return nil, fmt.Errorf("read staged chunks: %w", err)
	}
	resume := contiguousTransferChunkPrefix(ranges)
	return &TransferAttachmentChunkReport{
		SHA256:        sha,
		ReceivedBytes: summary.ReceivedBytes,
		ResumeOffset:  resume,
		TotalBytes:    req.TotalBytes,
		Complete:      resume >= req.TotalBytes,
		Duplicate:     rows == 0,
	}, nil
}

// CommitTransferAttachmentUpload assembles the staged chunks, verifies the
// blob against its own address, and only then writes the attachment row.
//
// A checksum mismatch discards the whole staging row: a body that does not
// hash to its address is not a partially-good body, it is the wrong bytes, and
// keeping them would make the next resume assemble the same wrong blob.
func CommitTransferAttachmentUpload(ctx context.Context, env TransferImportEnv, rawSHA string) (*TransferAttachmentReport, error) {
	if env.Queries == nil {
		return nil, fmt.Errorf("transfer import has no queries")
	}
	sha := strings.ToLower(strings.TrimSpace(rawSHA))
	if !ValidTransferAttachmentSHA256(sha) {
		return nil, &ImportError{Status: 400, Code: "transfer_attachment_sha256_invalid", Msg: "sha256 must be 64 lowercase hex characters"}
	}
	upload, err := env.Queries.GetTransferAttachmentUpload(ctx, db.GetTransferAttachmentUploadParams{
		WorkspaceID: env.TargetID,
		Sha256:      sha,
	})
	if err != nil {
		return nil, &ImportError{Status: 404, Code: "transfer_attachment_upload_missing", Msg: "no staged upload for this sha256"}
	}
	var meta TransferAttachmentMeta
	if err := unmarshalTransferAttachmentMeta(upload.Meta, &meta); err != nil {
		return nil, fmt.Errorf("decode staged attachment meta: %w", err)
	}
	chunks, err := env.Queries.ListTransferAttachmentUploadChunks(ctx, db.ListTransferAttachmentUploadChunksParams{
		WorkspaceID: env.TargetID,
		Sha256:      sha,
	})
	if err != nil {
		return nil, fmt.Errorf("read staged chunks: %w", err)
	}
	body, err := assembleTransferAttachmentChunks(chunks, upload.TotalBytes)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != sha {
		if derr := discardTransferAttachmentUpload(ctx, env, sha); derr != nil {
			return nil, derr
		}
		return nil, &ImportError{
			Status: 422,
			Code:   "transfer_attachment_checksum_mismatch",
			Msg:    fmt.Sprintf("staged bytes for %s do not hash to their sha256; the staging row was discarded", sha),
		}
	}
	report, err := ImportTransferAttachment(ctx, env, meta, body)
	if err != nil {
		return nil, err
	}
	if err := discardTransferAttachmentUpload(ctx, env, sha); err != nil {
		return nil, err
	}
	return report, nil
}

// assembleTransferAttachmentChunks checks coverage before it concatenates:
// a hole means the uploader's resume offset was wrong, and concatenating
// around it would silently store a body shorter than the meta claims.
func assembleTransferAttachmentChunks(chunks []db.ListTransferAttachmentUploadChunksRow, total int64) ([]byte, error) {
	ranges := make([]db.ListTransferAttachmentUploadChunkRangesRow, 0, len(chunks))
	for _, c := range chunks {
		ranges = append(ranges, db.ListTransferAttachmentUploadChunkRangesRow{OffsetBytes: c.OffsetBytes, SizeBytes: c.SizeBytes})
	}
	if prefix := contiguousTransferChunkPrefix(ranges); prefix < total {
		return nil, &ImportError{
			Status: 409,
			Code:   "transfer_attachment_upload_incomplete",
			Msg:    fmt.Sprintf("staged upload covers %d of %d bytes", prefix, total),
		}
	}
	body := make([]byte, 0, total)
	for _, c := range chunks {
		covered := int64(len(body))
		if c.OffsetBytes > covered {
			return nil, &ImportError{Status: 409, Code: "transfer_attachment_upload_incomplete", Msg: "staged chunks have a gap"}
		}
		// A slice can start before the assembled prefix and still carry new
		// bytes: a resume re-sends from whatever boundary the target reported,
		// which need not be a boundary an earlier pass used. Skipping such a
		// slice wholesale is what turns its tail into a phantom gap.
		if c.OffsetBytes+int64(len(c.Data)) <= covered {
			continue
		}
		body = append(body, c.Data[covered-c.OffsetBytes:]...)
	}
	if int64(len(body)) != total {
		return nil, &ImportError{
			Status: 409,
			Code:   "transfer_attachment_upload_incomplete",
			Msg:    fmt.Sprintf("assembled %d of %d bytes", len(body), total),
		}
	}
	return body, nil
}

// contiguousTransferChunkPrefix returns the number of bytes covered without a
// gap, starting at offset 0. Ranges must be sorted by offset.
func contiguousTransferChunkPrefix(ranges []db.ListTransferAttachmentUploadChunkRangesRow) int64 {
	var prefix int64
	for _, r := range ranges {
		if r.OffsetBytes > prefix {
			break
		}
		if end := r.OffsetBytes + r.SizeBytes; end > prefix {
			prefix = end
		}
	}
	return prefix
}

// cleanupTransferAttachmentUploads reaps this workspace's stale staging: first
// everything past its TTL, then the oldest uploads until the workspace is back
// under the byte cap. It runs on every staging request, so a workspace whose
// import died keeps shedding its partial blobs without anyone asking.
func cleanupTransferAttachmentUploads(ctx context.Context, env TransferImportEnv, now time.Time) error {
	uploads, err := env.Queries.ListTransferAttachmentUploadsForWorkspace(ctx, env.TargetID)
	if err != nil {
		return fmt.Errorf("list staged attachment uploads: %w", err)
	}
	if len(uploads) == 0 {
		return nil
	}
	// ListTransferAttachmentUploadsForWorkspace orders by created_at, so the
	// eviction order is the same list read once.
	stale := make([]string, 0, len(uploads))
	kept := make([]db.ListTransferAttachmentUploadsForWorkspaceRow, 0, len(uploads))
	var staged int64
	for _, u := range uploads {
		if u.CreatedAt.Valid && now.Sub(u.CreatedAt.Time) > TransferAttachmentUploadTTL {
			stale = append(stale, u.Sha256)
			continue
		}
		staged += u.ReceivedBytes
		kept = append(kept, u)
	}
	for _, sha := range stale {
		if err := discardTransferAttachmentUpload(ctx, env, sha); err != nil {
			return err
		}
	}
	for i := 0; i < len(kept) && staged > TransferAttachmentWorkspaceStagingMaxBytes; i++ {
		if err := discardTransferAttachmentUpload(ctx, env, kept[i].Sha256); err != nil {
			return err
		}
		staged -= kept[i].ReceivedBytes
	}
	return nil
}

func discardTransferAttachmentUpload(ctx context.Context, env TransferImportEnv, sha string) error {
	if err := env.Queries.DeleteTransferAttachmentUploadChunks(ctx, db.DeleteTransferAttachmentUploadChunksParams{
		WorkspaceID: env.TargetID,
		Sha256:      sha,
	}); err != nil {
		return fmt.Errorf("discard staged chunks: %w", err)
	}
	if err := env.Queries.DeleteTransferAttachmentUpload(ctx, db.DeleteTransferAttachmentUploadParams{
		WorkspaceID: env.TargetID,
		Sha256:      sha,
	}); err != nil {
		return fmt.Errorf("discard staged upload: %w", err)
	}
	return nil
}

func marshalTransferAttachmentMeta(meta TransferAttachmentMeta) ([]byte, error) {
	return json.Marshal(meta)
}

func unmarshalTransferAttachmentMeta(raw []byte, meta *TransferAttachmentMeta) error {
	return json.Unmarshal(raw, meta)
}
