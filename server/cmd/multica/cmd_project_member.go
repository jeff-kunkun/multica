package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// Project sharing: the workspace owner sees every project; a member sees the
// projects shared with them, and their agents act within the same scope.

var projectMembersCmd = &cobra.Command{
	Use:   "members <project-id>",
	Short: "List the members a project is shared with",
	Args:  exactArgs(1),
	RunE:  runProjectMembers,
}

var projectShareCmd = &cobra.Command{
	Use:   "share <project-id> <member>",
	Short: "Share a project with a workspace member (owner only)",
	Long: "Shares a project with a workspace member, given by name, email, or user id. " +
		"The member, and agents they own, can then see and work on the project's issues.",
	Args: exactArgs(2),
	RunE: runProjectShare,
}

var projectUnshareCmd = &cobra.Command{
	Use:   "unshare <project-id> <member>",
	Short: "Stop sharing a project with a member (owner only)",
	Args:  exactArgs(2),
	RunE:  runProjectUnshare,
}

func init() {
	projectCmd.AddCommand(projectMembersCmd)
	projectCmd.AddCommand(projectShareCmd)
	projectCmd.AddCommand(projectUnshareCmd)

	projectMembersCmd.Flags().String("output", "table", "Output format: table or json")
}

func runProjectMembers(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	projectRef, err := resolveProjectID(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve project: %w", err)
	}
	var result struct {
		Members []map[string]any `json:"members"`
	}
	if err := client.GetJSON(ctx, "/api/projects/"+projectRef.ID+"/members", &result); err != nil {
		return fmt.Errorf("list project members: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result.Members)
	}

	names := map[string][2]string{}
	var members []map[string]any
	if err := client.GetJSON(ctx, "/api/workspaces/"+client.WorkspaceID+"/members", &members); err == nil {
		for _, m := range members {
			names[strVal(m, "user_id")] = [2]string{strVal(m, "name"), strVal(m, "email")}
		}
	}
	rows := make([][]string, 0, len(result.Members))
	for _, m := range result.Members {
		id := strVal(m, "user_id")
		rows = append(rows, []string{names[id][0], names[id][1], id, strVal(m, "created_at")})
	}
	cli.PrintTable(os.Stdout, []string{"NAME", "EMAIL", "USER ID", "SHARED AT"}, rows)
	return nil
}

func runProjectShare(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	projectRef, userID, err := resolveProjectAndMember(ctx, client, args[0], args[1])
	if err != nil {
		return err
	}
	if err := client.PostJSON(ctx, "/api/projects/"+projectRef.ID+"/members", map[string]any{"user_id": userID}, nil); err != nil {
		return fmt.Errorf("share project: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Project %s shared with %s.\n", projectRef.Display, args[1])
	return nil
}

func runProjectUnshare(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	projectRef, userID, err := resolveProjectAndMember(ctx, client, args[0], args[1])
	if err != nil {
		return err
	}
	if err := client.DeleteJSON(ctx, "/api/projects/"+projectRef.ID+"/members/"+userID); err != nil {
		return fmt.Errorf("unshare project: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Project %s no longer shared with %s.\n", projectRef.Display, args[1])
	return nil
}

func resolveProjectAndMember(ctx context.Context, client *cli.APIClient, project, member string) (resolvedID, string, error) {
	projectRef, err := resolveProjectID(ctx, client, project)
	if err != nil {
		return resolvedID{}, "", fmt.Errorf("resolve project: %w", err)
	}
	_, userID, err := resolveAssignee(ctx, client, member, memberOnlyKinds)
	if err != nil {
		return resolvedID{}, "", fmt.Errorf("resolve member: %w", err)
	}
	return projectRef, userID, nil
}
