package execenv

import (
	"strings"
	"testing"
)

// TestClassifyTask pins the precedence rule on classifyTask. Both kinds
// plus a tiebreak case for safety.
func TestClassifyTask(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ctx  TaskContextForEnv
		want taskKind
	}{
		{"chat", TaskContextForEnv{ChatSessionID: "c"}, kindChat},
		{"issue-comment-triggered", TaskContextForEnv{IssueID: "i", TriggerCommentID: "c"}, kindIssue},
		{"issue-assignment-triggered", TaskContextForEnv{IssueID: "i"}, kindIssue},
		{"issue-bare", TaskContextForEnv{}, kindIssue},
		{"tiebreak-chat-vs-issue", TaskContextForEnv{ChatSessionID: "c", IssueID: "i", TriggerCommentID: "c"}, kindChat},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyTask(tc.ctx); got != tc.want {
				t.Errorf("classifyTask: got %d, want %d", got, tc.want)
			}
		})
	}
}

// TestBuildMetaSkillContentBriefContent pins that buildMetaSkillContent
// renders the (now sole) brief: the `issue get` one-liner is present and
// the retired legacy verbose description is not.
func TestBuildMetaSkillContentBriefContent(t *testing.T) {
	t.Parallel()

	out := buildMetaSkillContent("claude", TaskContextForEnv{
		IssueID:          "issue-1",
		TriggerCommentID: "comment-1",
		AgentName:        "Eve",
		AgentID:          "eve-1",
	})

	if !strings.Contains(out, "- `multica issue get <id>` / `issue list` / `issue children <id>` — read issues.\n") {
		t.Errorf("brief is missing the `issue get` one-liner\n---\n%s", out)
	}
	if strings.Contains(out, "Get full issue details.") {
		t.Errorf("brief still carries the retired legacy `issue get` description\n---\n%s", out)
	}
}

// TestBuildMetaSkillContentIssueBodyFormatting pins the shared issue-body
// hierarchy rule across every task kind that can author an issue.
func TestBuildMetaSkillContentIssueBodyFormatting(t *testing.T) {
	t.Parallel()

	fixtures := map[string]TaskContextForEnv{
		"issue": {IssueID: "i-1"},
		"chat":  {ChatSessionID: "c-1"},
	}

	for name, ctx := range fixtures {
		name, ctx := name, ctx
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out := buildMetaSkillContent("codex", ctx)
			for _, want := range []string{
				"## Writing bodies",
				"An issue's title is its H1, so a description starts with prose or `##` headings.",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("brief is missing issue-body formatting guidance %q\n---\n%s", want, out)
				}
			}
		})
	}
}

// TestBuildMetaSkillContentSlimKindMatrix locks in which sections the
// slim brief emits per task kind, machine-checking the matrix documented
// on `buildMetaSkillContentSlim`. Heading is matched as a discrete line
// (preceded by newline + followed by newline) so inline references like
// "see ## Comment Formatting" do not trip the absence assertions.
func TestBuildMetaSkillContentSlimKindMatrix(t *testing.T) {

	baseRepo := []RepoContextForEnv{{URL: "https://example.com/x.git", Description: "x"}}

	type sectionCheck struct {
		heading  string
		mustHave map[taskKind]bool
	}
	allKinds := map[taskKind]bool{kindIssue: true, kindChat: true}
	issueKinds := map[taskKind]bool{kindIssue: true}
	checks := []sectionCheck{
		{"# Multica Agent Runtime", allKinds},
		{"## Background work", allKinds},
		{"## Agent Identity", allKinds},
		{"## Commands", allKinds},
		{"## Writing bodies", allKinds},
		{"## Workflow", allKinds},
		{"## Output", allKinds},
		{"## Repositories", allKinds},
		{"## Skills", allKinds},
		{"## Mentions", issueKinds},
	}

	fixtures := map[taskKind]TaskContextForEnv{
		kindChat: {ChatSessionID: "c-1", AgentName: "Eve", AgentID: "eve-1",
			Repos: baseRepo},
		kindIssue: {IssueID: "i-1", AgentName: "Eve", AgentID: "eve-1",
			Repos: baseRepo},
	}

	for kind, ctx := range fixtures {
		out := buildMetaSkillContent("claude", ctx)
		for _, c := range checks {
			needle := "\n" + c.heading + "\n"
			firstLine := c.heading + "\n"
			present := strings.HasPrefix(out, firstLine) || strings.Contains(out, needle)
			want := c.mustHave[kind]
			if want && !present {
				t.Errorf("kind=%d: expected heading %q in slim brief", kind, c.heading)
			}
			if !want && present {
				t.Errorf("kind=%d: heading %q should NOT be in slim brief (matrix gating regression)", kind, c.heading)
			}
		}
	}
}

// TestBackgroundTaskSafetySlimHardPins pins the non-derivable boundaries of
// the Background work section (MUL-4140, MUL-5223, MUL-5274): the platform
// fact, the external-systems/CI ban with its single exception, and the
// persistent-service handoff.
func TestBackgroundTaskSafetySlimHardPins(t *testing.T) {

	out := buildMetaSkillContent("claude", TaskContextForEnv{
		IssueID: "i-1", TriggerCommentID: "tc-1",
		AgentName: "Eve", AgentID: "eve-1",
	})

	for _, want := range []string{
		"## Background work",
		"anything still running is orphaned and its result lost",
		"Never background work and yield",
		"block on results in the foreground",
		"Don't wait on CI or external systems",
		"(no `gh pr checks --watch`, `gh run watch` or sleep polls)",
		"CI running: <PR link>",
		"wait for it in ONE foreground `gh pr checks <pr> --watch`",
		"Only a service the user asked to keep running may outlive the turn",
		"durable logs",
		"a recorded PID",
		"reply with URL, logs and how to stop it",
		"its survival is best-effort",
		"Never kill `multica` by name",
		"stop only a PID you started",
		"`multica daemon status --output json`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("slim Background work missing hardened pin %q\n---\n%s", want, out)
		}
	}
	// Exactly one CI exception.
	if got := strings.Count(out, "explicitly ask for the CI result"); got != 1 {
		t.Errorf("slim brief must state the CI exception exactly once, got %d\n---\n%s", got, out)
	}
	// `gh run watch` may only appear as a banned command, never as the
	// section's example of how to wait properly.
	if strings.Contains(out, "e.g. `gh run watch`") {
		t.Errorf("slim Background work should not suggest waiting for external GitHub CI\n---\n%s", out)
	}
	if strings.Contains(out, "The rules above") {
		t.Errorf("slim Background work must not reintroduce the ambiguous \"The rules above\" scoping sentence\n---\n%s", out)
	}
}
