package main

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/botonboarding"
	"github.com/multica-ai/multica/server/internal/cli"
)

// ---------------------------------------------------------------------------
// Capability map (DENE-1804). Generated from the live cobra command tree every
// time it runs, so a new command shows up here without anyone editing a
// document; TestCapabilitiesEveryCommandIsDescribed keeps every command
// described. The onboarding steps above the map come from
// internal/botonboarding, the same source the bot setup text uses.
// ---------------------------------------------------------------------------

// Annotation keys a command may set to override the heuristics below.
const (
	// annotationAccess: "read" | "write" | "local".
	annotationAccess = "multica.access"
	// annotationBotKey: "yes" | "no" | "local" — can an external bot key run it.
	annotationBotKey = "multica.bot_key"
)

type capabilityCommand struct {
	Command string `json:"command"`
	Usage   string `json:"usage"`
	Summary string `json:"summary"`
	Access  string `json:"access"`
	BotKey  string `json:"bot_key"`
	Example string `json:"example"`
	Help    string `json:"help"`
}

type capabilityDomain struct {
	Key      string              `json:"key"`
	Title    string              `json:"title"`
	Commands []capabilityCommand `json:"commands"`
}

type capabilityMap struct {
	ExecutionRules []string             `json:"execution_rules"`
	CLIVersion     string               `json:"cli_version"`
	Onboarding     []botonboarding.Step `json:"onboarding"`
	Legend         map[string]string    `json:"legend"`
	Domains        []capabilityDomain   `json:"domains"`
	Count          int                  `json:"count"`
}

// capabilityDomains maps a top-level command to its domain. Anything not
// listed lands in "other" rather than failing, so a new top-level command is
// visible at once; add it here when it has a home.
var capabilityDomains = []struct {
	Key, Title string
	Roots      []string
}{
	{"issues", "票", []string{"issue", "ask", "plan"}},
	{"chats", "聊天", []string{"chat"}},
	{"agents", "智能体", []string{"agent", "squad", "skill"}},
	{"projects", "项目", []string{"project", "label", "domain", "property"}},
	{"automations", "自动化", []string{"autopilot"}},
	{"overview", "全景与收件", []string{"capabilities", "inbox", "logs", "changes", "runs", "events"}},
	{"workspace", "工作区与接入", []string{"workspace", "connection", "github-app", "settings", "user", "bot", "transfer", "attachment", "repo"}},
	{"local", "本机运行时", []string{"daemon", "runtime", "config", "login", "setup", "update", "version", "auth", "mcp"}},
}

// readVerbs are leaf names that only read. Anything else is a write unless
// the command annotates itself; `status` is a read when it takes no argument.
var readVerbs = map[string]bool{
	"list": true, "ls": true, "get": true, "show": true, "view": true, "context": true, "history": true,
	"search": true, "board": true, "children": true, "runs": true, "usage": true, "logs": true, "log": true,
	"timeline": true, "mentions": true, "overview": true, "capabilities": true, "version": true,
	"pull-requests": true, "events": true, "pending": true, "wait": true, "test": true, "check": true,
	"download": true, "whoami": true, "diff": true, "info": true, "tree": true,
}

var localRoots = map[string]bool{"daemon": true, "runtime": true, "config": true, "login": true, "setup": true, "update": true, "version": true, "auth": true}

var capabilitiesCmd = newCapabilitiesCommand()

func newCapabilitiesCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "capabilities",
		Short: "Map every command: what it reads or changes, an example, and whether a bot key can use it",
		Long: `Generate the command map from this binary's own command tree, grouped by
domain (issues, chats, agents, projects, automations, overview, workspace,
local). Each row carries the usage line, a one-line summary, access
(read / write / local), bot_key (yes / no / local) and a runnable example.
The onboarding steps printed first are the ones a bot gets with its key, so
the receipt and this map never disagree.

bot_key "yes" means an external bot key runs the command with its owner's
power; "no" marks commands that only an agent run or a human login may call;
"local" marks commands that act on this machine. The server's refusal is the
final word, so treat "yes" as "try it", not as a promise.`,
		Example: `  multica capabilities
  multica capabilities --domain issues
  multica capabilities --output json`,
		Args: cobra.NoArgs,
		RunE: runCapabilities,
	}
	cmd.Flags().String("domain", "", "Only this domain key (issues, chats, agents, projects, automations, overview, workspace, local, other)")
	cmd.Flags().String("output", "table", "Output format: table, json")
	return cmd
}

