package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/service"
)

// ---------------------------------------------------------------------------
// A target that records what actually arrived on the wire
// ---------------------------------------------------------------------------

// transferWireRecorder is a target instance reduced to what this file needs to
// observe: how many requests arrived, how big each was after decoding, and
// whether each was gzip-compressed.
type transferWireRecorder struct {
	mu       sync.Mutex
	requests []transferWireArrival
}

type transferWireArrival struct {
	path           string
	contentType    string
	encoding       string
	body           []byte
	decodedBytes   int
	encodedBytes   int
	transferIssues service.TransferIssuesRequest
}

func (r *transferWireRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

func (r *transferWireRecorder) all() []transferWireArrival {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]transferWireArrival, len(r.requests))
	copy(out, r.requests)
	return out
}

// server answers /health with the given capabilities and records every POST.
// respond decides the status: a nil respond means 200 with a small JSON body.
func (r *transferWireRecorder) server(t *testing.T, caps map[string]any, respond func(arrivalCount int, arrival transferWireArrival) int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/health" {
			payload := map[string]any{"status": "ok"}
			if caps != nil {
				payload["transfer"] = caps
			}
			writeJSONTest(w, payload)
			return
		}
		arrival := transferWireArrival{path: req.URL.Path, contentType: req.Header.Get("Content-Type"), encoding: req.Header.Get("Content-Encoding")}
		raw, _ := io.ReadAll(req.Body)
		arrival.encodedBytes = len(raw)
		arrival.body = raw
		if arrival.encoding == "gzip" {
			zr, err := gzip.NewReader(strings.NewReader(string(raw)))
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			decoded, err := io.ReadAll(zr)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			arrival.body = decoded
		}
		arrival.decodedBytes = len(arrival.body)
		_ = json.Unmarshal(arrival.body, &arrival.transferIssues)

		r.mu.Lock()
		r.requests = append(r.requests, arrival)
		n := len(r.requests)
		r.mu.Unlock()

		status := http.StatusOK
		if respond != nil {
			status = respond(n, arrival)
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		writeJSONTest(w, map[string]any{"applied": true, "issues_created": len(arrival.transferIssues.Issues)})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTransferTestSender(t *testing.T, baseURL string, configured int, caps map[string]any) (*transferWireSender, *transferWireRecorder) {
	t.Helper()
	recorder := &transferWireRecorder{}
	client := &cli.APIClient{BaseURL: baseURL, HTTPClient: &http.Client{Timeout: 30 * time.Second}}
	sender := newTransferWireSender(client, probeTestTarget(caps), configured, newTransferProgressReporter(io.Discard))
	return sender, recorder
}

// probeTestTarget builds the target the probe would have produced, without
// running one.
func probeTestTarget(caps map[string]any) transferWireTarget {
	if caps == nil {
		return transferWireTarget{host: "target.test"}
	}
	raw, _ := json.Marshal(caps)
	var parsed service.TransferCapabilities
	_ = json.Unmarshal(raw, &parsed)
	if parsed.MaxSchemaVersion == 0 {
		parsed.MaxSchemaVersion = service.TransferBundleSchemaVersion
	}
	return transferWireTarget{probed: true, caps: &parsed, host: "target.test"}
}

// ---------------------------------------------------------------------------
// Compression is negotiated, never assumed
// ---------------------------------------------------------------------------

// A target that advertises accepts_gzip has to receive a Content-Encoding the
// server can honor; the body it decodes must be the JSON the caller built.
func TestTransferWire_CompressesOnlyWhenTheTargetAdvertisesIt(t *testing.T) {
	for _, tc := range []struct {
		name        string
		caps        map[string]any
		wantGzip    bool
		wantDecoded bool
	}{
		{
			name: "advertised",
			caps: map[string]any{
				"max_schema_version": 2,
				"groups":             []string{"config", "conversations", "attachments", "issues"},
				"accepts_gzip":       true,
				"max_request_bytes":  20 << 20,
			},
			wantGzip: true,
		},
		{
			name: "old instance without the field",
			caps: map[string]any{
				"max_schema_version": 2,
				"groups":             []string{"config", "conversations", "attachments", "issues"},
			},
			wantGzip: false,
		},
		{
			name:     "no capability answer at all",
			caps:     nil,
			wantGzip: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &transferWireRecorder{}
			srv := recorder.server(t, tc.caps, nil)
			sender, _ := newTransferTestSender(t, srv.URL, 0, tc.caps)

			issue := service.TransferIssueRow{SourceID: "11111111-1111-1111-1111-111111111111", Number: 1, Title: strings.Repeat("compressible ", 200)}
			chunk := transferIssuesChunk{Issues: []service.TransferIssueRow{issue}}.wire(service.TransferRefs{}, false, false)
			if _, err := sender.post(context.Background(), "/api/workspaces/ws/transfer/issues", chunk); err != nil {
				t.Fatalf("post: %v", err)
			}

			arrivals := recorder.all()
			if len(arrivals) != 1 {
				t.Fatalf("want 1 request, got %d", len(arrivals))
			}
			got := arrivals[0]
			if tc.wantGzip && got.encoding != "gzip" {
				t.Fatalf("want Content-Encoding gzip, got %q", got.encoding)
			}
			if !tc.wantGzip && got.encoding != "" {
				t.Fatalf("want an uncompressed body, got Content-Encoding %q", got.encoding)
			}
			if len(got.transferIssues.Issues) != 1 || got.transferIssues.Issues[0].Title != issue.Title {
				t.Fatalf("target decoded %d issue(s): %+v", len(got.transferIssues.Issues), got.transferIssues.Issues)
			}
			if tc.wantGzip && got.encodedBytes >= got.decodedBytes {
				t.Fatalf("gzip did not shrink the body: encoded=%d decoded=%d", got.encodedBytes, got.decodedBytes)
			}
		})
	}
}

