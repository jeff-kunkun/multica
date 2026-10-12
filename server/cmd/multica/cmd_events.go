package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// ---------------------------------------------------------------------------
// The office event stream (DENE-1963): every chat answer, run ending, status
// change, delivered file and settings change in a workspace, in one durable,
// numbered stream. The same events are pushed to Bots one by one; this is the
// pull side, the receipts a Bot gives, and the delivery ledger behind the
// settings row "送达记录". Contract: server/docs/office-event-contract.md.
// ---------------------------------------------------------------------------

var eventsCmd = &cobra.Command{
	Use:   "events",
	Short: "Read the workspace event stream, its files, and what reached your Bot",
	Long: `Every chat answer, run ending, status change, delivered file and settings
change is written in the same transaction as the change itself, numbered
(stream_seq) and kept. Each event says what it means for a person:

  reply                     an answer arrived in a chat
  round_done                one run finished; the work may go on
  handoff                   handed over for review (in_review)
  task_done                 the whole ticket is finished
  release_pending_approval  routing closed a release ticket; the release is
                            NOT approved (this is not a deploy permission)
  blocked / failed          stopped, needs someone
  artifact                  a file was delivered
  change                    a setting, member or config moved (old → new,
                            credentials masked)

must_relay events are results the person must be told: a Bot closes them only
with a "relayed" receipt. Every event carries business_key, the same key the
older activity lane uses, so a Bot that gets both shows it once.

You see what you could open in the app: your chats, tickets you can see, and
settings only if you are an owner or admin.`,
}

var eventsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List events after a cursor, oldest first",
	Long: `Pages through the stream in order. Pass the next_cursor of one page as --after
of the next; a cursor never skips an event you can see. Rows you cannot see are
left out and the cursor moves past them.`,
	Example: `  multica events list
  multica events list --after 1200 --limit 100 --output json
  multica events list --issue DENE-12
  multica events list --family config`,
	Args:        exactArgs(0),
	RunE:        runEventsList,
	Annotations: map[string]string{annotationAccess: "read", annotationBotKey: "yes"},
}

var eventsGetCmd = &cobra.Command{
	Use:         "get <event-id>",
	Short:       "Show one event with its file catalogue and its Bot deliveries",
	Args:        exactArgs(1),
	RunE:        runEventsGet,
	Annotations: map[string]string{annotationAccess: "read", annotationBotKey: "yes"},
}

var eventsDownloadCmd = &cobra.Command{
	Use:   "download <event-id>",
	Short: "Download every file of an event, checking size and sha256",
	Long: `Downloads the event's ready files into -o/--output-dir (default: current
directory) and verifies each against the size and sha256 in the catalogue. Files
that are deleted or carry no checksum are listed under failures, not saved.`,
	Example:     `  multica events download 0192f1c3-... -o ./results`,
	Args:        exactArgs(1),
	RunE:        runEventsDownload,
	Annotations: map[string]string{annotationAccess: "read", annotationBotKey: "yes"},
}

var eventsReceiptCmd = &cobra.Command{
	Use:   "receipt <event-id>",
	Short: "Record a Bot's receipt for one pushed event (received / relayed / handled / relay_failed)",
	Long: `The Bot's durable acknowledgement. HTTP 200 on the webhook is not one: until a
receipt arrives the same event_id is pushed again with backoff.

  received      stored durably on the Bot side
  relayed       told the person (--channel, --ref name where)
  handled       looked, nothing to tell (--reason); refused for must_relay events
  relay_failed  could not tell the person (--error); the app shows it to them

Repeats are safe. --bot is the delivery.bot_id from the push.`,
	Example: `  multica events receipt <event-id> --bot <bot-id> --stage received
  multica events receipt <event-id> --bot <bot-id> --stage relayed --channel telegram --ref msg:4411
  multica events receipt <event-id> --bot <bot-id> --stage relayed --file <attachment-id>=sent`,
	Args:        exactArgs(1),
	RunE:        runEventsReceipt,
	Annotations: map[string]string{annotationAccess: "write", annotationBotKey: "yes"},
}

