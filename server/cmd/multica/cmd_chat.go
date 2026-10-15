package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/multica-ai/multica/server/internal/listshape"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var chatCmd = &cobra.Command{
	Use:   "chat",
	Short: "Read a chat conversation, or send to one",
}

var chatStopCmd = &cobra.Command{
	Use:   "stop [session-id|url]",
	Short: "Stop all queued and active runs in a chat",
	Long:  "Stop a chat's active, queued and deferred runs. With --related, also stop its source/spawned chats, bound issues and descendants. Every related chat must belong to the requesting person; every issue and private agent requires access. New human messages may start work again.",
	Args:  cobra.MaximumNArgs(1), RunE: runChatStop,
}

var chatClearQueueCmd = &cobra.Command{
	Use:   "clear-queue [session-id|url]",
	Short: "Drop the messages queued behind the current reply, keeping the reply",
	Long: `Cancel every message queued behind the reply in progress and remove them from
the chat; the reply itself keeps running. To stop the reply too and mark the
dropped messages "superseded" instead of removing them, send the new
instruction with multica chat send --mode replace.`,
	Example: "  multica chat clear-queue --session <id|url>",
	Args:    cobra.MaximumNArgs(1), RunE: runChatClearQueue,
}

var chatListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List chats visible to the task initiator (with --linked: only their own source chats)",
	Example: "  multica chat list --linked studio --output json",
	Args:    cobra.NoArgs,
	RunE:    runChatDirectoryList,
}

var chatSearchCmd = &cobra.Command{
	Use:   "search <词>",
	Short: "Search visible chat titles and messages",
	Args:  cobra.ExactArgs(1),
	RunE:  runChatDirectorySearch,
}

var chatHistoryCmd = &cobra.Command{
	Use:   "history",
	Short: "Overview of the channel this conversation is in (messages + thread list)",
	Long: `Show the overview of the chat channel (e.g. Slack) this conversation is in: the
recent top-level messages, and for each thread its thread_id, reply_count, and
latest_reply. It does NOT expand thread contents — it is the table of contents.

To read a specific thread's messages, take a thread_id from here and run
"multica chat thread <thread_id>".

It is the SAME command regardless of which channel the conversation came from.

Pass --session <url|id> to read a different chat in this workspace instead of
the one you are running in. The link looks like …/chat/<session-id> (an older
?session=<id> link works too). The default is a short summary plus the latest
messages, not the full transcript. hidden_queued_count is the session-wide number
of queued user messages omitted from this transcript (not an older-page count). Page older messages with --before
<next_cursor>.

When the user says "接管这个：<link>", run:
  multica chat history --session <link> --output json
You can only read a session in this workspace that this person is allowed to
open. Anyone outside the workspace gets nothing.`,
	Args: cobra.NoArgs,
	RunE: runChatHistory,
}

