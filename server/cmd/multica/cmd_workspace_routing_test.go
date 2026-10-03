package main

import (
	"github.com/spf13/cobra"
	"testing"
)

func newWorkspaceRoutingSetTestCmd() *cobra.Command {
	cmd := newRoutingProjectsTestCmd()
	for _, f := range []string{"source", "runtime", "model", "thinking", "continuation"} {
		cmd.Flags().String(f, "", "")
	}
	return cmd
}

func TestWorkspaceRoutingSetContinuationKeepsTheRest(t *testing.T) {
	var patched map[string]any
	routingProjectsServer(t, map[string]any{
		"routing": map[string]any{
			"enabled": true, "model": "jev-1",
			"analysis": map[string]any{"model": "a-1"},
		},
	}, &patched)

	cmd := newWorkspaceRoutingSetTestCmd()
	_ = cmd.Flags().Set("continuation", "on")
	if err := runWorkspaceRoutingSet(cmd, nil); err != nil {
		t.Fatalf("set: %v", err)
	}
	settings, _ := patched["settings"].(map[string]any)
	block, _ := settings["routing"].(map[string]any)
	if block["prefer_continuation"] != true {
		t.Fatalf("continuation not written: %v", block)
	}
	if block["enabled"] != true || block["model"] != "jev-1" {
		t.Fatalf("routing switch/model were not carried through: %v", block)
	}
	analysis, _ := block["analysis"].(map[string]any)
	if analysis["model"] != "a-1" {
		t.Fatalf("analysis block was not carried through: %v", block)
	}
}

func TestWorkspaceRoutingSetContinuationRejectsOtherValues(t *testing.T) {
	cmd := newWorkspaceRoutingSetTestCmd()
	_ = cmd.Flags().Set("continuation", "yes")
	if err := runWorkspaceRoutingSet(cmd, nil); err == nil {
		t.Fatal("want an error for --continuation yes")
	}
}

func TestRoutingViewReportsShadowByDefault(t *testing.T) {
	if got := routingView(nil); got["prefer_continuation"] != false || got["continuation_mode"] != "shadow" {
		t.Fatalf("default view = %v, want shadow", got)
	}
	if got := routingView(map[string]any{"prefer_continuation": true}); got["continuation_mode"] != "on" {
		t.Fatalf("on view = %v", got)
	}
}
