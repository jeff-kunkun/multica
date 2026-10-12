package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var runtimeModelsCmd = &cobra.Command{
	Use:   "models <runtime-id>",
	Short: "List the models a runtime reports, with each model's thinking levels",
	Long: "List the models a runtime's daemon reports and, per model, the thinking levels\n" +
		"`multica agent update --thinking-level` accepts for it.\n\n" +
		"THINKING lists the accepted tokens; DEFAULT is the level the model uses when the\n" +
		"agent leaves thinking_level empty (blank when unknown). SOURCE says where the levels\n" +
		"were declared when they come from the runtime's own config (Qwen Code's\n" +
		"modelProviders capabilities.reasoning) rather than the CLI itself.\n\n" +
		"The answer may come from the server's catalog cache; --refresh asks the daemon\n" +
		"again. Asking also records the catalog the server checks thinking levels against.",
	Example: "  multica runtime models <runtime-id>\n" +
		"  multica runtime models <runtime-id> --refresh --output json",
	Args: exactArgs(1),
	RunE: runRuntimeModels,
}

func init() {
	runtimeCmd.AddCommand(runtimeModelsCmd)
	runtimeModelsCmd.Flags().Bool("refresh", false, "Ask the daemon for a fresh list instead of the server cache")
	runtimeModelsCmd.Flags().String("agent", "", "Agent id whose custom_env should shape discovery (not cached)")
	runtimeModelsCmd.Flags().Duration("timeout", 90*time.Second, "How long to wait for the daemon")
	runtimeModelsCmd.Flags().String("output", "table", "Output format: table or json")
}

func runRuntimeModels(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	runtimeID := strings.TrimSpace(args[0])
	refresh, _ := cmd.Flags().GetBool("refresh")
	agentID, _ := cmd.Flags().GetString("agent")
	timeout, _ := cmd.Flags().GetDuration("timeout")

	q := url.Values{}
	if refresh {
		q.Set("force", "true")
	}
	if agentID = strings.TrimSpace(agentID); agentID != "" {
		q.Set("agent_id", agentID)
	}
	path := "/api/runtimes/" + url.PathEscape(runtimeID) + "/models"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var req map[string]any
	if err := client.PostJSON(ctx, path, map[string]any{}, &req); err != nil {
		return fmt.Errorf("list runtime models: %w", err)
	}
	for !modelListDone(strVal(req, "status")) {
		select {
		case <-ctx.Done():
			return fmt.Errorf("runtime %s did not answer within %s; is its daemon online?", runtimeID, timeout)
		case <-time.After(time.Second):
		}
		id := strVal(req, "id")
		if err := client.GetJSON(ctx, "/api/runtimes/"+url.PathEscape(runtimeID)+"/models/"+url.PathEscape(id), &req); err != nil {
			return fmt.Errorf("poll runtime models: %w", err)
		}
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, req)
	}
	if status := strVal(req, "status"); status != "completed" {
		msg := strVal(req, "error")
		if msg == "" {
			msg = status
		}
		return fmt.Errorf("runtime %s could not list models: %s", runtimeID, msg)
	}

	models, _ := req["models"].([]any)
	headers := []string{"MODEL", "DEFAULT_MODEL", "THINKING", "DEFAULT", "SOURCE"}
	rows := make([][]string, 0, len(models))
	for _, raw := range models {
		m, _ := raw.(map[string]any)
		if m == nil {
			continue
		}
		isDefault := ""
		if v, _ := m["default"].(bool); v {
			isDefault = "yes"
		}
		levels, defaultLevel, source := "", "", ""
		if thinking, _ := m["thinking"].(map[string]any); thinking != nil {
			var values []string
			if list, _ := thinking["supported_levels"].([]any); list != nil {
				for _, item := range list {
					if lvl, _ := item.(map[string]any); lvl != nil {
						values = append(values, strVal(lvl, "value"))
					}
				}
			}
			levels = strings.Join(values, ",")
			defaultLevel = strVal(thinking, "default_level")
			source = strVal(thinking, "source")
		}
		rows = append(rows, []string{strVal(m, "id"), isDefault, levels, defaultLevel, source})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	if cached, _ := req["cached"].(bool); cached {
		fmt.Fprintln(os.Stderr, "(from the server cache; --refresh asks the daemon again)")
	}
	return nil
}

func modelListDone(status string) bool {
	switch status {
	case "completed", "failed", "timeout":
		return true
	}
	return false
}
