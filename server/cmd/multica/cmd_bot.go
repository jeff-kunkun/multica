package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// ---------------------------------------------------------------------------
// Bot commands — project-manager Bots (Grok and the like) that drive the
// office over the remote MCP endpoint. Management is workspace-owner only;
// `bot events` is the catch-up feed and works with a Bot's own token.
// ---------------------------------------------------------------------------

var botCmd = &cobra.Command{
	Use:   "bot",
	Short: "Manage project-manager Bots and their tokens",
}

var botListCmd = &cobra.Command{
	Use:   "list",
	Short: "List Bots in the workspace",
	RunE:  runBotList,
}

var botCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a Bot and print its token (shown once)",
	Long: `Create a Bot and print its token and webhook secret, shown once.

The reply also carries setup_prompt: a ready-to-forward text with the MCP
URL, the token, a self-check and the work loop. Hand it to the Bot (Grok or
any MCP client) and it connects itself. ` + "`multica bot token`" + ` returns a
fresh setup_prompt with the new token.`,
	RunE: runBotCreate,
}

var botUpdateCmd = &cobra.Command{
	Use:   "update <bot>",
	Short: "Change a Bot's name, release authority or webhook",
	Args:  exactArgs(1),
	RunE:  runBotUpdate,
}

var botTokenCmd = &cobra.Command{
	Use:   "token <bot>",
	Short: "Issue a new token for a Bot, revoking the old one (shown once)",
	Args:  exactArgs(1),
	RunE:  runBotToken,
}

var botRevokeCmd = &cobra.Command{
	Use:   "revoke <bot>",
	Short: "Revoke a Bot's token without deleting the Bot",
	Args:  exactArgs(1),
	RunE:  runBotRevoke,
}

var botDeleteCmd = &cobra.Command{
	Use:   "delete <bot>",
	Short: "Delete a Bot: revoke its token and remove it from the workspace",
	Args:  exactArgs(1),
	RunE:  runBotDelete,
}

var botEventsCmd = &cobra.Command{
	Use:   "events",
	Short: "Read workspace events after a cursor (requires a Bot token)",
	RunE:  runBotEvents,
}

func init() {
	botCmd.AddCommand(botListCmd, botCreateCmd, botUpdateCmd, botTokenCmd, botRevokeCmd, botDeleteCmd, botEventsCmd)

	botListCmd.Flags().String("output", "table", "Output format: table or json")

	botCreateCmd.Flags().String("name", "", "Bot name (required)")
	botCreateCmd.Flags().Bool("can-release", false, "Allow this Bot to approve plans for the owner")
	botCreateCmd.Flags().String("webhook-url", "", "URL that receives workspace events")
	botCreateCmd.Flags().String("webhook-key", "", "Receiver's key, sent as Authorization: Bearer with every push")
	botCreateCmd.Flags().String("output", "json", "Output format: table or json")

	botUpdateCmd.Flags().String("name", "", "New name")
	botUpdateCmd.Flags().String("can-release", "", "true or false")
	botUpdateCmd.Flags().String("webhook-url", "", "New webhook URL; pass \"none\" to stop pushing")
	botUpdateCmd.Flags().String("webhook-key", "", "New receiver key; pass \"none\" to clear it")
	botUpdateCmd.Flags().String("output", "json", "Output format: table or json")

	botTokenCmd.Flags().String("output", "json", "Output format: table or json")
	botRevokeCmd.Flags().String("output", "json", "Output format: table or json")

	botEventsCmd.Flags().Int64("after", 0, "Return events after this cursor")
	botEventsCmd.Flags().Int("limit", 0, "Max events (server cap 200)")
}

func listBots(ctx context.Context, client *cli.APIClient) ([]map[string]any, error) {
	var bots []map[string]any
	if err := client.GetJSON(ctx, "/api/bots", &bots); err != nil {
		return nil, fmt.Errorf("list bots: %w", err)
	}
	return bots, nil
}

