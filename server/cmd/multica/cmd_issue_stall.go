package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

var issueStallCmd = &cobra.Command{Use: "stall", Short: "Inspect and control automatic stall actions"}
var issueStallListCmd = &cobra.Command{Use: "list", Short: "List announced and processed stall actions", Args: cobra.NoArgs, RunE: runIssueStallList}
var issueStallKeepCmd = &cobra.Command{Use: "keep <id>", Short: "Keep a ticket during its 24-hour announcement", Args: cobra.ExactArgs(1), RunE: runIssueStallAction("keep")}
var issueStallUndoCmd = &cobra.Command{Use: "undo <id>", Short: "Undo an automatic close or cancellation within 7 days", Args: cobra.ExactArgs(1), RunE: runIssueStallAction("undo")}

func init() {
	issueStallListCmd.Flags().String("output", "table", "Output format: table or json")
	issueStallKeepCmd.Flags().String("output", "table", "Output format: table or json")
	issueStallUndoCmd.Flags().String("output", "table", "Output format: table or json")
	issueStallCmd.AddCommand(issueStallListCmd, issueStallKeepCmd, issueStallUndoCmd)
	issueCmd.AddCommand(issueStallCmd)
}

func runIssueStallList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var out map[string]any
	if err := client.GetJSON(ctx, "/api/issues/stall-actions", &out); err != nil {
		return fmt.Errorf("list stall actions: %w", err)
	}
	format, _ := cmd.Flags().GetString("output")
	if format == "json" {
		return cli.PrintJSON(os.Stdout, out)
	}
	items, _ := out["items"].([]any)
	if len(items) == 0 {
		fmt.Println("No stall actions.")
		return nil
	}
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		fmt.Printf("%-12s %-16s %s\n", item["action"], item["issue_id"], item["title"])
	}
	return nil
}

func runIssueStallAction(action string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		client, err := newAPIClient(cmd)
		if err != nil {
			return err
		}
		ctx, cancel := cli.APIContext(context.Background())
		defer cancel()
		ref, err := resolveIssueRef(ctx, client, args[0])
		if err != nil {
			return fmt.Errorf("resolve issue: %w", err)
		}
		var out map[string]any
		if err := client.PostJSON(ctx, "/api/issues/"+url.PathEscape(ref.ID)+"/stall/"+action, map[string]any{}, &out); err != nil {
			return fmt.Errorf("stall %s: %w", action, err)
		}
		format, _ := cmd.Flags().GetString("output")
		if format == "json" {
			return cli.PrintJSON(os.Stdout, out)
		}
		fmt.Printf("%s: %s\n", strings.Title(action), ref.Display)
		if status, ok := out["status"].(string); ok && status != "" {
			fmt.Printf("Status: %s\n", status)
		}
		return nil
	}
}