var eventsDeliveriesCmd = &cobra.Command{
	Use:   "deliveries",
	Short: "List what was pushed to your Bot: status, attempts, errors and receipts",
	Long: `Newest first. status is pending, sending, sent (HTTP 200, no receipt yet),
received, relayed, handled or blocked. A blocked row says why and what fixes it;
fix the cause, then multica events retry. Without --bot this is your own Bot.`,
	Example: `  multica events deliveries
  multica events deliveries --status blocked --output json
  multica events deliveries --bot <bot-id>`,
	Args:        exactArgs(0),
	RunE:        runEventsDeliveries,
	Annotations: map[string]string{annotationAccess: "read", annotationBotKey: "yes"},
}

var eventsRetryCmd = &cobra.Command{
	Use:   "retry",
	Short: "Send blocked or waiting deliveries again now",
	Long: `Puts blocked and still-open deliveries back in the queue with the same
event_id. A row that is blocked again (still no permission, still no channel) is
not sent: the cause has to be fixed first.`,
	Example: `  multica events retry
  multica events retry --event <event-id>`,
	Args:        exactArgs(0),
	RunE:        runEventsRetry,
	Annotations: map[string]string{annotationAccess: "write", annotationBotKey: "yes"},
}

func officeEventsPath(wsID string) string {
	return "/api/workspaces/" + url.PathEscape(wsID) + "/office-events"
}

func officeDeliveriesPath(wsID, bot string) string {
	if bot == "" {
		return "/api/workspaces/" + url.PathEscape(wsID) + "/me/office-deliveries"
	}
	return botsPath(wsID) + "/" + url.PathEscape(bot) + "/office-deliveries"
}

func wantJSON(cmd *cobra.Command) bool {
	format, _ := cmd.Flags().GetString("output")
	return format == "json"
}

