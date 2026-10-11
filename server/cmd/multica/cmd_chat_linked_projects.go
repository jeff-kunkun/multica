package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

func init() { chatCmd.AddCommand(newChatLinkedProjectsCommand()) }

func newChatLinkedProjectsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "linked-projects [session-id]",
		Short: "List available or attached read-only reference projects, or replace a chat's selection",
		Long: `Read a chat's saved linked_projects, including unavailable entries. With --available,
list projects you can attach. With --reference LINK_ID:PROJECT_ID, replace the entire
selection; repeat the flag to keep multiple projects. Omit a pair to detach it, or
use --clear to detach all. Defaults to MULTICA_CHAT_SESSION_ID when no ID is given.

These projects are read-only references, not working projects or code sources.
Disconnecting or unsharing stops injection in the next run, not an ongoing run.
JSON entries include link_id, project_id, title, source_name, available and
unavailable_reason. The server does not disclose which access check failed.
Use --available to discover real IDs before changing a selection.`,
		Example: `  multica chat linked-projects --available --output json
  multica chat linked-projects --output json
  multica chat linked-projects "$MULTICA_CHAT_SESSION_ID" --reference "$LINK_ID:$PROJECT_ID"
  multica chat linked-projects "$MULTICA_CHAT_SESSION_ID" --clear`,
		Args: cobra.MaximumNArgs(1),
		RunE: runChatLinkedProjects,
	}
	cmd.Flags().Bool("available", false, "List source projects currently available to attach (no chat required)")
	cmd.Flags().StringSlice("reference", nil, "Replace selection with LINK_ID:PROJECT_ID pairs (repeatable)")
	cmd.Flags().Bool("clear", false, "Detach all reference projects")
	cmd.Flags().String("output", "json", "Output format: json or table")
	return cmd
}

// Validate syntax only; reachability, ownership and limits belong to the server.
func parseChatReferences(values []string) ([]map[string]string, error) {
	refs := make([]map[string]string, 0, len(values))
	for _, value := range values {
		link, project, ok := strings.Cut(value, ":")
		linkID, le := uuid.Parse(link)
		projectID, pe := uuid.Parse(project)
		if !ok || le != nil || pe != nil {
			return nil, fmt.Errorf("invalid reference %q: use LINK_ID:PROJECT_ID from multica chat linked-projects --available", value)
		}
		refs = append(refs, map[string]string{"link_id": linkID.String(), "project_id": projectID.String()})
	}
	return refs, nil
}

func chatReferenceRows(rows []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		available, _ := row["available"].(bool)
		reason := ""
		if !available {
			reason = "Reference is no longer accessible: the link may be disconnected, the project unshared, or access removed."
		}
		out = append(out, map[string]any{
			"link_id": row["link_id"], "project_id": row["project_id"], "title": row["title"],
			"source_name": row["source_name"], "available": available, "unavailable_reason": reason,
		})
	}
	return out
}

func runChatLinkedProjects(cmd *cobra.Command, args []string) error {
	output, _ := cmd.Flags().GetString("output")
	if output != "json" && output != "table" {
		return fmt.Errorf("invalid output %q: use json or table", output)
	}
	available, _ := cmd.Flags().GetBool("available")
	clear, _ := cmd.Flags().GetBool("clear")
	replacing := cmd.Flags().Changed("reference")
	if (clear && replacing) || (available && (clear || replacing || len(args) > 0)) {
		return fmt.Errorf("use --available alone, or a chat with exactly one of --reference or --clear")
	}
	values, _ := cmd.Flags().GetStringSlice("reference")
	refs, err := parseChatReferences(values)
	if err != nil {
		return err
	}
	sessionID := ""
	if !available {
		session := os.Getenv("MULTICA_CHAT_SESSION_ID")
		if len(args) > 0 {
			session = args[0]
		}
		ref, err := parseChatSessionLinkRef(session)
		if err != nil {
			return fmt.Errorf("pass a chat session ID from multica chat list: %w", err)
		}
		if ref.Slug != "" {
			return fmt.Errorf("use a chat ID in the current workspace; cross-workspace chat links cannot edit reference projects")
		}
		sessionID = ref.ID
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	var rows []map[string]any
	if available {
		var response struct {
			Projects []map[string]any `json:"projects"`
		}
		err = client.GetJSON(ctx, "/api/workspace-links/projects", &response)
		for _, row := range response.Projects {
			source, _ := row["source"].(map[string]any)
			row["project_id"], row["source_name"], row["available"] = row["id"], source["name"], true
		}
		rows = response.Projects
	} else {
		var response struct {
			LinkedProjects []map[string]any `json:"linked_projects"`
		}
		path := "/api/chat/sessions/" + url.PathEscape(sessionID)
		if replacing || clear {
			err = client.PatchJSON(ctx, path, map[string]any{"linked_projects": refs}, &response)
		} else {
			err = client.GetJSON(ctx, path, &response)
		}
		rows = response.LinkedProjects
	}
	if err != nil {
		return fmt.Errorf("chat reference projects: %w; check chat access with multica chat list and references with multica chat linked-projects --available", err)
	}
	rows = chatReferenceRows(rows)
	if output == "table" {
		for _, row := range rows {
			fmt.Fprintf(cmd.OutOrStdout(), "%s | %s | %s:%s | available=%v | %s\n", row["title"], row["source_name"], row["link_id"], row["project_id"], row["available"], row["unavailable_reason"])
		}
		if len(rows) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No reference projects.")
		}
		return nil
	}
	return cli.PrintJSON(cmd.OutOrStdout(), map[string]any{"session_id": sessionID, "available_only": available, "linked_projects": rows})
}