// resolveBot accepts a Bot id, id prefix, or exact name.
func resolveBot(ctx context.Context, client *cli.APIClient, ref string) (map[string]any, error) {
	bots, err := listBots(ctx, client)
	if err != nil {
		return nil, err
	}
	var matches []map[string]any
	for _, b := range bots {
		id := strVal(b, "id")
		if id == ref || strings.EqualFold(strVal(b, "name"), ref) || (len(ref) >= 4 && strings.HasPrefix(id, ref)) {
			matches = append(matches, b)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no bot matches %q", ref)
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("%q matches %d bots; use the full id", ref, len(matches))
	}
}

func printBots(cmd *cobra.Command, bots []map[string]any) error {
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		if len(bots) == 1 {
			return cli.PrintJSON(os.Stdout, bots[0])
		}
		return cli.PrintJSON(os.Stdout, bots)
	}
	headers := []string{"ID", "NAME", "CAN RELEASE", "TOKEN", "WEBHOOK", "WEBHOOK ERROR"}
	rows := make([][]string, 0, len(bots))
	for _, b := range bots {
		release := "no"
		if v, _ := b["can_release"].(bool); v {
			release = "yes"
		}
		token := strVal(b, "token_prefix")
		if token == "" {
			token = "(revoked)"
		} else {
			token += "…"
		}
		rows = append(rows, []string{
			strVal(b, "id"), strVal(b, "name"), release, token,
			strVal(b, "webhook_url"), strVal(b, "webhook_last_error"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	if t := strVal(bots[0], "token"); t != "" && len(bots) == 1 {
		fmt.Fprintf(os.Stdout, "\nToken (shown once): %s\n", t)
		if s := strVal(bots[0], "webhook_secret"); s != "" {
			fmt.Fprintf(os.Stdout, "Webhook secret (shown once): %s\n", s)
		}
		if k := strVal(bots[0], "webhook_key"); k != "" {
			fmt.Fprintf(os.Stdout, "Webhook key: %s\n", k)
		}
		if p := strVal(bots[0], "setup_prompt"); p != "" {
			fmt.Fprintf(os.Stdout, "\nSetup prompt (forward it to the Bot as is):\n\n%s", p)
		}
	}
	return nil
}

func runBotList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	bots, err := listBots(ctx, client)
	if err != nil {
		return err
	}
	output, _ := cmd.Flags().GetString("output")
	if len(bots) == 0 && output != "json" {
		fmt.Fprintln(os.Stdout, "No bots.")
		return nil
	}
	if output == "json" {
		return cli.PrintJSON(os.Stdout, bots)
	}
	return printBots(cmd, bots)
}

func runBotCreate(cmd *cobra.Command, _ []string) error {
	name, _ := cmd.Flags().GetString("name")
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("--name is required")
	}
	canRelease, _ := cmd.Flags().GetBool("can-release")
	webhookURL, _ := cmd.Flags().GetString("webhook-url")
	webhookKey, _ := cmd.Flags().GetString("webhook-key")

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var bot map[string]any
	if err := client.PostJSON(ctx, "/api/bots", map[string]any{
		"name": name, "can_release": canRelease, "webhook_url": webhookURL, "webhook_key": webhookKey,
	}, &bot); err != nil {
		return fmt.Errorf("create bot: %w", err)
	}
	return printBots(cmd, []map[string]any{bot})
}

func runBotUpdate(cmd *cobra.Command, args []string) error {
	body := map[string]any{}
	if cmd.Flags().Changed("name") {
		v, _ := cmd.Flags().GetString("name")
		body["name"] = v
	}
	if cmd.Flags().Changed("can-release") {
		v, _ := cmd.Flags().GetString("can-release")
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("--can-release must be true or false")
		}
		body["can_release"] = b
	}
	if cmd.Flags().Changed("webhook-url") {
		v, _ := cmd.Flags().GetString("webhook-url")
		if v == "none" {
			v = ""
		}
		body["webhook_url"] = v
	}
	if cmd.Flags().Changed("webhook-key") {
		v, _ := cmd.Flags().GetString("webhook-key")
		if v == "none" {
			v = ""
		}
		body["webhook_key"] = v
	}
	if len(body) == 0 {
		return fmt.Errorf("nothing to update: pass --name, --can-release, --webhook-url or --webhook-key")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	ref, err := resolveBot(ctx, client, args[0])
	if err != nil {
		return err
	}
	var bot map[string]any
	if err := client.PatchJSON(ctx, "/api/bots/"+url.PathEscape(strVal(ref, "id")), body, &bot); err != nil {
		return fmt.Errorf("update bot: %w", err)
	}
	return printBots(cmd, []map[string]any{bot})
}

func runBotToken(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	ref, err := resolveBot(ctx, client, args[0])
	if err != nil {
		return err
	}
	var bot map[string]any
	if err := client.PostJSON(ctx, "/api/bots/"+url.PathEscape(strVal(ref, "id"))+"/token", map[string]any{}, &bot); err != nil {
		return fmt.Errorf("issue token: %w", err)
	}
	return printBots(cmd, []map[string]any{bot})
}

func runBotRevoke(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	ref, err := resolveBot(ctx, client, args[0])
	if err != nil {
		return err
	}
	var bot map[string]any
	if err := client.DeleteJSONResponse(ctx, "/api/bots/"+url.PathEscape(strVal(ref, "id"))+"/token", &bot); err != nil {
		return fmt.Errorf("revoke token: %w", err)
	}
	return printBots(cmd, []map[string]any{bot})
}

func runBotDelete(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	ref, err := resolveBot(ctx, client, args[0])
	if err != nil {
		return err
	}
	if err := client.DeleteJSON(ctx, "/api/bots/"+url.PathEscape(strVal(ref, "id"))); err != nil {
		return fmt.Errorf("delete bot: %w", err)
	}
	fmt.Fprintf(os.Stdout, "Deleted bot %s.\n", strVal(ref, "name"))
	return nil
}

func runBotEvents(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	q := url.Values{}
	if v, _ := cmd.Flags().GetInt64("after"); v > 0 {
		q.Set("after", strconv.FormatInt(v, 10))
	}
	if v, _ := cmd.Flags().GetInt("limit"); v > 0 {
		q.Set("limit", strconv.Itoa(v))
	}
	var out map[string]any
	if err := client.GetJSON(ctx, "/api/bot/events?"+q.Encode(), &out); err != nil {
		return fmt.Errorf("read events: %w", err)
	}
	return cli.PrintJSON(os.Stdout, out)
}
