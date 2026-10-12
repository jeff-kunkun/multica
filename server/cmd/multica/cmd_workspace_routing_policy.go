package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/spf13/cobra"
)

// workspaceRoutingPolicyCmd is the 分工规则 (DENE-1834 part 4): who does what
// kind of work, written in plain language, read into rows by the routing
// model, confirmed before it takes effect. The three subcommands call the
// same endpoints the settings page uses.
var workspaceRoutingPolicyCmd = &cobra.Command{
	Use:   "policy",
	Short: "Show, preview or apply the division of labour (分工规则)",
	Long: `The division of labour decides which model family does which kind of work and
caps the tier for some kinds (default: 前端、审美交给 Claude，Codex 主要负责审查；
文档、沉淀类最多中档). Routing reads the ticket's kind of work from the analysis
model and applies the matching row; a row whose series has nobody online falls
back across series and the assignment comment says so.

  show      the rows in force, each with the sentence it came from
  preview   read plain text into rows with the routing model; saves nothing
  apply     confirm rows and make them take effect (bumps the version, keeps a
            change log with who and when); --reset goes back to the default

Only a person can apply; an agent run can show and preview.`,
}

var workspaceRoutingPolicyShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show the division of labour in force",
	Args:  cobra.NoArgs,
	RunE:  runWorkspaceRoutingPolicyShow,
}

var workspaceRoutingPolicyPreviewCmd = &cobra.Command{
	Use:   "preview",
	Short: "Read plain-language rules into rows without saving",
	Long: `Send plain text to the routing model and print the rows it read, each labelled
with the sentence it came from. Nothing is saved. Pipe --output json into a file,
edit it if needed, then: multica workspace routing policy apply --file <file>`,
	Args: cobra.NoArgs,
	RunE: runWorkspaceRoutingPolicyPreview,
}

var workspaceRoutingPolicyApplyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Confirm rows and make them take effect",
	Long: `Apply the rows from a preview file (--file, the JSON that preview --output json
printed, or just its "policy" object). A row you edited by hand should have its
"origin" cleared so the change log calls it 手改. --reset discards the custom
rules and goes back to the default; the change stays in the log.`,
	Args: cobra.NoArgs,
	RunE: runWorkspaceRoutingPolicyApply,
}

func init() {
	workspaceRoutingPolicyShowCmd.Flags().String("output", "table", "Output format: table or json")
	workspaceRoutingPolicyPreviewCmd.Flags().String("output", "table", "Output format: table or json")
	workspaceRoutingPolicyPreviewCmd.Flags().String("text", "", "The rules in plain language")
	workspaceRoutingPolicyPreviewCmd.Flags().String("text-file", "", "Read the rules text from a file (- for stdin)")
	workspaceRoutingPolicyApplyCmd.Flags().String("output", "table", "Output format: table or json")
	workspaceRoutingPolicyApplyCmd.Flags().String("file", "", "Rows to confirm: a preview's JSON (- for stdin)")
	workspaceRoutingPolicyApplyCmd.Flags().Bool("reset", false, "Go back to the default rules")
	workspaceRoutingPolicyCmd.AddCommand(workspaceRoutingPolicyShowCmd, workspaceRoutingPolicyPreviewCmd, workspaceRoutingPolicyApplyCmd)
	workspaceRoutingCmd.AddCommand(workspaceRoutingPolicyCmd)
}

// routingPolicyView mirrors the server's routingDispatchResponse.
type routingPolicyView struct {
	Policy    routing.DispatchPolicy `json:"policy"`
	IsDefault bool                   `json:"is_default"`
	Series    []string               `json:"series"`
	Tiers     []routing.TierOption   `json:"tiers"`
	Editable  bool                   `json:"editable"`
}

func runWorkspaceRoutingPolicyShow(cmd *cobra.Command, _ []string) error {
	client, wsID, err := routingProjectsSession(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	var view routingPolicyView
	if err := client.GetJSON(ctx, "/api/workspaces/"+wsID+"/routing/dispatch", &view); err != nil {
		return fmt.Errorf("read routing policy: %w", err)
	}
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		return cli.PrintJSON(os.Stdout, view)
	}
	printRoutingPolicy(view, os.Stdout)
	return nil
}

