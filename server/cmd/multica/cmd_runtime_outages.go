package main

import (
	"context"
	"fmt"
	"os"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

func init() {
	outages := &cobra.Command{Use: "outages", Short: "List your latest 100 machine outages, start/end times and alert times", Long: "Read your own machine outages in this workspace. started_at is the confirmed offline transition; ended_at is null while disconnected. alerted_at is null for a short restart or muted alert. runtimes on one physical host share a record.", Example: "  multica runtime outages --output json", RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAPIClient(cmd)
		if err != nil {
			return err
		}
		ctx, cancel := cli.APIContext(context.Background())
		defer cancel()
		var rows []map[string]any
		if err = client.GetJSON(ctx, "/api/runtimes/outages", &rows); err != nil {
			return fmt.Errorf("read outages: %w; check workspace access and retry", err)
		}
		output, _ := cmd.Flags().GetString("output")
		if output == "json" {
			return cli.PrintJSON(os.Stdout, rows)
		}
		values := [][]string{}
		for _, r := range rows {
			values = append(values, []string{strVal(r, "machine_name"), strVal(r, "started_at"), strVal(r, "ended_at"), strVal(r, "alerted_at")})
		}
		cli.PrintTable(os.Stdout, []string{"MACHINE", "STARTED_AT", "ENDED_AT", "ALERTED_AT"}, values)
		return nil
	}}
	outages.Flags().String("output", "table", "Output format: table or json")
	alerts := &cobra.Command{Use: "alerts [on|off]", Short: "Read or change your machine disconnect/recovery alerts", Long: "Controls your inbox and owner Bot machine alerts together in this workspace. Disconnects lasting more than three minutes generate one alert per machine; a recovery follows an alerted outage. Bot quiet hours and event subscriptions still apply.", Example: "  multica runtime alerts off\n  multica runtime alerts --output json", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAPIClient(cmd)
		if err != nil {
			return err
		}
		ctx, cancel := cli.APIContext(context.Background())
		defer cancel()
		var result map[string]any
		if len(args) == 0 {
			err = client.GetJSON(ctx, "/api/notification-preferences", &result)
		} else {
			value := "all"
			switch args[0] {
			case "on":
			case "off":
				value = "muted"
			default:
				return fmt.Errorf("use on or off; example: multica runtime alerts on")
			}
			err = client.PatchJSON(ctx, "/api/notification-preferences", map[string]any{"preferences": map[string]string{"runtime_alerts": value}}, &result)
		}
		if err != nil {
			return fmt.Errorf("runtime alerts: %w; check workspace access and retry", err)
		}
		prefs, _ := result["preferences"].(map[string]any)
		enabled := prefs["runtime_alerts"] != "muted"
		output, _ := cmd.Flags().GetString("output")
		if output == "json" {
			return cli.PrintJSON(os.Stdout, map[string]any{"workspace_id": result["workspace_id"], "runtime_alerts_enabled": enabled, "offline_grace_seconds": 180, "aggregation": "owner and physical machine"})
		}
		fmt.Fprintf(os.Stdout, "Machine disconnect/recovery alerts enabled: %t\n", enabled)
		return nil
	}}
	alerts.Flags().String("output", "table", "Output format: table or json")
	runtimeCmd.AddCommand(outages, alerts)
}
