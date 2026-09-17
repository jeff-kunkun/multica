package service

import "time"

// Wire-budget constants for the transfer import path.
//
// A transfer request does not travel to the origin directly: it crosses a CDN
// edge, and the edge cuts a request that has not finished in 100 seconds
// (Cloudflare answers 524). At the ~0.45 Mbps uplink a self-hosted instance was
// migrated over, a 10 MB JSON body needs about three minutes, so the request
// never lands and the user sees an edge timeout instead of an import. The
// budget below is the whole answer to that: keep every single request small
// enough to finish in a fraction of the edge's window, which matters far more
// than how many requests the bundle costs.
const (
	// TransferWireDefaultMaxRequestBytes is the default ceiling for one
	// transfer request body on the wire — compressed, when the target takes
	// gzip. It is deliberately far below the server's own 20 MiB parse cap:
	// the parse cap protects the process, this one protects the transfer.
	// 2 MiB at 0.45 Mbps is about 35 seconds, which leaves the edge's 100
	// second window most of its margin for a link slower than expected.
	TransferWireDefaultMaxRequestBytes = 2 << 20
	// TransferWireMinRequestBytes floors the adaptive budget. A measured rate
	// of zero (or a link so slow that the rate times the budget is tiny) must
	// not turn the import into thousands of one-row requests, so the budget
	// never drops below this and the halving retry covers what is left.
	TransferWireMinRequestBytes = 256 << 10
	// TransferWireUploadBudget is how long one request is allowed to spend in
	// flight. It sizes the adaptive budget: bytes = measured rate × this.
	TransferWireUploadBudget = 35 * time.Second
	// TransferWireSplitLimit is how many times one request may be halved after
	// the edge drops it. Three halvings turn one 2 MiB body into eight 256 KiB
	// bodies, which covers an uplink ~8× slower than the one estimated.
	TransferWireSplitLimit = 3
)

// TransferWireLimit picks how many wire bytes one request body may carry.
//
// Three inputs, and the answer is always the smallest of them:
//
//   - configured: the caller's `--max-request-bytes` (0 = the default);
//   - caps: what the target advertised as its own ceiling (0 = it did not
//     say, which is not a budget of zero);
//   - rate: the measured uplink in bytes per second (0 = not measured yet, so
//     the default stands for the first request and the measurement starts
//     influencing the ones after it).
//
// Only the measurement adjusts the budget downward: a fast link must not
// produce bodies larger than the operator allowed, so this never returns more
// than min(configured, target ceiling).
func TransferWireLimit(configured int, caps *TransferCapabilities, rate float64) int {
	limit := configured
	if limit <= 0 {
		limit = TransferWireDefaultMaxRequestBytes
	}
	if caps != nil && caps.MaxRequestBytes > 0 && caps.MaxRequestBytes < limit {
		limit = caps.MaxRequestBytes
	}
	if rate > 0 {
		adaptive := int(rate * TransferWireUploadBudget.Seconds())
		if adaptive < TransferWireMinRequestBytes {
			adaptive = TransferWireMinRequestBytes
		}
		if adaptive < limit {
			limit = adaptive
		}
	}
	return limit
}
