package execenv

import (
	"strings"
	"testing"
)

// The MUL-4899 delivery contract as the BRIEF states it. Two orthogonal
// properties are pinned here and must not be collapsed:
//
//   - The invariant ("never link a local path") is ALWAYS-ON — every task kind,
//     no exceptions. It lives outside writeOutput's kind switch so a future kind
//     cannot silently inherit no invariant at all; this test is what keeps that
//     true.
//   - The surface policy ("here is how a file actually gets delivered HERE") is
//     PER-KIND: issue surfaces attach to the comment, chat names the upload
//     command because the browser renders the bound file as a card.

// deliveryInvariantFixtures covers every task kind.
func deliveryInvariantFixtures() map[string]TaskContextForEnv {
	return map[string]TaskContextForEnv{
		"comment":     {IssueID: "i-1", TriggerCommentID: "tc-1", AgentName: "Eve", AgentID: "eve-1"},
		"assignment":  {IssueID: "i-1", AgentName: "Eve", AgentID: "eve-1"},
		"autopilot":   {AutopilotRunID: "r-1", AgentName: "Eve", AgentID: "eve-1"},
		"quickcreate": {QuickCreatePrompt: "p", AgentName: "Eve", AgentID: "eve-1"},
		"chat_direct": {ChatSessionID: "c-1", AgentName: "Eve", AgentID: "eve-1"},
	}
}

// inlineBlocksRule is the brief's one-line rule for charts and diagrams
// (MUL-7649). Surfaces the web renders carry it; surfaces that deliver plain
// text (autopilot results, quick-create stdout) must not, since
// a fenced html/mermaid block does not render there.
const inlineBlocksRule = "fenced `html` or `mermaid` code block"

func TestBriefDeliveryInvariantIsAlwaysOn(t *testing.T) {
	t.Parallel()

	// Phrases every kind must carry, whatever its surface can or cannot deliver.
	wantAll := []string{
		"Runtime-local paths are never deliverables",
		"NEVER write an absolute path or a `file://` URL as a clickable link",
		"`path/to/file.ts:42`",
	}

	for name, ctx := range deliveryInvariantFixtures() {
		out := buildMetaSkillContent("claude", ctx)
		for _, want := range wantAll {
			if !strings.Contains(out, want) {
				t.Errorf("kind=%s: brief is missing always-on delivery invariant %q", name, want)
			}
		}
	}
}

func TestBriefSurfaceDeliveryPolicy(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		mustHave []string
		mustNot  []string
	}{
		// Issue surfaces: files ride the comment.
		"comment": {
			mustHave: []string{"`--attachment <path>` to `multica issue comment add`", inlineBlocksRule},
			mustNot:  []string{"multica attachment upload"},
		},
		"assignment": {
			mustHave: []string{"`--attachment <path>` to `multica issue comment add`", inlineBlocksRule},
			mustNot:  []string{"multica attachment upload"},
		},
		// Direct chat: the upload binds to the reply and the browser renders a
		// card, so the file can sit inline where the agent puts it.
		"chat_direct": {
			mustHave: []string{"`multica attachment upload <local-path>`", inlineBlocksRule},
			mustNot:  []string{"text-only", "separate message"},
		},
		"autopilot": {
			mustHave: []string{"this surface is text-only"},
			mustNot:  []string{"multica attachment upload", inlineBlocksRule},
		},
		"quickcreate": {
			mustHave: []string{"your stdout is text-only", "`multica issue create` call itself via `--attachment <path>`"},
			mustNot:  []string{"multica attachment upload", inlineBlocksRule},
		},
	}

	fixtures := deliveryInvariantFixtures()
	for name, want := range cases {
		ctx, ok := fixtures[name]
		if !ok {
			t.Fatalf("no fixture for surface %q", name)
		}
		out := buildMetaSkillContent("claude", ctx)
		for _, phrase := range want.mustHave {
			if !strings.Contains(out, phrase) {
				t.Errorf("surface=%s: brief missing surface policy %q\n--- Output section ---\n%s",
					name, phrase, outputSection(out))
			}
		}
		// mustNot is checked against the WHOLE brief, not just `## Output`: a
		// denial is wrong wherever it is written, and scoping the search to
		// the section it usually lands in would let the next one land
		// somewhere else. The phrases are ordinary English, though, so a
		// future section could match one legitimately — quote the text around
		// the actual hit and say which section it was in, or the failure
		// points a reader at an Output section the phrase may not be in.
		for _, phrase := range want.mustNot {
			at := strings.Index(out, phrase)
			if at < 0 {
				continue
			}
			where := "in the `## Output` section"
			if !strings.Contains(outputSection(out), phrase) {
				where = "OUTSIDE the `## Output` section"
			}
			t.Errorf("surface=%s: brief must NOT carry %q — found %s\n--- context ---\n%s",
				name, phrase, where, briefContext(out, at, len(phrase)))
		}
	}
}

// TestBriefInboundAttachmentIsNotADeliverable locks the inbound half: a
// downloaded attachment's local path is a private working copy, and the most
// tempting one to echo back because it arrived from the conversation.
//
// The Attachments section owns that framing — it is what `## Output` cannot
// express, because Output does not know an attachment felt shared. The
// no-clickable-local-path rule itself belongs to Output and used to be restated
// here verbatim; MUL-5442 replaced the restatement with a pointer, so this test
// pins the framing plus the pointer and lets the delivery tests above own the
// rule.
func TestBriefInboundAttachmentIsNotADeliverable(t *testing.T) {
	t.Parallel()

	out := buildMetaSkillContent("claude", TaskContextForEnv{
		IssueID: "i-1", TriggerCommentID: "tc-1", AgentName: "Eve", AgentID: "eve-1",
	})
	for _, want := range []string{
		"private working copy",
		"not something the reader can open",
		"the link rules in `## Output` apply to it too",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Attachments section missing %q\n---\n%s", want, out)
		}
	}
	// The rule the pointer defers to must actually be present in the brief.
	if !strings.Contains(out, "NEVER write an absolute path or a `file://` URL as a clickable link") {
		t.Errorf("Attachments points at ## Output but the rule is missing\n---\n%s", out)
	}
}

// Quick actions moved to the daemon's dedicated post-completion suggestion
// pass; the brief must no longer teach the in-band footer syntax to anyone —
// a taught agent would emit footers the transcript then has to strip.
func TestQuickActionsInstructionsAbsentFromAllChatBriefs(t *testing.T) {
	t.Parallel()
	brief := buildMetaSkillContent("claude", TaskContextForEnv{ChatSessionID: "c-1", AgentName: "Eve", AgentID: "eve-1"})
	if strings.Contains(brief, "```quick-actions") || strings.Contains(brief, "### Quick Actions") {
		t.Fatal("chat brief must not teach the in-band quick-actions syntax")
	}
}

// briefContext quotes the brief around a match so a mustNot failure shows the
// sentence that actually tripped it rather than a section it may not be in.
func briefContext(brief string, at, length int) string {
	lo := at - 200
	if lo < 0 {
		lo = 0
	}
	hi := at + length + 200
	if hi > len(brief) {
		hi = len(brief)
	}
	return brief[lo:hi]
}

// outputSection extracts the brief's `## Output` section for readable failures.
func outputSection(brief string) string {
	idx := strings.Index(brief, "\n## Output\n")
	if idx < 0 {
		return "<no ## Output section>"
	}
	return brief[idx:]
}
