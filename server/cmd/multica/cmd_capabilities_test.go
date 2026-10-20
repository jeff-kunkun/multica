package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Every command in the real tree must be describable: a Short and a runnable
// example. Add a command without a Short and this fails, which is the point.
func TestCapabilitiesEveryCommandIsDescribed(t *testing.T) {
	if problems := validateCapabilityTree(rootCmd); len(problems) > 0 {
		t.Fatalf("%d commands the capability map cannot describe:\n  %s", len(problems), strings.Join(problems, "\n  "))
	}
}

func TestCapabilitiesValidatorCatchesMissingShortAndBadExample(t *testing.T) {
	root := &cobra.Command{Use: "multica"}
	group := &cobra.Command{Use: "thing", Short: "Things"}
	group.AddCommand(&cobra.Command{Use: "list", RunE: func(*cobra.Command, []string) error { return nil }})
	group.AddCommand(&cobra.Command{Use: "get <id>", Short: "Get", Example: "  thing get 1", RunE: func(*cobra.Command, []string) error { return nil }})
	root.AddCommand(group)
	problems := validateCapabilityTree(root)
	if len(problems) != 1 || !strings.Contains(problems[0], "multica thing list: missing Short") {
		t.Fatalf("validator problems = %v", problems)
	}
}

// A new command appears in the map without touching any document.
func TestCapabilitiesNewCommandAppearsAutomatically(t *testing.T) {
	root := &cobra.Command{Use: "multica"}
	issue := &cobra.Command{Use: "issue", Short: "Issues"}
	issue.AddCommand(&cobra.Command{Use: "list", Short: "List issues", Example: "  $ multica issue list --output json", RunE: noopRun})
	root.AddCommand(issue)
	brandNew := &cobra.Command{Use: "frobnicate <id>", Short: "Frobnicate a thing", RunE: noopRun}
	root.AddCommand(brandNew)
	inbox := &cobra.Command{Use: "inbox", Short: "Inbox"}
	dismiss := &cobra.Command{Use: "dismiss <issue>", Short: "Dismiss", RunE: noopRun, Annotations: map[string]string{annotationBotKey: "no"}}
	inbox.AddCommand(dismiss)
	root.AddCommand(inbox)
	daemon := &cobra.Command{Use: "daemon", Short: "Daemon"}
	daemon.AddCommand(&cobra.Command{Use: "status", Short: "Daemon status", RunE: noopRun})
	root.AddCommand(daemon)

	m := buildCapabilityMap(root, "test")
	if m.Count != 4 {
		t.Fatalf("count = %d, want 4: %+v", m.Count, m.Domains)
	}
	find := func(path string) capabilityCommand {
		for _, d := range m.Domains {
			for _, c := range d.Commands {
				if c.Command == path {
					return c
				}
			}
		}
		t.Fatalf("%s missing from map", path)
		return capabilityCommand{}
	}
	if c := find("multica frobnicate"); c.Example != "multica frobnicate <id>" || c.Access != "write" || c.BotKey != "yes" {
		t.Errorf("unlisted top-level command: %+v", c)
	}
	if m.Domains[len(m.Domains)-1].Key != "other" {
		t.Errorf("unlisted root must land in other: %+v", m.Domains)
	}
	if c := find("multica issue list"); c.Example != "multica issue list --output json" || c.Access != "read" {
		t.Errorf("explicit example not used: %+v", c)
	}
	if c := find("multica inbox dismiss"); c.BotKey != "no" {
		t.Errorf("annotation ignored: %+v", c)
	}
	if c := find("multica daemon status"); c.Access != "local" || c.BotKey != "local" {
		t.Errorf("local command: %+v", c)
	}
	if len(m.Onboarding) < 2 || !strings.HasPrefix(m.Onboarding[0].Command, "multica capabilities") {
		t.Errorf("onboarding steps: %+v", m.Onboarding)
	}
}

func TestCapabilitiesJSONAndTableFromRealTree(t *testing.T) {
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		rootCmd.SetOut(&out)
		rootCmd.SetErr(&out)
		rootCmd.SetArgs(append([]string{"capabilities"}, args...))
		defer func() { rootCmd.SetOut(nil); rootCmd.SetErr(nil); rootCmd.SetArgs(nil) }()
		err := rootCmd.Execute()
		return out.String(), err
	}
	raw, err := run("--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var m capabilityMap
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, raw)
	}
	if m.Count < 200 || len(m.Domains) < 7 {
		t.Fatalf("map too small: count=%d domains=%d", m.Count, len(m.Domains))
	}
	for _, d := range m.Domains {
		for _, c := range d.Commands {
			if c.Command == "multica inbox" || c.Command == "multica issue comment" {
				t.Errorf("a group made runnable only by the unknown-subcommand guard is not a command: %+v", c)
			}
			if c.Summary == "" || !strings.HasPrefix(c.Example, "multica ") || c.Help != c.Command+" --help" {
				t.Errorf("row incomplete: %+v", c)
			}
		}
	}
	table, err := run("--domain", "overview", "--output", "table")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1. multica capabilities", "multica inbox board", "BOT KEY", "全景与收件 (overview)"} {
		if !strings.Contains(table, want) {
			t.Errorf("table missing %q:\n%s", want, table)
		}
	}
	if _, err := run("--domain", "nope"); err == nil || !strings.Contains(err.Error(), "unknown domain") {
		t.Fatalf("bad domain: %v", err)
	}
}

func noopRun(*cobra.Command, []string) error { return nil }
