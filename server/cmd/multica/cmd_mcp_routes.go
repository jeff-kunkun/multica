package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

func newMCPRoutesCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "routes", Short: "Choose your project notification Bots and group destinations", Long: `Manage your notification routes through the same server API as Settings.
Choose the project's responsible Bot; omit --project to configure your fallback
(for example Grok Bot). --channel-ref must identify the actual Multica group.
The server includes this desired relay group in the event envelope. Saving a
route does not change external Bot settings or send a message: the receiving
Bot performs the group delivery and reports its receipt.`}
	list := &cobra.Command{Use: "list", Short: "List your project routes and fallback", Args: cobra.NoArgs, Annotations: map[string]string{annotationAccess: "read"}, RunE: func(cmd *cobra.Command, _ []string) error {
		client, ws, ctx, cancel, err := botClient(cmd)
		if err != nil {
			return err
		}
		defer cancel()
		var out []map[string]any
		if err := client.GetJSON(ctx, mcpRoutesPath(ws), &out); err != nil {
			return err
		}
		return cli.PrintJSON(cmd.OutOrStdout(), out)
	}}
	set := &cobra.Command{Use: "set", Short: "Set a project route or your fallback", Args: cobra.NoArgs, Annotations: map[string]string{annotationAccess: "write"}, RunE: func(cmd *cobra.Command, _ []string) error {
		project, err := mcpRouteProject(cmd)
		if err != nil {
			return err
		}
		bot, _ := cmd.Flags().GetString("bot")
		channel, _ := cmd.Flags().GetString("channel-ref")
		bot = strings.TrimSpace(bot)
		channel = strings.TrimSpace(channel)
		if !uuidRegexp.MatchString(bot) || channel == "" {
			return fmt.Errorf("--bot (UUID) and --channel-ref (actual Multica group reference) are required")
		}
		client, ws, ctx, cancel, err := botClient(cmd)
		if err != nil {
			return err
		}
		defer cancel()
		body := map[string]any{"project_id": project, "bot_id": bot, "channel_ref": channel}
		var out map[string]any
		if err := client.PutJSON(ctx, mcpRoutesPath(ws), body, &out); err != nil {
			return err
		}
		return cli.PrintJSON(cmd.OutOrStdout(), out)
	}}
	set.Flags().String("project", "", "Project UUID; omit to set your fallback")
	set.Flags().String("bot", "", "Your responsible Bot UUID (required)")
	set.Flags().String("channel-ref", "", "Actual Multica group reference for the Bot's relay (required)")
	remove := &cobra.Command{Use: "remove", Short: "Remove a project route or your fallback", Args: cobra.NoArgs, Annotations: map[string]string{annotationAccess: "write"}, RunE: func(cmd *cobra.Command, _ []string) error {
		project, err := mcpRouteProject(cmd)
		if err != nil {
			return err
		}
		client, ws, ctx, cancel, err := botClient(cmd)
		if err != nil {
			return err
		}
		defer cancel()
		path := mcpRoutesPath(ws)
		if project != nil {
			path += "?" + url.Values{"project_id": []string{*project}}.Encode()
		}
		if err := client.DeleteJSON(ctx, path); err != nil {
			return err
		}
		return cli.PrintJSON(cmd.OutOrStdout(), map[string]any{"project_id": project, "removed": true})
	}}
	remove.Flags().String("project", "", "Project UUID; omit to remove your fallback")
	for _, c := range []*cobra.Command{list, set, remove} {
		c.Flags().String("output", "json", "Output format: json")
		cmd.AddCommand(c)
	}
	return cmd
}
func mcpRoutesPath(ws string) string {
	return "/api/workspaces/" + url.PathEscape(ws) + "/office-bot-routes"
}
func mcpRouteProject(cmd *cobra.Command) (*string, error) {
	project, _ := cmd.Flags().GetString("project")
	project = strings.TrimSpace(project)
	if !cmd.Flags().Changed("project") {
		return nil, nil
	}
	if !uuidRegexp.MatchString(project) {
		return nil, fmt.Errorf("--project requires a project UUID; omit the flag for your fallback")
	}
	return &project, nil
}
