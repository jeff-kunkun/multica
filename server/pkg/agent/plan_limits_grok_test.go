package agent

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func grokVarint(value uint64) []byte {
	var out []byte
	for {
		b := byte(value & 0x7F)
		value >>= 7
		if value == 0 {
			out = append(out, b)
			return out
		}
		out = append(out, b|0x80)
	}
}

func grokFieldVarint(number, value uint64) []byte {
	out := grokVarint(number << 3)
	return append(out, grokVarint(value)...)
}

func grokFieldFloat(number uint64, value float32) []byte {
	out := grokVarint((number << 3) | 5)
	var bits [4]byte
	binary.LittleEndian.PutUint32(bits[:], math.Float32bits(value))
	return append(out, bits[:]...)
}

func grokFieldMessage(number uint64, payload []byte) []byte {
	out := grokVarint((number << 3) | 2)
	out = append(out, grokVarint(uint64(len(payload)))...)
	return append(out, payload...)
}

func grokGRPCWebFrame(flags byte, payload []byte) []byte {
	out := []byte{flags}
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(payload)))
	out = append(out, length[:]...)
	return append(out, payload...)
}

func TestParseGrokCreditsProtobufFramedPercentAndReset(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_750_000_000, 0)
	resetTS := uint64(now.Unix() + 30*86400)
	inner := append(grokFieldFloat(1, 37.5), grokFieldMessage(5, grokFieldVarint(1, resetTS))...)
	data := grokGRPCWebFrame(0, grokFieldMessage(1, inner))

	got, err := ParseGrokCreditsProtobuf(data, now)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Provider != "grok" || len(got.Windows) != 1 {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.Windows[0].Name != grokWindowCredits || got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 37.5 {
		t.Fatalf("window = %+v", got.Windows[0])
	}
	if got.Windows[0].ResetsAt == nil || *got.Windows[0].ResetsAt != int64(resetTS) {
		t.Fatalf("reset = %+v", got.Windows[0].ResetsAt)
	}
	if got.Windows[0].WindowMinutes == nil || *got.Windows[0].WindowMinutes != 43_200 {
		t.Fatalf("monthly minutes = %+v", got.Windows[0].WindowMinutes)
	}
}

func TestParseGrokCreditsProtobufBarePayload(t *testing.T) {
	t.Parallel()

	payload := grokFieldMessage(1, grokFieldFloat(1, 12))
	got, err := ParseGrokCreditsProtobuf(payload, time.Unix(1_750_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 12 {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestParseGrokCreditsProtobufPrefersShallowestPercent(t *testing.T) {
	t.Parallel()

	inner := append(grokFieldMessage(2, grokFieldFloat(1, 99)), grokFieldFloat(1, 25)...)
	data := grokGRPCWebFrame(0, grokFieldMessage(1, inner))
	got, err := ParseGrokCreditsProtobuf(data, time.Unix(1_750_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 25 {
		t.Fatalf("window = %+v", got.Windows[0])
	}
}

func TestParseGrokCreditsProtobufZeroUsagePeriod(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_750_000_000, 0)
	resetTS := uint64(now.Unix() + 7*86400)
	inner := append(grokFieldMessage(5, grokFieldVarint(1, resetTS)), grokFieldMessage(6, grokFieldVarint(1, 3))...)
	data := grokGRPCWebFrame(0, grokFieldMessage(1, inner))
	got, err := ParseGrokCreditsProtobuf(data, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 0 {
		t.Fatalf("window = %+v", got.Windows[0])
	}
	if got.Windows[0].WindowMinutes == nil || *got.Windows[0].WindowMinutes != sevenDayMinutes {
		t.Fatalf("weekly minutes = %+v", got.Windows[0].WindowMinutes)
	}
}

func TestParseGrokCreditsProtobufMissingPercentIsError(t *testing.T) {
	t.Parallel()

	data := grokGRPCWebFrame(0, grokFieldMessage(1, grokFieldVarint(7, 42)))
	got, err := ParseGrokCreditsProtobuf(data, time.Unix(1_750_000_000, 0))
	if err == nil || got != nil {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestParseGrokAuthJSONPrefersOIDC(t *testing.T) {
	t.Parallel()

	token := parseGrokAuthJSON(`{
		"https://accounts.x.ai/sign-in": {"key": "legacy-token"},
		"https://auth.x.ai::client-id": {"key": "oidc-token"}
	}`)
	if token != "oidc-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestParseGrokAuthJSONEmptyOIDCFallsBackToLegacy(t *testing.T) {
	t.Parallel()

	token := parseGrokAuthJSON(`{
		"https://auth.x.ai::client-id": {"key": ""},
		"https://accounts.x.ai/sign-in": {"key": "legacy-token"}
	}`)
	if token != "legacy-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestParseGrokAuthJSONMissingEntry(t *testing.T) {
	t.Parallel()

	if token := parseGrokAuthJSON(`{"other-scope": {"key": "x"}}`); token != "" {
		t.Fatalf("token = %q", token)
	}
}

func TestProbeGrokReadsAuthJSONAndPostsEmptyFrame(t *testing.T) {
	t.Setenv("GROK_HOME", "")
	home := t.TempDir()
	credDir := filepath.Join(home, ".grok")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "auth.json"), []byte(`{
		"https://auth.x.ai::client": {"key": "grok-token"}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotAuth, gotType string
	var gotBody []byte
	now := time.Unix(1_750_000_000, 0)
	resetTS := uint64(now.Unix() + 86400)
	inner := append(grokFieldFloat(1, 8), grokFieldMessage(5, grokFieldVarint(1, resetTS))...)
	payload := grokGRPCWebFrame(0, grokFieldMessage(1, inner))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/grpc-web+proto")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)

	probe := PlanQuotaProbe{
		Home:           home,
		Client:         server.Client(),
		GrokBillingURL: server.URL,
		LookupKeychain: func(string) (string, bool) { return "", false },
		Now:            func() time.Time { return now },
	}
	got, err := probe.ProbeGrok(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer grok-token" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotType != "application/grpc-web+proto" {
		t.Fatalf("content-type = %q", gotType)
	}
	if string(gotBody) != grokEmptyGRPCWebFrame {
		t.Fatalf("body = %v", gotBody)
	}
	if got == nil || got.Provider != "grok" || got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 8 {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestProbeGrokSkipsMissingCredentials(t *testing.T) {
	t.Setenv("GROK_HOME", "")
	probe := PlanQuotaProbe{
		Home:           t.TempDir(),
		LookupKeychain: func(string) (string, bool) { return "", false },
	}
	got, err := probe.ProbeGrok(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil snapshot, got %+v", got)
	}
}

func TestGrokPercentDecode(t *testing.T) {
	t.Parallel()

	if got := grokPercentDecode("no%20personal%20team"); got != "no personal team" {
		t.Fatalf("got %q", got)
	}
	if got := grokPercentDecode("50%ZZ"); got != "50%ZZ" {
		t.Fatalf("invalid sequence = %q", got)
	}
}
