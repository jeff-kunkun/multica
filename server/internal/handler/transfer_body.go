package handler

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Transfer request bodies may arrive gzip-compressed (Content-Encoding: gzip).
//
// The migration case is what forced it: a bundle whose issues group is one
// 1.5 MB issues shard plus an 8.4 MB comments shard leaves the CLI with a ~10 MB
// JSON body, and a self-hosted target behind a CDN edge cuts any request that
// has not finished in 100 seconds. Compressing the body is worth 5–10× on JSON
// — the single largest win available on that path — and it costs nothing on the
// wire for a client that does not opt in, because the header is what selects
// the behavior.
//
// The decompressed size is capped separately from the encoded one: a small
// gzip body can expand without limit, so honoring only the transfer endpoints'
// existing byte caps on the wire would let a zip bomb through the door they
// were built to close.

var errTransferBodyGzip = errors.New("transfer request body is not valid gzip")

// openTransferBody returns the request body with any Content-Encoding: gzip
// undone, reading at most limit bytes of the DECODED body.
//
// The caller owns the returned reader and must close it. A body that is
// declared gzip but is not fails here rather than inside the JSON decoder,
// because "your proxy re-encoded the body" and "your JSON is malformed" are
// different problems and only one of them is the client's.
func openTransferBody(w http.ResponseWriter, r *http.Request, limit int64) (io.ReadCloser, error) {
	// The original reader is captured here, never read back off r.Body: the
	// multipart caller assigns this function's result to r.Body, so a closure
	// that closed "r.Body" would close the wrapper it is inside and recurse
	// until the stack ran out.
	original := r.Body
	body := io.Reader(original)
	closeBody := func() { _ = original.Close() }
	if transferBodyIsGzip(r) {
		gz, err := gzip.NewReader(original)
		if err != nil {
			closeBody()
			return nil, fmt.Errorf("%w: %v", errTransferBodyGzip, err)
		}
		body = gz
		closeBody = func() {
			_ = gz.Close()
			_ = original.Close()
		}
	}
	// MaxBytesReader is what keeps the decoded body bounded. It answers reads
	// past the cap with *http.MaxBytesError, which the callers already map to
	// a readable 413, and it arms the connection's read deadline so a client
	// cannot hold the handler open by trickling bytes forever.
	limited := http.MaxBytesReader(w, io.NopCloser(body), limit)
	return closeOnly{limited, closeBody}, nil
}

// transferBodyIsGzip reports whether the request declares a gzip body. The
// header is a comma-separated list of encodings applied in order; gzip
// anywhere in it means this build can read the body (it only ever sees one).
func transferBodyIsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Content-Encoding"), ",") {
		if strings.EqualFold(strings.TrimSpace(part), "gzip") {
			return true
		}
	}
	return false
}

// closeOnly closes both the limited reader and the decoder underneath it in
// one Close, so every caller can keep the plain `defer body.Close()` shape.
type closeOnly struct {
	io.ReadCloser
	close func()
}

func (c closeOnly) Close() error {
	err := c.ReadCloser.Close()
	c.close()
	return err
}

// transferBodyErrorStatus maps openTransferBody's sentinel errors onto the
// response the caller should write. It reports ok=false for anything else,
// which is what lets the two error shapes that need different words — an
// undecodable gzip header and a body past the cap — stay apart.
func transferBodyErrorStatus(err error) (status int, code, msg string, ok bool) {
	if errors.Is(err, errTransferBodyGzip) {
		return http.StatusBadRequest, "transfer_bundle_invalid",
			"Content-Encoding says gzip but the request body is not valid gzip", true
	}
	return 0, "", "", false
}

// writeTransferDecodeError answers the body-level failures every transfer JSON
// handler shares: a decoded body past the endpoint's cap, and a body that
// cannot be read at all. tooLargeCode is the endpoint's own 413 code, so the
// config bundle keeps answering `config_bundle_too_large` — the code its
// contract and its callers already name — instead of being folded into the
// issues group's. It reports whether it wrote a response.
func writeTransferDecodeError(w http.ResponseWriter, err error, limitBytes int64, tooLargeCode string) bool {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		writeErrorCode(w, http.StatusRequestEntityTooLarge, tooLargeCode,
			fmt.Sprintf("the decoded transfer request body exceeds %d MiB", limitBytes>>20))
		return true
	}
	if status, code, msg, ok := transferBodyErrorStatus(err); ok {
		writeErrorCode(w, status, code, msg)
		return true
	}
	return false
}
