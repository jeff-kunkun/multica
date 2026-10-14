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
it to this profile and switches to the workspace. --webhook-url and
--webhook-key are the Bot's own HTTPS address and key: every event and what
needs the approver's decision is pushed there — nothing to set up afterwards.
The key never expires unless --expires-in-days asks for a lifetime (0 = never).

If the answer to the final step is lost, run the same command again: as long
as the key has not been used yet, the retry hands out a fresh one and the
lost key stops working. Once the key is in use, nothing is saved and the
command says so.`,
	Example: `  multica bot connect --server-url https://ai.example.com --name Grok \
    --webhook-url https://bot.example.com/multica --webhook-key s3cret`,
	Args: exactArgs(0),
	RunE: runBotConnect,
}

func init() {
	botCmd.AddCommand(botConnectCmd)
	botConnectCmd.Flags().String("name", "Grok", "Bot name shown to the person who agrees")
	botConnectCmd.Flags().String("type", "grok", "Bot type: grok")
	botConnectCmd.Flags().String("webhook-url", "", "The Bot's own HTTPS address for messages and decisions (required)")
	botConnectCmd.Flags().String("webhook-key", "", "Shared key for that address (required)")
	botConnectCmd.Flags().Int("expires-in-days", 0, "Days until the Bot's key expires; 0 = never expires (default)")
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
	if hookURL == "" || hookKey == "" {
		return fmt.Errorf("需要 --webhook-url 和 --webhook-key：这是 Bot 自己收消息和待决定事项的 HTTPS 地址和密钥，由 Bot 自己填")
	}
	body, err := botConnectStartBody(cmd, name, botType, hookURL, hookKey)
	if err != nil {
		return err
	}

	client := cli.NewAPIClient(serverURL, "", "")
	ctx, cancel := cli.APIContext(context.Background())
	var start botConnectStart
	err = client.PostJSON(ctx, "/api/bot-connect", body, &start)
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
	if err := claim.usable(); err != nil {
		return err
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
			"receives_push": true,
		})
	}
	fmt.Fprintf(os.Stderr, "已接入工作区「%s」，钥匙已存进本机配置。\n", claim.WorkspaceName)
	if guide := strings.TrimSpace(claim.Guide); guide != "" {
		fmt.Fprintln(os.Stdout, guide)
	}
	return nil
}

// botConnectStartBody builds the ask. expires_in_days is sent only when the
// flag is given, so an older server that does not know it still accepts the
// default ask (it rejects unknown fields).
func botConnectStartBody(cmd *cobra.Command, name, botType, hookURL, hookKey string) (map[string]any, error) {
	body := map[string]any{"name": name, "type": botType, "webhook_url": hookURL, "webhook_key": hookKey}
	if cmd.Flags().Changed("expires-in-days") {
		days, _ := cmd.Flags().GetInt("expires-in-days")
		if days < 0 {
			return nil, fmt.Errorf("--expires-in-days 不能是负数；0 表示永不过期")
		}
		body["expires_in_days"] = days
	}
	return body, nil
}

// usable says whether a claim answer can be saved. "claimed" without a key
// means the key was already handed out and is in use (DENE-2009): saving it
// would wipe this profile's login and still report success.
func (c botConnectClaim) usable() error {
	switch c.Status {
	case "claimed":
	case "denied":
		return fmt.Errorf("管理员拒绝了这次接入")
	case "expired":
		return fmt.Errorf("接入码已过期；重新运行 multica bot connect")
	default:
		return fmt.Errorf("接入没有完成（%s）；重新运行 multica bot connect", c.Status)
	}
	if c.Token == "" || c.BotID == "" || c.WorkspaceID == "" {
		return fmt.Errorf("这次接入的钥匙已经领走并在使用，本机配置没有改动；如需重新接入，重新运行 multica bot connect")
	}
	return nil
}
