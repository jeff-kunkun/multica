package main

import (
	"bytes"
	"encoding/json"
	"github.com/spf13/cobra"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBotWakeCLIFileAndStdin(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !strings.HasSuffix(r.URL.Path, "/bots/"+id+"/wake") {
			t.Error("wrong endpoint")
		}
		if r.Method == "PUT" {
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["webhook_key"] != "Bearer fixture-secret" || body["inherit_owner_follows"] != true {
				t.Error("wrong body")
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"webhook_configured": true, "hourly_limit": 6, "events": []string{"mention"}, "quiet_hours": map[string]any{"enabled": false, "start": "23:00", "end": "08:00", "timezone": "Asia/Shanghai"}})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	payload := `{"webhook_url":"https://receiver.example/hook","webhook_key":"Bearer fixture-secret","inherit_owner_follows":true}`
	for _, source := range []string{"file", "stdin"} {
		cmd := &cobra.Command{}
		cmd.Flags().String("output", "json", "")
		cmd.Flags().String("config-file", "", "")
		cmd.Flags().Bool("config-stdin", false, "")
		var out bytes.Buffer
		cmd.SetOut(&out)
		if source == "file" {
			p := filepath.Join(t.TempDir(), "wake.json")
			os.WriteFile(p, []byte(payload), 0600)
			cmd.Flags().Set("config-file", p)
		} else {
			cmd.Flags().Set("config-stdin", "true")
			cmd.SetIn(strings.NewReader(payload))
		}
		if err := runBotWake(cmd, []string{id}, "set"); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "fixture-secret") || strings.Contains(out.String(), "receiver.example") {
			t.Fatal("secret echoed")
		}
		t.Logf("set via %s: response redacted", source)
	}
	if calls != 2 {
		t.Fatal("missing requests")
	}
}

func TestBotWakeAckUsesWebhookEndpoint(t *testing.T) {
	const botID = "11111111-1111-4111-8111-111111111111"
	const eventID = "22222222-2222-4222-8222-222222222222"
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/bots/"+botID+"/wake/pending/ack") {
			t.Errorf("wrong ack route: %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			EventIDs []string `json:"event_ids"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.EventIDs) != 1 || body.EventIDs[0] != eventID {
			t.Errorf("wrong ack body: %+v", body)
		}
		json.NewEncoder(w).Encode(map[string]any{"acknowledged": 1})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	wake := &cobra.Command{Use: "wake"}
	addBotWakeReceipts(wake)
	var out bytes.Buffer
	wake.SetOut(&out)
	wake.SetArgs([]string{"ack", botID, eventID, "--output", "json"})
	if err := wake.Execute(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(out.String(), `"acknowledged": 1`) {
		t.Fatalf("ack result: calls=%d %s", calls, out.String())
	}
}
