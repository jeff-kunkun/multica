package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Export task run logs",
}

var logsExportCmd = &cobra.Command{
	Use:   "export <task-id>",
	Short: "Export a run's logs as a redacted bundle",
	Long: `Export the logs of an agent run as one zip bundle: logs.jsonl (structured
entries), meta.json (task id, agent, time window, exit code) and summary.md,
a brief written to be pasted straight into an AI conversation.

The bundle is built by the server — the same one the Web and Desktop
"Export logs" dialog downloads — and is redacted by default: tokens, passwords
and environment variable values are removed.

<task-id> is the run id shown by ` + "`multica issue runs <issue-id> --full-id`" + `; a
short prefix works together with --issue.`,
	Example: `  # This run only, written to the current directory
  $ multica logs export 01a0b932-de08-70fd-82c3-8bdef7f81fd6

  # Every run on the issue from the last 12 hours
  $ multica logs export 01a0b932 --issue DENE-599 --scope hours --hours 12

  # The whole task, reported to its issue with the assignee mentioned
  $ multica logs export 01a0b932-de08-70fd-82c3-8bdef7f81fd6 --scope task --report

  # Print only the AI summary
  $ multica logs export 01a0b932-de08-70fd-82c3-8bdef7f81fd6 --summary`,
	Args: exactArgs(1),
	RunE: runLogsExport,
}

func init() {
	logsCmd.AddCommand(logsExportCmd)

	logsExportCmd.Flags().String("scope", "run", "What to export: run (this run), hours (last N hours of the issue), task (every run on the issue)")
	logsExportCmd.Flags().Int("hours", 6, "Window size for --scope hours")
	logsExportCmd.Flags().String("issue", "", "Issue id or identifier; lets <task-id> be a short prefix")
	logsExportCmd.Flags().StringP("output-dir", "o", "", "Directory to write the bundle to (default: current directory)")
	logsExportCmd.Flags().Bool("summary", false, "Print the AI summary to stdout instead of writing the bundle")
	logsExportCmd.Flags().Bool("report", false, "Report the bundle to the run's issue as a comment that mentions the assignee")
	logsExportCmd.Flags().Bool("allow-partial", false, "Export what could be collected when some runs cannot be read")
	logsExportCmd.Flags().String("output", "json", "Output format: json")
}

func runLogsExport(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cli.AtLeastAPITimeout(120*time.Second))
	defer cancel()

	scope, _ := cmd.Flags().GetString("scope")
	hours, _ := cmd.Flags().GetInt("hours")
	allowPartial, _ := cmd.Flags().GetBool("allow-partial")
	switch scope {
	case "run", "hours", "task":
	default:
		return fmt.Errorf("invalid --scope %q (want run, hours or task)", scope)
	}

	issueRef, _ := cmd.Flags().GetString("issue")
	issueID := ""
	if issueRef != "" {
		resolved, err := resolveIssueRef(ctx, client, issueRef)
		if err != nil {
			return err
		}
		issueID = resolved.ID
	}
	run, err := resolveTaskRunID(ctx, client, issueID, args[0])
	if err != nil {
		return err
	}
	base := "/api/tasks/" + url.PathEscape(run.ID) + "/log-export"

	if report, _ := cmd.Flags().GetBool("report"); report {
		var out map[string]any
		body := map[string]any{"scope": scope, "hours": hours, "allow_partial": allowPartial}
		if err := client.PostJSON(ctx, base+"/report", body, &out); err != nil {
			return fmt.Errorf("report logs: %w", err)
		}
		where := "comment attachment"
		if strVal(out, "delivery") == "git" {
			where = strVal(out, "link")
		}
		fmt.Fprintf(os.Stderr, "Reported to %s (%s)\n", strVal(out, "issue_identifier"), where)
		if reason := strVal(out, "fallback_reason"); reason != "" {
			fmt.Fprintln(os.Stderr, "Log repository push failed, attached instead:", reason)
		}
		return cli.PrintJSON(os.Stdout, out)
	}

	query := url.Values{"scope": {scope}}
	if scope == "hours" {
		query.Set("hours", strconv.Itoa(hours))
	}
	if allowPartial {
		query.Set("allow_partial", "true")
	}

	var preview map[string]any
	if err := client.GetJSON(ctx, base+"?"+query.Encode(), &preview); err != nil {
		return fmt.Errorf("export logs: %w", err)
	}
	if empty, _ := preview["empty"].(bool); empty {
		return fmt.Errorf("no log entries in this scope; widen it with --scope hours --hours <N> or --scope task")
	}
	if summaryOnly, _ := cmd.Flags().GetBool("summary"); summaryOnly {
		fmt.Fprint(os.Stdout, strVal(preview, "summary"))
		return nil
	}

	query.Set("format", "zip")
	data, err := client.DownloadFile(ctx, base+"?"+query.Encode())
	if err != nil {
		return fmt.Errorf("download bundle: %w", err)
	}
	filename := filepath.Base(strVal(preview, "filename"))
	if filename == "" || filename == "." {
		filename = "multica-logs-" + run.ID + ".zip"
	}
	outputDir, _ := cmd.Flags().GetString("output-dir")
	if outputDir != "" {
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			return fmt.Errorf("create output directory: %w", err)
		}
	}
	destPath := filepath.Join(outputDir, filename)
	if err := os.WriteFile(destPath, data, 0o644); err != nil {
		return fmt.Errorf("write bundle: %w", err)
	}
	abs, err := filepath.Abs(destPath)
	if err != nil {
		abs = destPath
	}
	fmt.Fprintln(os.Stderr, "Exported:", abs)

	meta, _ := preview["meta"].(map[string]any)
	return cli.PrintJSON(os.Stdout, map[string]any{
		"path":        abs,
		"filename":    filename,
		"size_bytes":  len(data),
		"scope":       scope,
		"run_count":   meta["run_count"],
		"entry_count": meta["entry_count"],
		"partial":     meta["partial"],
	})
}
