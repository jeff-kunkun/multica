package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// ---------------------------------------------------------------------------
// Plan commands — propose a plan card on an issue, read it, approve it.
// Approval is the only thing that creates the step issues and starts work.
// ---------------------------------------------------------------------------

var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "Propose, read and approve issue plans",
}

var planProposeCmd = &cobra.Command{
	Use:   "propose <issue>",
	Short: "Post a new plan version on an issue (starts nothing)",
	Long: "Post a plan card: what to do, what not to do, 1-5 steps with one owner each, and when it is done.\n" +
		"Each call adds a new version and replaces any unapproved one. Nothing starts until an approver runs `multica plan approve`.\n\n" +
		"Example:\n  multica plan propose DENE-1 --goal \"...\" --out-of-scope \"...\" --done-when \"...\" \\\n" +
		"    --step \"孙悟空=Write the server API\" --step \"孙悟饭=Review the API\"",
	Args: exactArgs(1),
	RunE: runPlanPropose,
}

var planShowCmd = &cobra.Command{
	Use:   "show <issue>",
	Short: "Show the newest plan on an issue (or --version N)",
	Args:  exactArgs(1),
	RunE:  runPlanShow,
}

var planApproveCmd = &cobra.Command{
	Use:   "approve <issue>",
	Short: "Approve the newest plan: creates one child issue per step and starts them",
	Long: "Approve a plan version. Only the workspace owner or a granted approver can approve; agents never can.\n" +
		"Without --version the newest version is approved. Approving again finishes any step issue a failed attempt left out.",
	Args: exactArgs(1),
	RunE: runPlanApprove,
}

var planApproverCmd = &cobra.Command{
	Use:   "approver",
	Short: "Manage who besides owners can approve plans",
}

var planApproverListCmd = &cobra.Command{
	Use:   "list",
	Short: "List plan approvers (owners can always approve)",
	RunE:  runPlanApproverList,
}

var planApproverAddCmd = &cobra.Command{
	Use:   "add <member>",
	Short: "Let a member approve plans (owner only)",
	Args:  exactArgs(1),
	RunE:  runPlanApproverAdd,
}

var planApproverRemoveCmd = &cobra.Command{
	Use:   "remove <member>",
	Short: "Stop a member approving plans (owner only)",
	Args:  exactArgs(1),
	RunE:  runPlanApproverRemove,
}

func init() {
	planCmd.AddCommand(planProposeCmd)
	planCmd.AddCommand(planShowCmd)
	planCmd.AddCommand(planApproveCmd)
	planCmd.AddCommand(planApproverCmd)
	planApproverCmd.AddCommand(planApproverListCmd)
	planApproverCmd.AddCommand(planApproverAddCmd)
	planApproverCmd.AddCommand(planApproverRemoveCmd)

	for _, f := range []struct{ name, help string }{
		{"goal", "What the plan does (required)"},
		{"out-of-scope", "What the plan will not do (required)"},
		{"done-when", "How everyone can tell it is done (required)"},
	} {
		planProposeCmd.Flags().String(f.name, "", f.help)
		planProposeCmd.Flags().Bool(f.name+"-stdin", false, "Read --"+f.name+" from stdin")
		planProposeCmd.Flags().String(f.name+"-file", "", "Read --"+f.name+" from a UTF-8 file inside the working directory")
	}
	planProposeCmd.Flags().Bool("allow-external-file", false, "Allow --*-file paths outside the current working directory")
	planProposeCmd.Flags().StringArray("step", nil, "A step as \"<owner>=<what this step does>\"; owner is an agent or member name; repeat 1-5 times")
	planProposeCmd.Flags().String("output", "table", "Output format: table or json")

	planShowCmd.Flags().Int("version", 0, "Plan version (default: newest)")
	planShowCmd.Flags().Bool("all", false, "Show every version")
	planShowCmd.Flags().String("output", "table", "Output format: table or json")

	planApproveCmd.Flags().Int("version", 0, "Plan version (default: newest)")
	planApproveCmd.Flags().String("output", "table", "Output format: table or json")

	planApproverListCmd.Flags().String("output", "table", "Output format: table or json")
	planApproverAddCmd.Flags().String("output", "table", "Output format: table or json")
}

type planStepJSON struct {
	Title        string  `json:"title"`
	AssigneeType string  `json:"assignee_type"`
	AssigneeID   string  `json:"assignee_id"`
	IssueID      *string `json:"issue_id,omitempty"`
}

