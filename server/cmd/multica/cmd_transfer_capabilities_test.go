package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
)

// ---------------------------------------------------------------------------
// A target instance that answers /health
// ---------------------------------------------------------------------------

// transferCapabilityTarget is a target instance reduced to the one thing the
// pre-flight reads: its /health capabilities.
func transferCapabilityTarget(t *testing.T, maxSchemaVersion int, groups []string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		writeJSONTest(w, map[string]any{
			"status": "ok",
			"transfer": map[string]any{
				"max_schema_version": maxSchemaVersion,
				"groups":             groups,
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// transferCapabilitySource is an exportable source: the read API the exporter
// walks, with one task so the issues group has something to carry.
func transferCapabilitySource(t *testing.T) *httptest.Server {
	t.Helper()
	fixture := &transferIssueAPIFixture{
		issuePrefix: "SRC",
		issues:      []map[string]any{transferTestIssue("issue-1", 1, "carried task")},
		comments:    map[string][]map[string]any{},
		labels:      map[string][]map[string]any{},
		reactions:   map[string][]map[string]any{},
		attachments: map[string][]map[string]any{},
		bodies:      map[string][]byte{},
	}
	srv := fixture.server()
	t.Cleanup(srv.Close)
	return srv
}

func outOfTransferExportTest(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "bundle.zip")
}

func transferZipEntryNames(entries map[string][]byte) []string {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	return names
}

func hasTransferZipPrefix(entries map[string][]byte, prefix string) bool {
	for name := range entries {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// A target that cannot read the issues group is asked BEFORE the bundle is
// written, and the default answer is to refuse with the fix in the sentence
// rather than to write a bundle the import will reject (DENE-431).
func TestTransferExport_RefusesOutdatedTargetWithoutDowngrade(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	source := transferCapabilitySource(t)
	target := transferCapabilityTarget(t, 1, []string{"config", "conversations", "attachments"})

	out := outOfTransferExportTest(t)
	result, err := transferTestIssueCmd(t, source, map[string]string{
		"out":     out,
		"include": "config,issues",
		"target":  target.URL,
	})
	if err == nil {
		t.Fatal("export to a v1 target succeeded without --downgrade, want a refusal")
	}
	msg := err.Error()
	for _, want := range []string{"target_outdated", "schema_version 1", "schema_version 2", "issues", "--downgrade", "upgrade the target"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not contain %q — the user cannot act on it", msg, want)
		}
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Error("a refused export still wrote a bundle")
	}
	if strings.Contains(result.Stderr, "Export complete") {
		t.Error("a refused export reported success on stderr")
	}
}

// With --downgrade the same target gets a schema_version 1 bundle with no
// issues group at all — the whole point of the negotiation.
func TestTransferExport_DowngradesToOutdatedTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	source := transferCapabilitySource(t)
	target := transferCapabilityTarget(t, 1, []string{"config", "conversations", "attachments"})

	out := outOfTransferExportTest(t)
	result, err := transferTestIssueCmd(t, source, map[string]string{
		"out":       out,
		"include":   "config,issues",
		"target":    target.URL,
		"downgrade": "true",
	})
	if err != nil {
		t.Fatalf("downgraded export: %v\nstderr:\n%s", err, result.Stderr)
	}
	if !strings.Contains(result.Stderr, "drops the `issues` group") {
		t.Errorf("stderr does not name the dropped group:\n%s", result.Stderr)
	}

	entries := readTransferZipEntries(t, out)
	if hasTransferZipPrefix(entries, "issues/") {
		t.Fatalf("downgraded bundle still carries %v", transferZipEntryNames(entries))
	}
	var manifest transferManifestRefsTest
	decodeZipJSON(t, entries, "manifest.json", &manifest)
	if manifest.SchemaVersion != service.TransferBundleSchemaVersionV1 {
		t.Errorf("schema_version = %d, want %d after the downgrade", manifest.SchemaVersion, service.TransferBundleSchemaVersionV1)
	}
	if got := strings.Join(manifest.Options.Include, ","); got != "config" {
		t.Errorf("manifest options.include = %q, want %q — the bundle must record what it really carries", got, "config")
	}
}

// A target that can read everything changes nothing: no downgrade, no drop.
func TestTransferExport_CapableTargetKeepsRequestedGroups(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	source := transferCapabilitySource(t)
	target := transferCapabilityTarget(t, service.TransferBundleSchemaVersion, service.TransferIncludeGroups)

	out := outOfTransferExportTest(t)
	result, err := transferTestIssueCmd(t, source, map[string]string{
		"out":     out,
		"include": "config,issues",
		"target":  target.URL,
	})
	if err != nil {
		t.Fatalf("export to a capable target: %v\nstderr:\n%s", err, result.Stderr)
	}
	if strings.Contains(result.Stderr, "drops the") {
		t.Errorf("a capable target still dropped a group:\n%s", result.Stderr)
	}
	entries := readTransferZipEntries(t, out)
	if !hasTransferZipPrefix(entries, "issues/") {
		t.Fatalf("capable target did not receive the issues group; entries: %v", transferZipEntryNames(entries))
	}
	var manifest transferManifestRefsTest
	decodeZipJSON(t, entries, "manifest.json", &manifest)
	if manifest.SchemaVersion != service.TransferBundleSchemaVersionV2 {
		t.Errorf("schema_version = %d, want %d", manifest.SchemaVersion, service.TransferBundleSchemaVersionV2)
	}
}

// An unprobeable target must not block the export: an old instance without the
// /health field, and an unreachable host, both fall through to the requested
// bundle with a warning.
func TestTransferExport_UnprobeableTargetDoesNotBlock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	oldInstance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONTest(w, map[string]any{"status": "ok", "pid": 1, "commit": "old"})
	}))
	t.Cleanup(oldInstance.Close)
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()

	cases := []struct {
		name   string
		target string
	}{
		{name: "an old instance without the field", target: oldInstance.URL},
		{name: "an unreachable host", target: closedURL},
		{name: "a URL that is not an instance", target: oldInstance.URL + "/not-an-instance"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := transferCapabilitySource(t)
			out := outOfTransferExportTest(t)
			result, err := transferTestIssueCmd(t, source, map[string]string{
				"out":     out,
				"include": "config,issues",
				"target":  tc.target,
			})
			if err != nil {
				t.Fatalf("export with an unprobeable target: %v\nstderr:\n%s", err, result.Stderr)
			}
			if !strings.Contains(result.Stderr, "Could not confirm") {
				t.Errorf("stderr does not say the target version is unknown:\n%s", result.Stderr)
			}
			entries := readTransferZipEntries(t, out)
			if !hasTransferZipPrefix(entries, "issues/") {
				t.Fatalf("bundle differs from the no-target export; entries: %v", transferZipEntryNames(entries))
			}
			var manifest transferManifestRefsTest
			decodeZipJSON(t, entries, "manifest.json", &manifest)
			if manifest.SchemaVersion != service.TransferBundleSchemaVersionV2 {
				t.Errorf("schema_version = %d, want the requested %d", manifest.SchemaVersion, service.TransferBundleSchemaVersionV2)
			}
		})
	}
}

