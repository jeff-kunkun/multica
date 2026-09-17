package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/service"
)

// Resumable attachment upload (DENE-443).
//
// The attachments group of a real bundle is ~165 MB across 370+ files, and one
// request carries one blob. On the ~45 KB/s uplink these migrations run over,
// a single 6.2 MB blob is ~140 s — past Cloudflare's 100 s proxy timeout — and
// a killed import used to start over from byte zero. So on a target that
// advertises chunk staging the uploader asks once which blobs already exist and
// how far each one got, then sends only the missing bytes, one small slice per
// request. The slice size starts at the advertised default and follows the
// measured throughput, because the honest unit is seconds-on-the-wire, not
// bytes.
const (
	// transferAttachmentWholeBlobLimitBytes is the largest blob the fallback
	// single-request path may attempt. Above it the request cannot finish
	// inside a 100 s edge timeout on the uplinks this migration targets, so
	// the import refuses with the reason instead of uploading until the proxy
	// cuts the connection.
	transferAttachmentWholeBlobLimitBytes = 4 << 20

	// transferAttachmentChunkTargetSeconds is how long one chunk request
	// should take. 30 s inside a 100 s proxy timeout leaves room for the
	// handshake and the response on a link that is slower than measured.
	transferAttachmentChunkTargetSeconds = 30.0

	// transferAttachmentChunkSplits is how many times one failing chunk is
	// halved before the import gives up. The writes are idempotent on
	// (sha256, offset), so a retry that lands twice is free.
	transferAttachmentChunkSplits = 3

	// transferAttachmentChunkRewinds bounds how often one blob may rewind to
	// a target-reported resume point that moved backwards. One rewind is a
	// killed predecessor's request landing late; a run of them means the
	// target is not converging and the import should say so.
	transferAttachmentChunkRewinds = 3
)

// transferAttachmentUploader sends the attachments group for one import.
type transferAttachmentUploader struct {
	client *cli.APIClient
	// sender is the DENE-442 wire path. The whole-blob fallback goes through
	// it rather than through a second posting helper, so compression, the
	// request budget and the halving retry stay defined in one place.
	sender   *transferWireSender
	base     string
	chunkMax int64
	progress *transferProgressReporter

	// staged is the preflight answer, keyed by the bundle's source id.
	staged map[string]service.TransferAttachmentUploadStatus

	chunkBytes int64
	totalBytes int64
	doneBytes  int64
	totalCount int
	doneCount  int
}

func newTransferAttachmentUploader(client *cli.APIClient, sender *transferWireSender, base string, chunkMax int64, progress *transferProgressReporter, attachments []service.TransferAttachmentRow) *transferAttachmentUploader {
	u := &transferAttachmentUploader{
		client:     client,
		sender:     sender,
		base:       base,
		chunkMax:   chunkMax,
		progress:   progress,
		staged:     map[string]service.TransferAttachmentUploadStatus{},
		chunkBytes: service.TransferAttachmentChunkDefaultBytes,
		totalCount: len(attachments),
	}
	for _, att := range attachments {
		if att.BodyOmittedReason == nil {
			u.totalBytes += att.SizeBytes
		}
	}
	if u.chunkMax > 0 && u.chunkBytes > u.chunkMax {
		u.chunkBytes = u.chunkMax
	}
	return u
}

// preflight asks the target which blobs it already has. One request answers
// the whole bundle: asking per attachment would itself be hundreds of requests
// over the link this exists to survive.
func (u *transferAttachmentUploader) preflight(ctx context.Context, attachments []service.TransferAttachmentRow) error {
	if u.chunkMax <= 0 {
		return nil
	}
	entries := make([]service.TransferAttachmentUploadQuery, 0, len(attachments))
	for _, att := range attachments {
		if att.BodyOmittedReason != nil || att.SHA256 == "" {
			continue
		}
		entries = append(entries, service.TransferAttachmentUploadQuery{
			SourceID: att.SourceID,
			SHA256:   strings.ToLower(att.SHA256),
		})
	}
	if len(entries) == 0 {
		return nil
	}
	report, err := postTransferAttachmentStatus(ctx, u.client, u.base+"/transfer/attachments/status", entries)
	if err != nil {
		return fmt.Errorf("ask the target which attachments exist: %w", err)
	}
	for _, entry := range report.Entries {
		u.staged[entry.SourceID] = entry
	}
	return nil
}

