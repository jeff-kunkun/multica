package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// The `--include` groups a bundle can carry. The export only knows these four:
// every other read hangs off one of them.
const (
	TransferIncludeConfig        = "config"
	TransferIncludeConversations = "conversations"
	TransferIncludeAttachments   = "attachments"
)

// TransferIncludeGroups is every group this build can export. It includes
// TransferIncludeIssues (declared with the bundle versions in
// transfer_bundle.go), the one group that raises the outer schema_version.
var TransferIncludeGroups = []string{
	TransferIncludeConfig,
	TransferIncludeConversations,
	TransferIncludeAttachments,
	TransferIncludeIssues,
}

// TransferCapabilities is what an instance answers about the transfer bundles
// it can read. It travels on the unauthenticated `/health` payload under
// TransferHealthField, so an exporter can ask a target it has no credentials
// for — which is the whole migration case: the CLI is logged in to the source,
// not to the target it is about to import into.
type TransferCapabilities struct {
	// MaxSchemaVersion is the newest outer `schema_version` this build reads.
	MaxSchemaVersion int `json:"max_schema_version"`
	// Groups is every `--include` group this build understands. An empty list
	// means "unknown": an old instance that answers the field without a group
	// list is still version-gated, so the version is the only signal there.
	Groups []string `json:"groups"`
}

// TransferCapabilitiesForCurrentBuild is what this build advertises. It is
// derived from the bundle reader rather than written twice, so a target can
// never claim a version the import kernel would refuse.
func TransferCapabilitiesForCurrentBuild() TransferCapabilities {
	return TransferCapabilities{
		MaxSchemaVersion: TransferBundleSchemaVersion,
		Groups:           append([]string(nil), TransferIncludeGroups...),
	}
}

// TransferHealthField is the `/health` key the capabilities travel under. An
// instance that predates this change answers without it, which the probe
// reports as "unknown" instead of as a version.
const TransferHealthField = "transfer"

// transferCapabilityProbeBodyLimit bounds the probe read. `/health` is a few
// hundred bytes; the cap only keeps a wrong URL from streaming a body at us.
const transferCapabilityProbeBodyLimit = 64 << 10

// ProbeTransferCapabilities reads a target's transfer capabilities off its
// /health endpoint.
//
// Every failure — unreachable host, 404 from an instance without the route, a
// body that is not JSON, a payload without the field — is returned as an error
// rather than as a zero TransferCapabilities, because "the target said
// nothing" and "the target said version 0" must not be conflated by the caller:
// the first is a warning that must not block the export, the second would
// refuse every bundle.
func ProbeTransferCapabilities(ctx context.Context, httpClient *http.Client, baseURL string) (TransferCapabilities, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return TransferCapabilities{}, fmt.Errorf("target URL is empty")
	}
	endpoint := base + "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return TransferCapabilities{}, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return TransferCapabilities{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return TransferCapabilities{}, fmt.Errorf("GET %s returned %d", endpoint, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, transferCapabilityProbeBodyLimit))
	if err != nil {
		return TransferCapabilities{}, err
	}
	var payload struct {
		Transfer *TransferCapabilities `json:"transfer"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return TransferCapabilities{}, fmt.Errorf("GET %s did not answer JSON: %w", endpoint, err)
	}
	if payload.Transfer == nil {
		return TransferCapabilities{}, fmt.Errorf("GET %s carries no transfer capabilities", endpoint)
	}
	if payload.Transfer.MaxSchemaVersion <= 0 {
		return TransferCapabilities{}, fmt.Errorf("GET %s reports transfer max_schema_version %d",
			endpoint, payload.Transfer.MaxSchemaVersion)
	}
	return *payload.Transfer, nil
}

// TransferCapabilityNegotiation is the outcome of comparing a requested
// `--include` list against a target's advertised capabilities.
type TransferCapabilityNegotiation struct {
	// Requested / Kept / Dropped are group name lists. Kept plus Dropped is
	// always Requested, so a caller can report exactly what the target forced
	// out of the bundle.
	Requested []string
	Kept      []string
	Dropped   []string
	// RequestedVersion is the schema_version the requested groups produce;
	// TargetVersion is what the target advertises.
	RequestedVersion int
	TargetVersion    int
}

// Degraded reports whether the target cannot read the requested bundle as-is.
func (n TransferCapabilityNegotiation) Degraded() bool { return len(n.Dropped) > 0 }

// NegotiateTransferInclude picks the exportable subset of `requested` for a
// target that advertises `caps`.
//
// The rule is the target's list when it has one — a group the target does not
// name is a group it cannot read, even if its version would allow it — plus the
// version ceiling, which is what keeps the `issues` group (the only group that
// raises the outer schema_version) out of a V2-only target. A target that
// answers with an empty group list is version-gated only: inventing a group
// list for it would drop groups it may well accept.
//
// An empty `requested` is the exporter's implicit default (config +
// conversations + attachments), which is also what an empty list means to the
// export kernel itself.
func NegotiateTransferInclude(requested []string, caps TransferCapabilities) TransferCapabilityNegotiation {
	groups := requested
	if len(groups) == 0 {
		groups = []string{TransferIncludeConfig, TransferIncludeConversations, TransferIncludeAttachments}
	}
	n := TransferCapabilityNegotiation{
		Requested:        append([]string(nil), groups...),
		RequestedVersion: TransferBundleSchemaVersionForContent(groups),
		TargetVersion:    caps.MaxSchemaVersion,
	}
	supported := map[string]bool{}
	for _, g := range caps.Groups {
		if g = strings.TrimSpace(g); g != "" {
			supported[g] = true
		}
	}
	for _, g := range groups {
		if len(supported) > 0 && !supported[g] {
			n.Dropped = append(n.Dropped, g)
			continue
		}
		if g == TransferIncludeIssues && caps.MaxSchemaVersion < TransferBundleSchemaVersionV2 {
			n.Dropped = append(n.Dropped, g)
			continue
		}
		n.Kept = append(n.Kept, g)
	}
	return n
}
