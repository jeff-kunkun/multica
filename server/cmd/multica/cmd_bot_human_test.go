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
		"owned":             {"owned"},
		"followed,owned":    {"followed", "owned"},
		"both":              {"owned", "followed"},
		"off":               {},
		"":                  {},
		" none ":            {},
		"everything":        {"owner_visible_all"},
		"owner_visible_all": {"owner_visible_all"},
	} {
		got, err := parseActivityScope(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q -> %v %v, want %v", in, got, err, want)
		}
	}
	if _, err := parseActivityScope("galaxy"); err == nil {
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

func TestBotWritesCommandListsAuditAndSendsConfirmation(t *testing.T) {
	var gotURI, gotConfirm string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI, gotConfirm = r.URL.RequestURI(), r.Header.Get("X-Owner-Confirmation")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"writes": []any{map[string]any{
				"created_at": "2026-10-12T00:00:00Z", "label": "Bot〈小助〉代表〈阿坤〉", "method": "PATCH",
				"route": "/api/agents/{id}", "fields": []any{"name"}, "outcome": "done",
			}},
			"next_cursor": "",
		})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_OWNER_CONFIRMATION", " 0192f3a4-0000-7000-8000-000000000042 ")

	cmd := &cobra.Command{}
	cmd.Flags().Int("limit", 0, "")
	cmd.Flags().String("before", "", "")
	cmd.Flags().Bool("mine", false, "")
	cmd.Flags().String("output", "table", "")
	_ = cmd.Flags().Set("limit", "5")
	_ = cmd.Flags().Set("mine", "true")
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runBotWrites(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if gotURI != "/api/workspaces/ws-1/bot-writes?limit=5&mine=1" {
		t.Fatalf("request: %s", gotURI)
	}
	if gotConfirm != "0192f3a4-0000-7000-8000-000000000042" {
		t.Fatalf("confirmation header: %q", gotConfirm)
	}
	if !strings.Contains(out.String(), "Bot〈小助〉代表〈阿坤〉") || !strings.Contains(out.String(), "fields=name") {
		t.Fatalf("output: %s", out.String())
	}
}
