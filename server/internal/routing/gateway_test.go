package routing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/multica-ai/multica/server/pkg/llm"
)

// recordingUpstream is a fake OpenAI-compatible endpoint that answers with a
// well-formed verdict and remembers the Authorization header it was given.
func recordingUpstream(t *testing.T, hits *atomic.Int64, auth *atomic.Value) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		auth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"executor_tier\":\"strong\",\"executor_confidence\":0.9,\"reviewer\":\"none\",\"reviewer_confidence\":0.9,\"reason\":\"ok\"}"}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func dialer() func(baseURL, apiKey string) TextGenerator {
	return func(baseURL, apiKey string) TextGenerator {
		return llm.New(llm.Config{APIKey: apiKey, BaseURL: baseURL})
	}
}

// TestWorkspaceGatewayReceivesTheCall is the whole point of the workspace
// gateway: with both halves filled in, the request must reach the WORKSPACE's
// endpoint carrying the WORKSPACE's key, and the deployment endpoint must see
// nothing at all. A fallback that quietly used the deployment credentials here
// would send a shared key to a host the operator never chose.
func TestWorkspaceGatewayReceivesTheCall(t *testing.T) {
	var wsHits, deployHits atomic.Int64
	var wsAuth, deployAuth atomic.Value
	wsURL := recordingUpstream(t, &wsHits, &wsAuth)
	deployURL := recordingUpstream(t, &deployHits, &deployAuth)

	judge := LLMJudge{
		Gen:  llm.New(llm.Config{APIKey: "deployment-key", BaseURL: deployURL}),
		Dial: dialer(),
	}
	target := Target{Model: "m", BaseURL: wsURL, APIKey: "workspace-key"}
	if _, err := judge.Assign(context.Background(), target, JudgeState{Title: "t", Status: "todo"}); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if got := wsHits.Load(); got != 1 {
		t.Fatalf("workspace endpoint hits = %d, want 1", got)
	}
	if got := deployHits.Load(); got != 0 {
		t.Fatalf("deployment endpoint hits = %d, want 0", got)
	}
	if got, _ := wsAuth.Load().(string); !strings.Contains(got, "workspace-key") {
		t.Fatalf("workspace endpoint saw authorization %q, want the workspace key", got)
	}
	if got, _ := wsAuth.Load().(string); strings.Contains(got, "deployment-key") {
		t.Fatalf("deployment key reached the workspace endpoint: %q", got)
	}
}

// TestHalfFilledGatewayUsesTheDeployment covers the two ways somebody stops
// halfway through the form. Neither may send a credential somewhere it was
// not meant to go, so both fall back whole to the deployment gateway.
func TestHalfFilledGatewayUsesTheDeployment(t *testing.T) {
	cases := map[string]Target{
		"url without key": {Model: "m", BaseURL: "https://example.invalid/v1"},
		"key without url": {Model: "m", APIKey: "orphan-key"},
		"blank url":       {Model: "m", BaseURL: "   ", APIKey: "orphan-key"},
	}
	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			var deployHits atomic.Int64
			var deployAuth atomic.Value
			deployURL := recordingUpstream(t, &deployHits, &deployAuth)
			judge := LLMJudge{
				Gen:  llm.New(llm.Config{APIKey: "deployment-key", BaseURL: deployURL}),
				Dial: dialer(),
			}
			if _, err := judge.Assign(context.Background(), target, JudgeState{Title: "t", Status: "todo"}); err != nil {
				t.Fatalf("Assign: %v", err)
			}
			if got := deployHits.Load(); got != 1 {
				t.Fatalf("deployment endpoint hits = %d, want 1 (a half-filled pair must not dial the workspace url)", got)
			}
			if got, _ := deployAuth.Load().(string); strings.Contains(got, "orphan-key") {
				t.Fatalf("workspace key was sent to the deployment endpoint: %q", got)
			}
		})
	}
}

// TestAvailableFollowsTheTarget guards the settings section's most misleading
// possible state: a deployment with no internal LLM, and a workspace that just
// typed in its own endpoint. Before the target reached Available, that
// workspace was told the deployment had no LLM — true, and completely beside
// the point.
func TestAvailableFollowsTheTarget(t *testing.T) {
	judge := LLMJudge{Gen: llm.New(llm.Config{}), Dial: dialer()}
	if judge.Available(Target{Model: "m"}) {
		t.Fatal("no deployment LLM and no workspace gateway must report unavailable")
	}
	if !judge.Available(Target{Model: "m", BaseURL: "https://example.invalid/v1", APIKey: "k"}) {
		t.Fatal("a workspace that supplied both halves must report available")
	}
}

// TestUnwiredDialerFallsBack: a build with no factory cannot honour a
// workspace endpoint, and must keep routing on the deployment gateway rather
// than failing every pass.
func TestUnwiredDialerFallsBack(t *testing.T) {
	var deployHits atomic.Int64
	var deployAuth atomic.Value
	deployURL := recordingUpstream(t, &deployHits, &deployAuth)
	judge := LLMJudge{Gen: llm.New(llm.Config{APIKey: "deployment-key", BaseURL: deployURL})}
	target := Target{Model: "m", BaseURL: "https://example.invalid/v1", APIKey: "k"}
	if _, err := judge.Assign(context.Background(), target, JudgeState{Title: "t", Status: "todo"}); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if got := deployHits.Load(); got != 1 {
		t.Fatalf("deployment endpoint hits = %d, want 1", got)
	}
}

// TestTargetFromSettings pins the resolution rule the two layers above share.
func TestTargetFromSettings(t *testing.T) {
	s := Settings{Enabled: true, Model: "m", BaseURL: "  https://gw.example/v1  ", APIKey: "  k  "}
	target := s.Target()
	if target.BaseURL != "https://gw.example/v1" || target.APIKey != "k" {
		t.Fatalf("Target() did not trim: %+v", target)
	}
	if !target.Override() {
		t.Fatal("a full pair must be an override")
	}
	if (Settings{Model: "m", BaseURL: "https://gw.example/v1"}).Target().Override() {
		t.Fatal("a url with no key must not be an override")
	}
}

// TestAPIKeyIsNeverSerialised is the rule that keeps the opened key from being
// written back into the settings column by any code that round-trips this
// struct.
func TestAPIKeyIsNeverSerialised(t *testing.T) {
	encoded, err := json.Marshal(Settings{Enabled: true, Model: "m", APIKeyEnc: "sealed", APIKey: "plaintext-secret"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw := string(encoded)
	if strings.Contains(raw, "plaintext-secret") {
		t.Fatalf("settings JSON carried the opened key: %s", raw)
	}
	if !strings.Contains(raw, "sealed") {
		t.Fatalf("settings JSON dropped the sealed key: %s", raw)
	}
}