type planJSON struct {
	ID         string         `json:"id"`
	Version    int            `json:"version"`
	Status     string         `json:"status"`
	AuthorType string         `json:"author_type"`
	AuthorID   string         `json:"author_id"`
	Goal       string         `json:"goal"`
	OutOfScope string         `json:"out_of_scope"`
	Steps      []planStepJSON `json:"steps"`
	DoneWhen   string         `json:"done_when"`
	ApprovedBy *string        `json:"approved_by"`
	ApprovedAt *string        `json:"approved_at"`
	CreatedAt  string         `json:"created_at"`
}

// parsePlanStep splits "<owner>=<title>" on the first '='.
func parsePlanStep(raw string) (owner, title string, err error) {
	owner, title, found := strings.Cut(raw, "=")
	owner, title = strings.TrimSpace(owner), strings.TrimSpace(title)
	if !found || owner == "" || title == "" {
		return "", "", fmt.Errorf("--step %q must look like \"<owner>=<what this step does>\"", raw)
	}
	return owner, title, nil
}

func runPlanPropose(cmd *cobra.Command, args []string) error {
	fields := map[string]string{}
	for _, name := range []string{"goal", "out-of-scope", "done-when"} {
		value, set, err := resolveTextFlag(cmd, name)
		if err != nil {
			return err
		}
		if !set || strings.TrimSpace(value) == "" {
			return fmt.Errorf("--%s is required", name)
		}
		fields[name] = value
	}
	rawSteps, _ := cmd.Flags().GetStringArray("step")
	if len(rawSteps) == 0 || len(rawSteps) > 5 {
		return fmt.Errorf("give 1 to 5 --step flags")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}
	steps := make([]planStepJSON, 0, len(rawSteps))
	for _, raw := range rawSteps {
		owner, title, err := parsePlanStep(raw)
		if err != nil {
			return err
		}
		aType, aID, err := resolveAssignee(ctx, client, owner, memberOrAgentKinds)
		if err != nil {
			return fmt.Errorf("resolve step owner %q: %w", owner, err)
		}
		steps = append(steps, planStepJSON{Title: title, AssigneeType: aType, AssigneeID: aID})
	}

	var plan planJSON
	body := map[string]any{
		"goal":         fields["goal"],
		"out_of_scope": fields["out-of-scope"],
		"done_when":    fields["done-when"],
		"steps":        steps,
	}
	if err := client.PostJSON(ctx, "/api/issues/"+issueRef.ID+"/plans", body, &plan); err != nil {
		return fmt.Errorf("propose plan: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Plan v%d posted on %s. Nothing starts until an approver runs: multica plan approve %s --version %d\n",
		plan.Version, issueRef.Display, issueRef.Display, plan.Version)
	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, plan)
	}
	printPlan(ctx, client, plan)
	return nil
}

func fetchPlans(ctx context.Context, client *cli.APIClient, issueID string) ([]planJSON, bool, error) {
	var resp struct {
		Plans      []planJSON `json:"plans"`
		CanApprove bool       `json:"can_approve"`
	}
	if err := client.GetJSON(ctx, "/api/issues/"+issueID+"/plans", &resp); err != nil {
		return nil, false, err
	}
	return resp.Plans, resp.CanApprove, nil
}

func runPlanShow(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}
	plans, _, err := fetchPlans(ctx, client, issueRef.ID)
	if err != nil {
		return fmt.Errorf("list plans: %w", err)
	}
	if len(plans) == 0 {
		return fmt.Errorf("%s has no plan yet; propose one with `multica plan propose`", issueRef.Display)
	}
	all, _ := cmd.Flags().GetBool("all")
	version, _ := cmd.Flags().GetInt("version")
	selected := plans[:1]
	if all {
		selected = plans
	} else if version > 0 {
		selected = nil
		for _, p := range plans {
			if p.Version == version {
				selected = []planJSON{p}
			}
		}
		if selected == nil {
			return fmt.Errorf("%s has no plan v%d", issueRef.Display, version)
		}
	}
	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		if all {
			return cli.PrintJSON(os.Stdout, selected)
		}
		return cli.PrintJSON(os.Stdout, selected[0])
	}
	for i, p := range selected {
		if i > 0 {
			fmt.Println()
		}
		printPlan(ctx, client, p)
	}
	return nil
}

