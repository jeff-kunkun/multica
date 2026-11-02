package agent

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// toolActivityDirEnv names the directory the running-tool plugin keeps one
// file per running OpenCode tool call in.
const toolActivityDirEnv = "MULTICA_TOOL_ACTIVITY_DIR"

//go:embed supplementext/opencode_tools.js
var opencodeToolsPlugin string

// opencodeToolWatch makes OpenCode's running tool calls visible to the daemon
// (DENE-2900). `opencode run --format json` prints a tool only after it
// finished, so without this a long command is indistinguishable from a silent
// hang and the idle watchdog stops it on the no-command budget.
type opencodeToolWatch struct {
	root    string
	running string
}

// newOpenCodeToolWatch writes the plugin into a fresh private directory and
// returns the config content that also loads it.
func newOpenCodeToolWatch(configContent string) (*opencodeToolWatch, string, error) {
	root, err := os.MkdirTemp("", "multica-opencode-tools-")
	if err != nil {
		return nil, "", fmt.Errorf("opencode tool watch: %w", err)
	}
	w := &opencodeToolWatch{root: root, running: filepath.Join(root, "running")}
	pluginPath := filepath.Join(root, "multica-tool-activity.js")
	if err = os.Mkdir(w.running, 0o700); err == nil {
		err = os.WriteFile(pluginPath, []byte(opencodeToolsPlugin), 0o600)
	}
	if err == nil {
		configContent, err = opencodeConfigWithPlugin(configContent, pluginPath)
	}
	if err != nil {
		w.close()
		return nil, "", fmt.Errorf("opencode tool watch: %w", err)
	}
	return w, configContent, nil
}

func (w *opencodeToolWatch) env() string {
	return toolActivityDirEnv + "=" + w.running
}

// runningTools lists the calls the plugin reports as running, oldest first.
// Nil-safe; an unreadable directory or file reads as nothing running, so a
// broken plugin never keeps a run alive.
func (w *opencodeToolWatch) runningTools() []RunningTool {
	if w == nil {
		return nil
	}
	entries, err := os.ReadDir(w.running)
	if err != nil {
		return nil
	}
	var tools []RunningTool
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(w.running, entry.Name()))
		if err != nil {
			continue
		}
		var marker struct {
			CallID    string         `json:"call_id"`
			Tool      string         `json:"tool"`
			Input     map[string]any `json:"input"`
			StartedAt int64          `json:"started_at"`
		}
		if json.Unmarshal(data, &marker) != nil || marker.CallID == "" {
			continue
		}
		tool := RunningTool{CallID: marker.CallID, Tool: marker.Tool, Input: marker.Input}
		if marker.StartedAt > 0 {
			tool.StartedAt = time.UnixMilli(marker.StartedAt)
		}
		tools = append(tools, tool)
	}
	sort.SliceStable(tools, func(i, j int) bool { return tools[i].StartedAt.Before(tools[j].StartedAt) })
	return tools
}

func (w *opencodeToolWatch) close() {
	if w != nil {
		_ = os.RemoveAll(w.root)
	}
}