func runCapabilities(cmd *cobra.Command, _ []string) error {
	output, _ := cmd.Flags().GetString("output")
	if output != "table" && output != "json" {
		return fmt.Errorf("invalid output format %q: expected table or json", output)
	}
	m := buildCapabilityMap(cmd.Root(), version)
	if domain, _ := cmd.Flags().GetString("domain"); domain != "" {
		var kept []capabilityDomain
		for _, d := range m.Domains {
			if d.Key == domain {
				kept = append(kept, d)
			}
		}
		if len(kept) == 0 {
			return fmt.Errorf("unknown domain %q: see multica capabilities --help", domain)
		}
		m.Domains = kept
		m.Count = len(kept[0].Commands)
	}
	if output == "json" {
		return cli.PrintJSON(cmd.OutOrStdout(), m)
	}
	printCapabilityMap(cmd.OutOrStdout(), m)
	return nil
}

// buildCapabilityMap walks every available command under root. Group
// commands (those with subcommands) are not rows; their leaves are.
func buildCapabilityMap(root *cobra.Command, cliVersion string) capabilityMap {
	byRoot := map[string][]capabilityCommand{}
	var walk func(c *cobra.Command, top string)
	walk = func(c *cobra.Command, top string) {
		for _, sub := range c.Commands() {
			if !sub.IsAvailableCommand() {
				continue
			}
			rootName := top
			if rootName == "" {
				rootName = sub.Name()
			}
			if sub.HasSubCommands() {
				if sub.Runnable() && !isGuardedGroup(sub) {
					byRoot[rootName] = append(byRoot[rootName], describeCommand(sub, rootName))
				}
				walk(sub, rootName)
				continue
			}
			byRoot[rootName] = append(byRoot[rootName], describeCommand(sub, rootName))
		}
	}
	walk(root, "")

	m := capabilityMap{
		CLIVersion: cliVersion,
		ExecutionRules: []string{
			"Default chat messages and same-agent issue comments merge into queued or active work; issue comments across threads share that execution place. Explicit queue/restart/parallel keep their stated semantics.",
			"A running chat CLI without live-input support rejects default merging and offers explicit queue/restart. A retry is discarded when its issue or chat already has an active run.",
			"Related stop scope: an issue selects its descendants and each issue's source/bound chats; a chat selects its own direct parent/children and exact thread bindings, its directly opened/bound issues and their descendants, plus those issues' source/bound chats. Only child-issue links recurse: reached chats never expand to sibling/other tickets or further chat links. issue halt --related, issue status cancelled --related and chat stop --related share one authorized server operation; every target must be in this workspace and pass its own checks, or the whole operation is rejected.",
			"After chat-to-issue conversion, execution belongs to the issue. The source chat is read-only and links to that issue.",
		},
		Onboarding: botonboarding.Steps(),
		Legend: map[string]string{
			"machine_capacity": "runtime capacity <runtime-id> reads device-wide active runs and limit; --max-concurrent-runs 1..100 sets it via PATCH /api/runtimes/:id (owner only). Default 12, shared across providers/workspaces; overflow queues; lowering does not stop active runs. overview runtimes includes machine_capacity.",
			"access":           "read = only reads; write = creates or changes something; local = acts on this machine",
			"bot_key":          "yes = an external bot key can run it; no = agent run or human login only; local = no server call",
			"bot_wake_chain":   "Bot wake-chain limit is 4; agent replies/completed runs reset it; human messages and server-verified --per-quote messages are exempt; blocked chat sends exit nonzero with a saved-message receipt; blocked comment wakes emit warnings",
		},
	}
	placed := map[string]bool{}
	for _, d := range capabilityDomains {
		domain := capabilityDomain{Key: d.Key, Title: d.Title}
		for _, r := range d.Roots {
			domain.Commands = append(domain.Commands, byRoot[r]...)
			placed[r] = true
		}
		if len(domain.Commands) > 0 {
			m.Domains = append(m.Domains, domain)
			m.Count += len(domain.Commands)
		}
	}
	var rest []string
	for r := range byRoot {
		if !placed[r] {
			rest = append(rest, r)
		}
	}
	sort.Strings(rest)
	if len(rest) > 0 {
		other := capabilityDomain{Key: "other", Title: "其他"}
		for _, r := range rest {
			other.Commands = append(other.Commands, byRoot[r]...)
		}
		m.Domains = append(m.Domains, other)
		m.Count += len(other.Commands)
	}
	return m
}

