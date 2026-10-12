package main

import (
	"fmt"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/spf13/cobra"
	"os"
)

var workspaceRoutingShadowCmd = &cobra.Command{
	Use: "shadow", Short: "Compare reasoning against recent tickets without assigning or starting runs",
	Long: "Use the current analysis model, roster and routing prompt on recent ticket text. Writes no issue state. Each ticket makes one model call. Default: last 30 days, at most 20 tickets. Use --limit 1000 for a larger review; --offset resumes later pages. Review results before setting --decision-mode active.",
	Args: cobra.NoArgs, RunE: runWorkspaceRoutingShadow,
}

func init() {
	workspaceRoutingShadowCmd.Flags().Int("days", 30, "Look back 1–90 days")
	workspaceRoutingShadowCmd.Flags().Int("limit", 20, "Maximum model calls (1–1000)")
	workspaceRoutingShadowCmd.Flags().Int("offset", 0, "Start at this recent-ticket offset")
	workspaceRoutingShadowCmd.Flags().String("output", "json", "Output format: json")
	workspaceRoutingCmd.AddCommand(workspaceRoutingShadowCmd)
}
func runWorkspaceRoutingShadow(cmd *cobra.Command, _ []string) error {
	days, _ := cmd.Flags().GetInt("days")
	limit, _ := cmd.Flags().GetInt("limit")
	offset, _ := cmd.Flags().GetInt("offset")
	if days < 1 || days > 90 || limit < 1 || limit > 1000 || offset < 0 {
		return fmt.Errorf("days must be 1–90, limit 1–1000 and offset nonnegative")
	}
	client, wsID, err := routingProjectsSession(cmd)
	if err != nil {
		return err
	}
	results := []routing.ShadowComparison{}
	next := offset
	for len(results) < limit && next >= 0 {
		var page struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
			Next int `json:"next_offset"`
		}
		ctx, cancel := cli.APIContext(cmd.Context())
		err = client.GetJSON(ctx, fmt.Sprintf("/api/workspaces/%s/routing/shadow?days=%d&offset=%d", wsID, days, next), &page)
		cancel()
		if err != nil {
			return err
		}
		start := next
		next = page.Next
		for i, item := range page.Items {
			var result routing.ShadowComparison
			ctx, cancel := cli.APIContext(cmd.Context())
			err = client.PostJSON(ctx, "/api/workspaces/"+wsID+"/routing/shadow/"+item.ID, map[string]any{}, &result)
			cancel()
			if err != nil {
				result = routing.ShadowComparison{IssueID: item.ID, Error: err.Error()}
			}
			results = append(results, result)
			if len(results) == limit {
				if i+1 < len(page.Items) {
					next = start + i + 1
				}
				break
			}
		}
	}
	return cli.PrintJSON(os.Stdout, map[string]any{"days": days, "results": results, "next_offset": next, "complete": next < 0, "mutated": false})
}
