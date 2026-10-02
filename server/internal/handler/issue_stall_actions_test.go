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
