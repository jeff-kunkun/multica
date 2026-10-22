package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

var askCmd = &cobra.Command{Use: "ask", Short: "Read and answer owner-confirmation questions"}
var askGetCmd = &cobra.Command{Use: "get <id>", Args: exactArgs(1), Short: "Read a question", RunE: runAskGet}
var askAnswerCmd = &cobra.Command{Use: "answer <id>", Args: exactArgs(1), Short: "Answer a question", RunE: runAskAnswer}

func init() {
	askCmd.GroupID = groupCore
	askCmd.AddCommand(askGetCmd, askAnswerCmd)
	askGetCmd.Flags().String("output", "table", "Output format: table or json")
	askAnswerCmd.Flags().String("answers-file", "", "JSON object mapping question index or id to option id/text")
	askAnswerCmd.Flags().String("output", "table", "Output format: table or json")
	rootCmd.AddCommand(askCmd)
}

func askJSON(path string, out any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
func runAskGet(cmd *cobra.Command, args []string) error {
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var out map[string]any
	if err := c.GetJSON(ctx, "/api/asks/"+url.PathEscape(args[0]), &out); err != nil {
		return fmt.Errorf("get ask: %w", err)
	}
	return printAskOutput(cmd, out)
}
func runAskAnswer(cmd *cobra.Command, args []string) error {
	path, _ := cmd.Flags().GetString("answers-file")
	if path == "" {
		return fmt.Errorf("--answers-file is required")
	}
	var answers map[string]string
	if err := askJSON(path, &answers); err != nil {
		return fmt.Errorf("read answers: %w", err)
	}
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var out map[string]any
	if err := c.PostJSON(ctx, "/api/asks/"+url.PathEscape(args[0])+"/answer", map[string]any{"answers": answers}, &out); err != nil {
		return fmt.Errorf("answer ask: %w", err)
	}
	return printAskOutput(cmd, out)
}
func printAskOutput(cmd *cobra.Command, out map[string]any) error {
	format, _ := cmd.Flags().GetString("output")
	if format == "json" {
		return cli.PrintJSON(os.Stdout, out)
	}
	fmt.Printf("%v\n", out["status"])
	return nil
}
