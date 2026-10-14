package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// newAgentCopyTestCmd builds a standalone cobra.Command carrying the same flags
// runAgentCopy reads (via the shared registrar), plus the persistent --profile
// flag the API-client resolver needs. Tests mutate its flag state in isolation.
func newAgentCopyTestCmd() *cobra.Command {
	c := &cobra.Command{Use: "copy"}
	registerAgentCopyFlags(c)
	c.Flags().String("profile", "", "")
	return c
}

// fullSourceAgent is a representative GET /api/agents/<id> response with every
// portable field populated, so copy tests can assert the whole whitelist.
func fullSourceAgent() map[string]any {
	return map[string]any{
		"id":           "agent-src",
		"name":         "Src",
		"runtime_id":   "runtime-1",
		"description":  "a description",
		"instructions": "some instructions",
		"conversation_starters": []any{
			map[string]any{"label": "Review a PR", "prompt": "Review the open pull request."},
		},
		"avatar_url":           "https://img.example/a.png",
		"custom_args":          []any{"--foo", "--bar"},
		"max_concurrent_tasks": 9,
		// Legacy per-agent model fields an older server may still return;
		// the copy must never forward them.
		"model":              "claude-sonnet-4-6",
		"thinking_level":     "high",
		"service_tier":       "priority",
		"permission_mode":    "public_to",
		"invocation_targets": []any{map[string]any{"target_type": "workspace"}},
		// Secret metadata the copy must never turn into real values.
		"has_custom_env":       true,
		"custom_env_key_count": 2,
		"mcp_config_redacted":  true,
	}
}

// copyMockServer serves the source agent on GET and captures the create body on
// POST. The captured body pointer is filled in when the POST fires.
func copyMockServer(t *testing.T, source map[string]any, gotBody *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/agents/"+source["id"].(string):
			_ = json.NewEncoder(w).Encode(source)
		case r.Method == http.MethodPost && r.URL.Path == "/api/agents":
			if err := json.NewDecoder(r.Body).Decode(gotBody); err != nil {
				t.Errorf("decode create body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "agent-new", "name": "Src (copy)"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func setCopyTestEnv(t *testing.T, serverURL string) {
	t.Helper()
	// Run from a fresh temp dir so no daemon-task marker sits in the cwd
	// ancestry: the CLI then treats this as a normal (non-agent) context and
	// accepts the plain test token instead of demanding a task-scoped mat_ one.
	t.Chdir(t.TempDir())
	t.Setenv("MULTICA_SERVER_URL", serverURL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_AGENT_ID", "")
	t.Setenv("MULTICA_TASK_ID", "")
}

func TestAgentCopySameRuntimeCopiesPortableFields(t *testing.T) {
	var gotBody map[string]any
	srv := copyMockServer(t, fullSourceAgent(), &gotBody)
	defer srv.Close()
	setCopyTestEnv(t, srv.URL)

	cmd := newAgentCopyTestCmd()
	if err := runAgentCopy(cmd, []string{"agent-src"}); err != nil {
		t.Fatalf("runAgentCopy: %v", err)
	}

	if gotBody == nil {
		t.Fatal("create was never called")
	}
	if gotBody["name"] != "Src (copy)" {
		t.Errorf("name = %v, want \"Src (copy)\"", gotBody["name"])
	}
	// No --runtime-id: the copy stays on the source runtime.
	if gotBody["runtime_id"] != "runtime-1" {
		t.Errorf("runtime_id = %v, want runtime-1", gotBody["runtime_id"])
	}
	if gotBody["description"] != "a description" {
		t.Errorf("description = %v", gotBody["description"])
	}
	if gotBody["instructions"] != "some instructions" {
		t.Errorf("instructions = %v", gotBody["instructions"])
	}
	if !reflect.DeepEqual(gotBody["conversation_starters"], []any{
		map[string]any{"label": "Review a PR", "prompt": "Review the open pull request."},
	}) {
		t.Errorf("conversation_starters = %v", gotBody["conversation_starters"])
	}
	if gotBody["avatar_url"] != "https://img.example/a.png" {
		t.Errorf("avatar_url = %v", gotBody["avatar_url"])
	}
	if !reflect.DeepEqual(gotBody["custom_args"], []any{"--foo", "--bar"}) {
		t.Errorf("custom_args = %v", gotBody["custom_args"])
	}
	if gotBody["max_concurrent_tasks"] != float64(9) {
		t.Errorf("max_concurrent_tasks = %v, want 9", gotBody["max_concurrent_tasks"])
	}
	// The model is not a per-agent setting: legacy fields are never forwarded.
	for _, key := range []string{"model", "thinking_level", "service_tier"} {
		if v, ok := gotBody[key]; ok {
			t.Errorf("%s must not be copied, got %v", key, v)
		}
	}
	// Invocation permission is copied verbatim.
	if gotBody["permission_mode"] != "public_to" {
		t.Errorf("permission_mode = %v", gotBody["permission_mode"])
	}
	if !reflect.DeepEqual(gotBody["invocation_targets"], []any{map[string]any{"target_type": "workspace"}}) {
		t.Errorf("invocation_targets = %v", gotBody["invocation_targets"])
	}
	// Secrets / machine-local config must never be copied.
	for _, k := range []string{"custom_env", "mcp_config", "runtime_config", "has_custom_env"} {
		if _, ok := gotBody[k]; ok {
			t.Errorf("body must not contain %q, got %v", k, gotBody[k])
		}
	}
}

func TestAgentCopyOmitsInvalidHistoricalConcurrency(t *testing.T) {
	for _, value := range []any{0, -1, 51} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			source := fullSourceAgent()
			source["max_concurrent_tasks"] = value

			var gotBody map[string]any
			srv := copyMockServer(t, source, &gotBody)
			defer srv.Close()
			setCopyTestEnv(t, srv.URL)

			cmd := newAgentCopyTestCmd()
			if err := runAgentCopy(cmd, []string{"agent-src"}); err != nil {
				t.Fatalf("runAgentCopy: %v", err)
			}
			if _, ok := gotBody["max_concurrent_tasks"]; ok {
				t.Errorf("invalid historical max_concurrent_tasks must be omitted, got %v", gotBody["max_concurrent_tasks"])
			}
		})
	}
}

