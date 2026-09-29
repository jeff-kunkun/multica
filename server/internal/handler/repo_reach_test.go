package handler

import "testing"

func TestDecideRepoReach(t *testing.T) {
	cases := []struct {
		name, provider  string
		token, app, cli bool
		mode, next      string
	}{
		{"token wins", "github", true, true, true, "token", ""},
		{"github app", "github", false, true, false, "app", ""},
		{"cli fallback", "github", false, false, true, "cli", ""},
		{"github install", "github", false, false, false, "none", "install_app"},
		{"other connection", "gitlab", false, false, false, "none", "add_connection"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideRepoReach(tc.provider, tc.token, tc.app, tc.cli)
			if got.Mode != tc.mode || got.NextAction != tc.next {
				t.Fatalf("got %#v", got)
			}
		})
	}
}
