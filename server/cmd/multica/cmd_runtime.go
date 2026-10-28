package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/multica-ai/multica/server/internal/listshape"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var runtimeCmd = &cobra.Command{
	Use:   "runtime",
	Short: "Work with agent runtimes",
}

var runtimeListCmd = &cobra.Command{
	Use:   "list",
	Short: "List runtimes in the workspace",
	RunE:  runRuntimeList,
}

var runtimeUsageCmd = &cobra.Command{
	Use:   "usage <runtime-id>",
	Short: "Get token usage for a runtime",
	Args:  exactArgs(1),
	RunE:  runRuntimeUsage,
}

var runtimeActivityCmd = &cobra.Command{
	Use:   "activity <runtime-id>",
	Short: "Get hourly run activity for a runtime",
	Args:  exactArgs(1),
	RunE:  runRuntimeActivity,
}

var runtimeUpdateCmd = &cobra.Command{
	Use:   "update <runtime-id>",
	Short: "Initiate a CLI update on a runtime, or set its machine's default working directory",
	Long: "Initiate a CLI update with --target-version.\n\n" +
		"--default-dir sets the machine's default working directory: a run whose project binds no folder " +
		"and names no repository starts there, with its instruction files kept in a separate directory beside it. " +
		"Pass an empty value (\"\") to clear it. Only the machine owner may change it; " +
		"`multica runtime list --output json` shows the path and whether the machine reported it exists.",
	Example: "  multica runtime update <runtime-id> --default-dir /Users/me/work\n  multica runtime update <runtime-id> --target-version v1.2.3",
	Args:    exactArgs(1),
	RunE:    runRuntimeUpdate,
}

var runtimeRenameCmd = &cobra.Command{
	Use:   "rename <runtime-id> <name>",
	Short: "Set a custom display name for a runtime",
	Long: "Set (or clear) a runtime's custom display name.\n\n" +
		"Pass an empty name (\"\") to clear the custom name and fall back to the default. " +
		"Use --machine to apply the name to every runtime on the same machine.",
	Args: exactArgs(2),
	RunE: runRuntimeRename,
}

var runtimeDeleteCmd = &cobra.Command{
	Use:   "delete <runtime-id>",
	Short: "Delete a runtime from the workspace",
	Long: "Delete a runtime registration from the workspace.\n\n" +
		"By default this refuses when active agents are still bound to the runtime. " +
		"Pass --cascade to unbind those agents, cancel their queued or active runs, and delete the runtime. " +
		"Unbound agents keep their configuration, chats and run history; bind them to another runtime to run them again.",
	Args: exactArgs(1),
	RunE: runRuntimeDelete,
}

func init() {
	runtimeCmd.AddCommand(runtimeCapacityCmd)
	runtimeCapacityCmd.Flags().Int("max-concurrent-runs", 12, "Set machine-wide run limit (1-100; omitted reads current capacity)")
	runtimeCapacityCmd.Flags().String("output", "json", "Output format: table or json")
	runtimeCmd.AddCommand(runtimeListCmd)
	runtimeCmd.AddCommand(runtimeUsageCmd)
	runtimeCmd.AddCommand(runtimeActivityCmd)
	runtimeCmd.AddCommand(runtimeUpdateCmd)
	runtimeCmd.AddCommand(runtimeRenameCmd)
	runtimeCmd.AddCommand(runtimeDeleteCmd)

	// runtime list
	addListShapeFlags(runtimeListCmd)
	runtimeListCmd.Example = "  multica runtime list --compact --output json\n  multica runtime list --fields id,name,status --output json --workspace \"My workspace\""
	runtimeListCmd.Flags().String("output", "table", "Output format: table or json")

	// runtime usage
	runtimeUsageCmd.Flags().String("output", "table", "Output format: table or json")
	runtimeUsageCmd.Flags().Int("days", 90, "Number of days of usage data to retrieve (max 365)")

	// runtime activity
	runtimeActivityCmd.Flags().String("output", "table", "Output format: table or json")

	// runtime update
	runtimeUpdateCmd.Flags().String("target-version", "", "Target version to update to (required unless --default-dir is given)")
	runtimeUpdateCmd.Flags().String("default-dir", "", "Set the machine's default working directory (absolute path; \"\" clears it)")
	runtimeUpdateCmd.Flags().String("output", "json", "Output format: table or json")
	runtimeUpdateCmd.Flags().Bool("wait", false, "Wait for update to complete (poll until done)")

	// runtime rename
	runtimeRenameCmd.Flags().Bool("machine", false, "Apply the name to every runtime on the same machine")
	runtimeRenameCmd.Flags().String("output", "table", "Output format: table or json")

	// runtime delete
	runtimeDeleteCmd.Flags().Bool("cascade", false, "Unbind active agents from the runtime, cancel their runs, then delete the runtime")
	runtimeDeleteCmd.Flags().String("output", "table", "Output format: table or json")
}

