package main

import (
	"strings"
	"testing"
)

// TestIssueHelpCarriesMovedBriefRules pins the rules DENE-1329 and DENE-2679
// moved out of the agent brief into `--help`. The brief tells the agent to look
// verbs and flags up here, so a rule dropped from this text is gone from every
// agent's view.
func TestIssueHelpCarriesMovedBriefRules(t *testing.T) {
	cases := map[string]struct {
		long string
		want []string
	}{
		"issue comment add": {issueCommentAddCmd.Long, []string{
			"--content-file ./reply.md",
			"never /tmp or a shared path",
			"$LASTEXITCODE",
			// DENE-2679: the brief keeps one Mentions line and points here.
			"starts a run for that agent",
			"notifies a person",
			"followers already see the comment",
			"a thank-you or FYI mention of an agent costs",
			"a name written in prose stays plain text",
		}},
		"issue create": {issueCreateCmd.Long, []string{
			"{Project}: {what}",
			"The title already serves as the H1",
			"--description-file ./description.md",
			// DENE-2679: the brief's sub-issue clause names no flags, so they
			// stay discoverable here when no platform skill is installed.
			"--status todo starts an agent-assigned child",
			"--waiting-for \"<one line>\"",
			"--after <sibling> (repeatable) orders one sub-issue",
		}},
		"issue handoff": {issueHandoffCmd.Long, []string{
			"skips a\ntarget that already has an active run",
			"if the reviewer is empty or a person",
			"never a hand-written @mention",
		}},
	}
	for name, tc := range cases {
		for _, want := range tc.want {
			if !strings.Contains(tc.long, want) {
				t.Errorf("`multica %s --help` lost moved brief rule %q\n---\n%s", name, want, tc.long)
			}
		}
	}
}