func runEventsList(cmd *cobra.Command, _ []string) error {
	client, wsID, ctx, cancel, err := botClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	q := url.Values{}
	if v, _ := cmd.Flags().GetInt64("after"); v > 0 {
		q.Set("after", fmt.Sprint(v))
	}
	if v, _ := cmd.Flags().GetInt("limit"); v > 0 {
		q.Set("limit", fmt.Sprint(v))
	}
	if v, _ := cmd.Flags().GetString("family"); v != "" {
		q.Set("family", v)
	}
	if v, _ := cmd.Flags().GetString("issue"); v != "" {
		ref, err := resolveIssueRef(ctx, client, v)
		if err != nil {
			return err
		}
		q.Set("issue_id", ref.ID)
	}
	if v, _ := cmd.Flags().GetString("chat"); v != "" {
		q.Set("chat_session_id", v)
	}
	var out map[string]any
	if err := client.GetJSON(ctx, officeEventsPath(wsID)+"?"+q.Encode(), &out); err != nil {
		return fmt.Errorf("events list: %w", err)
	}
	if wantJSON(cmd) {
		return cli.PrintJSON(cmd.OutOrStdout(), out)
	}
	events, _ := out["events"].([]any)
	for _, raw := range events {
		ev, _ := raw.(map[string]any)
		flags := ""
		if ev["must_relay"] == true {
			flags += " [must_relay]"
		}
		if ev["pending_approval"] == true {
			flags += " [pending_approval]"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%v  %v  %-24v %v%s\n", ev["stream_seq"], ev["event_id"], ev["meaning"], ev["summary"], flags)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "next: --after %v\n", out["next_cursor"])
	return nil
}

func runEventsGet(cmd *cobra.Command, args []string) error {
	if _, err := uuid.Parse(args[0]); err != nil {
		return fmt.Errorf("provide the full event id")
	}
	client, wsID, ctx, cancel, err := botClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	var out map[string]any
	if err := client.GetJSON(ctx, officeEventsPath(wsID)+"/"+args[0], &out); err != nil {
		return fmt.Errorf("events get: %w", err)
	}
	return cli.PrintJSON(cmd.OutOrStdout(), out)
}

func runEventsDownload(cmd *cobra.Command, args []string) error {
	if _, err := uuid.Parse(args[0]); err != nil {
		return fmt.Errorf("provide the full event id")
	}
	client, wsID, ctx, cancel, err := botClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	var ev struct {
		Artifacts []struct {
			ID       string `json:"id"`
			Filename string `json:"filename"`
			Status   string `json:"status"`
		} `json:"artifacts"`
	}
	if err := client.GetJSON(ctx, officeEventsPath(wsID)+"/"+args[0], &ev); err != nil {
		return fmt.Errorf("events download: %w", err)
	}
	dctx, dcancel := context.WithTimeout(context.Background(), cli.AtLeastAPITimeout(10*time.Minute))
	defer dcancel()
	results := make([]attachmentDownloadResult, 0)
	failures := make([]map[string]string, 0)
	for _, a := range ev.Artifacts {
		if a.Status != "ready" {
			failures = append(failures, map[string]string{"id": a.ID, "filename": a.Filename, "error": "file is " + a.Status})
			continue
		}
		res, err := downloadAttachment(dctx, client, a.ID, cmd, "")
		if err != nil {
			failures = append(failures, map[string]string{"id": a.ID, "filename": a.Filename, "error": err.Error()})
			continue
		}
		results = append(results, res)
	}
	if err := cli.PrintJSON(os.Stdout, map[string]any{"files": results, "failures": failures}); err != nil {
		return err
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d files not downloaded; %d saved", len(failures), len(results))
	}
	return nil
}

func runEventsReceipt(cmd *cobra.Command, args []string) error {
	if _, err := uuid.Parse(args[0]); err != nil {
		return fmt.Errorf("provide the full event id")
	}
	bot, _ := cmd.Flags().GetString("bot")
	if _, err := uuid.Parse(bot); err != nil {
		return fmt.Errorf("--bot must be the full bot id (delivery.bot_id in the push)")
	}
	stage, _ := cmd.Flags().GetString("stage")
	body := map[string]any{"stage": stage}
	for flag, key := range map[string]string{"channel": "channel", "ref": "message_ref", "reason": "reason", "error": "error"} {
		if v, _ := cmd.Flags().GetString(flag); v != "" {
			body[key] = v
		}
	}
	files, _ := cmd.Flags().GetStringArray("file")
	if len(files) > 0 {
		list := make([]map[string]string, 0, len(files))
		for _, f := range files {
			id, status, ok := strings.Cut(f, "=")
			if !ok || (status != "sent" && status != "failed" && status != "skipped") {
				return fmt.Errorf("--file takes <attachment-id>=sent|failed|skipped, got %q", f)
			}
			list = append(list, map[string]string{"id": id, "status": status})
		}
		body["artifacts"] = list
	}
	client, wsID, ctx, cancel, err := botClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	var out map[string]any
	path := botsPath(wsID) + "/" + bot + "/office-events/" + args[0] + "/receipt"
	if err := client.PostJSON(ctx, path, body, &out); err != nil {
		return fmt.Errorf("events receipt: %w", err)
	}
	return cli.PrintJSON(cmd.OutOrStdout(), out)
}

func runEventsDeliveries(cmd *cobra.Command, _ []string) error {
	client, wsID, ctx, cancel, err := botClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	bot, _ := cmd.Flags().GetString("bot")
	q := url.Values{}
	if v, _ := cmd.Flags().GetString("status"); v != "" {
		q.Set("status", v)
	}
	if v, _ := cmd.Flags().GetInt("limit"); v > 0 {
		q.Set("limit", fmt.Sprint(v))
	}
	if v, _ := cmd.Flags().GetInt64("before"); v > 0 {
		q.Set("before", fmt.Sprint(v))
	}
	var out map[string]any
	if err := client.GetJSON(ctx, officeDeliveriesPath(wsID, bot)+"?"+q.Encode(), &out); err != nil {
		return fmt.Errorf("events deliveries: %w", err)
	}
	if wantJSON(cmd) {
		return cli.PrintJSON(cmd.OutOrStdout(), out)
	}
	if out["connected"] == false {
		fmt.Fprintln(cmd.OutOrStdout(), "No Bot connected (multica bot bind).")
		return nil
	}
	rows, _ := out["deliveries"].([]any)
	for _, raw := range rows {
		d, _ := raw.(map[string]any)
		ev, _ := d["event"].(map[string]any)
		line := fmt.Sprintf("%v  %-9v attempts=%v  %v", d["event_id"], d["status"], d["attempts"], ev["summary"])
		if r, _ := d["blocked_reason"].(string); r != "" {
			line += "  — " + r
		} else if e, _ := d["last_error"].(string); e != "" {
			line += "  — " + e
		}
		fmt.Fprintln(cmd.OutOrStdout(), line)
	}
	if next, _ := out["next_cursor"].(float64); next > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "next: --before %.0f\n", next)
	}
	return nil
}