// The target's advertised ceiling caps the budget even when the operator asked
// for something larger, and the operator's number wins when it is smaller.
func TestTransferWire_BudgetIsTheSmallestOfOverrideAndTargetCeiling(t *testing.T) {
	caps := &service.TransferCapabilities{MaxSchemaVersion: 2, AcceptsGzip: true, MaxRequestBytes: 4 << 20}
	for _, tc := range []struct {
		name       string
		configured int
		want       int
	}{
		{"default", 0, service.TransferWireDefaultMaxRequestBytes},
		{"operator below the target", 512 << 10, 512 << 10},
		{"operator above the target", 8 << 20, 4 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := service.TransferWireLimit(tc.configured, caps, 0); got != tc.want {
				t.Fatalf("TransferWireLimit(%d, target 4MB, unmeasured) = %d, want %d", tc.configured, got, tc.want)
			}
		})
	}
	// A measured link shrinks the budget below both, and never below the floor.
	if got := service.TransferWireLimit(0, caps, 56*1024); got != int(56*1024*service.TransferWireUploadBudget.Seconds()) {
		t.Fatalf("a 56 KB/s link should size the budget from the measured rate, got %d", got)
	}
	if got := service.TransferWireLimit(0, caps, 1); got != service.TransferWireMinRequestBytes {
		t.Fatalf("a dead-slow link should be floored at %d, got %d", service.TransferWireMinRequestBytes, got)
	}
}

// ---------------------------------------------------------------------------
// Chunking is by wire bytes, not by row count
// ---------------------------------------------------------------------------

