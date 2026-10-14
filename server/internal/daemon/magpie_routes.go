package daemon

import (
	"context"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/processtree"
)

// magpieRouteInterval is how often the daemon re-reads magpie's routing groups
// and reports each built-in runtime's route. A routing change in magpie should
// show on the agents page within about this long. Overridable for tests.
var magpieRouteInterval = time.Minute

// magpieCommandTimeout bounds one magpie invocation. `magpie groups` takes a
// few seconds on a busy machine; a run that overstays is dropped and the
// previously reported route stays as it was.
var magpieCommandTimeout = 30 * time.Second

// magpieAgentNames maps a built-in provider to the name magpie lists it under
// ("← Claude Code" in `magpie groups`). A provider missing here is reported as
// not routed through magpie.
var magpieAgentNames = map[string]string{
	"antigravity": "Antigravity CLI",
	"claude":      "Claude Code",
	"codebuddy":   "CodeBuddy Code",
	"codex":       "Codex",
	"cursor":      "Cursor",
	"dsh":         "DeepSeek Harness",
	"grok":        "Grok Build",
	"hermes":      "Hermes Agent",
	"kimi":        "Kimi Code",
	"opencode":    "OpenCode",
	"pi":          "Pi",
	"qwen":        "Qwen Code",
}

// MagpieModel is one model of a routing group, in magpie's order.
type MagpieModel struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// MagpieRoute is what a runtime's metadata.magpie carries. An empty Group
// means magpie is installed but this CLI is not routed through any group.
type MagpieRoute struct {
	Group  string        `json:"group"`
	Models []MagpieModel `json:"models"`
}

// runMagpie runs magpie with args and returns its stdout. A var so tests can
// answer without a real magpie.
var runMagpie = func(ctx context.Context, path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, magpieCommandTimeout)
	defer cancel()
	return processtree.Output(ctx, exec.CommandContext(ctx, path, args...), time.Second)
}

// lookupMagpie resolves the magpie executable, "" when it is not installed. A
// GUI-launched daemon often lacks ~/.local/bin on PATH, so a miss falls back
// to the user's login shell, cached like the agent CLI resolution. A var so
// tests can stub it.
var lookupMagpie = func() string {
	if path, err := exec.LookPath("magpie"); err == nil {
		return path
	}
	magpieShellMu.Lock()
	defer magpieShellMu.Unlock()
	if magpieShellResolvedAt.IsZero() || time.Since(magpieShellResolvedAt) >= shellResolveTTL {
		magpieShellPath = resolveAgentsViaLoginShell([]string{"magpie"})["magpie"]
		magpieShellResolvedAt = time.Now()
	}
	return magpieShellPath
}

var (
	magpieShellMu         sync.Mutex
	magpieShellPath       string
	magpieShellResolvedAt time.Time
)

// magpieRouteProber reads routes from magpie. Model display names cost one
// `magpie group <id>` per group, so they are re-read only when the
// `magpie groups` listing changes. The zero value is ready to use.
type magpieRouteProber struct {
	mu        sync.Mutex
	lastList  string
	lastNames map[string]string // model id -> display name
}

