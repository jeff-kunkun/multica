package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

const fastAccessMaxBytes = 50000

func newFastAccessCommand(runs bool) *cobra.Command {
	name, short := "changes", "Read one page of changes and active runs across visible workspaces"
	if runs {
		name, short = "runs", "Read one page of active runs across visible workspaces"
	}
	cmd := &cobra.Command{
		Use: name, Short: short, Args: cobra.NoArgs,
		Annotations: map[string]string{annotationAccess: "read"},
		Long: `Read one permission-filtered page using the existing caller identity. The server
owns visibility, active status selection and rate limits. This does not mark chats
read. No per-workspace polling, automatic retries or automatic pagination.

Choose --all-workspaces or --workspace UUID (repeatable); otherwise use the current
configured workspace. Find workspace IDs with multica workspace list --output json.
For changes, omit --since to rebuild current state, or provide a retained RFC3339
--since for a historical window, or the server's opaque
--cursor for continuation, never both. Continuation takes only --cursor (plus limit/max-bytes/output);
do not repeat scope flags because the server cursor already binds the scope.

JSON preserves the complete envelope: schema_version, mode, coverage_from, as_of,
scope_revision, changes, active_runs, complete, active_runs_complete, next_cursor,
checkpoint and retention_floor, including additional server fields. A false
complete or active_runs_complete means this page is partial. Follow next_cursor
explicitly; only persist checkpoint after completion. In durable_delta, pass the
completed checkpoint as --cursor for the next delta; event_id deduplicates delivery
and revision orders each object within its workspace. In timestamp_snapshot,
checkpoint is an observation receipt, not a cursor; start the next poll with
--since. timestamp_snapshot describes
current summaries, not historical deletions or every intermediate transition.
Runs preserve the server's status and phase (queued, executing or waiting),
including chat runs without an issue. The uncompressed UTF-8 response budget is
at most 50000 bytes. Diagnostics go to stderr; --output json owns stdout.

Permission errors are not empty results. On 429, wait for the server's retry_after
before retrying. On reset_required, history_unavailable or scope_reset, discard
all cached summaries and the stale cursor, then rebuild without --since. Unknown schema versions require a
compatible CLI. HTTP timeout follows MULTICA_HTTP_TIMEOUT.`,
	}
	cmd.Flags().Bool("all-workspaces", false, "Read all workspaces visible to the caller (server-authorized)")
	cmd.Flags().StringSlice("workspace", nil, "Workspace UUIDs (repeatable or comma-separated); defaults to current workspace")
	cmd.Flags().String("cursor", "", "Opaque next_cursor or durable_delta checkpoint from the server")
	cmd.Flags().Int("limit", 100, "Page item limit (1-200)")
	cmd.Flags().Int("max-bytes", fastAccessMaxBytes, "Maximum uncompressed UTF-8 response bytes (1-50000)")
	cmd.Flags().String("output", "table", "Output format: table or json (entire envelope)")
	if runs {
		cmd.Flags().Bool("active", false, "Required: request the server's active run set, preserving status and phase")
		cmd.Example = "  multica runs --active --all-workspaces --output json\n  multica runs --active --cursor 'SERVER_CURSOR' --output json"
	} else {
		cmd.Flags().String("since", "", "First-page RFC3339 timestamp; mutually exclusive with --cursor")
		cmd.Example = "  multica changes --since 2026-10-11T17:00:00Z --all-workspaces --output json\n  multica changes --cursor 'SERVER_CURSOR' --output json"
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error { return runFastAccess(cmd, runs) }
	return cmd
}

func runFastAccess(cmd *cobra.Command, runs bool) error {
	output, _ := cmd.Flags().GetString("output")
	if output != "json" && output != "table" {
		return fmt.Errorf("invalid output format %q: expected table or json", output)
	}
	limit, _ := cmd.Flags().GetInt("limit")
	budget, _ := cmd.Flags().GetInt("max-bytes")
	if limit < 1 || limit > 200 {
		return fmt.Errorf("--limit must be between 1 and 200")
	}
	if budget < 1 || budget > fastAccessMaxBytes {
		return fmt.Errorf("--max-bytes must be between 1 and 50000")
	}
	all, _ := cmd.Flags().GetBool("all-workspaces")
	workspaces, _ := cmd.Flags().GetStringSlice("workspace")
	if all && cmd.Flags().Changed("workspace") {
		return fmt.Errorf("--all-workspaces and --workspace are mutually exclusive")
	}
	cursor, _ := cmd.Flags().GetString("cursor")
	since := ""
	if runs {
		active, _ := cmd.Flags().GetBool("active")
		if !active {
			return fmt.Errorf("--active is required; see multica runs --help")
		}
	} else {
		since, _ = cmd.Flags().GetString("since")
		if cmd.Flags().Changed("since") && cmd.Flags().Changed("cursor") {
			return fmt.Errorf("--since and --cursor are mutually exclusive")
		}
		if since != "" {
			if _, err := time.Parse(time.RFC3339Nano, since); err != nil {
				return fmt.Errorf("--since must be RFC3339: %w", err)
			}
		}
	}
	if cmd.Flags().Changed("cursor") && strings.TrimSpace(cursor) == "" {
		return fmt.Errorf("--cursor cannot be empty")
	}
	if cursor != "" && (cmd.Flags().Changed("all-workspaces") || cmd.Flags().Changed("workspace") || cmd.Flags().Changed("workspace-id")) {
		return fmt.Errorf("--cursor already binds scope; omit --all-workspaces, --workspace and --workspace-id on continuation")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if cursor == "" && !all && !cmd.Flags().Changed("workspace") {
		workspaces = []string{client.WorkspaceID}
	}
	if cursor == "" && !all {
		if len(workspaces) == 0 {
			return fmt.Errorf("choose --workspace UUID or --all-workspaces")
		}
		for i, ws := range workspaces {
			workspaces[i] = strings.TrimSpace(ws)
			if workspaces[i] == "" {
				return fmt.Errorf("choose --workspace UUID or --all-workspaces; workspace cannot be empty")
			}
		}
	}
	// Decorate only this command's response transport; authentication and request
	// construction remain in the existing API client. Bound bytes before decoding.
	httpClient := *client.HTTPClient
	transport := httpClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	httpClient.Transport = fastAccessTransport{base: transport, budget: budget, notes: cmd.ErrOrStderr()}
	client.HTTPClient = &httpClient
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	var raw json.RawMessage
	if runs {
		query := url.Values{"active": {"true"}, "limit": {strconv.Itoa(limit)}, "max_bytes": {strconv.Itoa(budget)}}
		if cursor == "" {
			if all {
				query.Set("scope", "all_visible")
			} else {
				query.Set("workspace_ids", strings.Join(workspaces, ","))
			}
		}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		err = client.GetJSON(ctx, "/api/fast-access/v1/runs?"+query.Encode(), &raw)
	} else {
		scope := map[string]any{"mode": "workspace_ids", "workspace_ids": workspaces}
		if all {
			scope = map[string]any{"mode": "all_visible"}
		}
		body := map[string]any{"limit": limit, "max_bytes": budget}
		if cursor == "" {
			body["scope"] = scope
		}
		if since != "" {
			body["since"] = since
		}
		if cursor != "" {
			body["cursor"] = cursor
		}
		err = client.PostJSON(ctx, "/api/fast-access/v1/read", body, &raw)
	}
	if err != nil {
		// Keep stable server recovery fields visible even when the common terminal
		// formatter translates an HTTP error into a friendly status message.
		var httpErr *cli.HTTPError
		if errors.As(err, &httpErr) {
			var details struct {
				Code       string          `json:"code"`
				RetryAfter json.RawMessage `json:"retry_after,omitempty"`
			}
			if json.Unmarshal([]byte(httpErr.Body), &details) == nil && details.Code != "" {
				data, _ := json.Marshal(details)
				fmt.Fprintln(cmd.ErrOrStderr(), string(data))
			}
		}
		return fmt.Errorf("fast access: %w", err)
	}
	var envelope struct {
		SchemaVersion      json.RawMessage `json:"schema_version"`
		Mode               string          `json:"mode"`
		Complete           *bool           `json:"complete"`
		ActiveRunsComplete *bool           `json:"active_runs_complete"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("invalid fast-access envelope: %w", err)
	}
	if string(envelope.SchemaVersion) != `"1"` {
		return fmt.Errorf("unsupported_schema_version: fast-access schema_version %s; install a compatible CLI", envelope.SchemaVersion)
	}
	if envelope.Complete == nil || envelope.ActiveRunsComplete == nil {
		return fmt.Errorf("invalid fast-access envelope: missing complete or active_runs_complete")
	}
	if !*envelope.Complete || !*envelope.ActiveRunsComplete {
		fmt.Fprintln(cmd.ErrOrStderr(), "Partial page: inspect complete/active_runs_complete and continue explicitly with next_cursor; do not save an incomplete checkpoint.")
	}
	if envelope.Mode == "timestamp_snapshot" {
		fmt.Fprintln(cmd.ErrOrStderr(), "timestamp_snapshot: current summaries only; coverage does not include historical deletions or every intermediate state.")
	}
	if output == "json" {
		// Do not decode through float64 or pretty-print: preserve large revisions,
		// unknown fields and the server's bounded UTF-8 representation.
		_, err := cmd.OutOrStdout().Write(raw)
		return err
	}
	var snapshot map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&snapshot); err != nil {
		return err
	}
	return printFastAccess(cmd.OutOrStdout(), snapshot)
}

type fastAccessTransport struct {
	base   http.RoundTripper
	budget int
	notes  io.Writer
}

func (t fastAccessTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		if retry := resp.Header.Get("Retry-After"); retry != "" {
			fmt.Fprintf(t.notes, "Rate limited; Retry-After: %s. No automatic retry.\n", retry)
		}
	}
	if resp.StatusCode >= 400 {
		return resp, nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(t.budget)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > t.budget {
		return nil, fmt.Errorf("response_budget_exceeded: fast-access response exceeds %d uncompressed bytes; no partial JSON emitted", t.budget)
	}
	if !utf8.Valid(body) || !json.Valid(body) {
		return nil, fmt.Errorf("invalid fast-access response: expected one valid UTF-8 JSON envelope")
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

func printFastAccess(w io.Writer, snapshot map[string]any) error {
	for _, key := range []string{"schema_version", "mode", "as_of", "coverage_from", "scope_revision", "complete", "active_runs_complete", "next_cursor", "checkpoint", "retention_floor"} {
		value, err := json.Marshal(snapshot[key])
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "%s: %s\n", key, value)
	}
	// JSON cells quote control characters and retain Unicode and server values.
	for _, section := range []string{"changes", "active_runs"} {
		fmt.Fprintf(w, "\n%s\n", section)
		rows, _ := snapshot[section].([]any)
		for _, row := range rows {
			value, err := json.Marshal(row)
			if err != nil {
				return err
			}
			fmt.Fprintln(w, string(value))
		}
	}
	return nil
}
