package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// ---------------------------------------------------------------------------
// Decisions that need a person (DENE-1933). Same server routes as the account
// settings row "把要我决定的事交给我的 Bot": bind your own Bot and see its health.
// ---------------------------------------------------------------------------

func botBindingPath(wsID string) string {
	return "/api/workspaces/" + url.PathEscape(wsID) + "/me/bot-binding"
}

var botBindCmd = &cobra.Command{
	Use:   "bind [bot]",
	Short: "Send the things that need your decision to your own Bot",
	Long: `Choose your own Bot (id, id prefix or exact name) as the place your decisions
go. Whatever needs you — an @ of you, a blocked ticket waiting on you, an
approval — is then pushed to that Bot as a human_needed event, and the
Bot must tell you. The server asks the Bot to confirm it can receive
(binding.verify); until it answers, decisions still go to the app.

If the Bot stops, its key is invalid or the webhook is removed, decisions come
back to the app and the status shows "broken". Colleagues without a Bot are not
affected.

  --mode bot_first   (default) the Bot is the channel; the app keeps a quiet,
                     read copy that turns unread if the Bot fails
  --mode mirror      the Bot and the app both alert you

Only you can bind your own Bot: not an agent, and not a Bot's own key.

Office events (per person): --activity decides whether the Bot is also told
about office events that need nobody — things that changed in what you can
see, sent for your information. Events you must hear about (a decision, a
release closed in your name) are pushed either way, and human_needed stays the
top lane. Event text never carries keys, tokens or passwords (they are masked
before sending).

  --activity everything  also push office events you can see that need nobody
                         (spelled owner_visible_all in the API)
  --activity off         only what you must hear about

owned and followed are still accepted from older scripts; they now count as
off. Binding a Bot for the first time turns everything on; leave --activity
out to accept that. Binding again, or changing other settings, keeps the
choice you already made, and --activity off stays off.

What a Bot does with your permission is audited as "Bot〈name〉代表〈you〉"
(multica bot writes). Deleting the workspace, changing members or roles,
resetting keys and changing quota or billing are held until YOU have said yes
yourself: the first attempt opens a question card in your Multica inbox (and on
your Bot), you answer it there or with multica ask answer — a Bot cannot — and
the Bot then repeats the identical call once with the header
X-Owner-Confirmation set to the confirmation id it was given. It works for that
one call, in this workspace, for 30 minutes, once.

With a binding already in place the Bot name can be left out to change only
settings: multica bot bind --activity off.`,
	Example: `  multica bot bind "Multica 管家"
  multica bot bind "Multica 管家" --mode mirror --escalate-after 600
  multica bot bind --activity everything
  multica bot bind --activity off`,
	Args:        cobra.MaximumNArgs(1),
	RunE:        runBotBind,
	Annotations: map[string]string{annotationAccess: "write", annotationBotKey: "no"},
}

var botBindStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether your decisions go to your Bot, and if the channel is healthy",
	Long: `Prints bound, state (none | unverified | ok | broken), a one-line message, the
mode, the Bot, open_count — how many decisions the Bot still owes you — and
activity_scope, whether office events that need nobody are pushed too
(owner_visible_all = on, [] = off; a first bind starts with owner_visible_all).`,
	Args:        exactArgs(0),
	RunE:        runBotBindStatus,
	Annotations: map[string]string{annotationAccess: "read", annotationBotKey: "no"},
}

var botUnbindCmd = &cobra.Command{
	Use:         "unbind",
	Short:       "Stop sending your decisions to a Bot; they come back to the app",
	Long:        "Decisions the Bot still owed you return to the app as unread. Nothing else changes.",
	Args:        exactArgs(0),
	RunE:        runBotUnbind,
	Annotations: map[string]string{annotationAccess: "write", annotationBotKey: "no"},
}

var botBindVerifyCmd = &cobra.Command{
	Use:         "verify",
	Short:       "Ask your bound Bot to confirm it can receive again",
	Args:        exactArgs(0),
	RunE:        runBotBindVerify,
	Annotations: map[string]string{annotationAccess: "write", annotationBotKey: "no"},
}

var botWritesCmd = &cobra.Command{
	Use:   "writes",
	Short: "List what Bots did with an owner's permission (Bot〈name〉代表〈owner〉)",
	Long: `Every write made with a Bot's key is recorded as "Bot〈name〉代表〈owner〉": the
route, the names of the fields it touched (never their values), the outcome, and
for the four actions that need the owner's go-ahead first (delete workspace,
change members or roles, reset keys, change quota or billing) the confirmation
the Bot gave or "blocked" when it gave none.

Owners and admins see the whole workspace; everyone else sees the writes made
in their own name.`,
	Example: `  multica bot writes
  multica bot writes --mine --output json`,
	Args:        exactArgs(0),
	RunE:        runBotWrites,
	Annotations: map[string]string{annotationAccess: "read", annotationBotKey: "no"},
}

func runBotWrites(cmd *cobra.Command, _ []string) error {
	client, wsID, ctx, cancel, err := botClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	q := url.Values{}
	if n, _ := cmd.Flags().GetInt("limit"); n > 0 {
		q.Set("limit", fmt.Sprint(n))
	}
	if b, _ := cmd.Flags().GetString("before"); b != "" {
		q.Set("before", b)
	}
	if mine, _ := cmd.Flags().GetBool("mine"); mine {
		q.Set("mine", "1")
	}
	var out map[string]any
	if err := client.GetJSON(ctx, "/api/workspaces/"+url.PathEscape(wsID)+"/bot-writes?"+q.Encode(), &out); err != nil {
		return fmt.Errorf("bot writes: %w", err)
	}
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		return cli.PrintJSON(cmd.OutOrStdout(), out)
	}
	rows, _ := out["writes"].([]any)
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		fields := ""
		if list, ok := row["fields"].([]any); ok && len(list) > 0 {
			names := make([]string, 0, len(list))
			for _, f := range list {
				names = append(names, fmt.Sprint(f))
			}
			fields = " fields=" + strings.Join(names, ",")
		}
		note := ""
		if row["outcome"] == "blocked" {
			note = " [blocked: needs owner's go-ahead]"
		} else if row["dangerous"] != nil {
			note = fmt.Sprintf(" [confirmed: %v]", row["confirmation"])
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%v  %v  %v %v%s%s\n", row["created_at"], row["label"], row["method"], row["route"], fields, note)
	}
	if next, _ := out["next_cursor"].(string); next != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "next: --before %s\n", next)
	}
	return nil
}

