package main

import "testing"

func TestStageAdvanceCommandRegistered(t *testing.T) {
	if cmd, _, err := issueCmd.Find([]string{"stage", "advance"}); err != nil || cmd != issueStageAdvanceCmd {
		t.Fatalf("issue stage advance not registered: %v", err)
	}
}