func describeCommand(c *cobra.Command, rootName string) capabilityCommand {
	access := c.Annotations[annotationAccess]
	if access == "" {
		switch {
		case localRoots[rootName]:
			access = "local"
		case readVerbs[c.Name()], c.Name() == "status" && !strings.Contains(c.Use, "<"):
			access = "read"
		default:
			access = "write"
		}
	}
	botKey := c.Annotations[annotationBotKey]
	if botKey == "" {
		if access == "local" {
			botKey = "local"
		} else {
			botKey = "yes"
		}
	}
	return capabilityCommand{
		Command: c.CommandPath(),
		Usage:   c.UseLine(),
		Summary: c.Short,
		Access:  access,
		BotKey:  botKey,
		Example: commandExample(c),
		Help:    c.CommandPath() + " --help",
	}
}

// commandExample is the first line of the command's Example, or the usage
// line when it has none, so every row is runnable as written.
func commandExample(c *cobra.Command) string {
	for _, line := range strings.Split(c.Example, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "$"))
		if strings.HasPrefix(line, "multica ") {
			return line
		}
	}
	return strings.TrimSuffix(c.UseLine(), " [flags]")
}

func printCapabilityMap(w io.Writer, m capabilityMap) {
	if guard, ok := m.Legend["bot_wake_chain"]; ok {
		fmt.Fprintln(w, guard)
	}
	fmt.Fprintln(w, "接入步骤 / Getting started")
	fmt.Fprint(w, botonboarding.Render())
	for _, rule := range m.ExecutionRules {
		fmt.Fprintln(w, rule)
	}
	fmt.Fprintf(w, "\n%d commands · access: read/write/local · bot_key: yes/no/local (the server's refusal is final)\n", m.Count)
	for _, d := range m.Domains {
		fmt.Fprintf(w, "\n%s (%s) — %d\n", d.Title, d.Key, len(d.Commands))
		rows := make([][]string, 0, len(d.Commands))
		for _, c := range d.Commands {
			rows = append(rows, []string{c.Command, c.Access, c.BotKey, c.Summary, c.Example})
		}
		cli.PrintTable(w, []string{"COMMAND", "ACCESS", "BOT KEY", "SUMMARY", "EXAMPLE"}, rows)
	}
}

// validateCapabilityTree reports every available command that the map could
// not describe: a missing Short, or an example that is not a multica command.
// The test fails on any entry, so a new command cannot ship undescribed.
func validateCapabilityTree(root *cobra.Command) []string {
	var problems []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if !sub.IsAvailableCommand() {
				continue
			}
			if strings.TrimSpace(sub.Short) == "" {
				problems = append(problems, sub.CommandPath()+": missing Short (what it sees or does)")
			}
			if sub.HasSubCommands() && (!sub.Runnable() || isGuardedGroup(sub)) {
				walk(sub)
				continue
			}
			if ex := commandExample(sub); !strings.HasPrefix(ex, "multica ") {
				problems = append(problems, sub.CommandPath()+": example "+ex+" is not a multica command")
			}
			if sub.HasSubCommands() {
				walk(sub)
			}
		}
	}
	walk(root)
	return problems
}
