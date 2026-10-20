package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// ---------------------------------------------------------------------------
// Decisions that need a person (DENE-1933). Same server routes as the account
// settings row "把要我决定的事交给我的 Bot": bind your own Bot, see its health,
// and — for the Bot — report that it told you and what you answered.
// Agents ask a person with `multica issue ask-human` / `multica chat ask-human`.
// ---------------------------------------------------------------------------

func botBindingPath(wsID string) string {
	return "/api/workspaces/" + url.PathEscape(wsID) + "/me/bot-binding"
}

var botBindCmd = &cobra.Command{
	Use:   "bind [bot]",
	Short: "Send the things that need your decision to your own Bot",
	Long: `Choose your own Bot (id, id prefix or exact name) as the place your decisions
go. Whatever needs you — an @ of you, an ask-human, a blocked ticket waiting on
you, an approval — is then pushed to that Bot as a human_needed event, and the
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

// ---- receipts, from the Bot ----

func addHumanNeededReceipts(wake *cobra.Command) {
	relayed := &cobra.Command{
		Use:   "relayed <bot-id> <event-id>",
		Short: "Record that the Bot told the person about a human_needed event",
		Long: `Send this once the person has actually been told (message sent in their chat,
not merely received by the Bot). Without it the server escalates 5 minutes
after woken and brings the call back to the app.

  --channel      where you told them (e.g. telegram, grok-chat)
  --message-ref  a reference to the message, so a missing relay can be traced`,
		Args: exactArgs(2),
		RunE: runBotHumanRelayed,
	}
	relayed.Flags().String("channel", "", "Where the person was told")
	relayed.Flags().String("message-ref", "", "Reference to the message sent")
	relayed.Flags().String("output", "json", "Output format: json")

	answered := &cobra.Command{
		Use:   "answered <bot-id> <event-id>",
		Short: "Record the person's answer to a human_needed event and pass it on",
		Long: `Report what the person answered. For a ticket the answer is posted as a
comment first (your key acts as its owner); that comment is what wakes the
asking agent and clears the block. Then the event is closed as answered.

  --answer       the person's answer, in their words
  --comment-id   use a comment you already posted instead of posting --answer
  --answers      for a question card (the event lists "ask"): question number
                 to option id, e.g. 0=approve,1=later or {"0":"approve"}

A question card is filled by --answers; the server then comments on the ticket
as the person and wakes the asking agent once, so do not comment yourself.
A chat question with choices is a card too: --answers fills it and the
chosen option is written into the chat as the person, which wakes the agent
that asked. A free-text chat question takes --answer, written the same way.`,
		Args: exactArgs(2),
		RunE: runBotHumanAnswered,
	}
	answered.Flags().String("answer", "", "The person's answer")
	answered.Flags().String("comment-id", "", "Comment you already posted as the answer")
	answered.Flags().String("answers", "", "Card answers: question number to option id (0=a,1=b or JSON)")
	answered.Flags().String("output", "json", "Output format: json")

	open := &cobra.Command{
		Use:   "human-needed <bot-id>",
		Short: "List decisions this Bot still owes its person (catch up after a restart)",
		Args:  exactArgs(1),
		RunE:  runBotHumanList,
	}
	open.Flags().String("status", "", "Comma list of statuses (default sent,woken,relayed)")
	open.Flags().Int("limit", 50, "Page size (1..200)")
	open.Flags().String("before", "", "next_cursor from the previous page")
	open.Flags().String("output", "json", "Output format: json")

	wake.AddCommand(relayed, answered, open)
}

func parseBotEvent(args []string) (string, string, error) {
	for _, a := range args {
		if _, err := uuid.Parse(a); err != nil {
			return "", "", fmt.Errorf("provide full UUIDs")
		}
	}
	return args[0], args[1], nil
}

func runBotHumanRelayed(cmd *cobra.Command, args []string) error {
	botID, eventID, err := parseBotEvent(args)
	if err != nil {
		return err
	}
	client, wsID, ctx, cancel, err := botClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	channel, _ := cmd.Flags().GetString("channel")
	ref, _ := cmd.Flags().GetString("message-ref")
	var out map[string]any
	path := botsPath(wsID) + "/" + botID + "/wake/events/" + eventID + "/relayed"
	if err := client.PostJSON(ctx, path, map[string]any{"channel": channel, "message_ref": ref}, &out); err != nil {
		return fmt.Errorf("bot wake relayed: %w", err)
	}
	return cli.PrintJSON(cmd.OutOrStdout(), out)
}

func runBotHumanAnswered(cmd *cobra.Command, args []string) error {
	botID, eventID, err := parseBotEvent(args)
	if err != nil {
		return err
	}
	answer, _ := cmd.Flags().GetString("answer")
	commentID, _ := cmd.Flags().GetString("comment-id")
	answersFlag, _ := cmd.Flags().GetString("answers")
	answers, err := parseCardAnswers(answersFlag)
	if err != nil {
		return err
	}
	if strings.TrimSpace(answer) == "" && commentID == "" && len(answers) == 0 {
		return fmt.Errorf("provide --answer, --answers (for a question card) or --comment-id")
	}
	client, wsID, ctx, cancel, err := botClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	base := botsPath(wsID) + "/" + botID
	if commentID == "" && len(answers) == 0 && strings.TrimSpace(answer) != "" {
		// A ticket event needs the answer as a comment by the person; the Bot's
		// key acts as its owner, so posting it here is what wakes the asker.
		// A card or chat event is answered by the server itself.
		ref, err := humanEventRef(ctx, client, base, eventID)
		if err != nil {
			return err
		}
		if ref.IssueID != "" && ref.AskID == "" {
			var posted map[string]any
			if err := client.PostJSON(ctx, "/api/issues/"+url.PathEscape(ref.IssueID)+"/comments", map[string]any{"content": answer}, &posted); err != nil {
				return fmt.Errorf("post the answer on the ticket: %w", err)
			}
			commentID = strVal(posted, "id")
		}
	}
	body := map[string]any{"answer": answer}
	if commentID != "" {
		body["comment_id"] = commentID
	}
	if len(answers) > 0 {
		body["answers"] = answers
	}
	var out map[string]any
	if err := client.PostJSON(ctx, base+"/wake/events/"+eventID+"/answered", body, &out); err != nil {
		return fmt.Errorf("bot wake answered: %w", err)
	}
	return cli.PrintJSON(cmd.OutOrStdout(), out)
}

// parseCardAnswers reads --answers: a JSON object, or "0=a,1=b" pairs.
func parseCardAnswers(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	out := map[string]string{}
	if strings.HasPrefix(raw, "{") {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return nil, fmt.Errorf("--answers is not valid JSON: %w", err)
		}
		return out, nil
	}
	for _, pair := range strings.Split(raw, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("--answers wants question=option pairs like 0=approve,1=later, got %q", pair)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out, nil
}

type humanEventRefs struct {
	IssueID string
	AskID   string
}

// humanEventRef finds the ticket and question card a human_needed event is
// about (both empty for a chat event), by scanning the Bot's open list.
func humanEventRef(ctx context.Context, client *cli.APIClient, base, eventID string) (humanEventRefs, error) {
	var page struct {
		Events []map[string]any `json:"events"`
	}
	if err := client.GetJSON(ctx, base+"/human-needed?status=sent,woken,relayed,queued&limit=200", &page); err != nil {
		return humanEventRefs{}, fmt.Errorf("look up the event: %w", err)
	}
	for _, e := range page.Events {
		if strVal(e, "event_id") == eventID {
			return humanEventRefs{IssueID: strVal(e, "issue_id"), AskID: strVal(e, "ask_id")}, nil
		}
	}
	return humanEventRefs{}, fmt.Errorf("event %s is not open for this Bot", eventID)
}

func runBotHumanList(cmd *cobra.Command, args []string) error {
	if _, err := uuid.Parse(args[0]); err != nil {
		return fmt.Errorf("provide the full bot UUID")
	}
	client, wsID, ctx, cancel, err := botClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	q := url.Values{}
	if s, _ := cmd.Flags().GetString("status"); s != "" {
		q.Set("status", s)
	}
	if n, _ := cmd.Flags().GetInt("limit"); n > 0 {
		q.Set("limit", fmt.Sprint(n))
	}
	if b, _ := cmd.Flags().GetString("before"); b != "" {
		q.Set("before", b)
	}
	var out map[string]any
	if err := client.GetJSON(ctx, botsPath(wsID)+"/"+args[0]+"/human-needed?"+q.Encode(), &out); err != nil {
		return fmt.Errorf("bot wake human-needed: %w", err)
	}
	return cli.PrintJSON(cmd.OutOrStdout(), out)
}

// ---- asking a person ----

const askHumanLong = `Ask one person a question that only they can decide, and get it in front of
them for real. If they connected a Bot, the server pushes it to that Bot (the
Bot must tell them; if it does not, the server escalates and falls back to the
app). If not, it lands in their inbox and is @-ed on the ticket as before.
Do not @ a person in a comment to ask for a decision: use this.

  --to        a name, email, user id, or "owner" (the workspace owner)
  --question  one sentence: what they must decide
  --option    an allowed answer; repeat for several (at most 8)
  --urgent    ignore the Bot's night hours; push now
  --deadline  RFC3339 time the answer is needed by

Their answer wakes you and clears the block.`

var issueAskHumanCmd = &cobra.Command{
	Use:   "ask-human <id>",
	Short: "Ask a person to decide: pushed to their Bot if they have one, blocks the issue until they answer",
	Long: askHumanLong + `

The issue is set to blocked with that person as the next owner. With two or
more --option the question is also a card (the output shows ask_id); answering
the card, in the app or through their Bot, unblocks the issue and wakes you
once. Without options it is a free-text question answered by a comment.`,
	Example: `  multica issue ask-human DENE-12 --to owner --question "上线还是再等一天？" --option 现在上线 --option 明天上线
  multica issue ask-human DENE-12 --to 张三 --question "需要你批准这笔费用" --urgent --output json`,
	Args: exactArgs(1),
	RunE: runIssueAskHuman,
}

var chatAskHumanCmd = &cobra.Command{
	Use:   "ask-human",
	Short: "Ask the person you are chatting with to decide, through their Bot if they have one",
	Long: askHumanLong + `

In a chat the person asked is the chat's owner; --to is not needed. The
question is written into the chat as a message. With 2 to 6 --option values it
is also a question card (the same one the inbox, 'multica ask' and the Bot
fill in); whoever answers it, the choice is written back into the chat as the
person, which wakes you once. With a Bot it is also pushed there. Without a
Bot, or when the Bot is unreachable, they see it in the chat and in their
inbox. Free-text questions (no options) stay a chat message.`,
	Example: `  multica chat ask-human --question "用哪个域名？" --option a.example.com --option b.example.com`,
	Args:    exactArgs(0),
	RunE:    runChatAskHuman,
}

func init() {
	issueCmd.AddCommand(issueAskHumanCmd)
	issueAskHumanCmd.Flags().String("to", "", "the person: name, email, user id or owner (required)")
	chatCmd.AddCommand(chatAskHumanCmd)
	chatAskHumanCmd.Flags().String("session", "", "Chat session id or URL (defaults to MULTICA_CHAT_SESSION_ID)")
	for _, c := range []*cobra.Command{issueAskHumanCmd, chatAskHumanCmd} {
		c.Flags().String("question", "", "what they must decide (required)")
		c.Flags().StringArray("option", nil, "an allowed answer; repeat for several")
		c.Flags().Bool("urgent", false, "push now, ignoring the Bot's night hours")
		c.Flags().String("deadline", "", "RFC3339 time the answer is needed by")
		c.Flags().String("output", "table", "Output format: table or json")
	}
}

func askHumanBody(cmd *cobra.Command) (map[string]any, error) {
	question, _ := cmd.Flags().GetString("question")
	if strings.TrimSpace(question) == "" {
		return nil, fmt.Errorf("--question is required: what they must decide")
	}
	opts, _ := cmd.Flags().GetStringArray("option")
	options := make([]map[string]any, 0, len(opts))
	for i, o := range opts {
		if strings.TrimSpace(o) == "" {
			continue
		}
		options = append(options, map[string]any{"id": fmt.Sprint(i + 1), "label": o})
	}
	urgent, _ := cmd.Flags().GetBool("urgent")
	deadline, _ := cmd.Flags().GetString("deadline")
	return map[string]any{"question": question, "options": options, "urgent": urgent, "deadline": deadline}, nil
}

func printAskHuman(cmd *cobra.Command, out map[string]any) error {
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		return cli.PrintJSON(os.Stdout, out)
	}
	fmt.Printf("Asked: %v\nVia Bot: %v\n", out["recipient_name"], out["via_bot"])
	if id := strVal(out, "ask_id"); id != "" {
		fmt.Printf("Card: %s\n", id)
	}
	if lines, ok := out["result"].([]any); ok {
		for _, l := range lines {
			fmt.Printf("- %v\n", l)
		}
	}
	return nil
}

func runIssueAskHuman(cmd *cobra.Command, args []string) error {
	to, _ := cmd.Flags().GetString("to")
	if strings.TrimSpace(to) == "" {
		return fmt.Errorf("--to is required: the person to ask (or owner)")
	}
	body, err := askHumanBody(cmd)
	if err != nil {
		return err
	}
	body["to"] = to
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	ref, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}
	var out map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+url.PathEscape(ref.ID)+"/ask-human", body, &out); err != nil {
		return fmt.Errorf("ask-human: %w", err)
	}
	return printAskHuman(cmd, out)
}

func runChatAskHuman(cmd *cobra.Command, _ []string) error {
	session, _ := cmd.Flags().GetString("session")
	if strings.TrimSpace(session) == "" {
		session = os.Getenv("MULTICA_CHAT_SESSION_ID")
	}
	ref, err := parseChatSessionLinkRef(session)
	if err != nil {
		return fmt.Errorf("chat ask-human: --session is required (or set MULTICA_CHAT_SESSION_ID): %w", err)
	}
	body, err := askHumanBody(cmd)
	if err != nil {
		return err
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var out map[string]any
	if err := client.PostJSON(ctx, "/api/chat/sessions/"+url.PathEscape(ref.ID)+"/ask-human", body, &out); err != nil {
		return fmt.Errorf("ask-human: %w", err)
	}
	return printAskHuman(cmd, out)
}