func runWorkspaceRoutingPolicyPreview(cmd *cobra.Command, _ []string) error {
	text, _ := cmd.Flags().GetString("text")
	if file, _ := cmd.Flags().GetString("text-file"); file != "" {
		raw, err := readFileOrStdin(file)
		if err != nil {
			return err
		}
		text = string(raw)
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("--text or --text-file is required")
	}
	client, wsID, err := routingProjectsSession(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	var preview routing.DispatchPreview
	if err := client.PostJSON(ctx, "/api/workspaces/"+wsID+"/routing/dispatch/preview", map[string]any{"text": text}, &preview); err != nil {
		return fmt.Errorf("preview routing policy: %w", err)
	}
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		return cli.PrintJSON(os.Stdout, preview)
	}
	fmt.Println("Preview (nothing saved):")
	printRoutingPolicyRules(preview.Policy, nil, os.Stdout)
	for _, d := range preview.Dropped {
		fmt.Printf("  dropped: %s\n", d)
	}
	fmt.Println("To make it take effect: multica workspace routing policy preview ... --output json > rules.json && multica workspace routing policy apply --file rules.json")
	return nil
}

func runWorkspaceRoutingPolicyApply(cmd *cobra.Command, _ []string) error {
	reset, _ := cmd.Flags().GetBool("reset")
	file, _ := cmd.Flags().GetString("file")
	body := map[string]any{}
	switch {
	case reset && file != "":
		return fmt.Errorf("--reset and --file are exclusive")
	case reset:
		body["reset"] = true
	case file != "":
		raw, err := readFileOrStdin(file)
		if err != nil {
			return err
		}
		policy, err := routingPolicyFromJSON(raw)
		if err != nil {
			return err
		}
		body["text"], body["rules"] = policy.Text, policy.Rules
	default:
		return fmt.Errorf("--file <preview json> or --reset is required")
	}
	client, wsID, err := routingProjectsSession(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	var view routingPolicyView
	if err := client.PutJSON(ctx, "/api/workspaces/"+wsID+"/routing/dispatch", body, &view); err != nil {
		return fmt.Errorf("apply routing policy: %w", err)
	}
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		return cli.PrintJSON(os.Stdout, view)
	}
	fmt.Printf("Applied: version %d\n", view.Policy.Version)
	printRoutingPolicy(view, os.Stdout)
	return nil
}

// routingPolicyFromJSON accepts a preview response, a show response or a bare
// policy object, so the file a person edited can be any of the three.
func routingPolicyFromJSON(raw []byte) (routing.DispatchPolicy, error) {
	var wrapped struct {
		Policy *routing.DispatchPolicy `json:"policy"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return routing.DispatchPolicy{}, fmt.Errorf("the file is not JSON: %w", err)
	}
	if wrapped.Policy != nil {
		return *wrapped.Policy, nil
	}
	var policy routing.DispatchPolicy
	if err := json.Unmarshal(raw, &policy); err != nil {
		return routing.DispatchPolicy{}, fmt.Errorf("the file is not JSON: %w", err)
	}
	if len(policy.Rules) == 0 {
		return routing.DispatchPolicy{}, fmt.Errorf("the file has no rules; it should be what `policy preview --output json` printed")
	}
	return policy, nil
}

func readFileOrStdin(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return raw, nil
}

func printRoutingPolicy(view routingPolicyView, w *os.File) {
	source := "custom"
	if view.IsDefault {
		source = "default"
	}
	fmt.Fprintf(w, "Policy (%s, version %d):\n", source, view.Policy.Version)
	if strings.TrimSpace(view.Policy.Text) != "" {
		fmt.Fprintf(w, "  %s\n", view.Policy.Text)
	}
	tiers := map[string]string{}
	for _, t := range view.Tiers {
		tiers[t.Key] = t.Label
	}
	printRoutingPolicyRules(view.Policy, tiers, w)
	if len(view.Series) > 0 {
		fmt.Fprintf(w, "Series with a seat: %s\n", strings.Join(view.Series, ", "))
	}
	if len(view.Policy.History) > 0 {
		fmt.Fprintln(w, "History (newest last):")
		for _, h := range view.Policy.History {
			line := fmt.Sprintf("  v%d %s %s", h.Version, h.At, h.By)
			if h.RulesDiff != "" {
				line += "  " + h.RulesDiff
			}
			fmt.Fprintln(w, line)
		}
	}
	if !view.Editable {
		fmt.Fprintln(w, "This session cannot apply changes (agent run); a person confirms the rules.")
	}
}

func printRoutingPolicyRules(p routing.DispatchPolicy, tiers map[string]string, w *os.File) {
	fmt.Fprintln(w, "Rules:")
	for i, r := range p.Rules {
		what := make([]string, 0, 2)
		if r.Series != "" {
			what = append(what, "→ "+r.Series+" 系列")
		}
		if r.MaxTier != "" {
			label := r.MaxTier
			if tiers != nil && tiers[r.MaxTier] != "" {
				label = tiers[r.MaxTier]
			}
			what = append(what, "最多"+label+"档")
		}
		origin := r.Origin
		if origin == "" {
			origin = "manual"
		}
		fmt.Fprintf(w, "  %d. %s %s  [%s] 来源：「%s」\n", i+1, r.Category, strings.Join(what, "，"), origin, r.Source)
	}
}
