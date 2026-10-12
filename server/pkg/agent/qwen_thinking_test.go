package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// withQwenSystemSettings points the system-settings layer at path (which may
// not exist) for the duration of the test.
func withQwenSystemSettings(t *testing.T, path string) {
	t.Helper()
	t.Setenv("QWEN_CODE_SYSTEM_SETTINGS_PATH", path)
}

const nexusDeclaration = `{
  "security": {"auth": {"selectedType": "openai"}},
  "model": {"generationConfig": {"contextWindowSize": 131072}},
  "modelProviders": {"openai": [
    {"id": "nexus-coder", "name": "Nexus", "baseUrl": "BASE", "envKey": "OPENAI_API_KEY",
     "generationConfig": {"contextWindowSize": 131072},
     "capabilities": {"reasoning": {"thinking": true, "disableField": "enable_thinking", "efforts": ["low", "medium", "xhigh"]}}},
    {"id": "other", "baseUrl": "BASE"}
  ]}
}`

func qwenLevelValues(thinking *ModelThinking) []string {
	if thinking == nil {
		return nil
	}
	out := make([]string, 0, len(thinking.SupportedLevels))
	for _, lvl := range thinking.SupportedLevels {
		out = append(out, lvl.Value)
	}
	return out
}

func TestQwenModelThinkingFollowsQwenParser(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    []string
		def     string
		wantNil bool
	}{
		{name: "efforts plus off", raw: `{"thinking":true,"disableField":"enable_thinking","efforts":["xhigh","low","medium"]}`, want: []string{"low", "medium", "xhigh", "none"}},
		{name: "default effort", raw: `{"thinking":true,"disableField":"enable_thinking","efforts":["low","high"],"defaultEffort":"high"}`, want: []string{"low", "high", "none"}, def: "high"},
		{name: "cannot disable", raw: `{"thinking":true,"disableField":"enable_thinking","efforts":["low"],"canDisable":false}`, want: []string{"low"}},
		{name: "toggle only", raw: `{"thinking":true,"disableField":"enable_thinking","toggleOnly":true}`, want: []string{"none"}},
		{name: "vendor field not delivered", raw: `{"thinking":true,"disableField":"reasoning_effort","efforts":["low"]}`, wantNil: true},
		{name: "unknown effort", raw: `{"thinking":true,"disableField":"enable_thinking","efforts":["ultra"]}`, wantNil: true},
		{name: "duplicate effort", raw: `{"thinking":true,"disableField":"enable_thinking","efforts":["low","low"]}`, wantNil: true},
		{name: "default not listed", raw: `{"thinking":true,"disableField":"enable_thinking","efforts":["low"],"defaultEffort":"xhigh"}`, wantNil: true},
		{name: "canDisable true is invalid", raw: `{"thinking":true,"disableField":"enable_thinking","efforts":["low"],"canDisable":true}`, wantNil: true},
		{name: "thinking false", raw: `{"thinking":false,"disableField":"enable_thinking","efforts":["low"]}`, wantNil: true},
		{name: "malformed", raw: `{"thinking":"yes"}`, wantNil: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := qwenModelThinking(json.RawMessage(tc.raw), "src")
			if tc.wantNil {
				if got != nil {
					t.Fatalf("got %+v, want nil", got)
				}
				return
			}
			if !reflect.DeepEqual(qwenLevelValues(got), tc.want) || got.DefaultLevel != tc.def || got.Source != "src" {
				t.Fatalf("got %+v (levels %v), want %v default %q", got, qwenLevelValues(got), tc.want, tc.def)
			}
		})
	}
}

