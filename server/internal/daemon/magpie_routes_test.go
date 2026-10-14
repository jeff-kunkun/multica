package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// Real `magpie groups` output, captured from an office machine.
const magpieGroupsOutput = `  office-claude    group/office-claude    smart · 2 rules  claude/claude-opus-5-5 · claude/claude-sonnet-5-5  ← Claude Code
  office-codex     group/office-codex     smart · 2 rules  codex/gpt-6.1-sol · codex/gpt-6-luna  ← Codex
  office-opencode  group/office-opencode  smart            opencode-go/mimo-v2.6-pro · zhipu/glm-5.3 · opencode-go/kimi-k3  ← Antigravity CLI, OpenCode
  office-router    group/office-router    order            zhipu/glm-5.3-flash → opencode-go/deepseek-v4.1-flash
  found groups are off · magpie group auto on brings them back
`

const magpieGroupClaudeOutput = `  office-claude  group/office-claude
  routing   smart
  stays     auto
  models    1 claude/claude-opus-5-5  Claude Code · Claude Opus 5.5
            2 claude/claude-sonnet-5-5  Claude Code · Claude Sonnet 5.5
  rules     1 intent "writing, changing or debugging code" → claude/claude-opus-5-5
            2 intent "a quick question" → claude/claude-sonnet-5-5
  classifier group/office-router  tells which intent a message is
  used by   Claude Code
`

func TestParseMagpieGroups(t *testing.T) {
	groups, ok := parseMagpieGroups(magpieGroupsOutput)
	if !ok {
		t.Fatal("parse failed")
	}
	want := []magpieGroup{
		{name: "office-claude", id: "office-claude", models: []string{"claude/claude-opus-5-5", "claude/claude-sonnet-5-5"}, users: []string{"Claude Code"}},
		{name: "office-codex", id: "office-codex", models: []string{"codex/gpt-6.1-sol", "codex/gpt-6-luna"}, users: []string{"Codex"}},
		{name: "office-opencode", id: "office-opencode", models: []string{"opencode-go/mimo-v2.6-pro", "zhipu/glm-5.3", "opencode-go/kimi-k3"}, users: []string{"Antigravity CLI", "OpenCode"}},
	}
	if !reflect.DeepEqual(groups, want) {
		t.Fatalf("groups = %#v\nwant %#v", groups, want)
	}
}

func TestParseMagpieGroupsArrowOrder(t *testing.T) {
	groups, ok := parseMagpieGroups("  fast  group/fast  order  a/one → b/two  ← Codex\n")
	if !ok || len(groups) != 1 || !reflect.DeepEqual(groups[0].models, []string{"a/one", "b/two"}) {
		t.Fatalf("groups = %#v ok=%v", groups, ok)
	}
}

func TestParseMagpieGroupsRejectsUnknownShape(t *testing.T) {
	for _, out := range []string{
		"something ← Claude Code\n",
		"  office-claude  office-claude  smart  a/b  ← Claude Code\n",
		"  office-claude  group/office-claude  smart  a/b  ←  \n",
	} {
		if _, ok := parseMagpieGroups(out); ok {
			t.Errorf("parse accepted %q", out)
		}
	}
	if groups, ok := parseMagpieGroups(""); !ok || len(groups) != 0 {
		t.Errorf("empty listing: groups=%v ok=%v", groups, ok)
	}
}

func TestParseMagpieGroupModelNames(t *testing.T) {
	got := parseMagpieGroupModelNames(magpieGroupClaudeOutput)
	want := map[string]string{
		"claude/claude-opus-5-5":   "Claude Opus 5.5",
		"claude/claude-sonnet-5-5": "Claude Sonnet 5.5",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
}

// stubMagpie answers magpie invocations from a table and counts them.
func stubMagpie(t *testing.T, path string, answers map[string]string) *map[string]int {
	t.Helper()
	calls := map[string]int{}
	var mu sync.Mutex
	prevLookup, prevRun := lookupMagpie, runMagpie
	lookupMagpie = func() string { return path }
	runMagpie = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		key := strings.Join(args, " ")
		mu.Lock()
		calls[key]++
		mu.Unlock()
		out, ok := answers[key]
		if !ok {
			return nil, errors.New("exit status 1")
		}
		return []byte(out), nil
	}
	t.Cleanup(func() { lookupMagpie, runMagpie = prevLookup, prevRun })
	return &calls
}