func init() {
	botCmd.AddCommand(botBindCmd, botBindStatusCmd, botUnbindCmd, botBindVerifyCmd, botWritesCmd)
	botWritesCmd.Flags().Int("limit", 50, "Page size (1..200)")
	botWritesCmd.Flags().String("before", "", "next_cursor from the previous page")
	botWritesCmd.Flags().Bool("mine", false, "Only writes made in your own name (admins see the whole workspace by default)")
	botWritesCmd.Flags().String("output", "table", "Output format: table or json")
	botBindCmd.Flags().String("mode", "", "bot_first (default) or mirror")
	botBindCmd.Flags().Int("resend-after", 0, "Seconds without a receipt before the event is sent again (30..3600, default 180)")
	botBindCmd.Flags().Int("escalate-after", 0, "Seconds after the Bot woke without telling you before escalating (60..7200, default 300)")
	botBindCmd.Flags().String("activity", "", "Office events that need nobody: everything or off (a first bind defaults to everything)")
	for _, c := range []*cobra.Command{botBindCmd, botBindStatusCmd, botUnbindCmd, botBindVerifyCmd} {
		c.Flags().String("output", "table", "Output format: table or json")
	}
}

func printBotBinding(cmd *cobra.Command, out map[string]any) error {
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		return cli.PrintJSON(os.Stdout, out)
	}
	fmt.Printf("State: %v\n%v\n", out["state"], out["message"])
	if out["bound"] == true {
		fmt.Printf("Bot: %v\nMode: %v\nOpen: %v\n", out["bot_name"], out["mode"], out["open_count"])
		scope := "off"
		if list, ok := out["activity_scope"].([]any); ok && len(list) > 0 {
			parts := make([]string, 0, len(list))
			for _, v := range list {
				parts = append(parts, fmt.Sprint(v))
			}
			scope = strings.Join(parts, ",")
		}
		fmt.Printf("Office events: %s\n", scope)
	}
	return nil
}

func runBotBind(cmd *cobra.Command, args []string) error {
	client, wsID, ctx, cancel, err := botClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	body := map[string]any{}
	if len(args) == 1 {
		id, err := resolveBotID(ctx, client, wsID, args[0])
		if err != nil {
			return err
		}
		body["bot_id"] = id
	}
	if raw, _ := cmd.Flags().GetString("activity"); cmd.Flags().Changed("activity") {
		scope, err := parseActivityScope(raw)
		if err != nil {
			return err
		}
		body["activity_scope"] = scope
	}
	if mode, _ := cmd.Flags().GetString("mode"); mode != "" {
		body["mode"] = mode
	}
	if n, _ := cmd.Flags().GetInt("resend-after"); n > 0 {
		body["resend_after_seconds"] = n
	}
	if n, _ := cmd.Flags().GetInt("escalate-after"); n > 0 {
		body["escalate_after_seconds"] = n
	}
	var out map[string]any
	if err := client.PutJSON(ctx, botBindingPath(wsID), body, &out); err != nil {
		return fmt.Errorf("bot bind: %w", err)
	}
	return printBotBinding(cmd, out)
}

// parseActivityScope turns the --activity value into the scope list the
// server stores: "off" (or "none", or empty) is the empty list. owned and
// followed stay accepted so older scripts keep working.
func parseActivityScope(raw string) ([]string, error) {
	scope := []string{}
	for _, part := range strings.Split(raw, ",") {
		switch p := strings.ToLower(strings.TrimSpace(part)); p {
		case "", "off", "none":
		case "owned", "followed":
			scope = append(scope, p)
		case "all", "both":
			scope = append(scope, "owned", "followed")
		case "everything", "owner_visible_all":
			scope = append(scope, "owner_visible_all")
		default:
			return nil, fmt.Errorf("--activity: %q is not everything or off", part)
		}
	}
	return scope, nil
}

func runBotBindStatus(cmd *cobra.Command, _ []string) error {
	client, wsID, ctx, cancel, err := botClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	var out map[string]any
	if err := client.GetJSON(ctx, botBindingPath(wsID), &out); err != nil {
		return fmt.Errorf("bot status: %w", err)
	}
	return printBotBinding(cmd, out)
}

func runBotUnbind(cmd *cobra.Command, _ []string) error {
	client, wsID, ctx, cancel, err := botClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	var out map[string]any
	if err := client.DeleteJSONResponse(ctx, botBindingPath(wsID), &out); err != nil {
		return fmt.Errorf("bot unbind: %w", err)
	}
	return printBotBinding(cmd, out)
}

func runBotBindVerify(cmd *cobra.Command, _ []string) error {
	client, wsID, ctx, cancel, err := botClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	var out map[string]any
	if err := client.PostJSON(ctx, botBindingPath(wsID)+"/verify", map[string]any{}, &out); err != nil {
		return fmt.Errorf("bot verify: %w", err)
	}
	return printBotBinding(cmd, out)
}
