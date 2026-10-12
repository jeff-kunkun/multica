package daemon

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

func qwenModel(id string, def bool, levels ...string) agent.Model {
	m := agent.Model{ID: id, Default: def}
	if len(levels) > 0 {
		thinking := &agent.ModelThinking{Source: "~/.qwen/settings.json"}
		for _, l := range levels {
			thinking.SupportedLevels = append(thinking.SupportedLevels, agent.ThinkingLevel{Value: l})
		}
		m.Thinking = thinking
	}
	return m
}

// TestResolveTaskModelSelectionRefusesUndeclaredQwenLevel pins DENE-1950's
// run-time half: a Qwen level was accepted at save time against that moment's
// declaration. When the declaration later disappears — removed from the
// settings, a different endpoint, a different model — the run must not start
// with the level quietly cleared (that runs the model's default while the
// agent still shows the saved level). It is refused with a reason instead.
func TestResolveTaskModelSelectionRefusesUndeclaredQwenLevel(t *testing.T) {
	for _, tc := range []struct {
		name        string
		catalog     agent.Catalog
		in          taskModelSelection
		wantRefusal []string // substrings; nil = no refusal
	}{
		{
			name:    "still declared runs as saved",
			catalog: agent.Catalog{Models: []agent.Model{qwenModel("nexus-coder", true, "low", "medium", "xhigh", "none")}},
			in:      taskModelSelection{Model: "nexus-coder", ThinkingLevel: "none"},
		},
		{
			name:    "follow default needs no declaration",
			catalog: agent.Catalog{Models: []agent.Model{qwenModel("nexus-coder", true)}},
			in:      taskModelSelection{Model: "nexus-coder"},
		},
		{
			name:        "declaration removed after saving off",
			catalog:     agent.Catalog{Models: []agent.Model{qwenModel("nexus-coder", true)}},
			in:          taskModelSelection{Model: "nexus-coder", ThinkingLevel: "none"},
			wantRefusal: []string{`"none"`, `"nexus-coder"`, "none declared", `thinking_level=""`},
		},
		{
			name:        "declaration narrowed",
			catalog:     agent.Catalog{Models: []agent.Model{qwenModel("nexus-coder", true, "low", "xhigh")}},
			in:          taskModelSelection{Model: "nexus-coder", ThinkingLevel: "none"},
			wantRefusal: []string{"declared now: low, xhigh"},
		},
		{
			name:        "endpoint changed and the model is gone",
			catalog:     agent.Catalog{Models: []agent.Model{qwenModel("other-model", true, "low")}},
			in:          taskModelSelection{Model: "nexus-coder", ThinkingLevel: "low"},
			wantRefusal: []string{`"nexus-coder"`, "none declared"},
		},
		{
			name:        "default model lost its declaration",
			catalog:     agent.Catalog{Models: []agent.Model{qwenModel("nexus-coder", true)}},
			in:          taskModelSelection{ThinkingLevel: "xhigh"},
			wantRefusal: []string{`"xhigh"`, `"nexus-coder"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubModelDiscovery(t, map[string]agent.Catalog{"qwen": tc.catalog})
			got := resolveTaskModelSelection(context.Background(), "qwen", agent.Command{}, tc.in, quietTaskLog())
			if got.ThinkingLevel != tc.in.ThinkingLevel {
				t.Errorf("thinking level changed from %q to %q; a declared level is never cleared quietly", tc.in.ThinkingLevel, got.ThinkingLevel)
			}
			if tc.wantRefusal == nil {
				if got.ThinkingRefusal != "" {
					t.Errorf("unexpected refusal: %s", got.ThinkingRefusal)
				}
				return
			}
			for _, want := range tc.wantRefusal {
				if !strings.Contains(got.ThinkingRefusal, want) {
					t.Errorf("refusal %q does not mention %q", got.ThinkingRefusal, want)
				}
			}
		})
	}
}

// A discovery that cannot answer is not evidence that the declaration went
// away: the level passes through and the Qwen backend still applies it, the
// same fail-open posture as the other providers (MUL-7691).
func TestResolveTaskModelSelectionPassesQwenLevelOnDiscoveryError(t *testing.T) {
	orig := listModels
	listModels = func(context.Context, string, agent.Command) (agent.Catalog, error) {
		return agent.Catalog{}, context.DeadlineExceeded
	}
	t.Cleanup(func() { listModels = orig })

	in := taskModelSelection{Model: "nexus-coder", ThinkingLevel: "none"}
	if got := resolveTaskModelSelection(context.Background(), "qwen", agent.Command{}, in, quietTaskLog()); got != in {
		t.Errorf("got %+v, want %+v unchanged", got, in)
	}
}

// Other providers keep their existing behaviour: an unsupported level is
// dropped with a warning and the run goes ahead.
func TestResolveTaskModelSelectionOtherProvidersStillDrop(t *testing.T) {
	stubModelDiscovery(t, thinkingCatalogs())
	in := taskModelSelection{Model: "claude-opus-5", ThinkingLevel: "max"}
	got := resolveTaskModelSelection(context.Background(), "claude", agent.Command{}, in, quietTaskLog())
	if got.ThinkingLevel != "" || got.ThinkingRefusal != "" {
		t.Errorf("claude: got %+v, want the level dropped without a refusal", got)
	}
}

// The refusal is a config problem on the agent: reported as missing_config,
// which the server does not auto-retry, so a retry cannot loop on it.
func TestTaskRunFailureReasonUndeclaredThinkingIsMissingConfig(t *testing.T) {
	err := fmt.Errorf("%w: %s", errThinkingLevelUndeclared, "level gone")
	if got := taskRunFailureReason(err); got != taskfailure.ReasonAgentMissingConfig.String() {
		t.Errorf("reason = %q, want %q", got, taskfailure.ReasonAgentMissingConfig)
	}
}

// TestQwenUndeclaredLevelRefusedThroughRealDiscovery runs the daemon guard on
// the real Qwen discovery (a local /v1/models stand-in plus an isolated
// QWEN_HOME), not a stubbed catalog: save-time declaration present → the run
// keeps "none"; declaration removed, or pointed at another endpoint → the run
// is refused with the saved level intact. Qwen's endpoint discovery is not
// cached, so each run sees the settings as they are now.
func TestQwenUndeclaredLevelRefusedThroughRealDiscovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"nexus-coder"}]}`))
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	t.Setenv("QWEN_HOME", home)
	t.Setenv("QWEN_CODE_SYSTEM_SETTINGS_PATH", filepath.Join(home, "missing-system.json"))
	for _, k := range []string{"OPENAI_BASE_URL", "OPENAI_API_KEY", "OPENAI_MODEL", "QWEN_MODEL"} {
		t.Setenv(k, "")
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(home, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	settings := func(baseURL string, declared bool) string {
		capability := ""
		if declared {
			capability = `, "capabilities": {"reasoning": {"thinking": true, "disableField": "enable_thinking", "efforts": ["low", "medium", "xhigh"]}}`
		}
		return `{"security": {"auth": {"selectedType": "openai"}}, "modelProviders": {"openai": [` +
			`{"id": "nexus-coder", "baseUrl": "` + baseURL + `", "envKey": "OPENAI_API_KEY"` + capability + `}]}}`
	}
	write(".env", "OPENAI_BASE_URL="+srv.URL+"/v1\nOPENAI_API_KEY=k\nOPENAI_MODEL=nexus-coder\n")

	in := taskModelSelection{Model: "nexus-coder", ThinkingLevel: "none"}
	run := func() taskModelSelection {
		return resolveTaskModelSelection(context.Background(), "qwen", agent.Command{}, in, quietTaskLog())
	}

	write("settings.json", settings(srv.URL+"/v1", true))
	if got := run(); got != in {
		t.Fatalf("declared: got %+v, want %+v", got, in)
	}

	write("settings.json", settings(srv.URL+"/v1", false))
	if got := run(); got.ThinkingLevel != "none" || !strings.Contains(got.ThinkingRefusal, "none declared") {
		t.Fatalf("declaration removed: got %+v, want a refusal keeping none", got)
	}

	write("settings.json", settings("http://elsewhere.invalid/v1", true))
	if got := run(); got.ThinkingLevel != "none" || got.ThinkingRefusal == "" {
		t.Fatalf("other endpoint: got %+v, want a refusal keeping none", got)
	}
}
