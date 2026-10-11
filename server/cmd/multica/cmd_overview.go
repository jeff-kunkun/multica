package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

var overviewCmd = newOverviewCommand()

func newOverviewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "overview",
		Short: "See the workspace map in one request",
		Long: `Read agents, projects, active chats, automations, skills, squads, runtimes,
linked workspaces and external connections from one permission-filtered snapshot.
Restricted sections are listed in omitted_sections. Filters accept an ID or name
and are resolved by the server; combine them to narrow the workspace map.
Linked workspaces include shared projects and edit abilities; owners can read
change history with multica workspace link audit --output json.`,
		Example: `  multica overview
  multica overview --output json
  multica overview --project "My project" --compact
  multica overview --agent "My agent" --output json`,
		Args: cobra.NoArgs,
		RunE: runOverview,
	}
	cmd.Flags().String("project", "", "Only include records related to this project ID or name")
	cmd.Flags().String("agent", "", "Only include records related to this agent ID or name")
	cmd.Flags().Bool("compact", false, "Return names, IDs and key fields only; print one line per item")
	cmd.Flags().String("output", "table", "Output format: table, json")
	return cmd
}

func runOverview(cmd *cobra.Command, _ []string) error {
	output, _ := cmd.Flags().GetString("output")
	if output != "table" && output != "json" {
		return fmt.Errorf("invalid output format %q: expected table or json", output)
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	query := url.Values{}
	if client.WorkspaceID != "" {
		query.Set("workspace_id", client.WorkspaceID)
	}
	for _, key := range []string{"project", "agent"} {
		value, _ := cmd.Flags().GetString(key)
		if value != "" {
			query.Set(key, value)
		}
	}
	compact, _ := cmd.Flags().GetBool("compact")
	if compact {
		query.Set("compact", "true")
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	var snapshot map[string]any
	if err := client.GetJSON(ctx, "/api/overview?"+query.Encode(), &snapshot); err != nil {
		return fmt.Errorf("workspace overview: %w", err)
	}
	if output == "json" {
		return cli.PrintJSON(cmd.OutOrStdout(), snapshot)
	}
	return printOverview(cmd.OutOrStdout(), snapshot, compact)
}

func printOverview(w io.Writer, snapshot map[string]any, compact bool) error {
	fmt.Fprintf(w, "Workspace: %v\n", snapshot["workspace_id"])
	sections := []string{"agents", "projects", "chats", "automations", "skills", "squads", "runtimes", "workspace_links", "external_connections"}
	for _, section := range sections {
		rows := objectList(snapshot[section])
		fmt.Fprintf(w, "\n%s (%d)\n", strings.ReplaceAll(section, "_", " "), len(rows))
		for _, row := range rows {
			keys := make([]string, 0, len(row))
			for key := range row {
				if key != "id" && key != "name" {
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			for _, key := range []string{"id", "name"} {
				if _, ok := row[key]; ok {
					keys = append([]string{key}, keys...)
				}
			}
			fields := make([]string, 0, len(keys))
			for _, key := range keys {
				value := row[key]
				if value == nil {
					continue
				}
				var rendered string
				if s, ok := value.(string); ok {
					// Keep records on their own line even when descriptions contain newlines.
					rendered = strings.Join(strings.Fields(s), " ")
				} else {
					raw, err := json.Marshal(value)
					if err != nil {
						return err
					}
					rendered = string(raw)
				}
				fields = append(fields, key+"="+rendered)
			}
			separator := " | "
			if !compact {
				separator = "\n    "
			}
			fmt.Fprintf(w, "  %s\n", strings.Join(fields, separator))
		}
	}
	if omitted, ok := snapshot["omitted_sections"].([]any); ok && len(omitted) > 0 {
		names := make([]string, 0, len(omitted))
		for _, section := range omitted {
			names = append(names, fmt.Sprint(section))
		}
		fmt.Fprintf(w, "\nOmitted by permissions: %s\n", strings.Join(names, ", "))
	}
	return nil
}