var chatThreadCmd = &cobra.Command{
	Use:   "thread [id]",
	Short: "Read one thread's messages (the current thread, or a specific id)",
	Long: `Read the messages of a single thread.

With no id, read the thread you are currently in (the one you were @mentioned in).
With an id — a thread_id from "multica chat history" — read that specific thread.
Either way the thread is within the channel you are in.

Pass --session <url|id> (and no thread id) to read another chat session's
transcript — the same summary-plus-latest-page read as "chat history
--session". A web chat is a single thread, so there is no separate thread id
to pass.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runChatThread,
}

var chatProgressCmd = &cobra.Command{
	Use:   "progress [text]",
	Short: "Report or read the progress line under a chat's title",
	Long: `Report where this chat stands — the second line under its title in the
chat list and header. Your own line wins over the automatic summary the
platform writes after each reply.

  multica chat progress "Fixed the redirect; waiting for you to retest"
  multica chat progress "Need the API key to continue" --tone waiting
  multica chat progress --history

--tone sets the dot colour: working (blue, default), waiting (yellow),
stuck (red), done (green). --history lists earlier lines, newest first, plus
task_status, reason, queue_ahead (turns before this one), and runtime
(status, name, last_seen_at). Reasons include runtime_offline, running_no_logs,
and queued_behind_N.

  multica chat progress --session <session-id> --history --output json`,
	Args: cobra.MaximumNArgs(1),
	RunE: runChatProgress,
}

var chatTicketsCmd = &cobra.Command{
	Use:   "tickets",
	Short: "List the issues a chat opened or follows",
	Long: `List a chat's tickets, in the order they joined it, each with its source:

  created  opened from this chat — by its agent's "issue create" or
           "plan apply"
  auto     followed: this chat's run changed its status or assignee,
           commented on it, handed it off or closed it (naming an issue in a
           reply does not count)
  manual   pinned by hand ("multica chat tickets add" or the chat bar)

Each ticket carries status, assignee and the first line of its 目标 section,
plus its latest status move (from_status, changed_at) and phase — waiting_you,
in_progress or done, bucketed as "multica project report" does. A ticket that
is done, blocked, cancelled or in review also carries its result: the close's
summary, PRs and knowledge (summary, pull_requests, knowledge). The chat shows
the same list as its ticket cards and progress bar. An issue names the chat it
was opened from as source_chat in "multica issue get"; following never moves
that.

  multica chat tickets
  multica chat tickets --session <id|url> --output json
  multica chat tickets add DENE-12
  multica chat tickets remove DENE-12
`,
	Args: cobra.NoArgs,
	RunE: runChatTickets,
}

var chatTicketsAddCmd = &cobra.Command{
	Use:   "add <issue>",
	Short: "Pin an issue to a chat's tickets",
	Long: `Pin an issue to a chat's ticket list by hand (source manual). Also undoes
an earlier "remove". The chat bar's 挂上 does the same.

  multica chat tickets add DENE-12
  multica chat tickets add DENE-12 --session <id|url>
`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error { return runChatTicketsPin(cmd, args[0], false) },
}

var chatTicketsRemoveCmd = &cobra.Command{
	Use:   "remove <issue>",
	Short: "Take an issue off a chat's tickets",
	Long: `Take an issue off a chat's ticket list, whatever its source. The issue is
untouched, and the chat's run acting on it again does not bring it back; only
"add" does. The ticket card's 取下 does the same.

  multica chat tickets remove DENE-12
`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error { return runChatTicketsPin(cmd, args[0], true) },
}

var chatTitleCmd = &cobra.Command{
	Use:   "title <Project · topic>",
	Short: "Report the title for the current chat",
	Long: `Report a chat title from the agent runtime. The server validates the
Project · topic shape and refuses to overwrite a title a member has renamed.

  multica chat title "Billing · retry invoices"
`,
	Args: cobra.ExactArgs(1),
	RunE: runChatTitle,
}

var chatOpenCmd = &cobra.Command{
	Use:   "open",
	Short: "Open a new chat with another agent from the current chat",
	Long: `Open a new chat with an agent and send it a brief as the first message.
The server creates the chat, records which chat it came from, and posts a
card in the current chat linking to it, all in one step.

Only a chat run can open a chat (a task run gets chat_spawn_task_mode: use
"multica issue create" instead). A chat opened this way cannot open another
one, and its visibility and project can only be narrower than the current
chat. The workspace caps how many chats each chat and each run may open.

Refusals come back as JSON with a code:
  agent_spawn_disabled, agent_spawn_budget_exceeded, chat_spawn_depth_exceeded,
  chat_spawn_task_mode, chat_spawn_scope_exceeded, chat_spawn_agent_only

Retrying with the same --client-key (default: derived from agent, title and
brief) returns the chat already opened instead of a second one.

  multica chat open --agent 布尔玛 --brief-file ./brief.md --title "Billing · retry design"
`,
	Args: cobra.NoArgs,
	RunE: runChatOpen,
}

var chatSendCmd = &cobra.Command{
	Use:   "send [text]",
	Short: "Send a message to a chat, steering, queueing, restarting or replacing its reply",
	Long: `Send a message to a chat as if typed in its composer. --mode decides what
happens when the agent is still replying (ignored when it is not):

  steer    read it after the current step, keep working on the original ask
           (same process, same session on Claude, Codex, Grok, OpenCode 1.x
           and Pi; Cursor, Copilot, CodeArts, DevEco, Antigravity and OpenClaw
           restart the CLI on the same session to read it right away)
  queue    explicitly answer it after this reply finishes (new process, same session)
  restart  stop the reply now and start over from this message (new process,
           same session; the half-done step is dropped)
  replace  the person changed their mind: cancel every queued older message,
           stop the reply now, mark all of them "superseded" (the chat shows
           "replaced by a newer instruction") and start over from this message.
           Use it, not queue or restart, when the new message voids earlier
           ones. With nothing running it still voids the queued ones. JSON adds
           new_task_id, cancelled_task_ids and superseded_message_ids. Only the
           chat's creator or an agent may send it.

Without --mode, merge into the queued or active run instead of creating another
run. Queued messages form one input batch; an active reply receives the message
as a supplement. A CLI without live-input support returns a refusal with the
reason and explicit queue/restart choices; nothing is sent.

The response includes runtime (status, name, last_seen_at). Offline sends are
accepted with warning in JSON and stderr. With --wait, timeout exits nonzero
and returns task_status, reason and queue_ahead plus runtime last_seen_at.
Reasons include runtime_offline, running_no_logs and queued_behind_N.

  multica chat send --session <session-id> "Hello" --wait --wait-timeout 20s --output json

Bot wake-chain admission defaults to four unacknowledged triggers. A blocked
send saves the message, prints its receipt, and exits nonzero with the reason;
do not resend it. A genuine agent reply or completed run resets the chain.
Human messages and server-verified --per-quote messages are exempt.
Read or change the limit with multica settings get/set bot_wake_chain_limit.

With --linked <source>, a viewer agent can send to the run initiator’s own chat
through a managed link. Messages carry a forwarded-by label and enter the
receiver brief as external content. Only queued text is supported; --wait is unavailable.
JSON includes message_id, chat_session_id and linked_delivery provenance. A 403 means
access was refused; inspect multica workspace link view <link-id>.

  multica chat send --linked studio --session 019abcde-1234-7000-8000-000000000001 --content-file note.md --output json

  multica chat send --session <id|url> "Use the staging database instead" --mode steer
  multica chat send --session <id|url> --content-file ./msg.md --mode queue
  multica chat send --session <id|url> "算了，改成只改文案" --mode replace --wait
  multica chat send --session <id|url> "再补一句" --wait   # print the agent's reply

--urgent is for messages that are worthless late (a verification code, a
relayed SMS). It never queues behind an offline machine: if the agent's
runtime cannot claim right now, the send is refused with reason
"runtime_offline: <runtime> last_seen <time>" and next_steps, unless the agent
has a fallback runtime (multica agent update --fallback-runtime) that is
online — then the turn runs there and JSON carries fallback_runtime=true. An
accepted urgent turn restarts any reply in progress (so only the chat's
creator or an agent may send one), sits at the head of the queue, and is
failed with failure_reason=pickup_timeout plus a chat notice when no daemon
starts it by pickup_deadline_at (30s). --urgent always waits for that pickup
result and never longer than pickup_deadline_at: it returns as soon as the
turn is picked up (task_status=running, pickup_elapsed_ms) and exits nonzero
with task_status and reason (runtime_offline, pickup_timeout) when it is
refused or nobody starts it by the deadline. If the server has not judged the
turn when the deadline passes, the command stops waiting on its own:
task_status is whatever the server last reported (possibly still dispatched)
and reason_source=sender marks that the sender gave up; reason_source=server
marks a verdict the server wrote. --wait adds nothing and --wait-timeout does
not apply; read the reply later with multica chat history.

  multica chat send --session <id|url> --urgent "验证码 482913，请立刻填入" --output json
`,
	Args: cobra.MaximumNArgs(1),
	RunE: runChatSend,
}

var chatHandoffCmd = &cobra.Command{
	Use:   "handoff",
	Short: "Hand a chat to an agent: a new chat that opens with this one's summary",
	Long: `Hand a chat to an agent (the chat's own agent too, for a fresh start). The
server opens a new chat with --to whose
first message summarises this one (title, opening line, latest messages, and
how to read the full history), and starts that agent's reply. The old chat is
left as it is. Only the chat's owner can hand it over.

  multica chat handoff --to 孙悟饭
  multica chat handoff --session <id|url> --to <agent-name|id> --output table
`,
	Args: cobra.NoArgs,
	RunE: runChatHandoff,
}

func init() {
	for _, c := range []*cobra.Command{chatListCmd, chatSearchCmd} {
		c.Flags().String("project", "", "Filter to a project UUID, prefix or exact name (case-insensitive; defaults to the current project)")
		addListShapeFlags(c)
		c.Example = "  multica chat list --project \"My project\" --compact --output json\n  multica chat list --fields id,title,status --workspace \"My workspace\""
		c.Flags().Bool("all-projects", false, "Search across every project visible to the task initiator")
		c.Flags().String("since", "", "Only chats active since an RFC3339 timestamp")
		c.Flags().String("output", "json", "Output format: table or json")
	}
	chatCmd.AddCommand(chatStopCmd)
	chatCmd.AddCommand(chatClearQueueCmd)
	chatClearQueueCmd.Flags().String("session", "", "Chat session id or URL (defaults to MULTICA_CHAT_SESSION_ID)")
	chatClearQueueCmd.Flags().String("output", "json", "Output format: table or json")
	chatStopCmd.Flags().Bool("related", false, "Also stop related issues and chats (every target requires access)")
	chatStopCmd.Flags().String("session", "", "Chat session id or URL (defaults to MULTICA_CHAT_SESSION_ID)")
	chatStopCmd.Flags().String("output", "json", "Output format: table or json")
	chatCmd.AddCommand(chatListCmd)
	chatCmd.AddCommand(chatSearchCmd)
	for _, c := range []*cobra.Command{chatHistoryCmd, chatThreadCmd} {
		c.Flags().Int("limit", 0, "Maximum number of messages to return (the server clamps the range)")
		c.Flags().String("before", "", "Opaque cursor (a next_cursor from a prior page) to read older messages")
		c.Flags().String("session", "", "Read this chat instead of the current one: a session id or an internal URL (…/chat/<session-id> or ?session=<id>)")
		c.Flags().String("output", "json", "Output format: table or json")
	}
	chatCmd.AddCommand(chatHistoryCmd)
	chatCmd.AddCommand(chatThreadCmd)
	chatCmd.AddCommand(chatProgressCmd)
	chatCmd.AddCommand(chatTitleCmd)
	chatCmd.AddCommand(chatTicketsCmd)
	chatTicketsCmd.Flags().String("session", "", "Chat session id or URL (defaults to MULTICA_CHAT_SESSION_ID)")
	chatTicketsCmd.Flags().String("output", "table", "Output format: table or json")
	chatTicketsCmd.AddCommand(chatTicketsAddCmd, chatTicketsRemoveCmd)
	for _, c := range []*cobra.Command{chatTicketsAddCmd, chatTicketsRemoveCmd} {
		c.Flags().String("session", "", "Chat session id or URL (defaults to MULTICA_CHAT_SESSION_ID)")
		c.Flags().String("output", "table", "Output format: table or json")
	}
	chatCmd.AddCommand(chatOpenCmd)
	chatCmd.AddCommand(chatSendCmd)
	addLinkedFlag(chatListCmd, chatSendCmd)
	chatCmd.AddCommand(chatHandoffCmd)
	chatHandoffCmd.Flags().String("session", "", "Chat session id or URL (defaults to MULTICA_CHAT_SESSION_ID)")
	chatHandoffCmd.Flags().String("to", "", "Agent to hand the chat to: name or id (required)")
	chatHandoffCmd.Flags().String("output", "json", "Output format: table or json")
	chatSendCmd.Flags().String("session", "", "Chat session id or URL (required)")
	chatSendCmd.Flags().String("content-file", "", "Read the message from this file instead of the argument")
	chatSendCmd.Flags().String("mode", "", "Default: merge into existing work; choose steer, queue, restart or replace explicitly")
	chatSendCmd.Flags().String("per-quote", "", "Exact words from this caller’s earlier human message in this chat; server verifies the quote before exempting it from the Bot wake-chain limit")
	chatSendCmd.Flags().Bool("urgent", false, "Deliver now or fail now: refused (exit nonzero, JSON reason + next_steps) when the agent's runtime is offline and no online fallback runtime is configured; otherwise goes to the head of the queue (restart semantics) and waits for the pickup: exit 0 once a daemon starts it (task_status=running), nonzero with reason=pickup_timeout as soon as the server's 30s deadline (pickup_deadline_at) passes without one, never later; task_status then reports what the server last said. --wait-timeout does not apply.")
	chatSendCmd.Flags().String("output", "json", "Output format: table or json")
	chatSendCmd.Flags().StringSlice("attachment", nil, "Local files to attach to this message (repeatable)")
	chatSendCmd.Flags().Bool("allow-external-file", false, "Allow attachment paths outside the working directory")
	chatOpenCmd.Flags().String("agent", "", "Agent to talk to: name or id (required)")
	chatOpenCmd.Flags().String("brief-file", "", "File holding the first message for the agent (required)")
	chatOpenCmd.Flags().String("title", "", "Chat title, Project · topic")
	chatOpenCmd.Flags().String("project", "", "Project id or prefix (must be the current chat's project, or narrower)")
	chatOpenCmd.Flags().String("visibility", "", "private or workspace (cannot be wider than the current chat)")
	chatOpenCmd.Flags().String("client-key", "", "Idempotency key; defaults to a hash of agent, title and brief")
	chatOpenCmd.Flags().String("output", "json", "Output format: table or json")
	chatProgressCmd.Flags().String("session", "", "Chat session id or URL (defaults to MULTICA_CHAT_SESSION_ID)")
	chatProgressCmd.Flags().String("output", "json", "Output format: table or json")
	chatProgressCmd.Flags().String("tone", "", "Dot colour: working, waiting, stuck, or done (default working)")
	chatProgressCmd.Flags().Bool("history", false, "List earlier progress lines instead of reporting one")
	chatTitleCmd.Flags().String("session", "", "Chat session id or URL (defaults to MULTICA_CHAT_SESSION_ID)")
	chatTitleCmd.Flags().String("output", "json", "Output format: table or json")
}

func runChatTitle(cmd *cobra.Command, args []string) error {
	session, _ := cmd.Flags().GetString("session")
	if strings.TrimSpace(session) == "" {
		session = os.Getenv("MULTICA_CHAT_SESSION_ID")
	}
	ref, err := parseChatSessionLinkRef(session)
	if err != nil {
		return fmt.Errorf("chat title: --session is required (or set MULTICA_CHAT_SESSION_ID): %w", err)
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var out map[string]any
	path := "/api/chat/sessions/" + url.PathEscape(ref.ID) + "/title"
	if err := client.PostJSON(ctx, path, map[string]any{"title": args[0]}, &out); err != nil {
		return fmt.Errorf("report chat title: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		fmt.Printf("Title: %v\n", out["title"])
		return nil
	}
	return cli.PrintJSON(os.Stdout, out)
}

func runChatTickets(cmd *cobra.Command, _ []string) error {
	session, _ := cmd.Flags().GetString("session")
	if strings.TrimSpace(session) == "" {
		session = os.Getenv("MULTICA_CHAT_SESSION_ID")
	}
	ref, err := parseChatSessionLinkRef(session)
	if err != nil {
		return fmt.Errorf("chat tickets: --session is required (or set MULTICA_CHAT_SESSION_ID): %w", err)
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var out struct {
		ChatSessionID string           `json:"chat_session_id"`
		Tickets       []map[string]any `json:"tickets"`
	}
	if err := client.GetJSON(ctx, "/api/chat/sessions/"+url.PathEscape(ref.ID)+"/tickets", &out); err != nil {
		return fmt.Errorf("list chat tickets: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output != "table" {
		return cli.PrintJSON(os.Stdout, out)
	}
	if len(out.Tickets) == 0 {
		fmt.Println("This chat has no tickets: it has not opened or followed any issue.")
		return nil
	}
	headers := []string{"IDENTIFIER", "SOURCE", "STATUS", "PHASE", "ASSIGNEE", "TITLE", "RESULT"}
	rows := make([][]string, 0, len(out.Tickets))
	for _, t := range out.Tickets {
		assignee, _ := t["assignee_name"].(string)
		if assignee == "" {
			assignee = "-"
		}
		phase, _ := t["phase"].(string)
		if phase == "" {
			phase = "-"
		}
		source, _ := t["source"].(string)
		if source == "" {
			source = "-"
		}
		rows = append(rows, []string{fmt.Sprint(t["identifier"]), source, fmt.Sprint(t["status"]), phase, assignee, fmt.Sprint(t["title"]), chatTicketResult(t)})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// runChatTicketsPin is "chat tickets add|remove": the same server write the
// chat bar's 挂上 / 取下 uses.
func runChatTicketsPin(cmd *cobra.Command, issueArg string, remove bool) error {
	verb := "add"
	if remove {
		verb = "remove"
	}
	session, _ := cmd.Flags().GetString("session")
	if strings.TrimSpace(session) == "" {
		session = os.Getenv("MULTICA_CHAT_SESSION_ID")
	}
	ref, err := parseChatSessionLinkRef(session)
	if err != nil {
		return fmt.Errorf("chat tickets %s: --session is required (or set MULTICA_CHAT_SESSION_ID): %w", verb, err)
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	issueRef, err := resolveIssueRef(ctx, client, issueArg)
	if err != nil {
		return fmt.Errorf("resolve issue %s: %w", issueArg, err)
	}
	path := "/api/chat/sessions/" + url.PathEscape(ref.ID) + "/tickets"
	if remove {
		err = client.DeleteJSON(ctx, path+"/"+url.PathEscape(issueRef.ID))
	} else {
		err = client.PostJSON(ctx, path, map[string]any{"issue": issueRef.ID}, nil)
	}
	if err != nil {
		return fmt.Errorf("chat tickets %s: %w", verb, err)
	}
	result := map[string]any{"chat_session_id": ref.ID, "issue_id": issueRef.ID, "identifier": issueRef.Display, "removed": remove}
	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	if remove {
		fmt.Printf("Took %s off this chat's tickets.\n", issueRef.Display)
	} else {
		fmt.Printf("Pinned %s to this chat's tickets.\n", issueRef.Display)
	}
	return nil
}

// chatTicketResult is a ticket's receipt in one table cell: its close summary
// and PRs, "-" while it has none.
func chatTicketResult(t map[string]any) string {
	var parts []string
	if summary, _ := t["summary"].(string); summary != "" {
		if r := []rune(summary); len(r) > 60 {
			summary = string(r[:59]) + "…"
		}
		parts = append(parts, summary)
	}
	prs, _ := t["pull_requests"].([]any)
	for _, raw := range prs {
		if pr, ok := raw.(map[string]any); ok {
			parts = append(parts, fmt.Sprintf("PR #%v %v", pr["number"], pr["state"]))
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " · ")
}

func runChatProgress(cmd *cobra.Command, args []string) error {
	session, _ := cmd.Flags().GetString("session")
	if strings.TrimSpace(session) == "" {
		session = os.Getenv("MULTICA_CHAT_SESSION_ID")
	}
	ref, err := parseChatSessionLinkRef(session)
	if err != nil {
		return fmt.Errorf("chat progress: --session is required (or set MULTICA_CHAT_SESSION_ID): %w", err)
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	return reportOrListProgress(ctx, cmd, client, "/api/chat/sessions/"+url.PathEscape(ref.ID)+"/progress", args, "chat")
}

// reportOrListProgress is shared by `multica chat progress` and `multica
// issue progress`: POST a line, or GET the history with --history.
func reportOrListProgress(ctx context.Context, cmd *cobra.Command, client *cli.APIClient, path string, args []string, noun string) error {
	output, _ := cmd.Flags().GetString("output")
	if history, _ := cmd.Flags().GetBool("history"); history {
		if len(args) > 0 {
			return fmt.Errorf("%s progress: --history takes no text", noun)
		}
		var out map[string]any
		if err := client.GetJSON(ctx, path, &out); err != nil {
			return fmt.Errorf("list %s progress: %w", noun, err)
		}
		if output == "table" {
			if status := strVal(out, "task_status"); status != "" {
				fmt.Printf("Task: %s; reason: %s; runtime: %v\n", status, strVal(out, "reason"), out["runtime"])
			}
			items, _ := out["progress"].([]any)
			for _, item := range items {
				p, _ := item.(map[string]any)
				fmt.Printf("%v  %-7v %-7v %v\n", p["updated_at"], p["tone"], p["source"], p["text"])
			}
			return nil
		}
		return cli.PrintJSON(os.Stdout, out)
	}
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return fmt.Errorf("%s progress: text is required (or pass --history to read)", noun)
	}
	body := map[string]any{"text": args[0]}
	if tone, _ := cmd.Flags().GetString("tone"); tone != "" {
		body["tone"] = tone
	}
	var out map[string]any
	if err := client.PostJSON(ctx, path, body, &out); err != nil {
		return fmt.Errorf("report %s progress: %w", noun, err)
	}
	if output == "table" {
		fmt.Printf("Progress: %v\n", out["progress"])
		return nil
	}
	return cli.PrintJSON(os.Stdout, out)
}

func runChatDirectoryList(cmd *cobra.Command, _ []string) error {
	return runChatDirectory(cmd, "")
}

func runChatDirectorySearch(cmd *cobra.Command, args []string) error {
	return runChatDirectory(cmd, args[0])
}

func runChatDirectory(cmd *cobra.Command, keyword string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	q := url.Values{}
	if project, _ := cmd.Flags().GetString("project"); strings.TrimSpace(project) != "" {
		ref, err := resolveProjectID(ctx, client, project)
		if err != nil {
			return err
		}
		q.Set("project", ref.ID)
	}
	if all, _ := cmd.Flags().GetBool("all-projects"); all {
		q.Set("all_projects", "true")
	}
	if since, _ := cmd.Flags().GetString("since"); strings.TrimSpace(since) != "" {
		q.Set("since", strings.TrimSpace(since))
	}
	if strings.TrimSpace(keyword) != "" {
		q.Set("q", keyword)
	}
	fields, err := listShapeQuery(cmd, q, listshape.Chat)
	if err != nil {
		return err
	}
	path := "/api/chat/directory"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var rows []map[string]any
	if err := client.GetJSON(ctx, path, &rows); err != nil {
		return linkedChatRefusal(cmd, err, "list chats")
	}
	listshape.Filter(rows, fields)
	output, _ := cmd.Flags().GetString("output")
	if output != "table" {
		return cli.PrintJSON(os.Stdout, rows)
	}
	if compact, _ := cmd.Flags().GetBool("compact"); compact {
		tableRows := make([][]string, 0, len(rows))
		for _, row := range rows {
			tableRows = append(tableRows, []string{strVal(row, "id"), strVal(row, "title"), strVal(row, "project_title"), strVal(row, "agent_name"), strVal(row, "status"), strVal(row, "last_active_at"), numVal(row, "message_count")})
		}
		cli.PrintTable(os.Stdout, []string{"ID", "TITLE", "PROJECT", "AGENT", "STATUS", "LAST_ACTIVE", "MESSAGES"}, tableRows)
		return nil
	}
	headers := []string{"TITLE", "PROJECT", "AGENT", "ORIGINATOR", "LAST_ACTIVE", "MESSAGES", "SUMMARY"}
	tableRows := make([][]string, 0, len(rows))
	for _, row := range rows {
		tableRows = append(tableRows, []string{
			strVal(row, "title"), strVal(row, "project_title"), strVal(row, "agent_name"),
			strVal(row, "originator"), strVal(row, "last_active_at"), numVal(row, "message_count"), strVal(row, "summary"),
		})
	}
	cli.PrintTable(os.Stdout, headers, tableRows)
	return nil
}

func runChatHistory(cmd *cobra.Command, _ []string) error {
	if session, _ := cmd.Flags().GetString("session"); strings.TrimSpace(session) != "" {
		return runChatSessionHandoff(cmd, session)
	}
	resp, err := fetchChatRead(cmd, "/api/chat/history", "")
	if err != nil {
		return err
	}
	return renderChatRead(cmd, resp, true)
}

func runChatThread(cmd *cobra.Command, args []string) error {
	if session, _ := cmd.Flags().GetString("session"); strings.TrimSpace(session) != "" {
		if len(args) == 1 {
			return fmt.Errorf("--session reads that chat's transcript; a thread id only applies to the current channel. Use `multica chat history --session %s`", session)
		}
		return runChatSessionHandoff(cmd, session)
	}
	threadID := ""
	if len(args) == 1 {
		threadID = args[0]
	}
	resp, err := fetchChatRead(cmd, "/api/chat/thread", threadID)
	if err != nil {
		return err
	}
	return renderChatRead(cmd, resp, false)
}

func runChatSessionHandoff(cmd *cobra.Command, sessionRef string) error {
	ref, err := parseChatSessionLinkRef(sessionRef)
	if err != nil {
		return err
	}
	path := "/api/chat/sessions/" + url.PathEscape(ref.ID) + "/handoff"
	if ref.Slug != "" {
		path = "/api/chat/links/" + url.PathEscape(ref.Slug) + "/sessions/" + url.PathEscape(ref.ID) + "/handoff"
	}
	resp, err := fetchChatRead(cmd, path, "")
	if err != nil {
		return err
	}
	return renderChatRead(cmd, resp, false)
}

// parseChatSessionRef accepts a bare session UUID or an internal chat URL
// (`…/chat/<session-id>` or `?session=<id>`).
type chatSessionRef struct{ ID, Slug string }

func parseChatSessionRef(raw string) (string, error) {
	ref, err := parseChatSessionLinkRef(raw)
	if err != nil {
		return "", err
	}
	return ref.ID, nil
}

func parseChatSessionLinkRef(raw string) (chatSessionRef, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return chatSessionRef{}, fmt.Errorf("missing session id or url")
	}
	if id, ok := canonicalSessionID(raw); ok {
		return chatSessionRef{ID: id}, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return chatSessionRef{}, fmt.Errorf("could not read a chat session id from %q", raw)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i, part := range parts {
		if part == "chat" && i+1 < len(parts) {
			if id, ok := canonicalSessionID(parts[i+1]); ok {
				slug := ""
				if i > 0 {
					slug = parts[i-1]
				}
				return chatSessionRef{ID: id, Slug: slug}, nil
			}
		}
	}
	if id, ok := canonicalSessionID(parsed.Query().Get("session")); ok {
		return chatSessionRef{ID: id}, nil
	}
	return chatSessionRef{}, fmt.Errorf("could not read a chat session id from %q", raw)
}

func canonicalSessionID(raw string) (string, bool) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	return id.String(), true
}

