package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPExchangeURLSafety(t *testing.T) {
	for _, raw := range []string{"https://example.com/api/mcp/exchange", "http://localhost:8123/api/mcp/exchange", "http://127.0.0.1:8123/api/mcp/exchange", "http://[::1]:8123/api/mcp/exchange"} {
		if _, err := validateMCPExchangeURL(raw); err != nil {
			t.Errorf("valid URL %s: %v", raw, err)
		}
	}
	for _, raw := range []string{"http://example.com/api/mcp/exchange", "https://user:password@example.com/api/mcp/exchange", "https://example.com/api/mcp/exchange?secret=code", "https://example.com/api/mcp/exchange#code", "https://example.com/other", "http://localhost.evil.com/api/mcp/exchange", "file:///api/mcp/exchange"} {
		if _, err := validateMCPExchangeURL(raw); err == nil {
			t.Errorf("accepted unsafe URL %s", raw)
		}
	}
}

func TestMCPConnectExchange(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/mcp/exchange" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["code"] != "one-time-secret" {
			t.Error("wrong exchange body")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new-mcp-token", "token_type": "Bearer", "expires_in": 3600, "endpoint": server.URL + "/mcp"})
	}))
	defer server.Close()
	cmd := newMCPConnectCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader("one-time-secret\n"))
	cmd.SetArgs([]string{"--code-stdin", "--exchange-url", server.URL + "/api/mcp/exchange"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "one-time-secret") || !strings.Contains(out.String(), "Bearer new-mcp-token") {
		t.Fatalf("unexpected configuration output")
	}
	var config map[string]any
	if err := json.Unmarshal(out.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
}

func TestMCPConnectRefusesRedirectAndRedactsServerErrors(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusBadRequest} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			called := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called++
				w.Header().Set("Location", "/stolen")
				w.WriteHeader(status)
				fmt.Fprint(w, "one-time-secret new-mcp-token")
			}))
			defer server.Close()
			cmd := newMCPConnectCommand()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetIn(strings.NewReader("one-time-secret"))
			cmd.SetArgs([]string{"--code-stdin", "--exchange-url", server.URL + "/api/mcp/exchange"})
			err := cmd.Execute()
			if err == nil || called != 1 {
				t.Fatalf("error %v; requests %d", err, called)
			}
			if strings.Contains(err.Error()+out.String(), "one-time-secret") || strings.Contains(err.Error()+out.String(), "new-mcp-token") {
				t.Fatal("leaked exchange secret")
			}
		})
	}
}

func TestMCPConnectRequiresStdinAndNoCodeArgument(t *testing.T) {
	for _, args := range [][]string{{"--exchange-url", "https://example.com/api/mcp/exchange"}, {"--code", "secret"}, {"secret"}} {
		cmd := newMCPConnectCommand()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Error("accepted code without stdin")
		}
	}
}

func TestMCPConnectRefusesTokenExfiltrationEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"access_token":"new-mcp-token","token_type":"Bearer","endpoint":"https://other.example/mcp"}`)
	}))
	defer server.Close()
	cmd := newMCPConnectCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader("one-time-secret"))
	cmd.SetArgs([]string{"--code-stdin", "--exchange-url", server.URL + "/api/mcp/exchange"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("accepted foreign endpoint")
	}
	if strings.Contains(out.String(), "new-mcp-token") {
		t.Fatal("emitted token for foreign endpoint")
	}
}

func TestMCPConnectionsUseSharedAPI(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, key := range []string{"MULTICA_TASK_ID", "MULTICA_AGENT_ID", "MULTICA_DAEMON_PORT"} {
		t.Setenv(key, "")
	}
	for _, tc := range []struct {
		args                   []string
		method, path, response string
	}{
		{[]string{"list", "--output", "json"}, "GET", "/api/workspaces/ws-1/mcp-connections", `[]`},
		{[]string{"create", "--name", "Grok connection", "--bot", "bot-1", "--output", "json"}, "POST", "/api/workspaces/ws-1/mcp-connections", `{"id":"connection-1","code":"single-use"}`},
		{[]string{"revoke", "connection-1", "--output", "json"}, "DELETE", "/api/workspaces/ws-1/mcp-connections/connection-1", `{}`},
	} {
		t.Run(tc.args[0], func(t *testing.T) {
			count := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count++
				if r.Method != tc.method || r.URL.Path != tc.path {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				if r.Method == "POST" {
					var body map[string]string
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body["name"] != "Grok connection" || body["bot_id"] != "bot-1" || len(body) != 2 {
						t.Errorf("unexpected create body: %v", body)
					}
				}
				fmt.Fprint(w, tc.response)
			}))
			defer server.Close()
			t.Setenv("MULTICA_SERVER_URL", server.URL)
			t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
			t.Setenv("MULTICA_TOKEN", "personal-test-token")
			cmd := newMCPConnectionsCommand()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(tc.args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("request count %d", count)
			}
		})
	}
}
