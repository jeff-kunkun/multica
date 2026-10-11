package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// ---------------------------------------------------------------------------
// Unknown subcommands (DENE-1804). Cobra prints a parent's help when it is
// given a subcommand it does not have (`multica inbox list`), which tells a
// bot nothing about what went wrong. Every non-runnable group gets a RunE
// that names the unknown word, suggests the closest real subcommand, and
// answers in JSON when --output json was asked for.
// ---------------------------------------------------------------------------

// rawArgs returns the process arguments after the binary name. Tests replace
// it, because the group command tolerates unknown flags and so never sees the
// value of --output itself.
var rawArgs = func() []string { return os.Args[1:] }

// subcommandSynonyms maps a verb a caller might try to the names Multica uses
// for the same idea, so `inbox list` points at `board`, `issue show` at `get`.
var subcommandSynonyms = map[string][]string{
	"list":     {"ls", "board", "history", "children", "status", "all", "show"},
	"ls":       {"list", "board"},
	"show":     {"get", "context", "status", "history", "view"},
	"view":     {"get", "show", "context"},
	"info":     {"get", "status", "context"},
	"describe": {"get", "context"},
	"read":     {"get", "history", "context"},
	"get":      {"show", "context", "status"},
	"rm":       {"delete", "remove", "stop"},
	"del":      {"delete", "remove"},
	"remove":   {"delete", "stop"},
	"delete":   {"remove", "stop"},
	"create":   {"add", "new", "open"},
	"new":      {"create", "add", "open"},
	"make":     {"create", "add"},
	"add":      {"create", "new"},
	"edit":     {"update", "set", "status"},
	"set":      {"update", "edit", "status"},
	"change":   {"update", "status", "assign"},
	"modify":   {"update"},
	"send":     {"add", "comment", "reply"},
	"post":     {"add", "send", "comment"},
	"reply":    {"add", "send", "comment"},
	"start":    {"rerun", "run", "add", "new"},
	"run":      {"start", "rerun"},
	"search":   {"list", "find"},
	"find":     {"search", "list", "get"},
	"logs":     {"log", "history", "timeline"},
	"log":      {"logs", "history", "timeline"},
	"history":  {"timeline", "logs", "list"},
	"stop":     {"halt", "cancel", "delete"},
	"cancel":   {"halt", "stop", "status"},
	"finish":   {"close", "done", "status"},
	"done":     {"close", "finish", "status"},
	"close":    {"done", "finish", "status"},
}

// annotationGuardedGroup marks a group whose RunE only reports unknown
// subcommands, so the capability map does not list it as a command.
const annotationGuardedGroup = "multica.guarded_group"

// isGuardedGroup reports whether c is a group made runnable only by the guard.
func isGuardedGroup(c *cobra.Command) bool {
	return c.Annotations[annotationGuardedGroup] == "true"
}

type unknownSubcommandError struct {
	Error       string   `json:"error"`
	Code        string   `json:"code"`
	Command     string   `json:"command"`
	Unknown     string   `json:"unknown"`
	Suggestions []string `json:"suggestions"`
	Available   []string `json:"available"`
	Help        string   `json:"help"`
}

// installUnknownSubcommandGuard gives every non-runnable group under root a
// RunE and an argument validator, so an unknown subcommand is reported by
// name instead of being swallowed into help output. Groups with their own
// Run (`multica config`, `multica setup`) are left alone.
func installUnknownSubcommandGuard(root *cobra.Command) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.HasSubCommands() && !c.Runnable() {
			c.Args = cobra.ArbitraryArgs
			c.RunE = runUnknownSubcommand
			if c.Annotations == nil {
				c.Annotations = map[string]string{}
			}
			c.Annotations[annotationGuardedGroup] = "true"
			// The value of --output is read from the raw arguments, so an
			// unknown flag here must not turn into a flag error first.
			c.FParseErrWhitelist.UnknownFlags = true
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}

func runUnknownSubcommand(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return cmd.Help()
	}
	typed := args[0]
	var available []string
	for _, sub := range cmd.Commands() {
		if sub.IsAvailableCommand() {
			available = append(available, sub.Name())
		}
	}
	suggestions := suggestSubcommands(cmd, typed)
	report := unknownSubcommandError{
		Error:       fmt.Sprintf("unknown command %q for %q", typed, cmd.CommandPath()),
		Code:        "unknown_subcommand",
		Command:     cmd.CommandPath(),
		Unknown:     typed,
		Suggestions: suggestions,
		Available:   available,
		Help:        cmd.CommandPath() + " --help",
	}
	if wantsJSONOutput(rawArgs()) {
		if err := cli.PrintJSON(cmd.OutOrStdout(), report); err != nil {
			return err
		}
		return errSilent
	}
	w := cmd.ErrOrStderr()
	fmt.Fprintf(w, "Error: %s\n", report.Error)
	if len(suggestions) > 0 {
		fmt.Fprintf(w, "There is no %q here; you may want: %s\n", typed, strings.Join(suggestions, ", "))
	} else {
		fmt.Fprintf(w, "There is no %q here; subcommands: %s\n", typed, strings.Join(available, ", "))
	}
	fmt.Fprintln(w)
	_ = cmd.Help()
	return errSilent
}

// suggestSubcommands ranks the group's real subcommands for a typed word:
// aliases and SuggestFor first, then the synonym table, then cobra's own
// prefix/edit-distance matches. Order is kept, duplicates dropped.
func suggestSubcommands(cmd *cobra.Command, typed string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	lower := strings.ToLower(typed)
	for _, sub := range cmd.Commands() {
		if !sub.IsAvailableCommand() {
			continue
		}
		for _, alias := range append(append([]string{}, sub.Aliases...), sub.SuggestFor...) {
			if strings.EqualFold(alias, lower) {
				add(sub.Name())
			}
		}
	}
	for _, want := range subcommandSynonyms[lower] {
		for _, sub := range cmd.Commands() {
			if sub.IsAvailableCommand() && sub.Name() == want {
				add(sub.Name())
			}
		}
	}
	for _, name := range cmd.SuggestionsFor(typed) {
		add(name)
	}
	return out
}

// wantsJSONOutput reports whether the raw arguments asked for JSON output.
func wantsJSONOutput(args []string) bool {
	for i, a := range args {
		switch {
		case a == "--output" || a == "-o":
			if i+1 < len(args) && strings.EqualFold(args[i+1], "json") {
				return true
			}
		case strings.HasPrefix(a, "--output="):
			if strings.EqualFold(strings.TrimPrefix(a, "--output="), "json") {
				return true
			}
		}
	}
	return false
}