// --estimate negotiates too: the number a user sees must belong to the bundle
// that would actually be written.
func TestTransferExport_EstimateNegotiatesWithTheTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	source := transferCapabilitySource(t)
	target := transferCapabilityTarget(t, 1, []string{"config", "conversations", "attachments"})

	result, err := transferTestIssueCmd(t, source, map[string]string{
		"estimate":  "true",
		"include":   "config,conversations,attachments,issues",
		"target":    target.URL,
		"downgrade": "true",
	})
	if err != nil {
		t.Fatalf("estimate against a v1 target: %v\nstderr:\n%s", err, result.Stderr)
	}
	if !strings.Contains(result.Stderr, "drops the `issues` group") {
		t.Errorf("estimate did not report the downgrade:\n%s", result.Stderr)
	}
	// The issues group is what the downgrade removed, so the estimate must not
	// count the task it would have carried.
	if !strings.Contains(result.Stdout, `"issues": 0`) {
		t.Errorf("estimate still counts issues for a bundle that drops them: %s", result.Stdout)
	}
}

// A target that cannot be asked must not make an export fail for a bundle that
// needs no negotiation either.
func TestTransferExport_NoTargetFlagKeepsCurrentBehaviour(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	source := transferCapabilitySource(t)
	out := outOfTransferExportTest(t)
	result, err := transferTestIssueCmd(t, source, map[string]string{"out": out, "include": "config,issues"})
	if err != nil {
		t.Fatalf("export without --target: %v\nstderr:\n%s", err, result.Stderr)
	}
	if strings.Contains(result.Stderr, "Could not confirm") || strings.Contains(result.Stderr, "schema_version") {
		t.Errorf("an export without --target probed something:\n%s", result.Stderr)
	}
	if !hasTransferZipPrefix(readTransferZipEntries(t, out), "issues/") {
		t.Error("export without --target lost the issues group")
	}
}