// run uploads every attachment and reports byte progress.
func (u *transferAttachmentUploader) run(ctx context.Context, payload *loadedTransfer) error {
	if err := u.preflight(ctx, payload.Attachments); err != nil {
		return err
	}
	u.report()
	for _, att := range payload.Attachments {
		var blob []byte
		if att.SHA256 != "" {
			blob = payload.Blobs[att.SHA256]
		}
		if err := u.upload(ctx, att, blob); err != nil {
			return fmt.Errorf("upload attachment %s: %w", att.SourceID, err)
		}
		u.doneBytes += att.SizeBytes
		u.doneCount++
		u.report()
	}
	return nil
}

func (u *transferAttachmentUploader) upload(ctx context.Context, att service.TransferAttachmentRow, blob []byte) error {
	// A body-less row (omitted for size or secrecy) is metadata only: there is
	// nothing to chunk and nothing to time out.
	if att.BodyOmittedReason != nil || att.SHA256 == "" || len(blob) == 0 {
		return u.postWholeBlob(ctx, att, blob)
	}
	status := u.staged[att.SourceID]
	if status.Imported {
		return nil
	}
	if u.chunkMax <= 0 {
		if int64(len(blob)) > transferAttachmentWholeBlobLimitBytes {
			return fmt.Errorf(
				"target_unsupported: this blob is %d bytes and the target does not support resumable attachment "+
					"staging (no attachment_chunk_max_bytes in /health); a single %d-byte request cannot finish inside a "+
					"100 s edge timeout on a slow uplink — upgrade the target to a build with chunked attachments, or "+
					"import over a link that is not behind the proxy",
				len(blob), len(blob))
		}
		return u.postWholeBlob(ctx, att, blob)
	}
	if status.TotalBytes != 0 && status.TotalBytes != int64(len(blob)) {
		return fmt.Errorf("target holds %d staged bytes for sha256 %s but the bundle blob is %d bytes",
			status.TotalBytes, att.SHA256, len(blob))
	}
	if err := u.sendChunks(ctx, att, blob, status.ResumeOffset); err != nil {
		return err
	}
	return postTransferAttachmentCommit(ctx, u.client, u.base+"/transfer/attachments/commit", att.SHA256)
}

// postWholeBlob is the pre-chunking path: one request carries the whole blob.
// It is what a body-less row and a target without chunk staging get.
func (u *transferAttachmentUploader) postWholeBlob(ctx context.Context, att service.TransferAttachmentRow, blob []byte) error {
	chunk, err := transferAttachmentChunk(att, blob)
	if err != nil {
		return err
	}
	_, err = u.sender.post(ctx, u.base+"/transfer/attachments", chunk)
	return err
}

