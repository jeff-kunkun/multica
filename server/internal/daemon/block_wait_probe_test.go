package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWaitProbeShellUsesWorkingDirectoryAndExitCode(t *testing.T) {
	code, output := waitProbeShell(context.Background(), "test -f marker && printf ready", t.TempDir())
	if code != 1 {
		t.Fatalf("missing marker exit code = %d, want 1", code)
	}
	if output != "" {
		t.Fatalf("missing marker output = %q", output)
	}

	dir := t.TempDir()
	if err := writeTestFile(dir, "marker", ""); err != nil {
		t.Fatal(err)
	}
	code, output = waitProbeShell(context.Background(), "test -f marker && printf ready", dir)
	if code != 0 || output != "ready" {
		t.Fatalf("ready probe = (%d, %q), want (0, ready)", code, output)
	}
}

func TestWaitProbeShellPreservesPendingExitCodeAndCapsOutput(t *testing.T) {
	code, output := waitProbeShell(context.Background(), "printf pending >&2; exit 10", "")
	if code != 10 || output != "pending" {
		t.Fatalf("pending probe = (%d, %q), want (10, pending)", code, output)
	}

	code, output = waitProbeShell(context.Background(), "printf '%03000d' 1; exit 2", "")
	if code != 2 || len(output) != blockWaitProbeOutputLimit {
		t.Fatalf("capped probe = (%d, %d bytes), want (2, %d)", code, len(output), blockWaitProbeOutputLimit)
	}
}

func TestWaitProbeShellReportsTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	code, output := waitProbeShell(ctx, "sleep 1", "")
	if code != 124 || !strings.Contains(output, "timed out") {
		t.Fatalf("timeout probe = (%d, %q), want 124 and timeout text", code, output)
	}
}

func writeTestFile(dir, name, contents string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600)
}
