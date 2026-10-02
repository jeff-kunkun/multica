package handler

import (
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestAllChildrenTerminalRequiresChildrenAndEveryChildTerminal(t *testing.T) {
	terminal := func(issue db.Issue) bool { return issue.Status == "done" || issue.Status == "cancelled" }
	if allChildrenTerminal(nil, terminal) {
		t.Fatal("empty child set must not auto-close a parent")
	}
	children := []db.Issue{{Status: "done"}, {Status: "in_progress"}}
	if allChildrenTerminal(children, terminal) {
		t.Fatal("open child must keep parent open")
	}
	children[1].Status = "cancelled"
	if !allChildrenTerminal(children, terminal) {
		t.Fatal("all terminal children should close the barrier")
	}
}

func TestStallStringAndBoolReadOnlyMetadata(t *testing.T) {
	meta := map[string]any{stallActionKey: "announced", stallCandidateKey: true}
	if stallString(meta, stallActionKey) != stallActionAnnounced {
		t.Fatal("action metadata was not read")
	}
	if !stallBool(meta, stallCandidateKey) {
		t.Fatal("candidate metadata was not read")
	}
}

func TestParentAutoCompletionEligibilityRequiresQuietSafeParent(t *testing.T) {
	base := func() bool { return parentAutoCompletionEligible("in_progress", false, false, false, 2, true) }
	if !base() {
		t.Fatal("a quiet parent with terminal children should be eligible")
	}
	for name, got := range map[string]bool{
		"paused":      parentAutoCompletionEligible("in_progress", true, false, false, 2, true),
		"active run":  parentAutoCompletionEligible("in_progress", false, true, false, 2, true),
		"linked PR":   parentAutoCompletionEligible("in_progress", false, false, true, 2, true),
		"open child":  parentAutoCompletionEligible("in_progress", false, false, false, 2, false),
		"no children": parentAutoCompletionEligible("in_progress", false, false, false, 0, true),
		"backlog":     parentAutoCompletionEligible("backlog", false, false, false, 2, true),
	} {
		if got {
			t.Fatalf("%s parent must not be auto-completed", name)
		}
	}
}
