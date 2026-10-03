package main

import (
	"fmt"
	"os"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

// workspaceRoutingCmd edits the analysis role's transport and the 接着做
// switch without requiring a browser. It deliberately writes the same
// workspace.settings.routing block consumed by the web settings page.
var workspaceRoutingCmd = &cobra.Command{
	Use:   "routing",
	Short: "Configure the routing analysis model and the continuation rule",
}

var workspaceRoutingGetCmd = &cobra.Command{Use: "get", Short: "Show analysis routing settings and the continuation switch", Args: cobra.NoArgs, RunE: runWorkspaceRoutingGet}
var workspaceRoutingSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Set analysis source, runtime, model, thinking level, or the continuation switch",
	Long: `Set analysis source, runtime, model, thinking level, or the continuation switch.

--continuation on|off is 接着做: a ticket continuing a previous stage, its
parent, or its batch goes back to that work's executor when the seat is strong
enough, online, not out of quota, and in the right direction. Off (the default)
is shadow mode: routing keeps its own pick and only says in the assignment
comment who the rule would have picked.`,
	Args: cobra.NoArgs,
	RunE: runWorkspaceRoutingSet,
}

func init() {
	workspaceRoutingGetCmd.Flags().String("output", "json", "Output format: json")
	workspaceRoutingSetCmd.Flags().String("source", "", "Analysis source: api_gateway or runtime_subscription")
	workspaceRoutingSetCmd.Flags().String("runtime", "", "Runtime ID for runtime_subscription")
	workspaceRoutingSetCmd.Flags().String("model", "", "Analysis model ID")
	workspaceRoutingSetCmd.Flags().String("thinking", "", "Thinking level: low, medium, high")
	workspaceRoutingSetCmd.Flags().String("continuation", "", "接着做 switch: on (prefer the previous executor) or off (shadow mode)")
	workspaceRoutingCmd.AddCommand(workspaceRoutingGetCmd, workspaceRoutingSetCmd)
	workspaceCmd.AddCommand(workspaceRoutingCmd)
}

func runWorkspaceRoutingGet(cmd *cobra.Command, _ []string) error {
	client, wsID, err := routingProjectsSession(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	settings, err := loadWorkspaceSettings(ctx, client, wsID)
	if err != nil {
		return err
	}
	block, _ := settings["routing"].(map[string]any)
	return cli.PrintJSON(os.Stdout, routingView(block))
}

// routingView is what get and set print: the analysis transport, and the
// 接着做 switch with the mode it puts routing in.
func routingView(block map[string]any) map[string]any {
	analysis, _ := block["analysis"].(map[string]any)
	continuation, _ := block["prefer_continuation"].(bool)
	mode := "shadow"
	if continuation {
		mode = "on"
	}
	return map[string]any{
		"source": analysis["source"], "runtime_id": analysis["runtime_id"], "model": analysis["model"], "thinking_level": analysis["thinking_level"],
		"prefer_continuation": continuation, "continuation_mode": mode,
	}
}

func runWorkspaceRoutingSet(cmd *cobra.Command, _ []string) error {
	source, _ := cmd.Flags().GetString("source")
	runtimeID, _ := cmd.Flags().GetString("runtime")
	model, _ := cmd.Flags().GetString("model")
	thinking, _ := cmd.Flags().GetString("thinking")
	continuation, _ := cmd.Flags().GetString("continuation")
	if continuation != "" && continuation != "on" && continuation != "off" {
		return fmt.Errorf("--continuation must be on or off")
	}
	if source != "" && source != "api_gateway" && source != "runtime_subscription" {
		return fmt.Errorf("--source must be api_gateway or runtime_subscription")
	}
	if thinking != "" && thinking != "low" && thinking != "medium" && thinking != "high" {
		return fmt.Errorf("--thinking must be low, medium or high")
	}
	client, wsID, err := routingProjectsSession(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	settings, err := loadWorkspaceSettings(ctx, client, wsID)
	if err != nil {
		return err
	}
	block, _ := settings["routing"].(map[string]any)
	if block == nil {
		block = map[string]any{}
	}
	if continuation != "" {
		block["prefer_continuation"] = continuation == "on"
	}
	analysis, _ := block["analysis"].(map[string]any)
	if analysis == nil {
		analysis = map[string]any{}
	}
	if source != "" {
		analysis["source"] = source
	}
	if runtimeID != "" {
		analysis["runtime_id"] = runtimeID
	}
	if model != "" {
		analysis["model"] = model
	}
	if thinking != "" {
		analysis["thinking_level"] = thinking
	}
	if len(analysis) > 0 {
		block["analysis"] = analysis
	}
	settings["routing"] = block
	var out map[string]any
	if err := client.PatchJSON(ctx, "/api/workspaces/"+wsID, map[string]any{"settings": settings}, &out); err != nil {
		return err
	}
	return cli.PrintJSON(os.Stdout, routingView(block))
}