// fetchChatRead builds the request (shared --limit/--before paging, plus the
// optional thread id) and decodes the response.
func fetchChatRead(cmd *cobra.Command, basePath, threadID string) (map[string]any, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	limit, _ := cmd.Flags().GetInt("limit")
	before, _ := cmd.Flags().GetString("before")

	q := url.Values{}
	if threadID != "" {
		q.Set("id", threadID)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if before != "" {
		q.Set("before", before)
	}
	path := basePath
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var resp map[string]any
	if err := client.GetJSON(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("read chat: %w", err)
	}
	return resp, nil
}

// renderChatRead prints the response as JSON (default) or a table. The overview
// table adds the thread columns so the agent can pick a thread_id to drill into.
func renderChatRead(cmd *cobra.Command, resp map[string]any, overview bool) error {
	output, _ := cmd.Flags().GetString("output")
	if output != "table" {
		return cli.PrintJSON(os.Stdout, resp)
	}
	if summary := strVal(resp, "summary"); summary != "" {
		fmt.Fprintln(os.Stdout, summary)
		fmt.Fprintln(os.Stdout)
	}
	if note := strVal(resp, "note"); note != "" {
		fmt.Fprintln(os.Stdout, note)
		return nil
	}
	msgs, _ := resp["messages"].([]any)
	var headers []string
	if overview {
		headers = []string{"TS", "ROLE", "AUTHOR", "THREAD_ID", "REPLIES", "TEXT"}
	} else {
		headers = []string{"TS", "ROLE", "AUTHOR", "TEXT"}
	}
	rows := make([][]string, 0, len(msgs))
	for _, mi := range msgs {
		m, ok := mi.(map[string]any)
		if !ok {
			continue
		}
		if overview {
			rows = append(rows, []string{strVal(m, "ts"), strVal(m, "role"), withBotAttribution(strVal(m, "author"), m), strVal(m, "thread_id"), numVal(m, "reply_count"), strVal(m, "text")})
		} else {
			rows = append(rows, []string{strVal(m, "ts"), strVal(m, "role"), withBotAttribution(strVal(m, "author"), m), strVal(m, "text")})
		}
	}
	cli.PrintTable(os.Stdout, headers, rows)
	if count := numVal(resp, "hidden_queued_count"); count != "" {
		fmt.Fprintf(os.Stdout, "另有 %s 条排队消息\n", count)
	}
	if cursor := strVal(resp, "next_cursor"); cursor != "" && strVal(resp, "summary") != "" {
		fmt.Fprintf(os.Stdout, "next_cursor: %s\n", cursor)
	}
	return nil
}

// numVal renders a numeric JSON field as a string, blank when zero/absent.
func numVal(m map[string]any, key string) string {
	if v, ok := m[key].(float64); ok && v != 0 {
		return strconv.Itoa(int(v))
	}
	return ""
}

func runChatOpen(cmd *cobra.Command, _ []string) error {
	agentRef, _ := cmd.Flags().GetString("agent")
	briefPath, _ := cmd.Flags().GetString("brief-file")
	if strings.TrimSpace(agentRef) == "" || strings.TrimSpace(briefPath) == "" {
		return fmt.Errorf("chat open: --agent and --brief-file are required")
	}
	raw, err := os.ReadFile(briefPath)
	if err != nil {
		return fmt.Errorf("chat open: read brief: %w", err)
	}
	brief := strings.TrimSpace(string(raw))
	if brief == "" {
		return fmt.Errorf("chat open: brief file is empty")
	}
	title, _ := cmd.Flags().GetString("title")
	visibility, _ := cmd.Flags().GetString("visibility")
	project, _ := cmd.Flags().GetString("project")
	clientKey, _ := cmd.Flags().GetString("client-key")

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	agentID, err := resolveAgent(ctx, client, agentRef)
	if err != nil {
		return fmt.Errorf("chat open: %w", err)
	}
	if strings.TrimSpace(clientKey) == "" {
		sum := sha256.Sum256([]byte(agentID + "\x00" + title + "\x00" + brief))
		clientKey = hex.EncodeToString(sum[:16])
	}
	body := map[string]any{
		"agent_id":   agentID,
		"brief":      brief,
		"client_key": clientKey,
	}
	if title != "" {
		body["title"] = title
	}
	if visibility != "" {
		body["visibility"] = visibility
	}
	if project != "" {
		p, err := resolveProjectID(ctx, client, project)
		if err != nil {
			return fmt.Errorf("chat open: %w", err)
		}
		body["project_id"] = p.ID
	}

	output, _ := cmd.Flags().GetString("output")
	var out map[string]any
	if err := client.PostJSON(ctx, "/api/chat/sessions/spawn", body, &out); err != nil {
		var httpErr *cli.HTTPError
		var refusal map[string]any
		if errors.As(err, &httpErr) && json.Unmarshal([]byte(httpErr.Body), &refusal) == nil && refusal["code"] != nil {
			if output != "table" {
				_ = cli.PrintJSON(os.Stdout, refusal)
			}
			return fmt.Errorf("chat open refused (%v): %v", refusal["code"], refusal["error"])
		}
		return fmt.Errorf("open chat: %w", err)
	}
	if output == "table" {
		session, _ := out["session"].(map[string]any)
		fmt.Printf("Chat: %v (%v)\n", session["title"], session["id"])
		return nil
	}
	return cli.PrintJSON(os.Stdout, out)
}

func runChatSend(cmd *cobra.Command, args []string) error {
	linked, _ := cmd.Flags().GetString(linkedFlag)
	if strings.TrimSpace(linked) != "" {
		attachments, _ := cmd.Flags().GetStringSlice("attachment")
		wait, _ := cmd.Flags().GetBool("wait")
		if len(attachments) > 0 || wait {
			return fmt.Errorf("chat send --linked supports text only without --wait; remove --attachment and --wait, then inspect the returned message_id")
		}
	}

	// No MULTICA_CHAT_SESSION_ID fallback: a chat run sending to the chat it
	// is answering would queue a turn for itself.
	session, _ := cmd.Flags().GetString("session")
	ref, err := parseChatSessionLinkRef(session)
	if err != nil {
		return fmt.Errorf("chat send: --session is required: %w", err)
	}
	mode, _ := cmd.Flags().GetString("mode")
	if strings.TrimSpace(linked) != "" && mode != "queue" {
		return fmt.Errorf("chat send --linked supports --mode queue only; remove --mode or pass --mode queue")
	}
	switch mode {
	case "", "steer", "queue", "restart", "replace":
	default:
		return fmt.Errorf("chat send: --mode must be steer, queue, restart or replace")
	}
	content := ""
	if len(args) == 1 {
		content = args[0]
	}
	if path, _ := cmd.Flags().GetString("content-file"); path != "" {
		if content != "" {
			return fmt.Errorf("chat send: pass the message as an argument or --content-file, not both")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("chat send: read content file: %w", err)
		}
		content = string(raw)
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("chat send: message is empty")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	pending, err := collectChatSendAttachments(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cli.AtLeastAPITimeout(10*time.Minute))
	defer cancel()
	output, _ := cmd.Flags().GetString("output")
	var out map[string]any
	var attachmentIDs []string
	for _, att := range pending {
		id, err := client.UploadFile(ctx, att.data, att.path, "")
		if err != nil {
			return fmt.Errorf("upload chat attachment (uploaded IDs: %v): %w", attachmentIDs, err)
		}
		attachmentIDs = append(attachmentIDs, id)
	}
	urgent, _ := cmd.Flags().GetBool("urgent")
	if urgent && strings.TrimSpace(linked) != "" {
		return fmt.Errorf("chat send --urgent is not available with --linked")
	}
	path := "/api/chat/sessions/" + url.PathEscape(ref.ID) + "/messages"
	body := map[string]any{"content": content, "attachment_ids": attachmentIDs, "per_quote": flagString(cmd, "per-quote")}
	if mode != "" {
		body["mode"] = mode
	}
	if urgent {
		body["urgent"] = true
	}
	if err := client.PostJSON(ctx, path, body, &out); err != nil {
		if strings.TrimSpace(linked) != "" {
			return linkedChatRefusal(cmd, err, "chat send")
		}
		if urgent {
			if refused, uerr := urgentSendRefusal(err, output); refused {
				return uerr
			}
		}
		return sendModeRefusal(err, output, "chat send")
	}
	if out["mode"] == "blocked" {
		if output == "json" {
			if err := cli.PrintJSON(os.Stdout, out); err != nil {
				return err
			}
		}
		return blockedChatSendError(out)
	}
	printChatRuntimeWarning(cmd, out)
	if urgent {
		// An urgent send is only done once a daemon has it (DENE-1884): the
		// command always waits for the pickup result, up to the server's
		// pickup_deadline_at, and exits nonzero when nobody started the turn.
		// --wait adds nothing here and --wait-timeout does not apply.
		pickup, err := waitChatPickup(client, ref.ID, strVal(out, "task_id"), urgentPickupDeadline(out))
		for key, value := range pickup {
			out[key] = value
		}
		if err != nil {
			if output != "table" {
				_ = cli.PrintJSON(os.Stdout, out)
			}
			return err
		}
		if output == "table" {
			where := "primary runtime"
			if out["fallback_runtime"] == true {
				where = "fallback runtime"
			}
			fmt.Printf("Sent urgent (%v, %s): message %v, task %v", out["mode"], where, out["message_id"], out["task_id"])
			if status := strVal(out, "task_status"); status != "" {
				fmt.Printf(", task_status %s", status)
			}
			fmt.Println()
			return nil
		}
		return cli.PrintJSON(os.Stdout, out)
	}
	if wait, _ := cmd.Flags().GetBool("wait"); wait {
		return finishChatSend(cmd, client, ref.ID, out)
	}
	if output == "table" {
		fmt.Printf("Sent (%v): message %v\n", out["mode"], out["message_id"])
		return nil
	}
	return cli.PrintJSON(os.Stdout, out)
}

// urgentSendRefusal recognises the 409 an urgent send gets when the agent's
// runtime is offline and no online fallback is configured. The body is
// printed as-is (reason + next_steps) and the exit is nonzero; nothing was
// saved, so there is nothing to avoid resending.
func urgentSendRefusal(err error, output string) (bool, error) {
	var httpErr *cli.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusConflict {
		return false, nil
	}
	var refusal map[string]any
	if json.Unmarshal([]byte(httpErr.Body), &refusal) != nil || strVal(refusal, "code") != "runtime_offline" {
		return false, nil
	}
	if output != "table" {
		_ = cli.PrintJSON(os.Stdout, refusal)
	}
	steps := ""
	if list, ok := refusal["next_steps"].([]any); ok {
		parts := make([]string, 0, len(list))
		for _, item := range list {
			parts = append(parts, fmt.Sprint(item))
		}
		steps = "; next steps: " + strings.Join(parts, " | ")
	}
	detail := strVal(refusal, "reason")
	if fb := strVal(refusal, "fallback_reason"); fb != "" {
		detail += "; fallback: " + fb
	}
	return true, fmt.Errorf("chat send --urgent refused, nothing was sent: %s%s", detail, steps)
}

var chatPickupPollInterval = 2 * time.Second

// urgentPickupFallbackDeadline stands in for pickup_deadline_at when an older
// server did not return one; it mirrors the server's 30 s promise.
const urgentPickupFallbackDeadline = 30 * time.Second

// urgentPickupDeadline reads the server's pickup_deadline_at from the send
// receipt: the instant by which a daemon must have started the turn.
func urgentPickupDeadline(out map[string]any) time.Time {
	if raw := strVal(out, "pickup_deadline_at"); raw != "" {
		if deadline, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return deadline
		}
	}
	return time.Now().Add(urgentPickupFallbackDeadline)
}

// waitChatPickup polls the turn's progress until a daemon has started it
// (task_status running or a terminal state). A refused or timed-out pickup
// comes back as an error carrying the reason; the returned map is merged
// into the send output either way, with pickup_elapsed_ms added.
//
// deadline is the server's pickup_deadline_at and bounds the whole wait: every
// progress request and every sleep runs under a context that expires at that
// instant, so the command returns within the 30 s the server promised even
// when a request is slow or the server's sweep has not judged the turn yet.
// When the deadline passes without a server verdict the map keeps the last
// task_status the server reported (it may still be dispatched) and sets
// reason=pickup_timeout with reason_source=sender: the sender stopped waiting;
// the server alone decides the task's fate. A verdict the server did write
// (failed/cancelled) carries reason_source=server.
func waitChatPickup(client *cli.APIClient, sessionID, taskID string, deadline time.Time) (map[string]any, error) {
	if taskID == "" {
		return nil, fmt.Errorf("no task to wait for")
	}
	started := time.Now()
	waitCtx, stopWaiting := context.WithDeadline(context.Background(), deadline)
	defer stopWaiting()
	path := "/api/chat/sessions/" + url.PathEscape(sessionID) + "/progress?task_id=" + url.QueryEscape(taskID)
	last := map[string]any{}
	gaveUp := func() (map[string]any, error) {
		last["pickup_elapsed_ms"] = time.Since(started).Milliseconds()
		last["reason"] = "pickup_timeout"
		last["reason_source"] = "sender"
		status := strVal(last, "task_status")
		if status == "" {
			status = "unknown"
		}
		return last, fmt.Errorf("urgent message not picked up by its deadline %s (waited %s): sender stopped waiting; task_status=%s reason=pickup_timeout runtime=%v", deadline.UTC().Format(time.RFC3339), time.Since(started).Round(time.Millisecond), status, last["runtime"])
	}
	for {
		ctx, cancel := cli.APIContext(waitCtx)
		var diagnostics map[string]any
		err := client.GetJSON(ctx, path, &diagnostics)
		cancel()
		if err != nil {
			if waitCtx.Err() != nil {
				return gaveUp()
			}
			return nil, fmt.Errorf("wait for pickup: %w", err)
		}
		delete(diagnostics, "progress")
		diagnostics["pickup_elapsed_ms"] = time.Since(started).Milliseconds()
		last = diagnostics
		switch strVal(diagnostics, "task_status") {
		case "running", "completed":
			return diagnostics, nil
		case "failed", "cancelled":
			diagnostics["reason_source"] = "server"
			return diagnostics, fmt.Errorf("urgent message was not picked up: %s (task_status=%s)", strVal(diagnostics, "reason"), strVal(diagnostics, "task_status"))
		}
		timer := time.NewTimer(chatPickupPollInterval)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return gaveUp()
		case <-timer.C:
		}
	}
}

