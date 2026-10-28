package daemon

import (
	"strings"
	"testing"
)

// Plan first (DENE-2670): a run on a gated issue is told to propose a plan and
// stop. The block is appended, so the prompt ahead of it is unchanged, and an
// ungated issue (no plan_gate on the claim) sees no block at all.
func TestBuildPromptAppendsPlanGateBlock(t *testing.T) {
	base := Task{IssueID: "issue-1"}
	plain := BuildPrompt(base, "claude")
	if strings.Contains(plain, "[Plan first]") {
		t.Fatalf("ungated issue rendered a plan block:\n%s", plain)
	}
	for state, want := range map[string]string{
		"needs_plan":        "no plan is proposed yet",
		"awaiting_approval": "waiting for a person's approval",
		"changes_requested": "sent your plan back",
	} {
		task := base
		task.PlanGate = state
		out := BuildPrompt(task, "claude")
		if !strings.HasPrefix(out, plain) {
			t.Fatalf("%s: the plan block must be appended:\n%s", state, out)
		}
		for _, s := range []string{"[Plan first]", want, "multica issue plan propose", "same session"} {
			if !strings.Contains(out, s) {
				t.Fatalf("%s: prompt is missing %q:\n%s", state, s, out)
			}
		}
	}
	if out := BuildPrompt(Task{IssueID: "issue-1", PlanGate: "something_new"}, "claude"); strings.Contains(out, "[Plan first]") {
		t.Fatalf("an unknown state rendered a block:\n%s", out)
	}
}
