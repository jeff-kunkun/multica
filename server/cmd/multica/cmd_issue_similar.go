package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

var issueSimilarCmd = &cobra.Command{
	Use:   "similar",
	Short: "List same-project similar issues before creating work",
	Long: `Read-only duplicate preflight using the server's create-time rules.
Requires access to the project. Lists visible active issues, issues closed in
the last 2 hours, and issues completed in the last 72 hours. Planned siblings
and the chosen parent are excluded. No issue, comment or agent run is created.

JSON returns candidates (id, identifier, title, status, score 0..1, why,
recent_done), would_block and threshold; an empty result has candidates=[].
The create endpoint repeats the check under a transaction lock.

If work already exists, continue in its comments. For genuinely different work,
add a standalone description line "查重放行：和 DENE-123 不同在……", naming
each candidate and explaining the difference (at least 8 characters).
--allow-duplicate does not skip the same-project similarity guard.
Read real project IDs from "multica project list --output json".`,
	Example:     `  multica issue similar --project "<project-id-or-exact-title>" --title "修复登录超时" --output json`,
	Annotations: map[string]string{annotationAccess: "read"},
	RunE:        runIssueSimilar,
}

func init() {
	issueCmd.AddCommand(issueSimilarCmd)
	issueSimilarCmd.Flags().String("project", "", "Project UUID, ID prefix or exact title (required)")
	issueSimilarCmd.Flags().String("title", "", "Proposed issue title (required)")
	issueSimilarCmd.Flags().String("description", "", "Proposed description")
	issueSimilarCmd.Flags().String("description-file", "", "Read proposed description from this file")
	issueSimilarCmd.Flags().Bool("allow-external-file", false, "Allow a description file outside the task directory")
	issueSimilarCmd.Flags().String("parent", "", "Parent issue key or UUID, to exclude planned decomposition")
	issueSimilarCmd.Flags().String("output", "table", "Output format: table or json")
}

func runIssueSimilar(cmd *cobra.Command, _ []string) error {
	project, _ := cmd.Flags().GetString("project")
	title, _ := cmd.Flags().GetString("title")
	if strings.TrimSpace(project) == "" || strings.TrimSpace(title) == "" {
		return fmt.Errorf("--project and --title are required")
	}
	description, _, err := resolveTextFlag(cmd, "description")
	if err != nil {
		return err
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	projectID, err := resolveProjectID(ctx, client, project)
	if err != nil {
		return fmt.Errorf("resolve project: %w", err)
	}
	params := url.Values{"project_id": {projectID.ID}, "title": {title}, "description": {description}}
	if ref, _ := cmd.Flags().GetString("parent"); ref != "" {
		parent, err := resolveIssueRef(ctx, client, ref)
		if err != nil {
			return fmt.Errorf("resolve parent: %w", err)
		}
		params.Set("parent_issue_id", parent.ID)
	}
	var result map[string]any
	if err := client.GetJSON(ctx, "/api/issues/similar?"+params.Encode(), &result); err != nil {
		return fmt.Errorf("check similar issues: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(cmd.OutOrStdout(), result)
	}
	rows := [][]string{}
	candidates, _ := result["candidates"].([]any)
	for _, raw := range candidates {
		c, ok := raw.(map[string]any)
		if ok {
			rows = append(rows, []string{strVal(c, "identifier"), strVal(c, "title"), strVal(c, "status"), fmt.Sprint(c["score"]), strVal(c, "why")})
		}
	}
	cli.PrintTable(cmd.OutOrStdout(), []string{"KEY", "TITLE", "STATUS", "SCORE", "WHY"}, rows)
	return nil
}
