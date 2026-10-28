package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

// Plan first (DENE-2670): an issue created with --plan-first waits for an
// approved plan before its agent may move it to review or done or link a PR.

func init() { issueCmd.AddCommand(newIssuePlanCommand()) }

func newIssuePlanCommand() *cobra.Command {
	plan := &cobra.Command{
		Use:   "plan",
		Short: "Propose, read and answer the plan on a plan-first issue",
		Long: "An issue created with --plan-first asks its agent for a plan before the work.\n" +
			"Until a person approves one, the server refuses the agent's moves to\n" +
			"in_review or done and its PR links. Approval and change requests are\n" +
			"replies under the plan card, so the agent resumes the same session.",
	}

	show := &cobra.Command{
		Use:     "show <issue>",
		Short:   "Show whether the issue waits for a plan and every plan version",
		Example: "  multica issue plan show DENE-123 --output json",
		Args:    exactArgs(1),
		RunE:    runIssuePlanShow,
	}
	show.Flags().String("output", "table", "Output format: table or json")

	propose := &cobra.Command{
		Use:   "propose <issue>",
		Short: "Propose the next plan version and post its card on the issue",
		Long: "Writes a new plan version, replaces the earlier one still waiting, and posts\n" +
			"the plan card. Then end your turn: the reviewer's answer under the card wakes\n" +
			"you in the same session. Refused once a plan is approved.",
		Example: "  multica issue plan propose DENE-123 --goal \"Add plan cards\" --out-of-scope \"No mobile\" --step \"Server\" --step \"UI\" --done-when \"Tests pass\"",
		Args:    exactArgs(1),
		RunE:    runIssuePlanPropose,
	}
	propose.Flags().String("goal", "", "What the work will do (required)")
	propose.Flags().String("out-of-scope", "", "What the work will not do (required)")
	propose.Flags().StringArray("step", nil, "One step; repeat 1 to 5 times (required)")
	propose.Flags().String("done-when", "", "How a reviewer can tell it is done (required)")
	propose.Flags().String("output", "table", "Output format: table or json")

	approve := &cobra.Command{
		Use:   "approve <issue>",
		Short: "Approve a plan version and wake the agent to do it",
		Long: "People only: the issue's creator, a workspace owner or admin, or a plan\n" +
			"approver. Approving an approved version changes nothing.",
		Example:     "  multica issue plan approve DENE-123 --version 2",
		Args:        exactArgs(1),
		Annotations: map[string]string{annotationBotKey: "no"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIssuePlanAnswer(cmd, args, "approve")
		},
	}
	approve.Flags().Int("version", 0, "Plan version to approve (default: the newest)")
	approve.Flags().String("output", "table", "Output format: table or json")

	requestChanges := &cobra.Command{
		Use:         "request-changes <issue>",
		Short:       "Send a plan back with what to change, and wake the agent to revise it",
		Long:        "People only, same as approve. The feedback is posted under the plan card.",
		Example:     "  multica issue plan request-changes DENE-123 --feedback \"Drop step 3\"",
		Args:        exactArgs(1),
		Annotations: map[string]string{annotationBotKey: "no"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIssuePlanAnswer(cmd, args, "request-changes")
		},
	}
	requestChanges.Flags().Int("version", 0, "Plan version to send back (default: the newest)")
	requestChanges.Flags().String("feedback", "", "What to change (required)")
	requestChanges.Flags().String("output", "table", "Output format: table or json")

	plan.AddCommand(show, propose, approve, requestChanges)
	return plan
}

type cliIssuePlans struct {
	Gated      bool             `json:"gated"`
	Approved   bool             `json:"approved"`
	CanApprove bool             `json:"can_approve"`
	Plans      []map[string]any `json:"plans"`
}

func issuePlanClient(cmd *cobra.Command, input string) (*cli.APIClient, context.Context, context.CancelFunc, string, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, nil, nil, "", err
	}
	ctx, cancel := cli.APIContext(context.Background())
	ref, err := resolveIssueRef(ctx, client, input)
	if err != nil {
		cancel()
		return nil, nil, nil, "", fmt.Errorf("resolve issue: %w", err)
	}
	return client, ctx, cancel, "/api/issues/" + url.PathEscape(ref.ID) + "/plans", nil
}

