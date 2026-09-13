package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestParseDeepSeekBalanceJSONMapsCNYAndUSD(t *testing.T) {
	t.Parallel()

	got, err := ParseDeepSeekBalanceJSON([]byte(`{
		"is_available": true,
		"balance_infos": [
			{"currency": "CNY", "total_balance": "110.00", "granted_balance": "10.00", "topped_up_balance": "100.00"},
			{"currency": "USD", "total_balance": 12.5}
		]
	}`), time.Unix(80, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Provider != "dsh" || got.Status != protocol.PlanLimitsStatusAvailable {
		t.Fatalf("snapshot = %+v", got)
	}
	if len(got.Windows) != 2 {
		t.Fatalf("windows = %+v", got.Windows)
	}
	if got.Windows[0].Name != windowBalanceCNY || got.Windows[0].Remaining == nil || *got.Windows[0].Remaining != 110 {
		t.Fatalf("cny = %+v", got.Windows[0])
	}
	if got.Windows[1].Name != windowBalanceUSD || got.Windows[1].Remaining == nil || *got.Windows[1].Remaining != 12.5 {
		t.Fatalf("usd = %+v", got.Windows[1])
	}
}

func TestParseDeepSeekBalanceJSONExhaustedWhenUnavailable(t *testing.T) {
	t.Parallel()

	got, err := ParseDeepSeekBalanceJSON([]byte(`{
		"is_available": false,
		"balance_infos": [{"currency": "CNY", "total_balance": "0.00"}]
	}`), time.Unix(81, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Status != protocol.PlanLimitsStatusExhausted {
		t.Fatalf("status = %+v", got)
	}
	if got.Windows[0].Remaining == nil || *got.Windows[0].Remaining != 0 {
		t.Fatalf("remaining = %+v", got.Windows[0])
	}
}

func TestProbeDeepSeekSendsBearerAndOmitsSecretFromSnapshot(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"is_available":  true,
			"balance_infos": []map[string]any{{"currency": "CNY", "total_balance": "9.99"}},
		})
	}))
	t.Cleanup(server.Close)

	const secret = "sk-deepseek-secret"
	probe := PlanQuotaProbe{
		Client:             server.Client(),
		DeepSeekBalanceURL: server.URL,
		LookupAPIKey: func(provider string) (string, bool) {
			if provider == "deepseek" {
				return secret, true
			}
			return "", false
		},
		Now: func() time.Time { return time.Unix(90, 0) },
	}
	got, err := probe.ProbeDeepSeek(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer "+secret {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if got == nil || got.Provider != "dsh" || got.Windows[0].Remaining == nil || *got.Windows[0].Remaining != 9.99 {
		t.Fatalf("snapshot = %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(secret)) {
		t.Fatalf("snapshot leaked api key: %s", encoded)
	}
}

func TestProbeDeepSeekSkipsMissingCredentials(t *testing.T) {
	t.Parallel()

	probe := PlanQuotaProbe{
		LookupAPIKey: func(string) (string, bool) { return "", false },
	}
	got, err := probe.ProbeDeepSeek(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil snapshot, got %+v", got)
	}
}
