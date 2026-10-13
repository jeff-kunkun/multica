package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// `multica bot connect` (DENE-2009): run by the Bot itself. It asks to
// connect, prints the link for a person to agree, waits, then saves the key it
// is handed and switches to the workspace — the person never sees a key.

var botConnectCmd = &cobra.Command{
	Use:   "connect",
	Short: "Run by a Bot: ask to connect, wait for one yes, then log in by itself",
	Long: `Run on the Bot's own machine, without a key. Prints a link; a workspace
owner or admin opens it and agrees once. The Bot then receives its key, saves
it to this profile and switches to the workspace. With --webhook-url and
--webhook-key the Bot is also woken at that address for every event, and what
needs the approver's decision goes to it — nothing to set up afterwards.`,
	Example: `  multica bot connect --server-url https://ai.ferryway.cc --name Grok
  multica bot connect --server-url https://ai.ferryway.cc --name Grok \
    --webhook-url https://bot.example.com/multica --webhook-key s3cret`,
	Args: exactArgs(0),
	RunE: runBotConnect,
}

func init() {
	botCmd.AddCommand(botConnectCmd)
	botConnectCmd.Flags().String("name", "Grok", "Bot name shown to the person who agrees")
	botConnectCmd.Flags().String("type", "grok", "Bot type: grok")
	botConnectCmd.Flags().String("webhook-url", "", "Address the Bot is woken at (optional)")
	botConnectCmd.Flags().String("webhook-key", "", "Shared key for that address (required with --webhook-url)")
	botConnectCmd.Flags().Duration("wait", 15*time.Minute, "How long to wait for the yes")
	botConnectCmd.Flags().String("output", "text", "Output format: text or json")
}

type botConnectStart struct {
	Code                string `json:"code"`
	ClaimToken          string `json:"claim_token"`
	ApproveURL          string `json:"approve_url"`
	ExpiresAt           string `json:"expires_at"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
}

type botConnectClaim struct {
	Status        string `json:"status"`
	Token         string `json:"token"`
	BotID         string `json:"bot_id"`
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
	ServerURL     string `json:"server_url"`
	AppURL        string `json:"app_url"`
	Guide         string `json:"guide"`
}

func runBotConnect(cmd *cobra.Command, _ []string) error {
	if inDaemonManagedExecutionContext() {
		return fmt.Errorf("`multica bot connect` is run by the Bot on its own machine, not inside an agent task")
	}
	serverURL := resolveLoginTokenServerURL(cmd)
	name, _ := cmd.Flags().GetString("name")
	botType, _ := cmd.Flags().GetString("type")
	hookURL, _ := cmd.Flags().GetString("webhook-url")
	hookKey, _ := cmd.Flags().GetString("webhook-key")
	wait, _ := cmd.Flags().GetDuration("wait")
	output, _ := cmd.Flags().GetString("output")
	if (hookURL == "") != (hookKey == "") {
		return fmt.Errorf("--webhook-url and --webhook-key go together")
	}

	client := cli.NewAPIClient(serverURL, "", "")
	ctx, cancel := cli.APIContext(context.Background())
	var start botConnectStart
	err := client.PostJSON(ctx, "/api/bot-connect", map[string]string{
		"name": name, "type": botType, "webhook_url": hookURL, "webhook_key": hookKey,
	}, &start)
	cancel()
	if err != nil {
		return fmt.Errorf("ask to connect: %w", err)
	}
	fmt.Fprintf(os.Stderr, "请把这个链接发给工作区管理员，点一次「同意」就接好了：\n  %s\n接入码 %s，%s 前有效。等待同意…\n", start.ApproveURL, start.Code, start.ExpiresAt)

	interval := time.Duration(start.PollIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 3 * time.Second
	}
	deadline := time.Now().Add(wait)
	var claim botConnectClaim
	for {
		ctx, cancel := cli.APIContext(context.Background())
		err := client.PostJSON(ctx, "/api/bot-connect/claim", map[string]string{
			"code": start.Code, "claim_token": start.ClaimToken,
		}, &claim)
		cancel()
		if err == nil && claim.Status != "pending" {
			break
		}
		var httpErr *cli.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
			return fmt.Errorf("接入申请不见了（接入码 %s）；重新运行 multica bot connect", start.Code)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("没等到同意（接入码 %s）；重新运行 multica bot connect", start.Code)
		}
		time.Sleep(interval)
	}
	switch claim.Status {
	case "claimed":
	case "denied":
		return fmt.Errorf("管理员拒绝了这次接入")
	case "expired":
		return fmt.Errorf("接入码已过期；重新运行 multica bot connect")
	default:
		return fmt.Errorf("接入没有完成（%s）；重新运行 multica bot connect", claim.Status)
	}

	profile := resolveProfile(cmd)
	cfg, _ := cli.LoadCLIConfigForProfile(profile)
	cfg.Token = claim.Token
	cfg.ServerURL = serverURL
	if claim.ServerURL != "" {
		cfg.ServerURL = normalizeAPIBaseURL(claim.ServerURL)
	}
	if claim.AppURL != "" {
		cfg.AppURL = claim.AppURL
	}
	cfg.WorkspaceID = claim.WorkspaceID
	if err := cli.SaveCLIConfigForProfile(cfg, profile); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	if output == "json" {
		return cli.PrintJSON(os.Stdout, map[string]any{
			"status": "connected", "bot_id": claim.BotID,
			"workspace_id": claim.WorkspaceID, "workspace_name": claim.WorkspaceName,
			"receives_push": hookURL != "",
		})
	}
	fmt.Fprintf(os.Stderr, "已接入工作区「%s」，钥匙已存进本机配置。\n", claim.WorkspaceName)
	if guide := strings.TrimSpace(claim.Guide); guide != "" {
		fmt.Fprintln(os.Stdout, guide)
	}
	return nil
}
