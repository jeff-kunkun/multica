package handler

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/routing"
)

// routingHealthReportForTest is a minimal healthy report: these tests are
// about the deployment half of the payload, not about the breaker.
func routingHealthReportForTest() routing.HealthReport {
	return routing.HealthReport{State: routing.StateEnabled, Usable: true, Model: "gpt-5.6-luna"}
}

// gatewayHost reduces the deployment's LLM base URL to something the settings
// section can show. The cases that matter are the ones where the raw value
// carries more than a host: a path, a port, or — in a URL somebody pasted
// straight out of a provider dashboard — userinfo credentials. None of those
// may reach the client, and a value that cannot be parsed at all must produce
// nothing rather than be echoed back.
func TestGatewayHostShowsTheHostAndNothingElse(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"plain base url", "https://api.openai.com/v1", "api.openai.com"},
		{"with a port", "https://llm.internal.example:8443/v1", "llm.internal.example"},
		{"surrounding whitespace", "  https://api.openai.com/v1  ", "api.openai.com"},
		{"userinfo is dropped", "https://sk-secret:x@gateway.example/v1", "gateway.example"},
		{"empty", "", ""},
		{"not a url", "sk-this-is-a-key-not-a-url", ""},
		{"scheme only", "https://", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := gatewayHost(tc.in); got != tc.want {
				t.Fatalf("gatewayHost(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The payload is assembled from deployment config, so the one thing worth
// pinning is that the API key never appears in it in any form.
func TestRoutingHealthPayloadCarriesNoAPIKey(t *testing.T) {
	h := &Handler{cfg: Config{
		LLMAPIKey:       "sk-super-secret-value",
		LLMBaseURL:      "https://gateway.example/v1",
		LLMDefaultModel: "gpt-5.6-mini",
	}}
	got := h.routingHealthPayload(routingHealthReportForTest())
	if got.GatewayHost != "gateway.example" {
		t.Fatalf("GatewayHost = %q", got.GatewayHost)
	}
	if !got.GatewayConfigured {
		t.Fatal("GatewayConfigured = false, want true with both key and base URL set")
	}
	for _, field := range []string{got.GatewayHost, got.GatewayDefaultModel, got.Reason, got.Model} {
		if field == h.cfg.LLMAPIKey {
			t.Fatalf("payload field carries the API key: %q", field)
		}
	}
}

func TestRoutingHealthPayloadReportsAnUnconfiguredDeployment(t *testing.T) {
	// A key with no base URL is still unconfigured: pkg/llm needs both.
	h := &Handler{cfg: Config{LLMAPIKey: "sk-only"}}
	if h.routingHealthPayload(routingHealthReportForTest()).GatewayConfigured {
		t.Fatal("GatewayConfigured = true with no base URL")
	}
}

// TestWorkspaceGatewayWinsInThePayload. When the workspace brought its own
// endpoint, the settings section must name THAT host and say whose it is —
// showing the deployment's is how somebody debugs against the wrong server.
func TestWorkspaceGatewayWinsInThePayload(t *testing.T) {
	h := &Handler{cfg: Config{
		LLMAPIKey:  "sk-deployment",
		LLMBaseURL: "https://deployment.example/v1",
	}}
	rep := routingHealthReportForTest()
	rep.BaseURL = "https://sk-pasted:x@workspace.example/v1"
	rep.KeySet = true
	rep.UsesWorkspaceGateway = true

	got := h.routingHealthPayload(rep)
	if got.GatewayHost != "workspace.example" {
		t.Fatalf("GatewayHost = %q, want the workspace host", got.GatewayHost)
	}
	if got.GatewayScope != gatewayScopeWorkspace {
		t.Fatalf("GatewayScope = %q, want %q", got.GatewayScope, gatewayScopeWorkspace)
	}
	if !got.GatewayKeySet {
		t.Fatal("GatewayKeySet = false with a workspace key stored")
	}
	// Userinfo in the pasted URL must not survive into the client payload.
	if strings.Contains(got.GatewayHost, "sk-pasted") {
		t.Fatalf("GatewayHost leaked the raw url: %q", got.GatewayHost)
	}
}

// TestWorkspaceGatewayMakesAnUnconfiguredDeploymentConfigured is the state
// that used to read as a dead end: no deployment LLM, and a workspace that
// just supplied its own. GatewayConfigured=false there tells the reader there
// is nothing they can do, at the exact moment they have already done it.
func TestWorkspaceGatewayMakesAnUnconfiguredDeploymentConfigured(t *testing.T) {
	h := &Handler{cfg: Config{}}
	rep := routingHealthReportForTest()
	rep.BaseURL = "https://workspace.example/v1"
	rep.KeySet = true
	rep.UsesWorkspaceGateway = true
	if !h.routingHealthPayload(rep).GatewayConfigured {
		t.Fatal("GatewayConfigured = false for a workspace running on its own gateway")
	}
}

// TestKeyStorabilityIsReported so the section can disable the key field up
// front rather than accepting a credential it will then refuse.
func TestKeyStorabilityIsReported(t *testing.T) {
	if (&Handler{}).routingHealthPayload(routingHealthReportForTest()).WorkspaceKeyStorable {
		t.Fatal("WorkspaceKeyStorable = true with no secretbox")
	}
	box, err := NewRoutingSecretBox("deployment-jwt-secret")
	if err != nil {
		t.Fatalf("NewRoutingSecretBox: %v", err)
	}
	if !(&Handler{RoutingSecrets: box}).routingHealthPayload(routingHealthReportForTest()).WorkspaceKeyStorable {
		t.Fatal("WorkspaceKeyStorable = false with a secretbox wired")
	}
}

func containsAny(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && strings.Contains(s, sub)
}
