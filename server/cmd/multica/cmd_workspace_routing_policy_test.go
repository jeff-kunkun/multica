package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/spf13/cobra"
)

// DENE-1834 part 4: the CLI goes through the same three endpoints as the
// settings page — show, preview (saves nothing), apply (the confirmation).

func routingPolicyServer(t *testing.T, previewed *map[string]any, applied *map[string]any) {
	t.Helper()
	base := "/api/workspaces/" + routingProjectsTestWorkspace + "/routing/dispatch"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == base && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(map[string]any{"policy": routing.DefaultDispatchPolicy, "is_default": true, "series": []string{"Claude"}, "tiers": routing.DefaultLadder.TierOptions(), "editable": true})
		case r.URL.Path == base+"/preview" && r.Method == http.MethodPost:
			json.NewDecoder(r.Body).Decode(previewed)
			json.NewEncoder(w).Encode(map[string]any{"policy": map[string]any{"text": (*previewed)["text"], "rules": []map[string]any{
				{"id": "rule_1", "category": "前端", "series": "Claude", "source": "前端交给 Claude", "origin": "model"},
			}}, "dropped": []string{"rule 2 has no category"}})
		case r.URL.Path == base && r.Method == http.MethodPut:
			json.NewDecoder(r.Body).Decode(applied)
			json.NewEncoder(w).Encode(map[string]any{"policy": map[string]any{"text": (*applied)["text"], "rules": (*applied)["rules"], "version": 1}, "is_default": false, "editable": true})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_AGENT_ID", "")
	t.Setenv("MULTICA_TASK_ID", "")
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", routingProjectsTestWorkspace)
}

func newRoutingPolicyTestCmd(flags ...string) *cobra.Command {
	cmd := newRoutingProjectsTestCmd()
	for _, f := range flags {
		cmd.Flags().String(f, "", "")
	}
	cmd.Flags().Bool("reset", false, "")
	return cmd
}

func TestWorkspaceRoutingPolicyPreviewThenApply(t *testing.T) {
	var previewed, applied map[string]any
	routingPolicyServer(t, &previewed, &applied)

	show := newRoutingPolicyTestCmd()
	if err := runWorkspaceRoutingPolicyShow(show, nil); err != nil {
		t.Fatalf("show: %v", err)
	}

	preview := newRoutingPolicyTestCmd("text", "text-file")
	_ = preview.Flags().Set("text", "前端交给 Claude")
	if err := runWorkspaceRoutingPolicyPreview(preview, nil); err != nil {
		t.Fatalf("preview: %v", err)
	}
	if previewed["text"] != "前端交给 Claude" || applied != nil {
		t.Fatalf("preview sent %v, applied %v (preview must not apply)", previewed, applied)
	}
	if err := runWorkspaceRoutingPolicyPreview(newRoutingPolicyTestCmd("text", "text-file"), nil); err == nil {
		t.Fatal("preview without text was accepted")
	}

	file := filepath.Join(t.TempDir(), "rules.json")
	os.WriteFile(file, []byte(`{"policy":{"text":"前端交给 Claude","rules":[{"id":"rule_1","category":"前端","series":"Claude","source":"前端交给 Claude","origin":"model"}]},"dropped":["x"]}`), 0o600)
	apply := newRoutingPolicyTestCmd("file")
	_ = apply.Flags().Set("file", file)
	if err := runWorkspaceRoutingPolicyApply(apply, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	rules, _ := applied["rules"].([]any)
	if applied["text"] != "前端交给 Claude" || len(rules) != 1 {
		t.Fatalf("applied = %v", applied)
	}

	reset := newRoutingPolicyTestCmd("file")
	_ = reset.Flags().Set("reset", "true")
	if err := runWorkspaceRoutingPolicyApply(reset, nil); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if applied["reset"] != true {
		t.Fatalf("reset sent %v", applied)
	}
	if err := runWorkspaceRoutingPolicyApply(newRoutingPolicyTestCmd("file"), nil); err == nil {
		t.Fatal("apply with neither --file nor --reset was accepted")
	}
}

func TestRoutingPolicyFromJSONAcceptsThreeShapes(t *testing.T) {
	bare := `{"text":"t","rules":[{"category":"前端","series":"Claude"}]}`
	for _, raw := range []string{bare, `{"policy":` + bare + `}`, `{"policy":` + bare + `,"is_default":false}`} {
		p, err := routingPolicyFromJSON([]byte(raw))
		if err != nil || len(p.Rules) != 1 || p.Text != "t" {
			t.Fatalf("%s → %+v, %v", raw, p, err)
		}
	}
	if _, err := routingPolicyFromJSON([]byte(`{"text":"t"}`)); err == nil {
		t.Fatal("a file with no rules was accepted")
	}
	if _, err := routingPolicyFromJSON([]byte(`nope`)); err == nil {
		t.Fatal("non-JSON was accepted")
	}
}
