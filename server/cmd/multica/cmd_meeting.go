package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// ---------------------------------------------------------------------------
// Meeting commands — the meeting room: issues opened as meetings plus any
// issue carrying a plan, with plan status and step progress.
// ---------------------------------------------------------------------------

var meetingCmd = &cobra.Command{
	Use:   "meeting",
	Short: "List meetings (issues opened from the meeting room or carrying a plan)",
}

var meetingListCmd = &cobra.Command{
	Use:   "list",
	Short: "List meetings, newest activity first",
	Long: "List meetings: issues opened with `multica issue create --meeting` and any issue with a plan.\n" +
		"Shows the newest plan's status and version, and how many steps of the approved plan are done.",
	Args: exactArgs(0),
	RunE: runMeetingList,
}

func init() {
	meetingCmd.AddCommand(meetingListCmd)
	meetingListCmd.Flags().String("output", "table", "Output format: table or json")
}

type meetingJSON struct {
	Issue struct {
		ID         string `json:"id"`
		Identifier string `json:"identifier"`
		Title      string `json:"title"`
		Status     string `json:"status"`
	} `json:"issue"`
	Plan       *planJSON `json:"plan"`
	StepsTotal int       `json:"steps_total"`
	StepsDone  int       `json:"steps_done"`
}

func runMeetingList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var raw struct {
		Meetings json.RawMessage `json:"meetings"`
	}
	if err := client.GetJSON(ctx, "/api/meetings", &raw); err != nil {
		return fmt.Errorf("list meetings: %w", err)
	}
	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, raw.Meetings)
	}
	var resp struct {
		Meetings []meetingJSON
	}
	if err := json.Unmarshal(raw.Meetings, &resp.Meetings); err != nil {
		return fmt.Errorf("list meetings: %w", err)
	}
	if len(resp.Meetings) == 0 {
		fmt.Println("No meetings.")
		return nil
	}
	headers := []string{"ISSUE", "TITLE", "PLAN", "STEPS"}
	rows := make([][]string, 0, len(resp.Meetings))
	for _, m := range resp.Meetings {
		plan := "-"
		if m.Plan != nil {
			plan = fmt.Sprintf("v%d %s", m.Plan.Version, m.Plan.Status)
		}
		steps := "-"
		if m.StepsTotal > 0 {
			steps = fmt.Sprintf("%d/%d", m.StepsDone, m.StepsTotal)
		}
		rows = append(rows, []string{m.Issue.Identifier, m.Issue.Title, plan, steps})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}
