package main

import (
	"os"
	"reflect"
	"testing"
)

func TestDropOpenCodeSessionEnv(t *testing.T) {
	t.Run("inside an OpenCode session", func(t *testing.T) {
		t.Setenv("OPENCODE", "1")
		t.Setenv("OPENCODE_PID", "53172")
		t.Setenv("XDG_CONFIG_HOME", "/home/u/.config-opencode-kun")
		t.Setenv("XDG_DATA_HOME", "/home/u/.local-share-opencode-kun")
		t.Setenv("XDG_STATE_HOME", "/home/u/.local-state-opencode-kun")
		t.Setenv("XDG_CACHE_HOME", "/home/u/.cache-opencode-kun")
		kept := map[string]string{
			"HOME":                "/home/u",
			"PATH":                "/usr/local/bin:/usr/bin",
			"GH_CONFIG_DIR":       "/home/u/.config/gh",
			"XDG_DATA_DIRS":       "/usr/local/share:/usr/share",
			"OPENCODE_CONFIG_DIR": "/home/u/opencode-extra",
		}
		for key, value := range kept {
			t.Setenv(key, value)
		}

		got := dropOpenCodeSessionEnv()

		if !reflect.DeepEqual(got, openCodeSessionEnvKeys) {
			t.Fatalf("dropOpenCodeSessionEnv() = %v, want %v", got, openCodeSessionEnvKeys)
		}
		for _, key := range openCodeSessionEnvKeys {
			if _, ok := os.LookupEnv(key); ok {
				t.Errorf("%s still set after drop", key)
			}
		}
		for key, value := range kept {
			if os.Getenv(key) != value {
				t.Errorf("%s = %q, want %q kept", key, os.Getenv(key), value)
			}
		}
	})

	t.Run("outside an OpenCode session", func(t *testing.T) {
		// Register restores first: the test may itself run inside OpenCode.
		t.Setenv("OPENCODE", "")
		os.Unsetenv("OPENCODE")
		t.Setenv("XDG_CONFIG_HOME", "/home/u/.config-custom")

		if got := dropOpenCodeSessionEnv(); got != nil {
			t.Fatalf("dropOpenCodeSessionEnv() = %v, want nil", got)
		}
		if got := os.Getenv("XDG_CONFIG_HOME"); got != "/home/u/.config-custom" {
			t.Fatalf("XDG_CONFIG_HOME = %q, want the user's own value kept", got)
		}
	})
}
