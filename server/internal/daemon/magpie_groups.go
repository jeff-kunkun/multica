package daemon

import (
	"encoding/json"
	"strings"
)

// magpieGroupModelPrefix is how each CLI is pinned to a magpie group: the
// `--model` value prefix magpie's provider gives its groups in that CLI. A
// provider missing here runs on its CLI's own group whatever the agent lists.
var magpieGroupModelPrefix = map[string]string{
	"opencode": "magpie/group/",
}

// agentMagpieGroups reads runtime_config.magpie_groups: the magpie groups an
// agent can run on, its default first.
func agentMagpieGroups(runtimeConfig json.RawMessage) []string {
	if len(runtimeConfig) == 0 {
		return nil
	}
	var rc struct {
		MagpieGroups []string `json:"magpie_groups"`
	}
	if err := json.Unmarshal(runtimeConfig, &rc); err != nil {
		return nil
	}
	var groups []string
	for _, g := range rc.MagpieGroups {
		if g = strings.TrimSpace(g); g != "" {
			groups = append(groups, g)
		}
	}
	return groups
}

// resolveMagpieGroupArgs picks the magpie group a run uses and pins the CLI
// to it. The issue's choice wins when the agent lists that group; otherwise
// the agent's default does. An agent that lists no groups, or a CLI that
// cannot be pinned, keeps its custom args as they are and group is "".
func resolveMagpieGroupArgs(provider string, customArgs []string, runtimeConfig json.RawMessage, issueGroup string) (args []string, group string) {
	prefix, ok := magpieGroupModelPrefix[provider]
	groups := agentMagpieGroups(runtimeConfig)
	if !ok || len(groups) == 0 {
		return customArgs, ""
	}
	group = groups[0]
	for _, g := range groups {
		if g == issueGroup {
			group = g
			break
		}
	}
	args = make([]string, 0, len(customArgs)+2)
	for i := 0; i < len(customArgs); i++ {
		arg := customArgs[i]
		if arg == "--model" || arg == "-m" {
			i++
			continue
		}
		if strings.HasPrefix(arg, "--model=") {
			continue
		}
		args = append(args, arg)
	}
	return append(args, "--model", prefix+group), group
}