func runIssuePlanShow(cmd *cobra.Command, args []string) error {
	client, ctx, cancel, path, err := issuePlanClient(cmd, args[0])
	if err != nil {
		return err
	}
	defer cancel()
	var out cliIssuePlans
	if err := client.GetJSON(ctx, path, &out); err != nil {
		return fmt.Errorf("show plans: %w", err)
	}
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		return cli.PrintJSON(os.Stdout, out)
	}
	switch {
	case !out.Gated:
		fmt.Println("This issue does not ask for a plan first.")
	case out.Approved:
		fmt.Println("Plan approved: the work can go ahead.")
	default:
		fmt.Println("Waiting for an approved plan: no review, done or PR link before then.")
	}
	for _, p := range out.Plans {
		fmt.Printf("v%v  %v  %v\n", p["version"], p["status"], p["goal"])
	}
	return nil
}

func runIssuePlanPropose(cmd *cobra.Command, args []string) error {
	goal, _ := cmd.Flags().GetString("goal")
	outOfScope, _ := cmd.Flags().GetString("out-of-scope")
	doneWhen, _ := cmd.Flags().GetString("done-when")
	stepTitles, _ := cmd.Flags().GetStringArray("step")
	switch {
	case strings.TrimSpace(goal) == "":
		return fmt.Errorf("--goal is required")
	case strings.TrimSpace(outOfScope) == "":
		return fmt.Errorf("--out-of-scope is required")
	case strings.TrimSpace(doneWhen) == "":
		return fmt.Errorf("--done-when is required")
	case len(stepTitles) == 0 || len(stepTitles) > 5:
		return fmt.Errorf("give 1 to 5 --step flags")
	}
	steps := make([]map[string]string, len(stepTitles))
	for i, title := range stepTitles {
		steps[i] = map[string]string{"title": title}
	}
	client, ctx, cancel, path, err := issuePlanClient(cmd, args[0])
	if err != nil {
		return err
	}
	defer cancel()
	var out map[string]any
	body := map[string]any{"goal": goal, "out_of_scope": outOfScope, "steps": steps, "done_when": doneWhen}
	if err := client.PostJSON(ctx, path, body, &out); err != nil {
		return fmt.Errorf("propose plan: %w", err)
	}
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		return cli.PrintJSON(os.Stdout, out)
	}
	fmt.Printf("Plan v%v proposed. End your turn; the answer under the plan card resumes this session.\n", out["version"])
	return nil
}

func runIssuePlanAnswer(cmd *cobra.Command, args []string, action string) error {
	feedback := ""
	if action == "request-changes" {
		feedback, _ = cmd.Flags().GetString("feedback")
		if strings.TrimSpace(feedback) == "" {
			return fmt.Errorf("--feedback is required")
		}
	}
	client, ctx, cancel, path, err := issuePlanClient(cmd, args[0])
	if err != nil {
		return err
	}
	defer cancel()
	version, _ := cmd.Flags().GetInt("version")
	if version <= 0 {
		var plans cliIssuePlans
		if err := client.GetJSON(ctx, path, &plans); err != nil {
			return fmt.Errorf("find newest plan: %w", err)
		}
		if len(plans.Plans) == 0 {
			return fmt.Errorf("this issue has no plan yet")
		}
		v, ok := plans.Plans[0]["version"].(float64)
		if !ok {
			return fmt.Errorf("unreadable plan version")
		}
		version = int(v)
	}
	body := map[string]any{}
	if feedback != "" {
		body["feedback"] = feedback
	}
	var out map[string]any
	if err := client.PostJSON(ctx, path+"/"+strconv.Itoa(version)+"/"+action, body, &out); err != nil {
		return fmt.Errorf("%s plan: %w", action, err)
	}
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		return cli.PrintJSON(os.Stdout, out)
	}
	if action == "approve" {
		fmt.Printf("Plan v%d approved; the agent continues in the same session.\n", version)
	} else {
		fmt.Printf("Plan v%d sent back; the agent will propose a revised version.\n", version)
	}
	if wake, _ := out["wake"].(map[string]any); wake["none_woken"] == true {
		fmt.Fprintln(os.Stderr, "Warning: no agent was woken by this answer. Run the same command again to retry the wake.")
	}
	return nil
}
