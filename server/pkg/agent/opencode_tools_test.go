package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The daemon reads what the plugin writes: one JSON file per running call,
// oldest first, and nothing it cannot parse (DENE-2900).
func TestOpenCodeToolWatchReadsRunningMarkers(t *testing.T) {
	t.Parallel()
	watch, _, err := newOpenCodeToolWatch("")
	if err != nil {
		t.Fatal(err)
	}
	defer watch.close()

	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(watch.running, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("b.json", `{"call_id":"call-2","tool":"bash","input":{"command":"tail -f deploy.log"},"started_at":1793632600000}`)
	write("a.json", `{"call_id":"call-1","tool":"glob","input":{"pattern":"**/x/**","path":"/Users/k"},"started_at":1793632500000}`)
	write("c.json.tmp", `{"call_id":"call-3","tool":"bash"}`) // still being written
	write("d.json", `not json`)
	write("e.json", `{"tool":"bash"}`) // no call id

	tools := watch.runningTools()
	if len(tools) != 2 {
		t.Fatalf("runningTools = %+v, want the two complete markers", tools)
	}
	if tools[0].CallID != "call-1" || tools[0].Tool != "glob" || tools[0].Input["pattern"] != "**/x/**" {
		t.Fatalf("oldest call = %+v, want the glob", tools[0])
	}
	if want := time.UnixMilli(1793632500000); !tools[0].StartedAt.Equal(want) {
		t.Fatalf("StartedAt = %s, want %s", tools[0].StartedAt, want)
	}
	if tools[1].CallID != "call-2" || tools[1].Input["command"] != "tail -f deploy.log" {
		t.Fatalf("second call = %+v", tools[1])
	}

	watch.close()
	if _, err := os.Stat(watch.root); !os.IsNotExist(err) {
		t.Fatalf("close left %s behind (err=%v)", watch.root, err)
	}
	if tools := watch.runningTools(); tools != nil {
		t.Fatalf("a closed watch still reports %+v", tools)
	}
	var none *opencodeToolWatch
	if none.runningTools() != nil {
		t.Fatal("a nil watch must report nothing running")
	}
	none.close()
}

// The plugin joins whatever config the run already carries; OpenCode
// concatenates plugin lists, so the user's own plugins still load.
func TestOpenCodeToolWatchKeepsExistingConfig(t *testing.T) {
	t.Parallel()
	watch, content, err := newOpenCodeToolWatch(`{"plugin":["file:///user/plugin.js"],"mcp":{"x":{"type":"local"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	defer watch.close()
	var doc struct {
		Plugin []string        `json:"plugin"`
		MCP    json.RawMessage `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(content), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Plugin) != 2 || doc.Plugin[0] != "file:///user/plugin.js" || !strings.HasSuffix(doc.Plugin[1], "/multica-tool-activity.js") {
		t.Fatalf("plugins = %v", doc.Plugin)
	}
	if len(doc.MCP) == 0 {
		t.Fatal("the run's MCP config was dropped")
	}
	if got := watch.env(); got != toolActivityDirEnv+"="+watch.running {
		t.Fatalf("env = %q", got)
	}

	if _, _, err := newOpenCodeToolWatch(`{"plugin":"not a list"}`); err == nil {
		t.Fatal("a config whose plugin field is not a list must be refused")
	}
}

// The plugin itself, run under Node with the events OpenCode 1.x publishes:
// a running tool leaves a marker naming what it does (never a written file's
// body), a re-published running part writes nothing new, and completion or
// failure removes the marker.
func TestOpenCodeToolsPluginTracksRunningParts(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	t.Parallel()
	dir := t.TempDir()
	pluginPath := filepath.Join(dir, "plugin.mjs")
	if err := os.WriteFile(pluginPath, []byte(opencodeToolsPlugin), 0o600); err != nil {
		t.Fatal(err)
	}
	running := filepath.Join(dir, "running")
	if err := os.Mkdir(running, 0o700); err != nil {
		t.Fatal(err)
	}
	harness := `
import fs from "node:fs";
const { MulticaToolActivityPlugin } = await import(process.argv[2]);
const dir = process.env.MULTICA_TOOL_ACTIVITY_DIR;
const hooks = await MulticaToolActivityPlugin({});
const part = (callID, tool, status, input) => ({ type: "message.part.updated", properties: { part: { type: "tool", callID, tool, state: { status, input, time: { start: 1793632500000 } } } } });
const files = () => fs.readdirSync(dir).filter((n) => n.endsWith(".json")).map((n) => JSON.parse(fs.readFileSync(dir + "/" + n, "utf8")));
const out = {};
await hooks.event({ event: part("c1", "bash", "pending", {}) });
out.pending = files().length;
await hooks.event({ event: part("c1", "bash", "running", { command: "sleep 600", description: "wait" }) });
await hooks.event({ event: part("c2", "write", "running", { filePath: "/tmp/a.html", content: "SECRET BODY" }) });
await hooks.event({ event: { type: "message.part.updated", properties: { part: { type: "text", text: "hi" } } } });
out.running = files().sort((a, b) => a.call_id.localeCompare(b.call_id));
fs.rmSync(dir + "/" + fs.readdirSync(dir).find((n) => JSON.parse(fs.readFileSync(dir + "/" + n, "utf8")).call_id === "c1"));
await hooks.event({ event: part("c1", "bash", "running", { command: "sleep 600" }) });
out.republished = files().length;
await hooks.event({ event: part("c2", "write", "completed", {}) });
await hooks.event({ event: part("c3", "glob", "running", { pattern: "**" }) });
await hooks.event({ event: part("c3", "glob", "error", {}) });
out.after = files().length;
delete process.env.MULTICA_TOOL_ACTIVITY_DIR;
out.disabled = Object.keys(await MulticaToolActivityPlugin({})).length;
console.log(JSON.stringify(out));
`
	harnessPath := filepath.Join(dir, "harness.mjs")
	if err := os.WriteFile(harnessPath, []byte(harness), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, harnessPath, "file://"+filepath.ToSlash(pluginPath))
	cmd.Env = append(os.Environ(), toolActivityDirEnv+"="+running)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node harness: %v\n%s", err, raw)
	}
	var got struct {
		Pending int `json:"pending"`
		Running []struct {
			CallID    string            `json:"call_id"`
			Tool      string            `json:"tool"`
			Input     map[string]string `json:"input"`
			StartedAt int64             `json:"started_at"`
		} `json:"running"`
		Republished int `json:"republished"`
		After       int `json:"after"`
		Disabled    int `json:"disabled"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("harness output %q: %v", raw, err)
	}
	if got.Pending != 0 {
		t.Fatalf("a pending part (model still writing the call) was marked running")
	}
	if len(got.Running) != 2 {
		t.Fatalf("running markers = %+v, want bash and write", got.Running)
	}
	bash, write := got.Running[0], got.Running[1]
	if bash.Tool != "bash" || bash.Input["command"] != "sleep 600" || bash.Input["description"] != "wait" || bash.StartedAt != 1793632500000 {
		t.Fatalf("bash marker = %+v", bash)
	}
	if write.Input["filePath"] != "/tmp/a.html" || write.Input["content"] != "" {
		t.Fatalf("write marker = %+v, want the path and never the body", write)
	}
	if got.Republished != 1 {
		t.Fatalf("after one marker was removed by hand, a re-published running part rewrote it (%d markers)", got.Republished)
	}
	if got.After != 0 {
		t.Fatalf("%d markers left after every call completed or failed", got.After)
	}
	if got.Disabled != 0 {
		t.Fatal("without the directory env the plugin must register no hooks")
	}
}
