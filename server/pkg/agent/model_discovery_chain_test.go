package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeChainScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

func TestWalkChainEndpointHitSkipsListCommand(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "invoked")
	bin := writeChainScript(t, "#!/bin/sh\ntouch '"+marker+"'\nprintf 'should-not-win\\n'\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"nexus-coder"}]}`))
	}))
	t.Cleanup(srv.Close)

	sources := map[string]modelEndpointSource{
		"chain-probe": func(context.Context) (string, string, string, error) {
			return srv.URL + "/v1", "sk-test", "nexus-coder", nil
		},
	}
	commands := map[string]modelListCommand{
		"chain-probe": {Args: []string{"models"}, Parse: parseChainIDLines},
	}
	got, err := walkModelDiscoveryChain(context.Background(), "chain-probe", Command{Path: bin}, sources, commands)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "nexus-coder" || !got.Models[0].Default {
		t.Fatalf("models = %+v", got.Models)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("endpoint hit still ran the list command")
	}
}

func TestWalkChainConfirmedEmptyStops(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "invoked")
	bin := writeChainScript(t, "#!/bin/sh\ntouch '"+marker+"'\nprintf 'from-cli\\n'\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	sources := map[string]modelEndpointSource{
		"chain-probe": func(context.Context) (string, string, string, error) {
			return srv.URL + "/v1", "", "", nil
		},
	}
	commands := map[string]modelListCommand{
		"chain-probe": {Args: []string{"models"}, Parse: parseChainIDLines},
	}
	got, err := walkModelDiscoveryChain(context.Background(), "chain-probe", Command{Path: bin}, sources, commands)
	if err != nil {
		t.Fatalf("confirmed empty should not be an error: %v", err)
	}
	if len(got.Models) != 0 {
		t.Fatalf("models = %+v, want none", got.Models)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("confirmed empty list still ran the list command")
	}
}

func TestWalkChainListCommandRunsOnlyWhenRegistered(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "invoked")
	bin := writeChainScript(t, "#!/bin/sh\ntouch '"+marker+"'\nprintf 'from-cli\\n'\n")

	got, err := walkModelDiscoveryChain(context.Background(), "chain-probe", Command{Path: bin}, nil, map[string]modelListCommand{
		"chain-probe": {Args: []string{"models"}, Parse: parseChainIDLines},
	})
	if err != nil {
		t.Fatalf("registered command: %v", err)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "from-cli" {
		t.Fatalf("models = %+v", got.Models)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("registered list command did not run")
	}

	unregistered := filepath.Join(t.TempDir(), "unregistered")
	other := writeChainScript(t, "#!/bin/sh\ntouch '"+unregistered+"'\nprintf 'nope\\n'\n")
	_, err = walkModelDiscoveryChain(context.Background(), "chain-probe", Command{Path: other}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "暂时无法获取") {
		t.Fatalf("unregistered chain error = %v", err)
	}
	if _, statErr := os.Stat(unregistered); statErr == nil {
		t.Fatal("unregistered provider ran a list command")
	}
}

func TestWalkChainEndpointFailureFallsThroughToListCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	bin := writeChainScript(t, "#!/bin/sh\nprintf 'from-cli\\n'\n")

	got, err := walkModelDiscoveryChain(context.Background(), "chain-probe", Command{Path: bin}, map[string]modelEndpointSource{
		"chain-probe": func(context.Context) (string, string, string, error) {
			return srv.URL + "/v1", "sk-test", "", nil
		},
	}, map[string]modelListCommand{
		"chain-probe": {Args: []string{"models"}, Parse: parseChainIDLines},
	})
	if err != nil {
		t.Fatalf("list command should cover an endpoint miss: %v", err)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "from-cli" {
		t.Fatalf("models = %+v", got.Models)
	}
}

func TestWalkChainBothStepsFailKeepsEndpointReason(t *testing.T) {
	const secret = "sk-chain-do-not-leak"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, secret, http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	bin := writeChainScript(t, "#!/bin/sh\nprintf '"+secret+"\\n' >&2\nexit 1\n")

	_, err := walkModelDiscoveryChain(context.Background(), "chain-probe", Command{Path: bin}, map[string]modelEndpointSource{
		"chain-probe": func(context.Context) (string, string, string, error) {
			return srv.URL + "/v1", secret, "", nil
		},
	}, map[string]modelListCommand{
		"chain-probe": {Args: []string{"models"}, Parse: parseChainIDLines},
	})
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("error = %v, want the endpoint status", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked the key: %q", err)
	}
}

func TestListModelsUnknownProviderDoesNotSpawn(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "invoked")
	bin := writeChainScript(t, "#!/bin/sh\ntouch '"+marker+"'\nprintf 'nope\\n'\n")
	_, err := ListModels(context.Background(), "brand-new-runtime", Command{Path: bin})
	if err == nil || !strings.Contains(err.Error(), "暂时无法获取") {
		t.Fatalf("unknown provider error = %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("unknown provider spawned the runtime CLI")
	}
}

func parseChainIDLines(stdout []byte) ([]Model, error) {
	var models []Model
	for _, line := range strings.Split(string(stdout), "\n") {
		id := strings.TrimSpace(line)
		if id == "" {
			continue
		}
		models = append(models, Model{ID: id, Label: id})
	}
	return models, nil
}
