package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestParseActivityScope(t *testing.T) {
	for in, want := range map[string][]string{
		"owned":          {"owned"},
		"followed,owned": {"followed", "owned"},
		"both":           {"owned", "followed"},
		"off":            {},
		"":               {},
		" none ":         {},
	} {
		got, err := parseActivityScope(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q -> %v %v, want %v", in, got, err, want)
		}
	}
	if _, err := parseActivityScope("everything"); err == nil {
		t.Fatal("unknown scope accepted")
	}
}

func bindTestCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().String("mode", "", "")
	cmd.Flags().Int("resend-after", 0, "")
	cmd.Flags().Int("escalate-after", 0, "")
	cmd.Flags().String("activity", "", "")
	cmd.Flags().String("output", "json", "")
	return cmd
}

func TestBotBindActivityChangesOnlyTheScope(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || !strings.HasSuffix(r.URL.Path, "/me/bot-binding") {
			t.Errorf("request: %s %s", r.Method, r.URL)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]any{"bound": true, "state": "ok", "activity_scope": []string{"owned"}})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)

	cmd := bindTestCmd()
	_ = cmd.Flags().Set("activity", "owned")
	if err := runBotBind(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if _, hasBot := body["bot_id"]; hasBot || !reflect.DeepEqual(body["activity_scope"], []any{"owned"}) {
		t.Fatalf("body: %v", body)
	}

	// Without --activity the field is left out, so the server keeps the scope.
	body = nil
	if err := runBotBind(bindTestCmd(), nil); err != nil {
		t.Fatal(err)
	}
	if _, has := body["activity_scope"]; has {
		t.Fatalf("scope sent without --activity: %v", body)
	}

	cmd = bindTestCmd()
	_ = cmd.Flags().Set("activity", "off")
	if err := runBotBind(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if scope, ok := body["activity_scope"].([]any); !ok || len(scope) != 0 {
		t.Fatalf("off must send an empty list: %v", body)
	}
}

func TestBotWakeHandledAndActivityCommands(t *testing.T) {
	const bot = "11111111-1111-4111-8111-111111111111"
	const event = "22222222-2222-4222-8222-222222222222"
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		if strings.HasSuffix(r.URL.Path, "/handled") {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["reason"] != "已告诉他" {
				t.Errorf("reason: %v", body)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"events": []any{}, "next_cursor": ""})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)

	wake := &cobra.Command{Use: "wake"}
	addHumanNeededReceipts(wake)
	run := func(name string, flags map[string]string, args ...string) {
		t.Helper()
		sub, _, err := wake.Find([]string{name})
		if err != nil || sub.Name() != name {
			t.Fatalf("command %q missing: %v", name, err)
		}
		for k, v := range flags {
			if err := sub.Flags().Set(k, v); err != nil {
				t.Fatal(err)
			}
		}
		var out bytes.Buffer
		sub.SetOut(&out)
		if err := sub.RunE(sub, args); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	run("handled", map[string]string{"reason": "已告诉他"}, bot, event)
	run("activity", map[string]string{"importance": "high", "limit": "5"}, bot)
	want := []string{
		"POST /api/workspaces/ws-1/bots/" + bot + "/wake/events/" + event + "/handled",
		"GET /api/workspaces/ws-1/bots/" + bot + "/activity?importance=high&limit=5",
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("requests:\n%v\nwant\n%v", seen, want)
	}
}