func TestListModelsQwenAnnotatesDeclaredThinkingForThisEndpointOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"nexus-coder"},{"id":"other"},{"id":"undeclared"}]}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	writeQwenFile(t, dir, ".env", "OPENAI_BASE_URL="+srv.URL+"/v1\nOPENAI_API_KEY=k\nOPENAI_MODEL=nexus-coder\n")
	writeQwenFile(t, dir, "settings.json", strings.ReplaceAll(nexusDeclaration, "BASE", srv.URL+"/v1/"))
	withQwenHome(t, dir)
	withQwenSystemSettings(t, filepath.Join(dir, "missing-system.json"))

	got, err := ListModels(context.Background(), "qwen", Command{})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	byID := map[string]Model{}
	for _, m := range got.Models {
		byID[m.ID] = m
	}
	if want := []string{"low", "medium", "xhigh", "none"}; !reflect.DeepEqual(qwenLevelValues(byID["nexus-coder"].Thinking), want) {
		t.Fatalf("nexus-coder levels = %v, want %v", qwenLevelValues(byID["nexus-coder"].Thinking), want)
	}
	if src := byID["nexus-coder"].Thinking.Source; !strings.Contains(src, "modelProviders.openai[nexus-coder].capabilities.reasoning") {
		t.Fatalf("source = %q", src)
	}
	if byID["other"].Thinking != nil || byID["undeclared"].Thinking != nil {
		t.Fatalf("undeclared models got a catalog: %+v", got.Models)
	}

	// Same declaration, but the runtime now calls a different endpoint: the
	// row's baseUrl no longer matches, so nothing is declared.
	writeQwenFile(t, dir, "settings.json", strings.ReplaceAll(nexusDeclaration, "BASE", "http://elsewhere.invalid/v1"))
	got, err = ListModels(context.Background(), "qwen", Command{})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	for _, m := range got.Models {
		if m.Thinking != nil {
			t.Fatalf("model %s kept levels declared for another endpoint", m.ID)
		}
	}
}

func TestValidateThinkingLevelQwenUsesDeclaredLevels(t *testing.T) {
	declared := Catalog{Models: []Model{
		{ID: "nexus-coder", Default: true, Thinking: qwenModelThinking(json.RawMessage(`{"thinking":true,"disableField":"enable_thinking","efforts":["low","medium","xhigh"]}`), "s")},
		{ID: "plain"},
	}}
	load := func() (Catalog, error) { return declared, nil }
	for _, tc := range []struct {
		model, value string
		want         bool
	}{
		{"nexus-coder", "low", true},
		{"nexus-coder", "none", true},
		{"nexus-coder", "high", false},
		{"", "xhigh", true}, // empty model resolves to the default entry
		{"plain", "low", false},
	} {
		ok, err := ValidateThinkingLevelWith(load, "qwen", tc.model, tc.value)
		if err != nil || ok != tc.want {
			t.Fatalf("%s/%s = %v, %v; want %v", tc.model, tc.value, ok, err, tc.want)
		}
	}
	if !IsKnownThinkingValue("qwen", "xhigh") || !ThinkingDeclaredPerModel("qwen") || ThinkingDeclaredPerModel("codex") {
		t.Fatal("qwen must be a declared-per-model dynamic provider")
	}
}

func decodeSettings(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("settings not JSON: %v\n%s", err, body)
	}
	return out
}

func dig(m map[string]any, path ...string) any {
	var cur any = m
	for _, key := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[key]
	}
	return cur
}

func TestBuildQwenThinkingSettingsLayersOnSystemAndPatchesProviderRow(t *testing.T) {
	dir := t.TempDir()
	writeQwenFile(t, dir, "settings.json", strings.ReplaceAll(nexusDeclaration, "BASE", "http://127.0.0.1:8090/v1"))
	writeQwenFile(t, dir, ".env", "OPENAI_MODEL=nexus-coder\nOPENAI_BASE_URL=http://127.0.0.1:8090/v1\n")
	withQwenHome(t, dir)
	system := filepath.Join(dir, "system.json")
	writeQwenFile(t, dir, "system.json", `{"tools":{"exclude":["web_fetch"]},"model":{"generationConfig":{"timeout":5000}}}`)
	withQwenSystemSettings(t, system)

	body, err := buildQwenThinkingSettings("low", "", "", nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	got := decodeSettings(t, body)
	if !reflect.DeepEqual(dig(got, "tools", "exclude"), []any{"web_fetch"}) || dig(got, "model", "generationConfig", "timeout") != float64(5000) {
		t.Fatalf("existing system settings were dropped: %s", body)
	}
	wantKwargs := map[string]any{"enable_thinking": true, "reasoning_effort": "low"}
	if !reflect.DeepEqual(dig(got, "model", "generationConfig", "extra_body", "chat_template_kwargs"), wantKwargs) {
		t.Fatalf("model kwargs = %v", dig(got, "model", "generationConfig", "extra_body"))
	}
	if dig(got, "model", "reasoningEffort") != "low" {
		t.Fatalf("model.reasoningEffort = %v", dig(got, "model", "reasoningEffort"))
	}
	rows, _ := dig(got, "modelProviders", "openai").([]any)
	if len(rows) != 2 {
		t.Fatalf("provider rows = %v, want both rows carried", rows)
	}
	nexus := rows[0].(map[string]any)
	if !reflect.DeepEqual(dig(nexus, "generationConfig", "extra_body", "chat_template_kwargs"), wantKwargs) {
		t.Fatalf("matching row not patched: %v", nexus)
	}
	if dig(nexus, "generationConfig", "contextWindowSize") != float64(131072) || dig(nexus, "envKey") != "OPENAI_API_KEY" {
		t.Fatalf("row lost its own config: %v", nexus)
	}
	if dig(rows[1].(map[string]any), "generationConfig") != nil {
		t.Fatalf("non-matching row was patched: %v", rows[1])
	}

	off, err := buildQwenThinkingSettings("none", "nexus-coder", "", nil)
	if err != nil {
		t.Fatalf("build none: %v", err)
	}
	gotOff := decodeSettings(t, off)
	if !reflect.DeepEqual(dig(gotOff, "model", "generationConfig", "extra_body", "chat_template_kwargs"), map[string]any{"enable_thinking": false}) ||
		dig(gotOff, "model", "generationConfig", "reasoning") != false || dig(gotOff, "model", "reasoningEffort") != nil {
		t.Fatalf("none patch = %s", off)
	}
}

func TestBuildQwenThinkingSettingsRefusesUnreadableSystemSettings(t *testing.T) {
	dir := t.TempDir()
	withQwenHome(t, dir)
	writeQwenFile(t, dir, "system.json", `{not json`)
	withQwenSystemSettings(t, filepath.Join(dir, "system.json"))
	if _, err := buildQwenThinkingSettings("low", "m", "", nil); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("err = %v, want a refusal that keeps the administrator's settings", err)
	}
}

