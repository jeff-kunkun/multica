package daemon

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestResolveMagpieGroupArgs(t *testing.T) {
	both := json.RawMessage(`{"magpie_groups":["office-opencode","office-pojia"]}`)
	for _, tc := range []struct {
		name       string
		provider   string
		args       []string
		config     json.RawMessage
		issueGroup string
		wantArgs   []string
		wantGroup  string
	}{
		{
			name: "issue picks one of the agent's groups", provider: "opencode",
			args: []string{"--variant", "high"}, config: both, issueGroup: "office-pojia",
			wantArgs: []string{"--variant", "high", "--model", "magpie/group/office-pojia"}, wantGroup: "office-pojia",
		},
		{
			name: "no choice runs on the default", provider: "opencode",
			config:   both,
			wantArgs: []string{"--model", "magpie/group/office-opencode"}, wantGroup: "office-opencode",
		},
		{
			name: "a group the agent does not list runs on the default", provider: "opencode",
			config: both, issueGroup: "office-claude",
			wantArgs: []string{"--model", "magpie/group/office-opencode"}, wantGroup: "office-opencode",
		},
		{
			name: "the chosen group replaces a model in custom args", provider: "opencode",
			args: []string{"--model", "magpie/group/office-pojia", "-m", "x/y", "--model=a/b", "--keep"}, config: both,
			wantArgs: []string{"--keep", "--model", "magpie/group/office-opencode"}, wantGroup: "office-opencode",
		},
		{
			name: "an agent without groups is left alone", provider: "opencode",
			args: []string{"--model", "magpie/group/office-pojia"}, config: json.RawMessage(`{}`), issueGroup: "office-opencode",
			wantArgs: []string{"--model", "magpie/group/office-pojia"},
		},
		{
			name: "a CLI the office cannot pin is left alone", provider: "claude",
			args: []string{"--x"}, config: both, issueGroup: "office-pojia",
			wantArgs: []string{"--x"},
		},
		{
			name: "unreadable config is no groups", provider: "opencode",
			config: json.RawMessage(`{"magpie_groups":"office-pojia"}`),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args, group := resolveMagpieGroupArgs(tc.provider, tc.args, tc.config, tc.issueGroup)
			if !reflect.DeepEqual(args, tc.wantArgs) || group != tc.wantGroup {
				t.Fatalf("got %q %q, want %q %q", args, group, tc.wantArgs, tc.wantGroup)
			}
		})
	}
}
