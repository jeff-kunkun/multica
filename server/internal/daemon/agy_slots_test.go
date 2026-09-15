package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProbeAgyLoggedInDirs(t *testing.T) {
	home := t.TempDir()
	account1 := filepath.Join(home, ".gemini")
	account4 := filepath.Join(home, ".gemini-account4")
	empty := filepath.Join(home, ".gemini-account2")
	if err := os.MkdirAll(filepath.Join(account1), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(account4, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(account1, "oauth_creds.json"), []byte(`{"access_token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(account4, "oauth_creds.json"), []byte(`{"refresh_token":"y"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(empty, "oauth_creds.json"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}

	got := probeAgyLoggedInDirs(home)
	if len(got) != 2 {
		t.Fatalf("logged-in dirs = %#v, want 2 entries", got)
	}
	if got[0] != account1 || got[1] != account4 {
		t.Fatalf("logged-in dirs = %#v, want %q and %q", got, account1, account4)
	}
}

func TestProbeAgyLoggedInDirsIgnoresMissingHome(t *testing.T) {
	if got := probeAgyLoggedInDirs(""); got != nil {
		t.Fatalf("empty home = %#v, want nil", got)
	}
	if got := probeAgyLoggedInDirs(t.TempDir()); len(got) != 0 {
		t.Fatalf("empty home dir = %#v, want none", got)
	}
}
