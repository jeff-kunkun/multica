package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func runRoot(t *testing.T, raw []string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs(args)
	prev := rawArgs
	rawArgs = func() []string { return raw }
	defer func() {
		rawArgs = prev
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	}()
	err = rootCmd.Execute()
	return out.String(), errOut.String(), err
}

func TestUnknownSubcommandJSONSuggestsBoard(t *testing.T) {
	args := []string{"inbox", "list", "--output", "json"}
	stdout, _, err := runRoot(t, args, args...)
	if err != errSilent {
		t.Fatalf("err = %v, want errSilent", err)
	}
	var report unknownSubcommandError
	if jerr := json.Unmarshal([]byte(stdout), &report); jerr != nil {
		t.Fatalf("stdout is not a JSON error: %v\n%s", jerr, stdout)
	}
	if report.Code != "unknown_subcommand" || report.Unknown != "list" || report.Command != "multica inbox" {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Suggestions) == 0 || report.Suggestions[0] != "board" {
		t.Fatalf("suggestions = %v, want board first", report.Suggestions)
	}
	if strings.Contains(stdout, "USAGE") {
		t.Fatalf("json mode must not print help text:\n%s", stdout)
	}
}

func TestUnknownSubcommandTextNamesTheMissingWord(t *testing.T) {
	args := []string{"inbox", "list"}
	stdout, stderr, err := runRoot(t, args, args...)
	if err != errSilent {
		t.Fatalf("err = %v, want errSilent", err)
	}
	for _, want := range []string{`unknown command "list" for "multica inbox"`, "you may want: board", "USAGE"} {
		if !strings.Contains(stderr+stdout, want) {
			t.Errorf("stderr missing %q:\n%s\n%s", want, stderr, stdout)
		}
	}
}

func TestUnknownSubcommandGroupWithoutArgsStillPrintsHelp(t *testing.T) {
	stdout, stderr, err := runRoot(t, []string{"inbox"}, "inbox")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(stdout+stderr, "board") {
		t.Fatalf("help not printed:\n%s%s", stdout, stderr)
	}
}

func TestUnknownSubcommandUsesAliasesAndSynonyms(t *testing.T) {
	args := []string{"issue", "show", "DENE-1", "--output=json"}
	stdout, _, _ := runRoot(t, args, args...)
	var report unknownSubcommandError
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, stdout)
	}
	if len(report.Suggestions) == 0 || report.Suggestions[0] != "get" {
		t.Fatalf("issue show → %v, want get first", report.Suggestions)
	}
	args = []string{"issue", "prs"}
	if _, stderr, _ := runRoot(t, args, args...); strings.Contains(stderr, "unknown command") {
		t.Fatalf("an alias must still run as the real command:\n%s", stderr)
	}
}

func TestWantsJSONOutput(t *testing.T) {
	cases := map[string][]string{
		"true:--output json":  {"inbox", "list", "--output", "json"},
		"true:--output=json":  {"inbox", "--output=JSON", "list"},
		"false:--output text": {"inbox", "list", "--output", "table"},
		"false:none":          {"inbox", "list"},
	}
	for name, args := range cases {
		want := strings.HasPrefix(name, "true")
		if got := wantsJSONOutput(args); got != want {
			t.Errorf("%s: got %v", name, got)
		}
	}
}
