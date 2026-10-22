package main

import (
	"fmt"
	"io"
)

// warnEmptyExecutor is printed when a ticket is blocked with nobody in the
// executor slot: when the wait ends there is nobody to wake.
func warnEmptyExecutor(w io.Writer, display string) {
	fmt.Fprintf(w, "Executor is empty. When the wait ends nobody can be woken.\n"+
		"  Pick one: multica issue assign %s --to <agent-name> --no-start\n", display)
}
