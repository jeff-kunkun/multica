package main

import (
	"fmt"
	"net/url"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

func init() {
	mentions := &cobra.Command{Use: "mentions", Short: "List visible Bot mention targets", Args: exactArgs(0), RunE: func(cmd *cobra.Command, args []string) error {
		client, ws, ctx, cancel, err := botClient(cmd)
		if err != nil {
			return err
		}
		defer cancel()
		issue, _ := cmd.Flags().GetString("issue")
		chat, _ := cmd.Flags().GetString("chat")
		if issue != "" && chat != "" {
			return fmt.Errorf("provide only --issue or --chat")
		}
		var out []map[string]any
		err = client.GetJSON(ctx, botsPath(ws)+"/mentions?issue_id="+url.QueryEscape(issue)+"&chat_session_id="+url.QueryEscape(chat), &out)
		if err != nil {
			return err
		}
		return cli.PrintJSON(cmd.OutOrStdout(), out)
	}}
	mentions.Flags().String("issue", "", "Filter by accessible issue UUID")
	mentions.Flags().String("chat", "", "Filter by accessible chat UUID")
	mentions.Flags().String("output", "json", "Output format: json")
	botCmd.AddCommand(mentions)
}

func addBotWakeReceipts(wake *cobra.Command) {
	for _, verb := range []string{"woken", "result", "ack"} {
		verb := verb
		cmd := &cobra.Command{Use: verb + " <bot-id> <event-id> [event-id...]", Short: "Record " + verb + " independently of HTTP delivery", Args: cobra.MinimumNArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
			if verb != "ack" && len(args) != 2 {
				return fmt.Errorf("provide one event ID")
			}
			for _, id := range args {
				if _, err := uuid.Parse(id); err != nil {
					return fmt.Errorf("provide full UUIDs")
				}
			}
			client, ws, ctx, cancel, err := botClient(cmd)
			if err != nil {
				return err
			}
			defer cancel()
			path := botsPath(ws) + "/" + args[0] + "/wake"
			body := map[string]any{}
			if verb == "ack" {
				path += "/pending/ack"
				body["event_ids"] = args[1:]
			} else {
				path += "/events/" + args[1] + "/" + verb
				if verb == "result" {
					v, _ := cmd.Flags().GetString("result-url")
					if v == "" {
						return fmt.Errorf("--result-url required")
					}
					body["result_url"] = v
				}
			}
			var out map[string]any
			if err = client.PostJSON(ctx, path, body, &out); err != nil {
				return err
			}
			return cli.PrintJSON(cmd.OutOrStdout(), out)
		}}
		cmd.Flags().String("output", "json", "Output format: json")
		if verb == "result" {
			cmd.Flags().String("result-url", "", "Canonical API URL of the event source")
		}
		wake.AddCommand(cmd)
	}
}
