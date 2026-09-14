package daemon

import "testing"

func TestWithHostHomeDir(t *testing.T) {
	t.Setenv("HOME", "/tmp/agy-home")
	t.Setenv("USERPROFILE", "/tmp/agy-home")
	req := withHostHomeDir(map[string]any{"workspace_id": "ws"})
	if req["home_dir"] != "/tmp/agy-home" {
		t.Fatalf("home_dir = %#v, want /tmp/agy-home", req["home_dir"])
	}
}