func TestQwenBackendDeliversThinkingThroughPerTaskSystemSettings(t *testing.T) {
	dir := t.TempDir()
	withQwenHome(t, dir)
	withQwenSystemSettings(t, filepath.Join(dir, "absent.json"))
	userSettings := strings.ReplaceAll(nexusDeclaration, "BASE", "http://127.0.0.1:8090/v1")
	writeQwenFile(t, dir, "settings.json", userSettings)

	// Two tasks with different levels at once must each see their own file.
	levels := []string{"low", "none", "xhigh"}
	captures := make([]string, len(levels))
	var wg sync.WaitGroup
	for i, level := range levels {
		captures[i] = filepath.Join(t.TempDir(), "sys.json")
		backend := newFakeQwenBackend(t, map[string]string{"QWEN_SYS_CAPTURE_FILE": captures[i]})
		wg.Add(1)
		go func(level string, backend *qwenBackend) {
			defer wg.Done()
			session, err := backend.Execute(context.Background(), "task", ExecOptions{Model: "nexus-coder", ThinkingLevel: level})
			if err != nil {
				t.Errorf("Execute %s: %v", level, err)
				return
			}
			if _, result := awaitQwenResult(t, session); result.Status != "completed" {
				t.Errorf("%s result = %+v", level, result)
			}
		}(level, backend)
	}
	wg.Wait()
	for i, level := range levels {
		body, err := os.ReadFile(captures[i])
		if err != nil {
			t.Fatalf("read capture %s: %v", level, err)
		}
		got := decodeSettings(t, body)
		kwargs := dig(got, "model", "generationConfig", "extra_body", "chat_template_kwargs").(map[string]any)
		if level == "none" {
			if kwargs["enable_thinking"] != false {
				t.Fatalf("none kwargs = %v", kwargs)
			}
		} else if kwargs["reasoning_effort"] != level {
			t.Fatalf("task %s saw kwargs %v — levels crossed between tasks", level, kwargs)
		}
		path, _ := os.ReadFile(captures[i] + ".path")
		if _, err := os.Stat(string(path)); !os.IsNotExist(err) {
			t.Fatalf("per-task settings %s not removed after exit (err=%v)", path, err)
		}
	}
	after, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if string(after) != userSettings {
		t.Fatal("shared user settings.json was modified")
	}

	// No level: nothing is injected, so Qwen reads the system settings it
	// would have read anyway.
	capture := filepath.Join(t.TempDir(), "sys.json")
	backend := newFakeQwenBackend(t, map[string]string{"QWEN_SYS_CAPTURE_FILE": capture})
	session, err := backend.Execute(context.Background(), "task", ExecOptions{Model: "nexus-coder"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	awaitQwenResult(t, session)
	if path, _ := os.ReadFile(capture + ".path"); string(path) != filepath.Join(dir, "absent.json") {
		t.Fatalf("empty level changed the system settings path to %q", path)
	}
}
