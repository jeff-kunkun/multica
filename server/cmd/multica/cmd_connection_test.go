package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

const connectionTestToken = "ghp_TESTTOKEN_SHOULD_NOT_LEAK"

func TestConnectionAddFromGHDoesNotLeakToken(t *testing.T) {
	restore := chdirConnectionTest(t)
	defer restore()
	resetConnectionAddFlags(t)

	var argv []string
	origCmd := connectionTokenCommand
	origScope := discoverConnectionScope
	t.Cleanup(func() {
		connectionTokenCommand = origCmd
		discoverConnectionScope = origScope
	})
	connectionTokenCommand = func(name string, args ...string) ([]byte, error) {
		argv = append([]string{name}, args...)
		return []byte(connectionTestToken + "\n"), nil
	}
	discoverConnectionScope = func(ctx context.Context, provider, instance, token string) ([]string, error) {
		if token != connectionTestToken {
			t.Fatalf("scope probe received a different token")
		}
		if strings.Contains(provider+instance, connectionTestToken) {
			t.Fatalf("token leaked into scope probe address")
		}
		return []string{"github.com/octocat"}, nil
	}

	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","account_login":"octocat","covers":["octocat"],"webhook_secret":"whsec"}`))
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "user-token")
	t.Setenv("MULTICA_AGENT_ID", "")

	mustSetFlag(t, "from-gh", "true")
	mustSetFlag(t, "yes", "true")
	mustSetFlag(t, "output", "json")
	var stdout, stderr bytes.Buffer
	connectionAddCmd.SetOut(&stdout)
	connectionAddCmd.SetErr(&stderr)
	connectionAddCmd.SetIn(strings.NewReader(""))

	if err := runConnectionAdd(connectionAddCmd, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Join(argv, " ") != "gh auth token" {
		t.Fatalf("argv = %q", argv)
	}
	if posted["personal"] != true {
		t.Fatalf("personal = %#v", posted["personal"])
	}
	blob := stdout.String() + stderr.String()
	if strings.Contains(blob, connectionTestToken) {
		t.Fatalf("token leaked into output:\n%s", blob)
	}
	if !strings.Contains(stderr.String(), "github.com/octocat") {
		t.Fatalf("scope was not printed: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "octocat") {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestConnectionAddFromGlabCancelSkipsPost(t *testing.T) {
	restore := chdirConnectionTest(t)
	defer restore()
	resetConnectionAddFlags(t)

	origCmd := connectionTokenCommand
	origScope := discoverConnectionScope
	t.Cleanup(func() {
		connectionTokenCommand = origCmd
		discoverConnectionScope = origScope
	})
	connectionTokenCommand = func(name string, args ...string) ([]byte, error) {
		if name != "glab" || strings.Join(args, " ") != "auth token" {
			t.Fatalf("argv = %s %v", name, args)
		}
		return []byte(connectionTestToken), nil
	}
	discoverConnectionScope = func(context.Context, string, string, string) ([]string, error) {
		return []string{"gitlab.com/octocat"}, nil
	}
	posted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posted = true
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "user-token")
	t.Setenv("MULTICA_AGENT_ID", "")

	mustSetFlag(t, "from-glab", "true")
	mustSetFlag(t, "yes", "false")
	var stdout, stderr bytes.Buffer
	connectionAddCmd.SetOut(&stdout)
	connectionAddCmd.SetErr(&stderr)
	connectionAddCmd.SetIn(strings.NewReader("n\n"))
	err := runConnectionAdd(connectionAddCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("err = %v", err)
	}
	if posted {
		t.Fatal("cancel still posted the token")
	}
	blob := stdout.String() + stderr.String() + err.Error()
	if strings.Contains(blob, connectionTestToken) {
		t.Fatalf("token leaked: %s", blob)
	}
	if !strings.Contains(stderr.String(), "gitlab.com/octocat") {
		t.Fatalf("scope missing: %s", stderr.String())
	}
}

func TestConnectionAddHelperFailureOmitsToken(t *testing.T) {
	restore := chdirConnectionTest(t)
	defer restore()
	resetConnectionAddFlags(t)
	orig := connectionTokenCommand
	t.Cleanup(func() { connectionTokenCommand = orig })
	connectionTokenCommand = func(string, ...string) ([]byte, error) {
		return []byte(connectionTestToken), os.ErrPermission
	}
	t.Setenv("MULTICA_SERVER_URL", "http://127.0.0.1:1")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "user-token")
	mustSetFlag(t, "from-gh", "true")
	var stderr bytes.Buffer
	connectionAddCmd.SetOut(&stderr)
	connectionAddCmd.SetErr(&stderr)
	err := runConnectionAdd(connectionAddCmd, nil)
	if err == nil {
		t.Fatal("expected helper failure")
	}
	if strings.Contains(err.Error(), connectionTestToken) {
		t.Fatalf("error contains token: %v", err)
	}
}

func chdirConnectionTest(t *testing.T) func() {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return func() { _ = os.Chdir(wd) }
}

func resetConnectionAddFlags(t *testing.T) {
	t.Helper()
	for _, pair := range [][2]string{
		{"from-gh", "false"},
		{"from-glab", "false"},
		{"yes", "false"},
		{"workspace", "false"},
		{"token-file", ""},
		{"provider", ""},
		{"instance-url", ""},
		{"output", "table"},
	} {
		mustSetFlag(t, pair[0], pair[1])
	}
}

func mustSetFlag(t *testing.T, name, value string) {
	t.Helper()
	if err := connectionAddCmd.Flags().Set(name, value); err != nil {
		t.Fatal(err)
	}
}