func TestAgentCopyValidConcurrencyOverrideRepairsInvalidSource(t *testing.T) {
	source := fullSourceAgent()
	source["max_concurrent_tasks"] = 0

	var gotBody map[string]any
	srv := copyMockServer(t, source, &gotBody)
	defer srv.Close()
	setCopyTestEnv(t, srv.URL)

	cmd := newAgentCopyTestCmd()
	_ = cmd.Flags().Set("max-concurrent-tasks", "12")

	if err := runAgentCopy(cmd, []string{"agent-src"}); err != nil {
		t.Fatalf("runAgentCopy: %v", err)
	}
	if gotBody["max_concurrent_tasks"] != float64(12) {
		t.Errorf("max_concurrent_tasks = %v, want 12", gotBody["max_concurrent_tasks"])
	}
}

func TestAgentCopyRejectsInvalidConcurrencyOverrideBeforeRequest(t *testing.T) {
	cmd := newAgentCopyTestCmd()
	_ = cmd.Flags().Set("max-concurrent-tasks", "51")

	err := runAgentCopy(cmd, []string{"agent-src"})
	if err == nil {
		t.Fatal("expected invalid --max-concurrent-tasks error")
	}
	if !strings.Contains(err.Error(), "between 1 and 50") {
		t.Fatalf("error = %q, want 1-50 range", err.Error())
	}
}

// Copying onto a different runtime needs no model choice: the target CLI's
// own configuration decides the model.
func TestAgentCopyCrossRuntimeNeedsNoModel(t *testing.T) {
	var gotBody map[string]any
	srv := copyMockServer(t, fullSourceAgent(), &gotBody)
	defer srv.Close()
	setCopyTestEnv(t, srv.URL)

	cmd := newAgentCopyTestCmd()
	_ = cmd.Flags().Set("runtime-id", "runtime-2")

	if err := runAgentCopy(cmd, []string{"agent-src"}); err != nil {
		t.Fatalf("runAgentCopy: %v", err)
	}
	if gotBody["runtime_id"] != "runtime-2" {
		t.Errorf("runtime_id = %v, want runtime-2", gotBody["runtime_id"])
	}
	for _, key := range []string{"model", "thinking_level", "service_tier"} {
		if v, ok := gotBody[key]; ok {
			t.Errorf("%s must not be sent, got %v", key, v)
		}
	}
}

// A permission override flag fully defines the copy's permission and must not
// mix with the source's copied permission_mode / allow-list.
func TestAgentCopyPermissionOverrideReplacesSource(t *testing.T) {
	var gotBody map[string]any
	srv := copyMockServer(t, fullSourceAgent(), &gotBody)
	defer srv.Close()
	setCopyTestEnv(t, srv.URL)

	cmd := newAgentCopyTestCmd()
	_ = cmd.Flags().Set("permission-mode", "private")

	if err := runAgentCopy(cmd, []string{"agent-src"}); err != nil {
		t.Fatalf("runAgentCopy: %v", err)
	}
	if gotBody["permission_mode"] != "private" {
		t.Errorf("permission_mode = %v, want private", gotBody["permission_mode"])
	}
	// applyAgentPermissionFlags emits an explicit empty allow-list; the source's
	// workspace target must be gone.
	if got, ok := gotBody["invocation_targets"].([]any); !ok || len(got) != 0 {
		t.Errorf("invocation_targets = %v, want empty list", gotBody["invocation_targets"])
	}
}

// custom_env is never read from the source, but an explicit --custom-env sets a
// fresh map on the copy.
func TestAgentCopyAcceptsExplicitCustomEnv(t *testing.T) {
	var gotBody map[string]any
	srv := copyMockServer(t, fullSourceAgent(), &gotBody)
	defer srv.Close()
	setCopyTestEnv(t, srv.URL)

	cmd := newAgentCopyTestCmd()
	_ = cmd.Flags().Set("custom-env", `{"API_KEY":"fresh"}`)

	if err := runAgentCopy(cmd, []string{"agent-src"}); err != nil {
		t.Fatalf("runAgentCopy: %v", err)
	}
	ce, ok := gotBody["custom_env"].(map[string]any)
	if !ok {
		t.Fatalf("custom_env = %v, want a JSON object", gotBody["custom_env"])
	}
	if ce["API_KEY"] != "fresh" {
		t.Errorf("custom_env[API_KEY] = %v, want fresh", ce["API_KEY"])
	}
}

func TestAgentCopyDoesNotExposeConversationStartersOverride(t *testing.T) {
	if agentCopyCmd.Flag("conversation-starters") != nil {
		t.Error("agent copy must carry conversation_starters without a dedicated override flag")
	}
}