// Runtime commands

func runRuntimeList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	q := url.Values{}
	fields, err := listShapeQuery(cmd, q, listshape.Runtime)
	if err != nil {
		return err
	}
	var runtimes []map[string]any
	if err := client.GetJSON(ctx, "/api/runtimes?"+q.Encode(), &runtimes); err != nil {
		return fmt.Errorf("list runtimes: %w", err)
	}

	listshape.Filter(runtimes, fields)
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, runtimes)
	}

	headers := []string{"ID", "NAME", "MODE", "PROVIDER", "STATUS", "LAST_SEEN"}
	rows := make([][]string, 0, len(runtimes))
	for _, rt := range runtimes {
		rows = append(rows, []string{
			strVal(rt, "id"),
			strVal(rt, "name"),
			strVal(rt, "runtime_mode"),
			strVal(rt, "provider"),
			strVal(rt, "status"),
			strVal(rt, "last_seen_at"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runRuntimeUsage(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	days, _ := cmd.Flags().GetInt("days")
	if days < 1 || days > 365 {
		return fmt.Errorf("--days must be between 1 and 365")
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var usage []map[string]any
	path := fmt.Sprintf("/api/runtimes/%s/usage?days=%d", args[0], days)
	if err := client.GetJSON(ctx, path, &usage); err != nil {
		return fmt.Errorf("get runtime usage: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, usage)
	}

	headers := []string{"DATE", "PROVIDER", "MODEL", "INPUT_TOKENS", "OUTPUT_TOKENS", "CACHE_READ", "CACHE_WRITE"}
	rows := make([][]string, 0, len(usage))
	for _, u := range usage {
		rows = append(rows, []string{
			strVal(u, "date"),
			strVal(u, "provider"),
			strVal(u, "model"),
			strVal(u, "input_tokens"),
			strVal(u, "output_tokens"),
			strVal(u, "cache_read_tokens"),
			strVal(u, "cache_write_tokens"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runRuntimeActivity(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var activity []map[string]any
	if err := client.GetJSON(ctx, "/api/runtimes/"+args[0]+"/activity", &activity); err != nil {
		return fmt.Errorf("get runtime activity: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, activity)
	}

	headers := []string{"HOUR", "COUNT"}
	rows := make([][]string, 0, len(activity))
	for _, a := range activity {
		rows = append(rows, []string{
			strVal(a, "hour"),
			strVal(a, "count"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runRuntimeDelete(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	runtimeID := args[0]
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	err = client.DeleteJSON(ctx, "/api/runtimes/"+runtimeID)
	if err == nil {
		return printRuntimeDeleteResult(cmd, map[string]any{
			"id":      runtimeID,
			"deleted": true,
		})
	}

	conflict, ok := runtimeDeleteConflict(err)
	if !ok {
		// Any other 409 is a deliberate, already-explained refusal — most often
		// a profile-backed instance that cannot be deleted on its own. Show the
		// server's guidance instead of a raw HTTP wrapper; --cascade cannot get
		// past these, so there is nothing more for this command to try.
		if _, msg, isConflict := serverConflictMessage(err); isConflict {
			return errors.New(msg)
		}
		return fmt.Errorf("delete runtime: %w", err)
	}

	cascade, _ := cmd.Flags().GetBool("cascade")
	if !cascade {
		return fmt.Errorf(
			"delete runtime: runtime has active agents bound to it (%s); rebind them to another runtime first, or rerun with --cascade to unbind them and delete the runtime (the agents and their history are kept)",
			strings.Join(conflict.AgentDisplays(), ", "),
		)
	}

	body := map[string]any{
		"expected_active_agent_ids": conflict.AgentIDs(),
	}
	var result map[string]any
	if err := client.PostJSON(ctx, "/api/runtimes/"+runtimeID+"/unbind-agents-and-delete", body, &result); err != nil {
		return fmt.Errorf("cascade delete runtime: %w", err)
	}
	result["id"] = runtimeID
	result["deleted"] = true
	return printRuntimeDeleteResult(cmd, result)
}

func runRuntimeRename(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	machine, _ := cmd.Flags().GetBool("machine")
	body := map[string]any{"custom_name": args[1]}
	if machine {
		body["apply_to_machine"] = true
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var rt map[string]any
	if err := client.PatchJSON(ctx, "/api/runtimes/"+args[0], body, &rt); err != nil {
		return fmt.Errorf("rename runtime: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, rt)
	}

	if strings.TrimSpace(args[1]) == "" {
		fmt.Fprintf(os.Stderr, "Custom name cleared; runtime is now %q.\n", strVal(rt, "name"))
	} else {
		fmt.Fprintf(os.Stderr, "Runtime renamed to %q.\n", strVal(rt, "custom_name"))
	}
	return nil
}

func runRuntimeUpdate(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	targetVersion, _ := cmd.Flags().GetString("target-version")
	if cmd.Flags().Changed("default-dir") {
		if err := setRuntimeDefaultDir(cmd, client, args[0]); err != nil {
			return err
		}
		if targetVersion == "" {
			return nil
		}
	}
	if targetVersion == "" {
		return fmt.Errorf("--target-version is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), cli.AtLeastAPITimeout(150*time.Second))
	defer cancel()

	body := map[string]any{
		"target_version": targetVersion,
	}

	var update map[string]any
	if err := client.PostJSON(ctx, "/api/runtimes/"+args[0]+"/update", body, &update); err != nil {
		return fmt.Errorf("initiate update: %w", err)
	}

	wait, _ := cmd.Flags().GetBool("wait")
	if !wait {
		output, _ := cmd.Flags().GetString("output")
		if output == "json" {
			return cli.PrintJSON(os.Stdout, update)
		}
		fmt.Printf("Update initiated: %s (status: %s)\n", strVal(update, "id"), strVal(update, "status"))
		return nil
	}

	// Poll until completed/failed/timeout.
	updateID := strVal(update, "id")
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for update (last status: %s)", strVal(update, "status"))
		case <-time.After(2 * time.Second):
		}

		if err := client.GetJSON(ctx, "/api/runtimes/"+args[0]+"/update/"+updateID, &update); err != nil {
			return fmt.Errorf("get update status: %w", err)
		}

		status := strVal(update, "status")
		if status == "completed" || status == "failed" || status == "timeout" {
			output, _ := cmd.Flags().GetString("output")
			if output == "json" {
				return cli.PrintJSON(os.Stdout, update)
			}
			if status == "completed" {
				fmt.Printf("Update completed: %s\n", strVal(update, "output"))
			} else {
				fmt.Printf("Update %s: %s\n", status, strVal(update, "error"))
			}
			return nil
		}
	}
}

// setRuntimeDefaultDir saves --default-dir through the same PATCH the
// settings page uses. The server stores it per machine and cannot see the
// disk, so whether the path exists arrives later from the daemon.
func setRuntimeDefaultDir(cmd *cobra.Command, client *cli.APIClient, runtimeID string) error {
	dir, _ := cmd.Flags().GetString("default-dir")
	dir = strings.TrimSpace(dir)
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var rt map[string]any
	if err := client.PatchJSON(ctx, "/api/runtimes/"+runtimeID, map[string]any{"default_work_dir": dir}, &rt); err != nil {
		return fmt.Errorf("set default working directory: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, rt)
	}
	if dir == "" {
		fmt.Fprintln(os.Stderr, "Default working directory cleared.")
	} else {
		fmt.Fprintf(os.Stderr, "Default working directory set to %q; the machine reports whether it exists within a minute.\n", dir)
	}
	return nil
}

// serverConflictMessage pulls the server's own sentence out of a 409 body.
//
// The refusals behind runtime and profile deletion are written to be read by
// the person who ran the command — they name the machine, the profile and what
// to do instead. HTTPError.Error() would bury that sentence inside a raw JSON
// dump of the whole response, which is what a blocked user used to see.
func serverConflictMessage(err error) (code string, message string, ok bool) {
	var httpErr *cli.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusConflict {
		return "", "", false
	}
	body := strings.TrimSpace(httpErr.Body)
	if body == "" {
		return "", "", false
	}
	var payload struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(body), &payload) != nil {
		// Not JSON. An older or proxied server can still put a readable
		// sentence here, and swallowing it would be worse than passing it
		// through — there is nothing to leak when the body was never
		// structured in the first place.
		return "", body, true
	}
	if strings.TrimSpace(payload.Error) == "" {
		return payload.Code, "", false
	}
	return payload.Code, payload.Error, true
}

type runtimeDeleteConflictPayload struct {
	Code         string `json:"code"`
	Error        string `json:"error"`
	ActiveAgents []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"active_agents"`
}

func runtimeDeleteConflict(err error) (runtimeDeleteConflictPayload, bool) {
	var httpErr *cli.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusConflict {
		return runtimeDeleteConflictPayload{}, false
	}
	var payload runtimeDeleteConflictPayload
	if json.Unmarshal([]byte(httpErr.Body), &payload) != nil {
		return runtimeDeleteConflictPayload{}, false
	}
	if payload.Code != "runtime_has_active_agents" || len(payload.ActiveAgents) == 0 {
		return runtimeDeleteConflictPayload{}, false
	}
	return payload, true
}

func (p runtimeDeleteConflictPayload) AgentIDs() []string {
	ids := make([]string, 0, len(p.ActiveAgents))
	for _, agent := range p.ActiveAgents {
		if agent.ID != "" {
			ids = append(ids, agent.ID)
		}
	}
	return ids
}

func (p runtimeDeleteConflictPayload) AgentDisplays() []string {
	displays := make([]string, 0, len(p.ActiveAgents))
	for _, agent := range p.ActiveAgents {
		switch {
		case agent.Name != "" && agent.ID != "":
			displays = append(displays, fmt.Sprintf("%s (%s)", agent.Name, agent.ID))
		case agent.Name != "":
			displays = append(displays, agent.Name)
		case agent.ID != "":
			displays = append(displays, agent.ID)
		}
	}
	return displays
}

func printRuntimeDeleteResult(cmd *cobra.Command, result map[string]any) error {
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}

	if agentsUnbound, ok := result["agents_unbound"]; ok {
		fmt.Fprintf(os.Stderr, "Runtime %s deleted; unbound %v agent(s)", strVal(result, "id"), agentsUnbound)
		if paused, ok := result["autopilots_paused"]; ok {
			fmt.Fprintf(os.Stderr, " and paused %v autopilot(s)", paused)
		}
		fmt.Fprintln(os.Stderr, ".")
		return nil
	}
	// Compatibility fallback for an older server that only returns the
	// pre-MUL-5559 mirror.
	if agentsArchived, ok := result["agents_archived"]; ok {
		fmt.Fprintf(os.Stderr, "Runtime %s deleted; processed %v agent(s).\n", strVal(result, "id"), agentsArchived)
		return nil
	}
	fmt.Fprintf(os.Stderr, "Runtime %s deleted.\n", strVal(result, "id"))
	return nil
}

// Capacity reads and edits the server setting, independently of CLI upgrades.
var runtimeCapacityCmd = &cobra.Command{
	Annotations: map[string]string{annotationAccess: "write", annotationBotKey: "yes"},
	Example:     "multica runtime capacity <runtime-id> --max-concurrent-runs 8 --output json",
	Use:         "capacity <runtime-id>",
	Short:       "Read or set the machine's simultaneous run limit (default 12)",
	Long:        "Find runtime IDs with multica runtime list. Read the device-wide active run count and limit, or set it with --max-concurrent-runs (1-100). All providers and workspaces on the same device share this limit. Only the machine owner may change it. Excess runs queue; lowering the limit does not stop active runs.",
	Args:        exactArgs(1),
	RunE:        runRuntimeCapacity,
}

func runRuntimeCapacity(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var result map[string]any
	if cmd.Flags().Changed("max-concurrent-runs") {
		limit, _ := cmd.Flags().GetInt("max-concurrent-runs")
		if limit < 1 || limit > 100 {
			return fmt.Errorf("--max-concurrent-runs must be between 1 and 100")
		}
		if err := client.PatchJSON(ctx, "/api/runtimes/"+args[0], map[string]any{"max_concurrent_runs": limit}, &result); err != nil {
			return fmt.Errorf("set machine capacity: %w", err)
		}
	} else {
		var runtimes []map[string]any
		if err := client.GetJSON(ctx, "/api/runtimes", &runtimes); err != nil {
			return err
		}
		for _, rt := range runtimes {
			if strVal(rt, "id") == args[0] {
				result = rt
				break
			}
		}
		if result == nil {
			return fmt.Errorf("runtime not found")
		}
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(cmd.OutOrStdout(), result)
	}
	capacity, ok := result["machine_capacity"].(map[string]any)
	if !ok {
		return fmt.Errorf("machine capacity is not available on this server")
	}
	cli.PrintTable(cmd.OutOrStdout(), []string{"RUNTIME", "ACTIVE", "LIMIT"}, [][]string{{args[0], fmt.Sprint(capacity["active_runs"]), fmt.Sprint(capacity["max_concurrent_runs"])}})
	return nil
}
