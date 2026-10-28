package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

// Live status (DENE-2667 PR 4): the latest view a daemon reported for each running
// issue run — the command in flight, the last output line and how long the run
// has been quiet. The server redacts every field before storing it.

const runsWatchMinInterval = time.Second

// taskLive is one entry of GET /api/task-live.
type taskLive struct {
	TaskID        string    `json:"task_id"`
	IssueID       string    `json:"issue_id"`
	AgentID       string    `json:"agent_id"`
	Tool          string    `json:"tool"`
	Command       string    `json:"command"`
	LastOutput    string    `json:"last_output"`
	ToolsInFlight int       `json:"tools_in_flight"`
	IdleSeconds   int64     `json:"idle_seconds"`
	ReportedAt    time.Time `json:"reported_at"`
}

// fetchTaskLive reads one workspace's live views, keeping each entry's raw
// JSON so --output json passes server fields through untouched.
func fetchTaskLive(cmd *cobra.Command, client *cli.APIClient, workspaceID string) ([]json.RawMessage, error) {
	c := *client
	c.WorkspaceID = workspaceID
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	var resp struct {
		Live []json.RawMessage `json:"live"`
	}
	if err := c.GetJSON(ctx, "/api/task-live", &resp); err != nil {
		var httpErr *cli.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			return nil, errNoLiveStatus
		}
		return nil, fmt.Errorf("live status: %w", err)
	}
	return resp.Live, nil
}

var errNoLiveStatus = errors.New("live status: this server does not report live status")

func decodeTaskLive(raw json.RawMessage) (taskLive, bool) {
	var live taskLive
	if json.Unmarshal(raw, &live) != nil || live.TaskID == "" {
		return taskLive{}, false
	}
	return live, true
}

// attachRunsLive adds a top-level "live" array to the runs envelope: the live
// views of the page's runs, whose task_id is the run's id. Each workspace on
// the page is read once. The envelope's own bytes are kept, and a failed read
// is a note on stderr, never a lost page.
func attachRunsLive(cmd *cobra.Command, client *cli.APIClient, raw json.RawMessage) (json.RawMessage, map[string]taskLive, error) {
	var page struct {
		ActiveRuns []struct {
			WorkspaceID string `json:"workspace_id"`
			ID          string `json:"id"`
		} `json:"active_runs"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, nil, err
	}
	onPage := map[string]bool{}
	var workspaces []string
	for _, run := range page.ActiveRuns {
		onPage[run.ID] = true
		if run.WorkspaceID != "" && !onPage["ws:"+run.WorkspaceID] {
			onPage["ws:"+run.WorkspaceID] = true
			workspaces = append(workspaces, run.WorkspaceID)
		}
	}
	entries := []json.RawMessage{}
	byTask := map[string]taskLive{}
	for _, ws := range workspaces {
		raws, err := fetchTaskLive(cmd, client, ws)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "workspace %s: %v\n", ws, err)
			if errors.Is(err, errNoLiveStatus) {
				break
			}
			continue
		}
		for _, entry := range raws {
			if live, ok := decodeTaskLive(entry); ok && onPage[live.TaskID] {
				entries = append(entries, entry)
				byTask[live.TaskID] = live
			}
		}
	}
	liveJSON, err := json.Marshal(entries)
	if err != nil {
		return nil, nil, err
	}
	body := bytes.TrimRight(raw, " \t\r\n")
	if len(body) < 2 || body[0] != '{' || body[len(body)-1] != '}' {
		return nil, nil, fmt.Errorf("invalid fast-access envelope: not a JSON object")
	}
	var out bytes.Buffer
	out.Write(body[:len(body)-1])
	if len(bytes.TrimSpace(body[1:len(body)-1])) > 0 {
		out.WriteByte(',')
	}
	out.WriteString(`"live":`)
	out.Write(liveJSON)
	out.WriteByte('}')
	return out.Bytes(), byTask, nil
}

// formatTaskLive renders a live view as one terminal line. Control characters
// are dropped so reported text cannot drive the terminal.
func formatTaskLive(live taskLive) string {
	parts := []string{fmt.Sprintf("quiet %s", time.Duration(live.IdleSeconds)*time.Second)}
	if live.Command != "" {
		parts = append(parts, "running: "+terminalSafe(live.Command))
	} else if live.ToolsInFlight == 0 {
		parts = append(parts, "no command running")
	}
	if live.LastOutput != "" {
		parts = append(parts, "last: "+terminalSafe(live.LastOutput))
	}
	return strings.Join(parts, " | ")
}

func terminalSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func newRunsWatchCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Follow the live status of the workspace's running issue runs",
		Args:  cobra.NoArgs,
		Long: `Poll the current workspace's live status: for each running issue run, the
command in flight, the last output line and how long the run has been quiet, as
its daemon last reported them (every 15 seconds, redacted before they leave the
machine). Only runs on issues you can see are listed. A daemon reports live
status only when started with MULTICA_LIVE_STATUS=on.

Table output prints one block per poll. --output json prints one JSON object
per poll and line: {"as_of": ..., "workspace_id": ..., "live": [...]}, with the
server's entries unchanged. Stops after --count polls, or on Ctrl-C.`,
		Example:     "  multica runs watch\n  multica runs watch --interval 5s --count 1 --output json",
		Annotations: map[string]string{annotationAccess: "read"},
		RunE:        runRunsWatch,
	}
	cmd.Flags().Duration("interval", 15*time.Second, "Time between polls (at least 1s)")
	cmd.Flags().Int("count", 0, "Stop after this many polls (0 = until interrupted)")
	cmd.Flags().String("output", "table", "Output format: table or json (one object per poll)")
	return cmd
}

