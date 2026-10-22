package main

import (
	"strings"
	"testing"
)

// TestIssueHelpCarriesMovedBriefRules pins the rules DENE-1329 moved out of
// the agent brief into `--help`. The brief now carries one line per verb and
// tells the agent to look flags up here, so a rule dropped from this text is
// gone from every agent's view.
func TestIssueHelpCarriesMovedBriefRules(t *testing.T) {
	cases := map[string]struct {
		long string
		want []string
	}{
		"issue comment add": {issueCommentAddCmd.Long, []string{
			"--content-file ./reply.md",
			"never /tmp or a shared path",
			"$LASTEXITCODE",
		}},
		"issue create": {issueCreateCmd.Long, []string{
			"{Project}: {what}",
			"The title already serves as the H1",
			"--description-file ./description.md",
		}},
		"issue handoff": {issueHandoffCmd.Long, []string{
			"skips a\ntarget that already has an active run",
			"if the reviewer is empty or a person",
			"never a hand-written @mention",
		}},
		"issue summon": {issueSummonCmd.Long, []string{
			"blocked with --needs-human already calls that person",
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
