package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

func init() {
	projectCmd.AddCommand(newReferenceProjectsCommand("project"))
	autopilotCmd.AddCommand(newReferenceProjectsCommand("autopilot"))
}

// Both owners use the same reference settings endpoint contract. No --linked
// flag: these settings belong to the current workspace, not to a managed source.
func newReferenceProjectsCommand(kind string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "linked-projects <id>",
		Short: "Read or replace the reference projects included in run briefs",
		Long: `Read reference settings, or replace the entire selection with --reference.
Each reference is LINK_ID:PROJECT_ID; discover IDs with multica workspace link list.
--clear selects no reference projects. References supply context only, never code sources.
JSON returns linked_projects (saved selection), effective_linked_projects (resolved
selection with title, source_name and available), and inherited (project defaults used).
Unavailable references remain visible but are omitted from the next run brief.`,
		Example: fmt.Sprintf("  multica %s linked-projects 11111111-1111-4111-8111-111111111111 --output json\n  multica %s linked-projects 11111111-1111-4111-8111-111111111111 --reference 22222222-2222-4222-8222-222222222222:33333333-3333-4333-8333-333333333333\n  multica %s linked-projects 11111111-1111-4111-8111-111111111111 --clear", kind, kind, kind),
		Args:    exactArgs(1),
	}
	cmd.Flags().StringSlice("reference", nil, "Replace selection with LINK_ID:PROJECT_ID pairs (repeatable)")
	cmd.Flags().Bool("clear", false, "Save an empty selection; do not inherit")
	cmd.Flags().String("output", "json", "Output format: json")
	if kind == "autopilot" {
		cmd.Flags().Bool("inherit", false, "Remove the override and inherit the owning project's references")
		cmd.Example += "\n  multica autopilot linked-projects 11111111-1111-4111-8111-111111111111 --inherit"
		cmd.Long += "\nFor autopilots, --inherit stores linked_projects: null; [] explicitly disables inheritance."
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		output, _ := cmd.Flags().GetString("output")
		if output != "json" {
			return fmt.Errorf("invalid output %q: use --output json", output)
		}
		refs, _ := cmd.Flags().GetStringSlice("reference")
		clear, _ := cmd.Flags().GetBool("clear")
		inherit := false
		if kind == "autopilot" {
			inherit, _ = cmd.Flags().GetBool("inherit")
		}
		selected := 0
		if cmd.Flags().Changed("reference") {
			selected++
		}
		if clear {
			selected++
		}
		if inherit {
			selected++
		}
		if selected > 1 {
			return fmt.Errorf("choose exactly one of --reference, --clear or --inherit")
		}
		var links []map[string]string
		if !inherit {
			links = make([]map[string]string, 0, len(refs))
		}
		for _, ref := range refs {
			link, project, ok := strings.Cut(ref, ":")
			if !ok {
				return fmt.Errorf("invalid reference %q: use LINK_ID:PROJECT_ID from multica workspace link list", ref)
			}
			if _, err := uuid.Parse(link); err != nil {
				return fmt.Errorf("invalid link ID %q: use a workspace-link UUID", link)
			}
			if _, err := uuid.Parse(project); err != nil {
				return fmt.Errorf("invalid project ID %q: use the source project's UUID", project)
			}
			links = append(links, map[string]string{"link_id": link, "project_id": project})
		}
		client, err := newAPIClient(cmd)
		if err != nil {
			return err
		}
		ctx, cancel := cli.APIContext(cmd.Context())
		defer cancel()
		id := args[0]
		if kind == "project" {
			ref, err := resolveProjectID(ctx, client, id)
			if err != nil {
				return err
			}
			id = ref.ID
		} else {
			ref, err := resolveAutopilotID(ctx, client, id)
			if err != nil {
				return err
			}
			id = ref.ID
		}
		path := "/api/" + kind + "s/" + url.PathEscape(id) + "/linked-projects"
		var response map[string]any
		if selected == 0 {
			err = client.GetJSON(ctx, path, &response)
		} else {
			err = client.PutJSON(ctx, path, map[string]any{"linked_projects": links}, &response)
		}
		if err != nil {
			return fmt.Errorf("%s reference projects: %w; check access and available projects with multica workspace link list", kind, err)
		}
		return cli.PrintJSON(cmd.OutOrStdout(), response)
	}
	return cmd
}
