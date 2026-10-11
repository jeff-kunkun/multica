package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

const selectedWorkspaceAnnotation = "multica.selected-workspace"

// A per-invocation annotation feeds all existing workspace consumers without
// mutating environment variables or the saved profile. Daemon commands and
// transfer's existing local --workspace flag retain their own semantics.
func prepareWorkspaceSelection(cmd *cobra.Command, _ []string) error {
	if cmd.Annotations != nil {
		delete(cmd.Annotations, selectedWorkspaceAnnotation)
	}
	for c := cmd; c != nil; c = c.Parent() {
		if c.Name() == "daemon" || c.Name() == "transfer" || (c.Name() == "connection" && cmd.Name() == "add") {
			return nil
		}
	}
	flag := cmd.Flags().Lookup("workspace")
	// Some commands already own a local --workspace (e.g. changes/runs use
	// a multi-workspace scope). Do not reinterpret their flag or label that
	// wider scope as a single workspace.
	if inherited := cmd.Root().PersistentFlags().Lookup("workspace"); inherited != nil && flag != inherited {
		return nil
	}
	if flag != nil && flag.Value.Type() != "string" {
		return nil
	}
	if flag != nil && flag.Changed {
		if linked, _ := cmd.Flags().GetString(linkedFlag); linked != "" {
			return fmt.Errorf("--workspace and --linked are exclusive")
		}
		input := strings.TrimSpace(flag.Value.String())
		if input == "" {
			return fmt.Errorf("--workspace requires a name or UUID")
		}
		ws := workspaceSummary{ID: input}
		if !uuidRegexp.MatchString(input) {
			ctx, cancel := cli.APIContext(context.Background())
			defer cancel()
			var err error
			ws, err = resolveWorkspaceRef(ctx, cmd, input)
			if err != nil {
				return err
			}
		}
		if cmd.Annotations == nil {
			cmd.Annotations = map[string]string{}
		}
		cmd.Annotations[selectedWorkspaceAnnotation] = ws.ID
		if env := strings.TrimSpace(os.Getenv("MULTICA_WORKSPACE_ID")); env != "" && !strings.EqualFold(env, ws.ID) {
			fmt.Fprintf(cmd.ErrOrStderr(), "--workspace overrides MULTICA_WORKSPACE_ID (%s).\n", env)
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Workspace: %s %s\n", ws.ID, ws.Name)
		return nil
	}
	if isWorkspaceRead(cmd) {
		if linked, _ := cmd.Flags().GetString(linkedFlag); linked != "" {
			return nil
		}
		if id := resolveWorkspaceID(cmd); id != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "Workspace: %s\n", id)
		}
	}
	return nil
}

func isWorkspaceRead(cmd *cobra.Command) bool {
	switch cmd.Name() {
	case "list", "get", "search", "history", "thread", "context", "overview", "children", "runs", "timeline", "usage", "board", "report", "view", "capabilities":
		return true
	}
	return false
}
