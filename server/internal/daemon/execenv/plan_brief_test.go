package execenv

import (
	"strings"
	"testing"
)

// Plan approval is opt-in per workspace: the `plan propose` line appears only
// when the claim says the workspace uses it, and the rest of the Commands
// section stays byte-identical either way.
func TestAvailableCommandsPlanProposeGated(t *testing.T) {
	ctx := TaskContextForEnv{IssueID: "issue-1"}
	var off strings.Builder
	writeAvailableCommands(&off, ctx)
	if strings.Contains(off.String(), "plan propose") {
		t.Fatalf("brief without plan flow must not mention plan propose\n---\n%s", off.String())
	}

	ctx.PlanFlow = true
	var on strings.Builder
	writeAvailableCommands(&on, ctx)
	line := "- `plan propose <issue>` — propose a plan when the issue asks for one, naming its project (`--project <id>`) or `--general`; nothing starts until it is approved\n"
	if !strings.Contains(on.String(), line) {
		t.Fatalf("brief with plan flow is missing the plan propose line\n---\n%s", on.String())
	}
	if got := strings.Replace(on.String(), line, "", 1); got != off.String() {
		t.Fatalf("plan flow changed more than its own line\n--- off\n%s\n--- on\n%s", off.String(), on.String())
	}
}
