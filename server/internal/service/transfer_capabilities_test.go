package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// Negotiation is the whole pre-flight decision, so its matrix lives here: the
// CLI only renders the outcome.
func TestNegotiateTransferInclude(t *testing.T) {
	v1Groups := []string{TransferIncludeConfig, TransferIncludeConversations, TransferIncludeAttachments}
	v2Groups := append(append([]string(nil), v1Groups...), TransferIncludeIssues)

	cases := []struct {
		name      string
		requested []string
		caps      TransferCapabilities
		kept      []string
		dropped   []string
	}{
		{
			name:      "capable target keeps every requested group",
			requested: []string{"config", "issues"},
			caps:      TransferCapabilities{MaxSchemaVersion: 2, Groups: v2Groups},
			kept:      []string{"config", "issues"},
		},
		{
			name:      "v1 target loses the issues group",
			requested: []string{"config", "issues"},
			caps:      TransferCapabilities{MaxSchemaVersion: 1, Groups: v1Groups},
			kept:      []string{"config"},
			dropped:   []string{"issues"},
		},
		{
			name:      "v1 target keeps a bundle that never carried issues",
			requested: []string{"config", "conversations", "attachments"},
			caps:      TransferCapabilities{MaxSchemaVersion: 1, Groups: v1Groups},
			kept:      []string{"config", "conversations", "attachments"},
		},
		{
			name:      "an empty group list is version-gated only",
			requested: []string{"config", "conversations", "attachments"},
			caps:      TransferCapabilities{MaxSchemaVersion: 1},
			kept:      []string{"config", "conversations", "attachments"},
		},
		{
			name:      "an empty group list still drops issues below v2",
			requested: []string{"config", "issues"},
			caps:      TransferCapabilities{MaxSchemaVersion: 1},
			kept:      []string{"config"},
			dropped:   []string{"issues"},
		},
		{
			name:      "a named group the target omits is dropped even at a high version",
			requested: []string{"config", "conversations"},
			caps:      TransferCapabilities{MaxSchemaVersion: 2, Groups: []string{"config"}},
			kept:      []string{"config"},
			dropped:   []string{"conversations"},
		},
		{
			name:      "the implicit default is config conversations attachments",
			requested: nil,
			caps:      TransferCapabilities{MaxSchemaVersion: 1, Groups: v1Groups},
			kept:      []string{"config", "conversations", "attachments"},
		},
		{
			name:      "an inconsistent v1 target that lists issues still loses them",
			requested: []string{"config", "issues"},
			caps:      TransferCapabilities{MaxSchemaVersion: 1, Groups: v2Groups},
			kept:      []string{"config"},
			dropped:   []string{"issues"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NegotiateTransferInclude(tc.requested, tc.caps)
			wantKept := tc.kept
			if !reflect.DeepEqual(nilIfEmpty(got.Kept), nilIfEmpty(wantKept)) {
				t.Errorf("Kept = %v, want %v", got.Kept, wantKept)
			}
			if !reflect.DeepEqual(nilIfEmpty(got.Dropped), nilIfEmpty(tc.dropped)) {
				t.Errorf("Dropped = %v, want %v", got.Dropped, tc.dropped)
			}
			if got.Degraded() != (len(tc.dropped) > 0) {
				t.Errorf("Degraded = %v with dropped=%v", got.Degraded(), got.Dropped)
			}
			// Kept plus Dropped is always Requested, so a caller can render
			// the full picture without re-reading the flag.
			if len(got.Kept)+len(got.Dropped) != len(got.Requested) {
				t.Errorf("Kept(%v) + Dropped(%v) != Requested(%v)", got.Kept, got.Dropped, got.Requested)
			}
			// Whatever survives must be readable by the target: if it still
			// needs v2 the downgrade did not actually rescue the export.
			if got.RequestedVersion > got.TargetVersion && len(got.Kept) > 0 {
				if v := TransferBundleSchemaVersionForContent(got.Kept); v > got.TargetVersion {
					t.Errorf("kept groups %v still produce schema_version %d > target %d",
						got.Kept, v, got.TargetVersion)
				}
			}
		})
	}
}

func nilIfEmpty(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return in
}

func TestTransferCapabilitiesForCurrentBuildMatchesTheBundleReader(t *testing.T) {
	caps := TransferCapabilitiesForCurrentBuild()
	if caps.MaxSchemaVersion != TransferBundleSchemaVersion {
		t.Errorf("max_schema_version = %d, want %d", caps.MaxSchemaVersion, TransferBundleSchemaVersion)
	}
	for _, group := range TransferIncludeGroups {
		if !containsString(caps.Groups, group) {
			t.Errorf("capabilities omit the %q group the exporter can write", group)
		}
	}
	// The advertised list must produce the newest version the reader accepts,
	// or a target would promise less than it can do.
	if v := TransferBundleSchemaVersionForContent(caps.Groups); v != caps.MaxSchemaVersion {
		t.Errorf("advertised groups produce schema_version %d, but max_schema_version is %d", v, caps.MaxSchemaVersion)
	}
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func TestProbeTransferCapabilities(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    TransferCapabilities
		wantErr bool
	}{
		{
			name: "reads the capabilities off /health",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/health" {
					t.Errorf("probe hit %s, want /health", r.URL.Path)
				}
				w.Write([]byte(`{"status":"ok","pid":1,"transfer":{"max_schema_version":2,"groups":["config","issues"]}}`))
			},
			want: TransferCapabilities{MaxSchemaVersion: 2, Groups: []string{"config", "issues"}},
		},
		{
			name: "an old instance without the field is an unknown, not a zero version",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"status":"ok","pid":1,"commit":"abc"}`))
			},
			wantErr: true,
		},
		{
			name: "a non-JSON body is an unknown",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`<html>proxy</html>`))
			},
			wantErr: true,
		},
		{
			name: "a 404 is an unknown",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.NotFound(w, r)
			},
			wantErr: true,
		},
		{
			name: "max_schema_version 0 is an unknown",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"transfer":{"max_schema_version":0}}`))
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			got, err := ProbeTransferCapabilities(context.Background(), srv.Client(), srv.URL+"/")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("probe returned %+v, want an error the caller reports as unknown", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("probe: %v", err)
			}
			if got.MaxSchemaVersion != tc.want.MaxSchemaVersion || !reflect.DeepEqual(got.Groups, tc.want.Groups) {
				t.Errorf("probe = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestProbeTransferCapabilitiesUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	if _, err := ProbeTransferCapabilities(context.Background(), srv.Client(), url); err == nil {
		t.Fatal("probe against a closed listener returned capabilities, want an error")
	}
}
