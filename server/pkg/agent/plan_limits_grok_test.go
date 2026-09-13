package agent

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseGrokBillingJSONCreditsPercent(t *testing.T) {
	t.Parallel()

	got, err := ParseGrokBillingJSON([]byte(`{
		"config": {
			"creditUsagePercent": 15.5,
			"currentPeriod": {"type": "WEEK", "end": "2027-01-20T12:00:00Z"}
		}
	}`), time.Unix(20, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Provider != "grok" || len(got.Windows) != 1 {
		t.Fatalf("snapshot = %+v", got)
	}
	window := got.Windows[0]
	if window.Name != grokWindowCredits || window.UsedPercent == nil || *window.UsedPercent != 15.5 {
		t.Fatalf("window = %+v", window)
	}
	if window.WindowMinutes == nil || *window.WindowMinutes != sevenDayMinutes {
		t.Fatalf("minutes = %+v", window.WindowMinutes)
	}
	if window.ResetsAt == nil {
		t.Fatal("missing reset")
	}
}

func TestParseGrokBillingJSONCentsFallback(t *testing.T) {
	t.Parallel()

	got, err := ParseGrokBillingJSON([]byte(`{
		"used": {"val": 1500},
		"monthlyLimit": {"val": 10000},
		"billingPeriodEnd": "2027-01-20T12:00:00Z"
	}`), time.Unix(20, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 15 {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestParseGrokBillingJSONEmpty(t *testing.T) {
	t.Parallel()

	got, err := ParseGrokBillingJSON([]byte(`{"config":{}}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestParseGrokCreditsProtobufHeuristic(t *testing.T) {
	t.Parallel()

	reset := int64(1_800_000_000)
	inner := appendProtoDouble(nil, 1, 22.5)
	inner = appendProtoVarint(inner, 5, uint64(reset))
	payload := appendProtoBytes(nil, 1, inner)
	frame := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)

	got := ParseGrokCreditsProtobuf(frame, time.Unix(30, 0))
	if got == nil || got.Provider != "grok" || len(got.Windows) != 1 {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 22.5 {
		t.Fatalf("used = %+v", got.Windows[0])
	}
	if got.Windows[0].ResetsAt == nil || *got.Windows[0].ResetsAt != reset {
		t.Fatalf("reset = %+v", got.Windows[0].ResetsAt)
	}
}

func TestParseGrokAuthJSONSkipsExpiredAndAPIKey(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 13, 6, 0, 0, 0, time.UTC)
	token := parseGrokAuthJSON(`{
		"https://auth.x.ai::old": {
			"key": "expired-token",
			"auth_mode": "oidc",
			"expires_at": "2026-09-13T05:00:00Z"
		},
		"https://auth.x.ai::live": {
			"key": "live-token",
			"auth_mode": "oidc",
			"expires_at": "2026-09-13T08:00:00Z"
		},
		"https://auth.x.ai::key": {
			"key": "api-key-mode",
			"auth_mode": "api_key",
			"expires_at": "2026-09-13T09:00:00Z"
		}
	}`, now)
	if token != "live-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestProbeGrokReadsAuthJSONAndQueriesBilling(t *testing.T) {
	t.Setenv("GROK_HOME", "")
	home := t.TempDir()
	credDir := filepath.Join(home, ".grok")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "auth.json"), []byte(`{
		"https://auth.x.ai::cli": {
			"key": "grok-token",
			"auth_mode": "oidc",
			"expires_at": "2027-01-15T04:00:00Z"
		}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotAuth, gotTokenAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTokenAuth = r.Header.Get("x-xai-token-auth")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"config": map[string]any{
				"creditUsagePercent": 8,
				"currentPeriod":      map[string]any{"type": "WEEK", "end": "2027-01-20T12:00:00Z"},
			},
		})
	}))
	t.Cleanup(server.Close)

	probe := PlanQuotaProbe{
		Home:           home,
		Client:         server.Client(),
		GrokBillingURL: server.URL,
		Now:            func() time.Time { return time.Unix(40, 0) },
	}
	got, err := probe.ProbeGrok(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer grok-token" || gotTokenAuth != "xai-grok-cli" {
		t.Fatalf("headers auth=%q token-auth=%q", gotAuth, gotTokenAuth)
	}
	if got == nil || got.Provider != "grok" || got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 8 {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestProbeGrokSkipsMissingCredentials(t *testing.T) {
	t.Parallel()

	probe := PlanQuotaProbe{Home: t.TempDir()}
	got, err := probe.ProbeGrok(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil snapshot, got %+v", got)
	}
}

func TestParseGrokCreditsProtobufMalformed(t *testing.T) {
	t.Parallel()

	if got := ParseGrokCreditsProtobuf([]byte("not-proto"), time.Now()); got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func appendProtoVarint(dst []byte, field int, value uint64) []byte {
	dst = append(dst, byte(field<<3))
	for value >= 0x80 {
		dst = append(dst, byte(value)|0x80)
		value >>= 7
	}
	return append(dst, byte(value))
}

func appendProtoDouble(dst []byte, field int, value float64) []byte {
	dst = append(dst, byte(field<<3)|1)
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], math.Float64bits(value))
	return append(dst, buf[:]...)
}

func appendProtoBytes(dst []byte, field int, value []byte) []byte {
	dst = append(dst, byte(field<<3)|2)
	ln := uint64(len(value))
	for ln >= 0x80 {
		dst = append(dst, byte(ln)|0x80)
		ln >>= 7
	}
	dst = append(dst, byte(ln))
	return append(dst, value...)
}