// The whole point of the budget: every request that leaves the client has to
// fit, whatever the bundle's rows look like. Compressible rows pack many more
// per request than incompressible ones — which is exactly what a row-count
// rule cannot express.
func TestTransferWire_ChunksByCompressedBytesNotRows(t *testing.T) {
	const limit = 32 << 10
	refs := service.TransferRefs{}

	compressible := make([]service.TransferIssueRow, 0, 200)
	random := make([]service.TransferIssueRow, 0, 200)
	for i := 0; i < 200; i++ {
		compressible = append(compressible, service.TransferIssueRow{
			SourceID: fmt.Sprintf("11111111-1111-1111-1111-%012d", i),
			Number:   int32(i + 1),
			Title:    strings.Repeat("the quick brown fox jumps over the lazy dog ", 20),
		})
		random = append(random, service.TransferIssueRow{
			SourceID: fmt.Sprintf("22222222-2222-2222-2222-%012d", i),
			Number:   int32(i + 1),
			Title:    pseudoRandomString(i, 1200),
		})
	}

	check := func(t *testing.T, rows []service.TransferIssueRow) []transferIssuesChunk {
		t.Helper()
		planner := newTransferIssuesPlanner(limit, refs, true, false, rows, nil, nil)
		chunks := planner.plan()
		if len(chunks) == 0 {
			t.Fatal("planner produced no chunks")
		}
		total := 0
		for i, chunk := range chunks {
			wire := chunk.wire(refs, true, false)
			if got := len(gzipBytes(wire.Body)); got > limit {
				t.Fatalf("chunk %d is %d compressed bytes, over the %d budget", i, got, limit)
			}
			total += len(chunk.Issues)
		}
		if total != len(rows) {
			t.Fatalf("chunks carry %d rows, want %d", total, len(rows))
		}
		return chunks
	}

	compressibleChunks := check(t, compressible)
	randomChunks := check(t, random)
	if len(compressibleChunks) >= len(randomChunks) {
		t.Fatalf("compressible rows should pack into fewer requests than incompressible ones: %d vs %d",
			len(compressibleChunks), len(randomChunks))
	}

	// And the split has to be a partition: no row may appear in two requests,
	// because a duplicated task is a duplicated write on a re-run.
	seen := map[string]int{}
	for _, chunk := range randomChunks {
		for _, row := range chunk.Issues {
			seen[row.SourceID]++
		}
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("row %s appears in %d chunks", id, n)
		}
	}
}

// A single task whose comments are larger than the whole budget still has to
// get there: comments are split, and the task row rides along in each piece.
func TestTransferWire_SplitsOneOversizedTaskByItsComments(t *testing.T) {
	const limit = 8 << 10
	issue := service.TransferIssueRow{SourceID: "33333333-3333-3333-3333-333333333333", Number: 1, Title: "carrier"}
	var comments []service.TransferCommentRow
	for i := 0; i < 120; i++ {
		comments = append(comments, service.TransferCommentRow{
			SourceID: fmt.Sprintf("44444444-4444-4444-4444-%012d", i),
			IssueID:  issue.SourceID,
			Content:  pseudoRandomString(i, 600),
		})
	}
	planner := newTransferIssuesPlanner(limit, service.TransferRefs{}, true, false, []service.TransferIssueRow{issue}, comments, nil)
	chunks := planner.plan()
	if len(chunks) < 2 {
		t.Fatalf("want the oversized task split across requests, got %d chunk(s)", len(chunks))
	}
	seenComment := map[string]int{}
	for i, chunk := range chunks {
		wire := chunk.wire(service.TransferRefs{}, true, false)
		if got := len(gzipBytes(wire.Body)); got > limit {
			t.Fatalf("chunk %d is %d compressed bytes, over the %d budget", i, got, limit)
		}
		if len(chunk.Issues) != 1 || chunk.Issues[0].SourceID != issue.SourceID {
			t.Fatalf("chunk %d does not carry the task row: %+v", i, chunk.Issues)
		}
		for _, c := range chunk.Comments {
			seenComment[c.SourceID]++
		}
	}
	if len(seenComment) != len(comments) {
		t.Fatalf("chunks carry %d of %d comments", len(seenComment), len(comments))
	}
	for id, n := range seenComment {
		if n != 1 {
			t.Fatalf("comment %s appears in %d chunks", id, n)
		}
	}
}

// ---------------------------------------------------------------------------
// A dropped request is halved and retried
// ---------------------------------------------------------------------------

