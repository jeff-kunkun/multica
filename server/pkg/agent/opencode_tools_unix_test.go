//go:build unix

package agent

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A run's session reports the calls the plugin marks as running while the
// CLI works, and nothing once it ended (DENE-2900). The fake CLI stands in for
// OpenCode and its plugin: it writes a marker where the backend told it to,
// then waits before it prints anything, the way a long command keeps
// `opencode run --format json` silent.
func TestOpencodeExecuteReportsRunningTools(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	envPath := filepath.Join(dir, "env.txt")
	readyPath := filepath.Join(dir, "ready")
	goPath := filepath.Join(dir, "go")
	script := fmt.Sprintf("#!/bin/sh\n"+
		"cat > /dev/null\n"+
		"printf '%%s\\n%%s\\n' \"$MULTICA_TOOL_ACTIVITY_DIR\" \"$OPENCODE_CONFIG_CONTENT\" > %[1]q\n"+
		"printf '%%s' '{\"call_id\":\"call-1\",\"tool\":\"bash\",\"input\":{\"command\":\"sleep 600\"},\"started_at\":1793632500000}' > \"$MULTICA_TOOL_ACTIVITY_DIR/m.json\"\n"+
		": > %[2]q\n"+
		"while [ ! -f %[3]q ]; do sleep 0.05; done\n"+
		"rm -f \"$MULTICA_TOOL_ACTIVITY_DIR/m.json\"\n", envPath, readyPath, goPath) + opencodeStreamTail
	fakePath := filepath.Join(dir, "opencode")
	writeTestExecutable(t, fakePath, []byte(script))

	backend, err := New("opencode", Config{ExecutablePath: fakePath, Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	session, err := backend.Execute(t.Context(), "p", ExecOptions{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for range session.Messages {
		}
	}()
	if session.RunningTools == nil {
		t.Fatal("an OpenCode 1.x session must report its running tools")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake opencode never wrote its marker")
		}
		time.Sleep(20 * time.Millisecond)
	}
	tools := session.RunningTools()
	if len(tools) != 1 || tools[0].CallID != "call-1" || tools[0].Input["command"] != "sleep 600" {
		t.Fatalf("RunningTools mid-run = %+v, want the sleeping bash call", tools)
	}

	if err := os.WriteFile(goPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if result := <-session.Result; result.Status != "completed" {
		t.Fatalf("status = %q, error = %q", result.Status, result.Error)
	}
	envRaw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitN(strings.TrimSpace(string(envRaw)), "\n", 2)
	if len(lines) != 2 || lines[0] == "" || !strings.Contains(lines[1], "multica-tool-activity.js") {
		t.Fatalf("the CLI did not get the plugin and its directory: %q", envRaw)
	}
	if _, err := os.Stat(lines[0]); !os.IsNotExist(err) {
		t.Fatalf("the marker directory %s outlived the run (err=%v)", lines[0], err)
	}
	if tools := session.RunningTools(); len(tools) != 0 {
		t.Fatalf("RunningTools after the run = %+v", tools)
	}
}