func runRunsWatch(cmd *cobra.Command, _ []string) error {
	output, _ := cmd.Flags().GetString("output")
	if output != "json" && output != "table" {
		return fmt.Errorf("invalid output format %q: expected table or json", output)
	}
	interval, _ := cmd.Flags().GetDuration("interval")
	if interval < runsWatchMinInterval {
		return fmt.Errorf("--interval must be at least %s", runsWatchMinInterval)
	}
	count, _ := cmd.Flags().GetInt("count")
	if count < 0 {
		return fmt.Errorf("--count cannot be negative")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if client.WorkspaceID == "" {
		return fmt.Errorf("choose a workspace with --workspace or MULTICA_WORKSPACE_ID")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for poll := 1; ; poll++ {
		raws, err := fetchTaskLive(cmd, client, client.WorkspaceID)
		var httpErr *cli.HTTPError
		switch {
		case errors.Is(err, errNoLiveStatus):
			return err
		case errors.As(err, &httpErr) && httpErr.StatusCode < 500 && httpErr.StatusCode != http.StatusTooManyRequests:
			return err
		case err != nil:
			fmt.Fprintf(cmd.ErrOrStderr(), "%v; trying again in %s\n", err, interval)
		default:
			if err := printRunsWatch(cmd.OutOrStdout(), output, client.WorkspaceID, raws); err != nil {
				return err
			}
		}
		if count > 0 && poll >= count {
			return nil
		}
		select {
		case <-cmd.Context().Done():
			return nil
		case <-ticker.C:
		}
	}
}

func printRunsWatch(w io.Writer, output, workspaceID string, raws []json.RawMessage) error {
	asOf := time.Now().UTC().Format(time.RFC3339)
	if raws == nil {
		raws = []json.RawMessage{}
	}
	if output == "json" {
		line, err := json.Marshal(struct {
			AsOf        string            `json:"as_of"`
			WorkspaceID string            `json:"workspace_id"`
			Live        []json.RawMessage `json:"live"`
		}{asOf, workspaceID, raws})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(line))
		return err
	}
	fmt.Fprintf(w, "%s  %d running issue run(s) with live status\n", asOf, len(raws))
	for _, raw := range raws {
		live, ok := decodeTaskLive(raw)
		if !ok {
			continue
		}
		fmt.Fprintf(w, "  run %s  issue %s\n    %s\n", live.TaskID, live.IssueID, formatTaskLive(live))
	}
	return nil
}
