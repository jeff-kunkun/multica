package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var chatCmd = &cobra.Command{
	Use:   "chat",
	Short: "Work with the current chat conversation",
}

var chatHistoryCmd = &cobra.Command{
	Use:   "history",
	Short: "Read this conversation's transcript",
	Long: `Read the transcript of the chat conversation you are currently running for,
oldest message first. Page to older messages with --before, passing the
next_cursor of the previous page.

It reads only the conversation you are currently running for — it cannot read
any other session.`,
	Args: cobra.NoArgs,
	RunE: runChatHistory,
}

func init() {
	chatHistoryCmd.Flags().Int("limit", 0, "Maximum number of messages to return (the server clamps the range)")
	chatHistoryCmd.Flags().String("before", "", "Opaque cursor (a next_cursor from a prior page) to read older messages")
	chatHistoryCmd.Flags().String("output", "json", "Output format: table or json")
	chatCmd.AddCommand(chatHistoryCmd)
}

func runChatHistory(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	limit, _ := cmd.Flags().GetInt("limit")
	before, _ := cmd.Flags().GetString("before")

	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if before != "" {
		q.Set("before", before)
	}
	path := "/api/chat/history"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var resp map[string]any
	if err := client.GetJSON(ctx, path, &resp); err != nil {
		return fmt.Errorf("read chat: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output != "table" {
		return cli.PrintJSON(os.Stdout, resp)
	}
	msgs, _ := resp["messages"].([]any)
	headers := []string{"TS", "ROLE", "AUTHOR", "TEXT"}
	rows := make([][]string, 0, len(msgs))
	for _, mi := range msgs {
		m, ok := mi.(map[string]any)
		if !ok {
			continue
		}
		rows = append(rows, []string{strVal(m, "ts"), strVal(m, "role"), strVal(m, "author"), strVal(m, "text")})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}
