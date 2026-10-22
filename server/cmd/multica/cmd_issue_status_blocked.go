package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// blockedWaitFlags are the flags that tell the platform what a blocked issue
// waits on. Without one the block patrol can only guess (DENE-1255).
var blockedWaitFlags = []string{"blocked-by", "wake-at", "wait-condition", "needs-human"}

// warnUnregisteredBlock is printed, not enforced: a member may park a ticket
// without saying why, but the patrol then cannot tell when to wake it.
func warnUnregisteredBlock(cmd *cobra.Command, w io.Writer, display string) {
	for _, flag := range blockedWaitFlags {
		if v, _ := cmd.Flags().GetString(flag); strings.TrimSpace(v) != "" {
			return
		}
	}
	if display == "" {
		display = "<id>"
	}
	fmt.Fprintf(w, "Warning: blocked without saying what it waits on — the platform cannot tell when to wake it.\n"+
		"  Record the wait in the same call: multica issue status %s blocked --blocked-by <DENE-N>\n"+
		"  (or --wake-at / --wait-condition / --needs-human).\n", display)
}

// warnEmptyExecutor is printed when a ticket is blocked with nobody in the
// executor slot: when the wait ends there is nobody to wake.
func warnEmptyExecutor(w io.Writer, display string) {
	fmt.Fprintf(w, "Executor is empty. When the wait ends nobody can be woken.\n"+
		"  Pick one: multica issue assign %s --to <agent-name> --no-start\n", display)
}