func TestMagpieProbeReadsNamesOnlyWhenListingChanges(t *testing.T) {
	calls := stubMagpie(t, "/bin/magpie", map[string]string{
		"groups":              magpieGroupsOutput,
		"group office-claude": magpieGroupClaudeOutput,
	})
	var p magpieRouteProber
	for i := 0; i < 2; i++ {
		routes, installed, ok := p.probe(context.Background())
		if !installed || !ok {
			t.Fatalf("round %d: installed=%v ok=%v", i, installed, ok)
		}
		want := MagpieRoute{Group: "office-claude", Models: []MagpieModel{
			{ID: "claude/claude-opus-5-5", Name: "Claude Opus 5.5"},
			{ID: "claude/claude-sonnet-5-5", Name: "Claude Sonnet 5.5"},
		}}
		if got := magpieRouteFor(routes, "claude"); !reflect.DeepEqual(got, want) {
			t.Fatalf("claude route = %#v", got)
		}
		// Detail for codex failed: the route still goes out, under ids.
		if got := magpieRouteFor(routes, "codex"); got.Group != "office-codex" || got.Models[0].Name != "" {
			t.Fatalf("codex route = %#v", got)
		}
		if got := magpieRouteFor(routes, "antigravity"); got.Group != "office-opencode" {
			t.Fatalf("antigravity route = %#v", got)
		}
		if got := magpieRouteFor(routes, "kiro"); got.Group != "" || got.Models == nil {
			t.Fatalf("kiro route = %#v", got)
		}
	}
	// Names that could not all be read are read again next round.
	if (*calls)["groups"] != 2 || (*calls)["group office-codex"] != 2 {
		t.Fatalf("calls = %v", *calls)
	}

	// Once every group's names are read, an unchanged listing reads none.
	calls = stubMagpie(t, "/bin/magpie", map[string]string{
		"groups":                magpieGroupsOutput,
		"group office-claude":   magpieGroupClaudeOutput,
		"group office-codex":    "",
		"group office-opencode": "",
	})
	var q magpieRouteProber
	q.probe(context.Background())
	q.probe(context.Background())
	if (*calls)["groups"] != 2 || (*calls)["group office-claude"] != 1 {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestMagpieProbeStatesWithoutMagpie(t *testing.T) {
	stubMagpie(t, "", nil)
	var p magpieRouteProber
	if _, installed, ok := p.probe(context.Background()); installed || !ok {
		t.Fatalf("not installed: installed=%v ok=%v", installed, ok)
	}

	stubMagpie(t, "/bin/magpie", nil)
	if _, installed, ok := p.probe(context.Background()); !installed || ok {
		t.Fatalf("failing magpie: installed=%v ok=%v", installed, ok)
	}

	stubMagpie(t, "/bin/magpie", map[string]string{"groups": "garbage ← Codex\n"})
	if _, installed, ok := p.probe(context.Background()); !installed || ok {
		t.Fatalf("unparseable magpie: installed=%v ok=%v", installed, ok)
	}
}

func TestReportMagpieRoutes(t *testing.T) {
	var mu sync.Mutex
	got := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got[r.URL.Path] = strings.TrimSpace(string(body))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	newDaemon := func() *Daemon {
		return &Daemon{
			client: NewClient(srv.URL),
			logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			workspaces: map[string]*workspaceState{
				"ws": {workspaceID: "ws", runtimeIDs: []string{"rt-claude", "rt-kiro", "rt-custom"}},
			},
			runtimeIndex: map[string]Runtime{
				"rt-claude": {ID: "rt-claude", Provider: "claude"},
				"rt-kiro":   {ID: "rt-kiro", Provider: "kiro"},
				"rt-custom": {ID: "rt-custom", Provider: "claude", ProfileID: "p"},
			},
		}
	}
	decode := func(path string) map[string]json.RawMessage {
		mu.Lock()
		defer mu.Unlock()
		var body map[string]json.RawMessage
		if err := json.Unmarshal([]byte(got[path]), &body); err != nil {
			t.Fatalf("%s body %q: %v", path, got[path], err)
		}
		return body
	}

	stubMagpie(t, "/bin/magpie", map[string]string{"groups": magpieGroupsOutput, "group office-claude": magpieGroupClaudeOutput})
	newDaemon().reportMagpieRoutes(context.Background())
	if b := decode("/api/daemon/runtimes/rt-claude/magpie"); !strings.Contains(string(b["magpie"]), `"group":"office-claude"`) {
		t.Fatalf("claude report = %s", b["magpie"])
	}
	if b := decode("/api/daemon/runtimes/rt-kiro/magpie"); string(b["magpie"]) != `{"group":"","models":[]}` {
		t.Fatalf("kiro report = %s", b["magpie"])
	}
	mu.Lock()
	_, custom := got["/api/daemon/runtimes/rt-custom/magpie"]
	mu.Unlock()
	if custom {
		t.Fatal("custom profile runtime was reported")
	}

	// magpie uninstalled: the route is cleared.
	stubMagpie(t, "", nil)
	newDaemon().reportMagpieRoutes(context.Background())
	if b := decode("/api/daemon/runtimes/rt-claude/magpie"); string(b["magpie"]) != "null" {
		t.Fatalf("cleared report = %s", b["magpie"])
	}

	// magpie unreadable: nothing is sent, the last route stands.
	mu.Lock()
	got = map[string]string{}
	mu.Unlock()
	stubMagpie(t, "/bin/magpie", nil)
	newDaemon().reportMagpieRoutes(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 0 {
		t.Fatalf("reports sent for unreadable magpie: %v", got)
	}
}