func runEventsRetry(cmd *cobra.Command, _ []string) error {
	client, wsID, ctx, cancel, err := botClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	bot, _ := cmd.Flags().GetString("bot")
	body := map[string]any{}
	if v, _ := cmd.Flags().GetString("event"); v != "" {
		if _, err := uuid.Parse(v); err != nil {
			return fmt.Errorf("--event must be the full event id")
		}
		body["event_id"] = v
	}
	var out map[string]any
	if err := client.PostJSON(ctx, officeDeliveriesPath(wsID, bot)+"/retry", body, &out); err != nil {
		return fmt.Errorf("events retry: %w", err)
	}
	if wantJSON(cmd) {
		return cli.PrintJSON(cmd.OutOrStdout(), out)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Queued again: %v\n", out["requeued"])
	return nil
}

func init() {
	eventsCmd.AddCommand(eventsListCmd, eventsGetCmd, eventsDownloadCmd, eventsReceiptCmd, eventsDeliveriesCmd, eventsRetryCmd)
	rootCmd.AddCommand(eventsCmd)

	eventsListCmd.Flags().Int64("after", 0, "next_cursor from the previous page (stream_seq)")
	eventsListCmd.Flags().Int("limit", 50, "Page size (1..200)")
	eventsListCmd.Flags().String("family", "", "Only chat, issue, artifact or config")
	eventsListCmd.Flags().String("issue", "", "Only one ticket (id or identifier)")
	eventsListCmd.Flags().String("chat", "", "Only one chat (chat session id)")

	eventsDownloadCmd.Flags().StringP("output-dir", "o", ".", "Directory to save the files")
	eventsDownloadCmd.Flags().String("output-file", "", "")
	_ = eventsDownloadCmd.Flags().MarkHidden("output-file")

	eventsReceiptCmd.Flags().String("bot", "", "Bot id (delivery.bot_id in the push)")
	eventsReceiptCmd.Flags().String("stage", "", "received, relayed, handled or relay_failed")
	eventsReceiptCmd.Flags().String("channel", "", "Where the person was told (telegram, lark, ...)")
	eventsReceiptCmd.Flags().String("ref", "", "Message id on that channel")
	eventsReceiptCmd.Flags().String("reason", "", "Why nothing needed telling (handled)")
	eventsReceiptCmd.Flags().String("error", "", "Why the person could not be told (relay_failed)")
	eventsReceiptCmd.Flags().StringArray("file", nil, "Per-file outcome <attachment-id>=sent|failed|skipped (repeatable)")
	_ = eventsReceiptCmd.MarkFlagRequired("bot")
	_ = eventsReceiptCmd.MarkFlagRequired("stage")

	for _, c := range []*cobra.Command{eventsDeliveriesCmd, eventsRetryCmd} {
		c.Flags().String("bot", "", "A Bot id (needs that Bot's key, or owner/admin); default: your own Bot")
	}
	eventsDeliveriesCmd.Flags().String("status", "", "Comma-separated statuses, e.g. blocked,sent")
	eventsDeliveriesCmd.Flags().Int("limit", 50, "Page size (1..200)")
	eventsDeliveriesCmd.Flags().Int64("before", 0, "next_cursor from the previous page")
	eventsRetryCmd.Flags().String("event", "", "Only this event id")

	for _, c := range []*cobra.Command{eventsListCmd, eventsGetCmd, eventsReceiptCmd, eventsDeliveriesCmd, eventsRetryCmd} {
		c.Flags().String("output", "table", "Output format: table or json")
	}
	eventsDownloadCmd.Flags().String("output", "json", "Result format: json")
}