// probe returns each magpie agent name's route. installed is false when magpie
// is not on this machine; ok is false when magpie is there but could not be
// read this round (failed, timed out, unparseable), in which case nothing
// should be reported.
func (p *magpieRouteProber) probe(ctx context.Context) (routes map[string]MagpieRoute, installed, ok bool) {
	path := lookupMagpie()
	if path == "" {
		return nil, false, true
	}
	out, err := runMagpie(ctx, path, "groups")
	if err != nil {
		return nil, true, false
	}
	groups, ok := parseMagpieGroups(string(out))
	if !ok {
		return nil, true, false
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if string(out) != p.lastList || p.lastNames == nil {
		names := map[string]string{}
		complete := true
		for _, g := range groups {
			detail, err := runMagpie(ctx, path, "group", g.id)
			if err != nil {
				complete = false
				continue
			}
			for id, name := range parseMagpieGroupModelNames(string(detail)) {
				names[id] = name
			}
		}
		// A group whose names could not be read is retried next round; until
		// then its models go out under their ids.
		p.lastList = ""
		if complete {
			p.lastList = string(out)
		}
		p.lastNames = names
	}

	routes = map[string]MagpieRoute{}
	for _, g := range groups {
		models := make([]MagpieModel, 0, len(g.models))
		for _, id := range g.models {
			models = append(models, MagpieModel{ID: id, Name: p.lastNames[id]})
		}
		for _, user := range g.users {
			if _, taken := routes[user]; !taken {
				routes[user] = MagpieRoute{Group: g.name, Models: models}
			}
		}
	}
	return routes, true, true
}

// magpieRouteFor picks a provider's route out of a probe, unrouted when magpie
// lists no group for it.
func magpieRouteFor(routes map[string]MagpieRoute, provider string) MagpieRoute {
	if name, known := magpieAgentNames[provider]; known {
		if route, ok := routes[name]; ok {
			return route
		}
	}
	return MagpieRoute{Group: "", Models: []MagpieModel{}}
}

type magpieGroup struct {
	name   string
	id     string
	models []string
	users  []string
}

var (
	magpieColumnSep   = regexp.MustCompile(`\s{2,}`)
	magpieModelSep    = regexp.MustCompile(` (?:·|→) `)
	magpieModelDetail = regexp.MustCompile(`^\s*(?:models\s+)?\d+ (\S+/\S+)\s{2,}(.+)$`)
)

// parseMagpieGroups reads the groups that serve at least one agent from
// `magpie groups`, whose rows look like
//
//	office-claude  group/office-claude  smart · 2 rules  claude/a · claude/b  ← Claude Code
//
// Rows without "←" serve no agent and are skipped, as are notes that are not
// group rows. ok is false when a row that names a group and its agents does
// not have the expected columns.
func parseMagpieGroups(out string) ([]magpieGroup, bool) {
	var groups []magpieGroup
	for _, line := range strings.Split(out, "\n") {
		left, users, served := strings.Cut(line, "←")
		if !served {
			continue
		}
		cols := magpieColumnSep.Split(strings.TrimSpace(left), -1)
		if len(cols) < 4 || !strings.HasPrefix(cols[1], "group/") {
			return nil, false
		}
		g := magpieGroup{name: cols[0], id: strings.TrimPrefix(cols[1], "group/")}
		for _, m := range magpieModelSep.Split(cols[len(cols)-1], -1) {
			if m = strings.TrimSpace(m); m != "" {
				g.models = append(g.models, m)
			}
		}
		for _, u := range strings.Split(users, ",") {
			if u = strings.TrimSpace(u); u != "" {
				g.users = append(g.users, u)
			}
		}
		if g.name == "" || g.id == "" || len(g.models) == 0 || len(g.users) == 0 {
			return nil, false
		}
		groups = append(groups, g)
	}
	return groups, true
}

// parseMagpieGroupModelNames reads the model display names out of
// `magpie group <id>`, whose model rows look like
//
//	models    1 claude/claude-opus-5-5  Claude Code · Claude Opus 5.5
//	          2 claude/claude-sonnet-5-5  Claude Code · Claude Sonnet 5.5
//
// The name is the part after the provider label.
func parseMagpieGroupModelNames(out string) map[string]string {
	names := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		m := magpieModelDetail.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		label := strings.TrimSpace(m[2])
		if i := strings.LastIndex(label, " · "); i >= 0 {
			label = strings.TrimSpace(label[i+len(" · "):])
		}
		if label != "" {
			names[m[1]] = label
		}
	}
	return names
}

// magpieRouteLoop reports every built-in runtime's magpie route once at start
// and then every magpieRouteInterval. The server keeps it in the runtime's
// metadata and only touches the row when the route actually changed, so an
// unchanged report is a no-op there.
func (d *Daemon) magpieRouteLoop(ctx context.Context) {
	ticker := time.NewTicker(magpieRouteInterval)
	defer ticker.Stop()
	for {
		d.reportMagpieRoutes(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// reportMagpieRoutes is one round of magpieRouteLoop. Nothing here may disturb
// the rest of the daemon: a magpie that cannot be read skips the round, and a
// failed report waits for the next one.
func (d *Daemon) reportMagpieRoutes(ctx context.Context) {
	targets := d.builtinRuntimeProviders()
	if len(targets) == 0 {
		return
	}
	routes, installed, ok := d.magpieRoutes.probe(ctx)
	if !ok {
		return
	}
	ids := make([]string, 0, len(targets))
	for id := range targets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		// nil clears the route: magpie is gone from this machine.
		var route *MagpieRoute
		if installed {
			r := magpieRouteFor(routes, targets[id])
			route = &r
		}
		if err := d.client.ReportMagpieRoute(ctx, id, route); err != nil {
			d.logger.Debug("report magpie route failed; will retry", "runtime_id", id, "error", err)
		}
	}
}

// builtinRuntimeProviders maps every tracked built-in runtime to its provider.
// Custom runtime profiles are left out: magpie routes the CLIs it knows by
// name, not a workspace's own launch commands.
func (d *Daemon) builtinRuntimeProviders() map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]string{}
	for _, ws := range d.workspaces {
		for _, id := range ws.runtimeIDs {
			if rt, ok := d.runtimeIndex[id]; ok && rt.ProfileID == "" {
				out[id] = rt.Provider
			}
		}
	}
	return out
}