// The edge answers 524 for anything larger than a threshold, which is what the
// real one does on a slow uplink. The import has to keep going by cutting the
// request down, and every row must still arrive exactly once.
func TestTransferWire_HalvesAndRetriesWhenTheEdgeTimesOut(t *testing.T) {
	const edgeLimit = 6 << 10
	var mu sync.Mutex
	var accepted []service.TransferIssueRow
	recorder := &transferWireRecorder{}
	srv := recorder.server(t, map[string]any{
		"max_schema_version": 2,
		"groups":             []string{"issues"},
		"accepts_gzip":       true,
	}, func(_ int, arrival transferWireArrival) int {
		if arrival.encodedBytes > edgeLimit {
			return transferStatusEdgeTimeout
		}
		mu.Lock()
		accepted = append(accepted, arrival.transferIssues.Issues...)
		mu.Unlock()
		return http.StatusOK
	})

	caps := map[string]any{"max_schema_version": 2, "groups": []string{"issues"}, "accepts_gzip": true}
	sender, _ := newTransferTestSender(t, srv.URL, 64<<10, caps)

	var rows []service.TransferIssueRow
	for i := 0; i < 60; i++ {
		rows = append(rows, service.TransferIssueRow{
			SourceID: fmt.Sprintf("55555555-5555-5555-5555-%012d", i),
			Number:   int32(i + 1),
			Title:    pseudoRandomString(i, 400),
		})
	}
	chunks := newTransferIssuesPlanner(sender.chunkLimit(), service.TransferRefs{}, false, false, rows, nil, nil).plan()
	if len(chunks) != 1 {
		t.Fatalf("fixture should plan one oversized request, planned %d", len(chunks))
	}
	sender.beginStage("issues", len(chunks))
	responses, err := sender.post(context.Background(), "/api/workspaces/ws/transfer/issues", chunks[0].wire(service.TransferRefs{}, false, false))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if len(responses) < 2 {
		t.Fatalf("a halved request should answer more than once, got %d response(s)", len(responses))
	}
	if recorder.count() < 2 {
		t.Fatalf("want at least one rejected request plus the retries, got %d", recorder.count())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(accepted) != len(rows) {
		t.Fatalf("accepted %d row(s), want %d", len(accepted), len(rows))
	}
	seen := map[string]int{}
	for _, row := range accepted {
		seen[row.SourceID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("row %s was accepted %d times", id, n)
		}
	}
}

// ---------------------------------------------------------------------------
// An unretryable request says what actually happened
// ---------------------------------------------------------------------------

func TestTransferWire_EdgeFailureNamesBytesRateAndSplits(t *testing.T) {
	recorder := &transferWireRecorder{}
	srv := recorder.server(t, map[string]any{
		"max_schema_version": 2,
		"groups":             []string{"issues"},
		"accepts_gzip":       true,
	}, func(int, transferWireArrival) int { return transferStatusEdgeTimeout })

	caps := map[string]any{"max_schema_version": 2, "groups": []string{"issues"}, "accepts_gzip": true}
	sender, _ := newTransferTestSender(t, srv.URL, 0, caps)
	rows := []service.TransferIssueRow{{SourceID: "66666666-6666-6666-6666-666666666666", Number: 1, Title: "never lands"}}
	chunk := transferIssuesChunk{Issues: rows}.wire(service.TransferRefs{}, false, false)

	_, err := sender.post(context.Background(), "/api/workspaces/ws/transfer/issues", chunk)
	if err == nil {
		t.Fatal("want an error when every attempt is cut short")
	}
	msg := cli.FormatError(err, false)
	for _, want := range []string{"edge_timeout", "carried", "so far", "What to do next", "--max-request-bytes", "re-run the same command"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message is missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "temporarily unavailable") {
		t.Fatalf("message still points at the server:\n%s", msg)
	}
	if !strings.Contains(msg, humanBytes(int64(len(gzipBytes(chunk.Body))))) {
		t.Fatalf("message should name the request size (%s):\n%s", humanBytes(int64(len(gzipBytes(chunk.Body)))), msg)
	}
	if !strings.Contains(msg, "/s") {
		t.Fatalf("message should name the measured rate:\n%s", msg)
	}
}

// A refusal that is about the content is not retried: halving it would send the
// same refusal twice.
func TestTransferWire_ContentRefusalIsNotRetried(t *testing.T) {
	recorder := &transferWireRecorder{}
	srv := recorder.server(t, map[string]any{
		"max_schema_version": 2,
		"groups":             []string{"issues"},
		"accepts_gzip":       true,
	}, func(int, transferWireArrival) int { return http.StatusConflict })

	caps := map[string]any{"max_schema_version": 2, "groups": []string{"issues"}, "accepts_gzip": true}
	sender, _ := newTransferTestSender(t, srv.URL, 0, caps)
	chunk := transferIssuesChunk{Issues: []service.TransferIssueRow{
		{SourceID: "77777777-7777-7777-7777-777777777777", Number: 1},
		{SourceID: "88888888-8888-8888-8888-888888888888", Number: 2},
	}}.wire(service.TransferRefs{}, false, false)

	_, err := sender.post(context.Background(), "/api/workspaces/ws/transfer/issues", chunk)
	if err == nil {
		t.Fatal("want the 409 to surface")
	}
	if recorder.count() != 1 {
		t.Fatalf("a 409 should not be retried, got %d request(s)", recorder.count())
	}
	if msg := cli.FormatError(err, false); strings.Contains(msg, "edge_timeout") {
		t.Fatalf("a content refusal must not be reported as an edge timeout:\n%s", msg)
	}
}

// ---------------------------------------------------------------------------
// Progress
// ---------------------------------------------------------------------------

func TestTransferWire_ReportsRequestSizeAndRate(t *testing.T) {
	recorder := &transferWireRecorder{}
	srv := recorder.server(t, map[string]any{
		"max_schema_version": 2,
		"groups":             []string{"issues"},
		"accepts_gzip":       true,
	}, nil)
	caps := map[string]any{"max_schema_version": 2, "groups": []string{"issues"}, "accepts_gzip": true}

	var buf strings.Builder
	client := &cli.APIClient{BaseURL: srv.URL, HTTPClient: &http.Client{Timeout: 30 * time.Second}}
	sender := newTransferWireSender(client, probeTestTarget(caps), 0, newTransferProgressReporter(&buf))
	sender.beginStage("issues", 2)
	chunk := transferIssuesChunk{Issues: []service.TransferIssueRow{{SourceID: "99999999-9999-9999-9999-999999999999"}}}.wire(service.TransferRefs{}, false, false)
	for i := 0; i < 2; i++ {
		if _, err := sender.post(context.Background(), "/api/workspaces/ws/transfer/issues", chunk); err != nil {
			t.Fatalf("post %d: %v", i, err)
		}
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want one progress line per request, got %d:\n%s", len(lines), buf.String())
	}
	var ev transferProgressEvent
	if err := json.Unmarshal([]byte(lines[1]), &ev); err != nil {
		t.Fatalf("progress line is not JSON: %v", err)
	}
	if ev.Stage != "issues" || ev.RequestIndex != 2 || ev.RequestsTotal != 2 {
		t.Fatalf("progress line reports %+v", ev)
	}
	if ev.RequestBytes == 0 {
		t.Fatal("progress line carries no request size")
	}
	if ev.UploadBytesPerSecond <= 0 {
		t.Fatal("progress line carries no measured rate")
	}
}

func TestParseTransferByteSize(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    int
		wantErr bool
	}{
		{in: "", want: 0},
		{in: "512KB", want: 512 * 1000},
		{in: "512KiB", want: 512 << 10},
		{in: "2MB", want: 2 * 1000 * 1000},
		{in: "2mb", want: 2 * 1000 * 1000},
		{in: "2097152", want: 2097152},
		{in: " 1GB ", want: 1000 * 1000 * 1000},
		{in: "nonsense", wantErr: true},
		{in: "0", wantErr: true},
		{in: "-1KB", wantErr: true},
	} {
		got, err := parseTransferByteSize(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("parseTransferByteSize(%q) = %d, want an error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parseTransferByteSize(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("parseTransferByteSize(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// pseudoRandomString builds text that gzip cannot shrink: a deterministic
// sequence of printable-but-arbitrary bytes stands in for the base64 and blob
// content a real bundle carries.
//
// `variant` matters more than it looks. An identical string repeated on every
// row is one long repeat, which gzip collapses to nothing — the fixture would
// then prove the opposite of what it is here to prove. Each row gets its own
// stream.
func pseudoRandomString(variant, n int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var b strings.Builder
	seed := uint64(0x9e3779b97f4a7c15) ^ (uint64(variant+1) * 0x2545f4914f6cdd1d)
	for i := 0; i < n; i++ {
		seed ^= seed << 13
		seed ^= seed >> 7
		seed ^= seed << 17
		b.WriteByte(alphabet[seed%uint64(len(alphabet))])
	}
	return b.String()
}