func blockedChatSendError(out map[string]any) error {
	warning, _ := out["warning"].(string)
	if warning == "" {
		warning = "Bot wake-chain limit reached; wait for the agent to reply or complete a run, use a human login, or ask an administrator to change bot_wake_chain_limit"
	}
	return fmt.Errorf("message %v saved, but agent was not woken: %s; do not resend the saved message", out["message_id"], warning)
}

// sendModeRefusal turns a steer refusal into the reason plus the modes that
// still work, shared by chat send and issue comment add.
func sendModeRefusal(err error, output, verb string) error {
	var httpErr *cli.HTTPError
	var refusal struct {
		Error          string   `json:"error"`
		Code           string   `json:"code"`
		AvailableModes []string `json:"available_modes"`
	}
	if errors.As(err, &httpErr) && json.Unmarshal([]byte(httpErr.Body), &refusal) == nil && refusal.Code == "steer_unsupported" {
		if output != "table" {
			_ = cli.PrintJSON(os.Stdout, refusal)
		}
		return fmt.Errorf("%s: cannot steer: %s (use --mode %s)", verb, refusal.Error, strings.Join(refusal.AvailableModes, " or --mode "))
	}
	return fmt.Errorf("%s: %w", verb, err)
}

func runChatHandoff(cmd *cobra.Command, _ []string) error {
	session := os.Getenv("MULTICA_CHAT_SESSION_ID")
	if raw, _ := cmd.Flags().GetString("session"); strings.TrimSpace(raw) != "" {
		session = raw
	}
	ref, err := parseChatSessionLinkRef(session)
	if err != nil {
		return fmt.Errorf("chat handoff: session is required (or set MULTICA_CHAT_SESSION_ID): %w", err)
	}
	to, _ := cmd.Flags().GetString("to")
	if strings.TrimSpace(to) == "" {
		return fmt.Errorf("chat handoff: --to is required")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var out map[string]any
	if err := client.PostJSON(ctx, "/api/chat/sessions/"+url.PathEscape(ref.ID)+"/handoff", map[string]any{"to": to}, &out); err != nil {
		return fmt.Errorf("hand chat over: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		s, _ := out["session"].(map[string]any)
		fmt.Printf("New chat: %v (%v)\n", s["title"], s["id"])
		return nil
	}
	return cli.PrintJSON(os.Stdout, out)
}

// withBotAttribution keeps the member identity while exposing the writing bot.
func withBotAttribution(author string, record map[string]any) string {
	if name := strVal(record, "via_bot_name"); name != "" {
		return author + " (via " + name + ")"
	}
	return author
}

func collectChatSendAttachments(cmd *cobra.Command) ([]pendingAttachment, error) {
	paths, _ := cmd.Flags().GetStringSlice("attachment")
	return collectLocalAttachments(cmd, paths)
}

// linkedChatRefusal preserves the server status and gives the supported next step.
func linkedChatRefusal(cmd *cobra.Command, err error, verb string) error {
	linked, _ := cmd.Flags().GetString(linkedFlag)
	if strings.TrimSpace(linked) != "" {
		return fmt.Errorf("%s: %w; inspect `multica workspace link view <link-id>` for this source", verb, err)
	}
	return fmt.Errorf("%s: %w", verb, err)
}

func runChatClearQueue(cmd *cobra.Command, args []string) error {
	session, _ := cmd.Flags().GetString("session")
	if len(args) > 0 {
		session = args[0]
	}
	if strings.TrimSpace(session) == "" {
		session = os.Getenv("MULTICA_CHAT_SESSION_ID")
	}
	ref, err := parseChatSessionLinkRef(session)
	if err != nil {
		return fmt.Errorf("chat clear-queue: provide a session id or --session: %w", err)
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	if err := client.DeleteJSON(ctx, "/api/chat/sessions/"+url.PathEscape(ref.ID)+"/queued-tasks"); err != nil {
		return fmt.Errorf("clear chat queue: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		fmt.Fprintln(os.Stdout, "Cleared the queued messages")
		return nil
	}
	return cli.PrintJSON(os.Stdout, map[string]any{"chat_session_id": ref.ID, "cleared": true})
}

func runChatStop(cmd *cobra.Command, args []string) error {
	session, _ := cmd.Flags().GetString("session")
	if len(args) > 0 {
		session = args[0]
	}
	if strings.TrimSpace(session) == "" {
		session = os.Getenv("MULTICA_CHAT_SESSION_ID")
	}
	ref, err := parseChatSessionLinkRef(session)
	if err != nil {
		return fmt.Errorf("chat stop: provide a session id or --session: %w", err)
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	related, _ := cmd.Flags().GetBool("related")
	var result map[string]any
	if err := client.PostJSON(ctx, "/api/chat/sessions/"+url.PathEscape(ref.ID)+"/stop", map[string]any{"related": related}, &result); err != nil {
		return fmt.Errorf("stop chat: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		fmt.Fprintf(os.Stdout, "Stopped %v runs\n", result["cancelled_count"])
		return nil
	}
	return cli.PrintJSON(os.Stdout, result)
}
