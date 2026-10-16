package glabmr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListRequestsAllMergeRequests(t *testing.T) {
	dir := t.TempDir()
	bin := t.TempDir()
	argsFile := filepath.Join(bin, "args")
	outFile := filepath.Join(bin, "output")
	if err := os.WriteFile(filepath.Join(bin, "glab"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$GLAB_ARGS_FILE\"\ncat \"$GLAB_OUTPUT_FILE\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outFile, []byte(`[{"iid":490,"state":"merged","web_url":"http://gitlab.example/acme/game/-/merge_requests/490"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GLAB_ARGS_FILE", argsFile)
	t.Setenv("GLAB_OUTPUT_FILE", outFile)
	if _, err := List(context.Background(), dir); err != nil {
		t.Fatalf("List: %v", err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(got) < 3 || got[0] != "mr" || got[1] != "list" || got[2] != "--all" {
		t.Fatalf("glab args = %#v, want mr list --all", got)
	}
}
