package main

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "Propose and approve issue plans",
}

var issueStageCmd = &cobra.Command{
	Use:   "stage",
	Short: "Work with the stages of a parent issue",
}

var issueStageAdvanceCmd = &cobra.Command{
	Use:   "advance <parent>",
	Short: "Promote the next stage once every stage below it is terminal",
	Long: "Checks the parent's staged sub-issues. When every stage below the next one\n" +
		"is terminal (done or cancelled), the next stage's backlog sub-issues move to\n" +
		"todo and their executors are woken. When the current stage still has open\n" +
		"work, nothing is written and the refusal names each ticket it waits on.",
	Args: exactArgs(1),
	RunE: runIssueStageAdvance,
}

func init() {
	rootCmd.AddCommand(planCmd)
	issueCmd.AddCommand(issueStageCmd)
	issueStageCmd.AddCommand(issueStageAdvanceCmd)
	issueStageAdvanceCmd.Flags().String("output", "table", "Output format: table or json")
}

func runIssueStageAdvance(cmd *cobra.Command, args []string) error {
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
	if err := client.PostJSON(ctx, "/api/issues/"+url.PathEscape(ref.ID)+"/stage-advance", map[string]any{}, &out); err != nil {
		return fmt.Errorf("advance stage: %w", err)
	}
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		return cli.PrintJSON(os.Stdout, out)
	}
	fmt.Println(out["message"])
	return nil
}
