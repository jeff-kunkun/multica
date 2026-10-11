package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBotGuideReadsServerSetupTextWithoutAKey(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/bots"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "0b0d0000-0000-7000-8000-000000000001", "name": "管家"}})
		case strings.HasSuffix(r.URL.Path, "/bots/setup-text"):
			_ = json.NewEncoder(w).Encode(map[string]any{"setup_text": "你是「管家」\n1. multica overview --output json\n2. multica capabilities\nmultica login --token <钥匙>\n"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")
	t.Setenv("MULTICA_SERVER_URL", srv.URL)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetArgs([]string{"bot", "guide", "管家"})
	defer func() { rootCmd.SetOut(nil); rootCmd.SetArgs(nil) }()
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || !strings.HasSuffix(paths[1], "/bots/setup-text?bot=0b0d0000-0000-7000-8000-000000000001") {
		t.Fatalf("requests = %v", paths)
	}
	if !strings.Contains(out.String(), "2. multica capabilities") || strings.Contains(out.String(), "mul_test") {
		t.Fatalf("guide output: %q", out.String())
	}
}