func runPlanApprove(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}
	version, _ := cmd.Flags().GetInt("version")
	if version <= 0 {
		plans, _, err := fetchPlans(ctx, client, issueRef.ID)
		if err != nil {
			return fmt.Errorf("list plans: %w", err)
		}
		if len(plans) == 0 {
			return fmt.Errorf("%s has no plan to approve", issueRef.Display)
		}
		version = plans[0].Version
	}
	var resp struct {
		Plan   planJSON         `json:"plan"`
		Issues []map[string]any `json:"issues"`
	}
	if err := client.PostJSON(ctx, "/api/issues/"+issueRef.ID+"/plans/"+strconv.Itoa(version)+"/approve", map[string]any{}, &resp); err != nil {
		return fmt.Errorf("approve plan: %w", err)
	}
	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, resp)
	}
	keys := make([]string, 0, len(resp.Issues))
	for _, issue := range resp.Issues {
		keys = append(keys, issueDisplayKey(issue))
	}
	if len(keys) == 0 {
		fmt.Printf("Plan v%d on %s is approved; every step already has its issue.\n", resp.Plan.Version, issueRef.Display)
		return nil
	}
	fmt.Printf("Plan v%d on %s approved. Started: %s\n", resp.Plan.Version, issueRef.Display, strings.Join(keys, ", "))
	return nil
}

func printPlan(ctx context.Context, client *cli.APIClient, p planJSON) {
	lookup := loadActorDisplayLookup(ctx, client)
	fmt.Printf("Plan v%d (%s) by %s\n", p.Version, p.Status, lookup.actor(p.AuthorType, p.AuthorID))
	fmt.Printf("\nDo:\n  %s\n", p.Goal)
	fmt.Printf("\nNot doing:\n  %s\n", p.OutOfScope)
	fmt.Println("\nSteps:")
	for i, s := range p.Steps {
		line := fmt.Sprintf("  %d. %s — %s", i+1, s.Title, lookup.actor(s.AssigneeType, s.AssigneeID))
		if s.IssueID != nil {
			line += " [issue " + *s.IssueID + "]"
		}
		fmt.Println(line)
	}
	fmt.Printf("\nDone when:\n  %s\n", p.DoneWhen)
}

type planApproverJSON struct {
	UserID    string `json:"user_id"`
	Name      string `json:"name"`
	GrantedBy string `json:"granted_by"`
	CreatedAt string `json:"created_at"`
}

func runPlanApproverList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var resp struct {
		Approvers []planApproverJSON `json:"approvers"`
	}
	if err := client.GetJSON(ctx, "/api/plan-approvers", &resp); err != nil {
		return fmt.Errorf("list approvers: %w", err)
	}
	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, resp.Approvers)
	}
	fmt.Println("Workspace owners can always approve plans.")
	if len(resp.Approvers) == 0 {
		fmt.Println("No other approvers.")
		return nil
	}
	for _, a := range resp.Approvers {
		fmt.Printf("  %s (%s)\n", a.Name, a.UserID)
	}
	return nil
}

func resolvePlanApproverMember(ctx context.Context, client *cli.APIClient, name string) (string, error) {
	aType, aID, err := resolveAssignee(ctx, client, name, assigneeKinds{member: true})
	if err != nil {
		return "", fmt.Errorf("resolve member %q: %w", name, err)
	}
	if aType != "member" {
		return "", fmt.Errorf("%q is not a member; only members can approve plans", name)
	}
	return aID, nil
}

func runPlanApproverAdd(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	userID, err := resolvePlanApproverMember(ctx, client, args[0])
	if err != nil {
		return err
	}
	var row planApproverJSON
	if err := client.PostJSON(ctx, "/api/plan-approvers", map[string]any{"user_id": userID}, &row); err != nil {
		return fmt.Errorf("add approver: %w", err)
	}
	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, row)
	}
	fmt.Printf("%s can now approve plans.\n", row.Name)
	return nil
}

func runPlanApproverRemove(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	userID, err := resolvePlanApproverMember(ctx, client, args[0])
	if err != nil {
		return err
	}
	if err := client.DeleteJSON(ctx, "/api/plan-approvers/"+url.PathEscape(userID)); err != nil {
		return fmt.Errorf("remove approver: %w", err)
	}
	fmt.Printf("%s can no longer approve plans.\n", args[0])
	return nil
}