// sendChunks walks the blob from resumeOffset to the end. The server answers
// each slice with the contiguous prefix it now holds, and that answer — not
// the client's arithmetic — is what the next slice starts from.
func (u *transferAttachmentUploader) sendChunks(ctx context.Context, att service.TransferAttachmentRow, blob []byte, resumeOffset int64) error {
	total := int64(len(blob))
	offset := resumeOffset
	if offset < 0 || offset > total {
		return fmt.Errorf("target reported resume offset %d for a %d-byte blob", offset, total)
	}
	rewinds := 0
	meta := service.TransferAttachmentMeta{
		SourceID:          att.SourceID,
		ChatSessionID:     att.ChatSessionID,
		ChatMessageID:     att.ChatMessageID,
		IssueID:           att.IssueID,
		CommentID:         att.CommentID,
		Filename:          att.Filename,
		ContentType:       att.ContentType,
		SizeBytes:         att.SizeBytes,
		CreatedAt:         att.CreatedAt,
		SHA256:            att.SHA256,
		BodyOmittedReason: att.BodyOmittedReason,
	}
	for offset < total {
		size := int64(u.nextChunkSize(total - offset))
		chunk := blob[offset : offset+size]
		started := time.Now()
		report, err := u.sendChunkWithSplits(ctx, meta, att.SHA256, offset, total, chunk)
		elapsed := time.Since(started)
		if err != nil {
			return err
		}
		if report.ResumeOffset < offset {
			// The target's staged prefix moved backwards under us: a killed
			// run's request landed after this run asked where to continue, or
			// staging was reaped between the preflight and now. The target is
			// the authority on what it holds, so rewind and re-send from there
			// instead of failing a resume that is still making progress.
			rewinds++
			if rewinds > transferAttachmentChunkRewinds {
				return fmt.Errorf("target keeps moving the resume point for sha256 %s backwards (now %d); giving up",
					att.SHA256, report.ResumeOffset)
			}
			offset = report.ResumeOffset
			continue
		}
		if report.ResumeOffset == offset {
			return fmt.Errorf("target accepted %d bytes at offset %d and still reports no contiguous progress",
				len(chunk), offset)
		}
		u.observeThroughput(int64(len(chunk)), elapsed)
		offset = report.ResumeOffset
		if offset > total {
			return fmt.Errorf("target reports %d contiguous bytes for a %d-byte blob", offset, total)
		}
		u.reportWithInFlight(offset - resumeOffset)
	}
	return nil
}

// sendChunkWithSplits retries one slice, halving it on every retryable
// failure. A request that dies mid-body is retried at the same offset, which
// is safe because staging is keyed by (sha256, offset).
func (u *transferAttachmentUploader) sendChunkWithSplits(ctx context.Context, meta service.TransferAttachmentMeta, sha string, offset, total int64, chunk []byte) (*service.TransferAttachmentChunkReport, error) {
	size := len(chunk)
	var lastErr error
	for attempt := 0; attempt <= transferAttachmentChunkSplits; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		report, err := postTransferAttachmentChunk(ctx, u.client, u.base+"/transfer/attachments/chunk", meta, sha, offset, total, chunk[:size])
		if err == nil {
			return report, nil
		}
		lastErr = err
		if !transferAttachmentRequestRetryable(err) {
			return nil, err
		}
		// A small blob is one chunk however the arithmetic comes out, so the
		// split budget — not the adaptive floor — is what stops the retries.
		if attempt == transferAttachmentChunkSplits || size <= 1 {
			return nil, fmt.Errorf("chunk at offset %d (sha256 %s) kept failing: %w", offset, sha, lastErr)
		}
		size /= 2
		if size < 1 {
			size = 1
		}
		if size < int(u.chunkBytes) {
			u.chunkBytes = int64(size)
		}
	}
	return nil, fmt.Errorf("chunk at offset %d (sha256 %s) kept failing: %w", offset, sha, lastErr)
}

// nextChunkSize clamps the adaptive size to what is left and to the server's cap.
func (u *transferAttachmentUploader) nextChunkSize(remaining int64) int {
	size := u.chunkBytes
	if u.chunkMax > 0 && size > u.chunkMax {
		size = u.chunkMax
	}
	if size > remaining {
		size = remaining
	}
	if size < 1 {
		size = 1
	}
	return int(size)
}

