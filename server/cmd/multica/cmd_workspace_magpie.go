package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var workspaceMagpieCmd = &cobra.Command{
	Use:   "magpie [workspace-id|slug|prefix]",
	Short: "Show or set the workspace's Magpie switch",
	Long: "Shows whether the workspace is connected to Magpie. On: daemons' " +
		"Magpie routes are kept, the agents page shows each agent's Magpie " +
		"groups and runs are pinned to them. Off (the default): the workspace " +
		"behaves as official Multica and stored routes are dropped.\n\n" +
		"Pass --set on|off to change it. Only a workspace owner can change it, " +
		"and never from inside an agent run.",
	Example: "  multica workspace magpie\n" +
		"  multica workspace magpie kunkunya --set on\n" +
		"  multica workspace magpie --output json",
	Args: cobra.MaximumNArgs(1),
	RunE: runWorkspaceMagpie,
}

func init() {
	workspaceCmd.AddCommand(workspaceMagpieCmd)
	workspaceMagpieCmd.Flags().String("set", "", "Turn the switch on or off: on|off")
	workspaceMagpieCmd.Flags().String("output", "table", "Output format: table or json")
}

func runWorkspaceMagpie(cmd *cobra.Command, args []string) error {
	wsID, err := resolveWorkspaceArg(cmd, args)
	if err != nil {
		return err
	}
	if wsID == "" {
		return fmt.Errorf("workspace ID is required: pass an id/slug/prefix as argument or set MULTICA_WORKSPACE_ID")
	}

	var body map[string]any
	if cmd.Flags().Changed("set") {
		value, _ := cmd.Flags().GetString("set")
		switch value {
		case "on":
			body = map[string]any{"enabled": true}
		case "off":
			body = map[string]any{"enabled": false}
		default:
			return fmt.Errorf("--set must be on or off")
		}
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var state struct {
		Enabled bool `json:"enabled"`
	}
	path := "/api/workspaces/" + wsID + "/magpie"
	if body != nil {
		if err := client.PutJSON(ctx, path, body, &state); err != nil {
			return fmt.Errorf("set magpie switch: %w", err)
		}
	} else if err := client.GetJSON(ctx, path, &state); err != nil {
		return fmt.Errorf("get magpie switch: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, map[string]any{"workspace_id": wsID, "enabled": state.Enabled})
	}
	value := "off"
	if state.Enabled {
		value = "on"
	}
	cli.PrintTable(os.Stdout, []string{"WORKSPACE", "MAGPIE"}, [][]string{{wsID, value}})
	return nil
}
