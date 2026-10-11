package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

func init() {
	wake := &cobra.Command{Use: "wake", Short: "Manage receiver-owned event webhooks and receipts", Long: `Read or update a bot's wake rules, and inspect delivered or pending events.
The bot may manage only itself; workspace administrators may manage any bot.
HTTP 200 means received, not running or completed. Uncertain deliveries are
never automatically replayed. Disabled, quiet and limited events remain pending.`}
	for _, verb := range []string{"get", "set", "events", "pending"} {
		verb := verb
		cmd := &cobra.Command{Use: verb + " <bot-id>", Short: map[string]string{"get": "Read wake rules (no secrets)", "set": "Update rules or write webhook credentials from file/stdin", "events": "List paginated delivery receipts", "pending": "List paginated events awaiting review"}[verb], Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error { return runBotWake(cmd, args, verb) }}
		cmd.Flags().String("output", "json", "Output format: json")
		if verb == "set" {
			cmd.Flags().String("config-file", "", "JSON settings file; webhook_url and webhook_key are write-only")
			cmd.Flags().Bool("config-stdin", false, "Read JSON settings from standard input")
			cmd.Long = `Update only supplied fields. Pass JSON using exactly one of --config-file
or --config-stdin. Never pass secret values as command arguments.
Fields: wake_from {member,agent,bot}: {mode: all|none|list, ids: []}, events (array), hourly_limit (0..120), quiet_hours
{enabled,start,end,timezone}, webhook_url, webhook_key.
Write URL and key together. The key is the complete Authorization header value
(e.g. Bearer …), as specified by the receiver. Two empty strings disconnect.
Defaults: 6 attempts/hour, 23:00–08:00 Asia/Shanghai.`
		}
		if verb == "events" || verb == "pending" {
			cmd.Flags().Int("limit", 50, "Page size (1..100)")
			cmd.Flags().String("before", "", "next_cursor from preceding page; pending consumers restart at first page after each scan")
		}
		wake.AddCommand(cmd)
	}
	addBotWakeReceipts(wake)
	botCmd.AddCommand(wake)
}
func runBotWake(cmd *cobra.Command, args []string, verb string) error {
	id, err := uuid.Parse(args[0])
	if err != nil {
		return fmt.Errorf("provide the full bot UUID")
	}
	output, _ := cmd.Flags().GetString("output")
	if output != "json" {
		return fmt.Errorf("--output must be json")
	}
	var body map[string]any
	if verb == "set" {
		file, _ := cmd.Flags().GetString("config-file")
		stdin, _ := cmd.Flags().GetBool("config-stdin")
		if (file != "") == stdin {
			return fmt.Errorf("provide exactly one of --config-file or --config-stdin")
		}
		var reader io.Reader = cmd.InOrStdin()
		if file != "" {
			f, err := os.Open(file)
			if err != nil {
				return fmt.Errorf("cannot open settings file")
			}
			defer f.Close()
			reader = f
		}
		data, err := io.ReadAll(io.LimitReader(reader, 16385))
		if err != nil || len(data) > 16384 {
			return fmt.Errorf("settings must be at most 16 KiB")
		}
		if json.Unmarshal(data, &body) != nil || body == nil {
			return fmt.Errorf("settings must be a JSON object")
		}
	}
	client, ws, ctx, cancel, err := botClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	path := botsPath(ws) + "/" + id.String() + "/wake"
	if verb == "events" || verb == "pending" {
		limit, _ := cmd.Flags().GetInt("limit")
		if limit < 1 || limit > 100 {
			return fmt.Errorf("--limit must be 1..100")
		}
		before, _ := cmd.Flags().GetString("before")
		path += "/" + verb + "?limit=" + fmt.Sprint(limit) + "&before=" + url.QueryEscape(before)
	}
	var response map[string]any
	if verb == "set" {
		err = client.PutJSON(ctx, path, body, &response)
	} else {
		err = client.GetJSON(ctx, path, &response)
	}
	if err != nil {
		return fmt.Errorf("bot wake %s: %w", verb, err)
	}
	return cli.PrintJSON(cmd.OutOrStdout(), response)
}