// observeThroughput retargets the slice size at roughly
// transferAttachmentChunkTargetSeconds per request. A measured rate is the only
// honest input: the bundle's own metadata says nothing about the link.
func (u *transferAttachmentUploader) observeThroughput(bytes int64, elapsed time.Duration) {
	if bytes <= 0 || elapsed <= 0 {
		return
	}
	rate := float64(bytes) / elapsed.Seconds()
	target := int64(rate * transferAttachmentChunkTargetSeconds)
	if target < service.TransferAttachmentChunkMinBytes {
		target = service.TransferAttachmentChunkMinBytes
	}
	if u.chunkMax > 0 && target > u.chunkMax {
		target = u.chunkMax
	}
	u.chunkBytes = target
}

func (u *transferAttachmentUploader) report() {
	u.reportWithInFlight(0)
}

func (u *transferAttachmentUploader) reportWithInFlight(inFlight int64) {
	if u.progress == nil {
		return
	}
	done := u.doneBytes + inFlight
	if done > u.totalBytes {
		done = u.totalBytes
	}
	u.progress.reportUploadBytes(done, u.totalBytes, u.doneCount, u.totalCount)
}

// transferAttachmentRequestRetryable reports whether re-sending a slice could
// succeed. Edge timeouts (522/524), gateway errors, 408, 429 and a transport
// that died mid-body are all "the request did not make it", not "the request
// was wrong".
func transferAttachmentRequestRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var he *cli.HTTPError
	if errors.As(err, &he) {
		switch he.StatusCode {
		case http.StatusRequestTimeout, http.StatusTooManyRequests,
			http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout,
			522, 524:
			return true
		default:
			return false
		}
	}
	var ie *service.ImportError
	if errors.As(err, &ie) {
		return ie.Status >= 500
	}
	// No HTTP response at all: EOF, connection reset, an edge that closed the
	// socket while the body was still going up. That is the failure this whole
	// path exists for, so it is retryable by default.
	return true
}

func postTransferAttachmentStatus(ctx context.Context, client *cli.APIClient, path string, entries []service.TransferAttachmentUploadQuery) (*service.TransferAttachmentStatusReport, error) {
	body := service.TransferAttachmentStatusRequest{Entries: entries}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(client.BaseURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	applyTransferHeaders(req, client)
	var out service.TransferAttachmentStatusReport
	if err := doTransferJSON(req, client, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func postTransferAttachmentCommit(ctx context.Context, client *cli.APIClient, path, sha string) error {
	raw, err := json.Marshal(map[string]string{"sha256": sha})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(client.BaseURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	applyTransferHeaders(req, client)
	var out service.TransferAttachmentReport
	return doTransferJSON(req, client, &out)
}

func postTransferAttachmentChunk(ctx context.Context, client *cli.APIClient, path string, meta service.TransferAttachmentMeta, sha string, offset, total int64, chunk []byte) (*service.TransferAttachmentChunkReport, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	if err := mw.WriteField("meta", string(metaJSON)); err != nil {
		return nil, err
	}
	if err := mw.WriteField("sha256", sha); err != nil {
		return nil, err
	}
	if err := mw.WriteField("offset", fmt.Sprintf("%d", offset)); err != nil {
		return nil, err
	}
	if err := mw.WriteField("total_bytes", fmt.Sprintf("%d", total)); err != nil {
		return nil, err
	}
	part, err := mw.CreateFormFile("file", meta.Filename)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(chunk); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(client.BaseURL, "/")+path, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	applyTransferHeaders(req, client)
	var out service.TransferAttachmentChunkReport
	if err := doTransferJSON(req, client, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func applyTransferHeaders(req *http.Request, client *cli.APIClient) {
	if client.Token != "" {
		req.Header.Set("Authorization", "Bearer "+client.Token)
	}
	if client.WorkspaceID != "" {
		req.Header.Set("X-Workspace-ID", client.WorkspaceID)
	}
}

func doTransferJSON(req *http.Request, client *cli.APIClient, out any) error {
	resp, err := client.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return &cli.HTTPError{
			Method:     req.Method,
			Path:       req.URL.Path,
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(body)),
		}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
