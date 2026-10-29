package main

import "os"

// openCodeSessionEnvKeys are what an OpenCode session leaves in the shell it
// runs commands from: its own markers, and the XDG base dirs that a wrapper
// such as opencode-kun points at a private OpenCode install. None of them
// describe this machine.
var openCodeSessionEnvKeys = []string{
	"OPENCODE",
	"OPENCODE_PID",
	"XDG_CONFIG_HOME",
	"XDG_DATA_HOME",
	"XDG_STATE_HOME",
	"XDG_CACHE_HOME",
}

// dropOpenCodeSessionEnv removes an OpenCode session's environment from a
// daemon started inside one (an agent ran `multica daemon start` or `restart`
// from its shell). The daemon keeps its environment for its whole life and
// hands it to every task, so otherwise gh and every other XDG-aware tool in
// every task reads the private install's config, and the daemon's own lookups
// of runtime config follow it there too (DENE-2753). Outside an OpenCode
// session nothing is touched: XDG dirs set by the user's own environment stay.
// It returns the keys it removed.
func dropOpenCodeSessionEnv() []string {
	if os.Getenv("OPENCODE") == "" {
		return nil
	}
	var dropped []string
	for _, key := range openCodeSessionEnvKeys {
		if _, ok := os.LookupEnv(key); ok {
			os.Unsetenv(key)
			dropped = append(dropped, key)
		}
	}
	return dropped
}
