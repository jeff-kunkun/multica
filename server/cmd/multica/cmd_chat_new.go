package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// chat new — a person (or an external bot holding a person's key, DENE-1788)
// starts a chat the way the web composer does: the human create-chat route,
// then the first message. Agent runs keep using "chat open", whose spawn
// guards (depth, budget, scope) this command does not touch.

var chatNewCmd = &cobra.Command{
	Use:   "new [text]",
	Short: "Start a new chat with an agent and send the first message",
	Long: `Start a new chat with an agent, as if you opened one in the app and typed
the first message. With --wait, block until the agent's reply arrives and
print it.

Attach read-only references with --reference LINK_ID:PROJECT_ID (repeatable).
Find IDs with "multica chat linked-projects --available --output json".
References are not working projects; disconnecting stops injection in the next run.

For people and external bots logged in with a personal key. An agent run
is refused here: use "multica chat open" from a chat run instead.

  multica chat new --agent 孙悟空 "帮我看看 DENE-1788 现在卡在哪" --wait
  multica chat new --agent 孙悟空 --content-file ./ask.md --title "Multica · 外部 Bot" --wait --output table
`,
	Args: cobra.MaximumNArgs(1),
	RunE: runChatNew,
}

func init() {
	chatCmd.AddCommand(chatNewCmd)
	chatNewCmd.Flags().String("agent", "", "Agent to talk to: name or id (required)")
	chatNewCmd.Flags().String("content-file", "", "Read the first message from this file instead of the argument")
	chatNewCmd.Flags().String("title", "", "Chat title (optional; the server names it otherwise)")
	chatNewCmd.Flags().String("project", "", "Project id or prefix to give the chat as context")
	chatNewCmd.Flags().StringSlice("reference", nil, "Read-only LINK_ID:PROJECT_ID references (repeatable; not working projects)")
	chatNewCmd.Flags().Bool("wait", false, "Wait for the agent's reply and print it")
	chatNewCmd.Flags().Duration("wait-timeout", 10*time.Minute, "How long --wait waits for the reply")
	chatNewCmd.Flags().String("output", "json", "Output format: table or json")

	chatSendCmd.Flags().Bool("wait", false, "Wait for the agent's reply to this message and print it")
	chatSendCmd.Flags().Duration("wait-timeout", 10*time.Minute, "How long --wait waits; timeout exits nonzero with task_status, reason and runtime last_seen_at")
}

func runChatNew(cmd *cobra.Command, args []string) error {
	if inAgentExecutionContext() {
		return fmt.Errorf("chat new: an agent run cannot start a chat this way; use \"multica chat open\" from a chat run")
	}
	agentRef, _ := cmd.Flags().GetString("agent")
	if strings.TrimSpace(agentRef) == "" {
		return fmt.Errorf("chat new: --agent is required")
	}
	content, err := readChatContent(cmd, args, "chat new")
	if err != nil {
		return err
	}
	values, _ := cmd.Flags().GetStringSlice("reference")
	refs, err := parseChatReferences(values)
	if err != nil {
		return err
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	agentID, err := resolveAgent(ctx, client, agentRef)
	if err != nil {
		return fmt.Errorf("chat new: %w", err)
	}
	body := map[string]any{"agent_id": agentID}
	if cmd.Flags().Changed("reference") {
		body["linked_projects"] = refs
	}
	if title, _ := cmd.Flags().GetString("title"); strings.TrimSpace(title) != "" {
		body["title"] = strings.TrimSpace(title)
	}
	if project, _ := cmd.Flags().GetString("project"); project != "" {
		p, err := resolveProjectID(ctx, client, project)
		if err != nil {
			return fmt.Errorf("chat new: %w", err)
		}
		body["project_ids"] = []string{p.ID}
	}
	var session map[string]any
	if err := client.PostJSON(ctx, "/api/chat/sessions", body, &session); err != nil {
		return fmt.Errorf("chat new: create chat: %w", err)
	}
	sessionID := strVal(session, "id")
	var sent map[string]any
	if err := client.PostJSON(ctx, "/api/chat/sessions/"+url.PathEscape(sessionID)+"/messages", map[string]any{"content": content}, &sent); err != nil {
		return fmt.Errorf("chat new: chat %s created, first message failed (retry with: multica chat send --session %s): %w", sessionID, sessionID, err)
	}
	out := map[string]any{
		"session_id": sessionID,
		"title":      strVal(session, "title"),
		"message_id": strVal(sent, "message_id"),
		"task_id":    strVal(sent, "task_id"),
	}
	for _, key := range []string{"runtime", "warning"} {
		if value, ok := sent[key]; ok {
			out[key] = value
		}
	}
	printChatRuntimeWarning(cmd, out)
	return finishChatSend(cmd, client, sessionID, out)
}

// readChatContent takes the message from the argument or --content-file.
func readChatContent(cmd *cobra.Command, args []string, verb string) (string, error) {
	content := ""
	if len(args) == 1 {
		content = args[0]
	}
	if path, _ := cmd.Flags().GetString("content-file"); path != "" {
		if content != "" {
			return "", fmt.Errorf("%s: pass the message as an argument or --content-file, not both", verb)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("%s: read content file: %w", verb, err)
		}
		content = string(raw)
	}
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("%s: message is empty", verb)
	}
	return content, nil
}

// finishChatSend optionally waits for the reply, then prints the result.
func finishChatSend(cmd *cobra.Command, client *cli.APIClient, sessionID string, out map[string]any) error {
	output, _ := cmd.Flags().GetString("output")
	if wait, _ := cmd.Flags().GetBool("wait"); wait {
		timeout, _ := cmd.Flags().GetDuration("wait-timeout")
		reply, err := waitChatReply(client, sessionID, strVal(out, "task_id"), timeout)
		if err != nil {
			if reply != nil {
				for key, value := range reply {
					out[key] = value
				}
			}
			if output != "table" {
				_ = cli.PrintJSON(os.Stdout, out)
			}
			return err
		}
		out["reply"] = reply
	}
	if output != "table" {
		return cli.PrintJSON(os.Stdout, out)
	}
	fmt.Printf("Chat: %s (%s)\n", strVal(out, "title"), sessionID)
	if reply, ok := out["reply"].(map[string]any); ok {
		fmt.Println()
		fmt.Println(strVal(reply, "content"))
	}
	return nil
}

var chatReplyPollInterval = 3 * time.Second

// waitChatReply polls the chat until the assistant row for taskID appears.
// A failed reply comes back as an error carrying the failure reason.
func waitChatReply(client *cli.APIClient, sessionID, taskID string, timeout time.Duration) (map[string]any, error) {
	if taskID == "" {
		return nil, fmt.Errorf("no reply to wait for (the message did not start a reply); read it later with: multica chat history --session %s", sessionID)
	}
	deadline := time.Now().Add(timeout)
	path := "/api/chat/sessions/" + url.PathEscape(sessionID) + "/messages"
	for {
		ctx, cancel := cli.APIContext(context.Background())
		var msgs []map[string]any
		err := client.GetJSON(ctx, path, &msgs)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("wait for reply: %w", err)
		}
		for _, m := range msgs {
			if strVal(m, "role") == "assistant" && strVal(m, "task_id") == taskID {
				if reason := strVal(m, "failure_reason"); reason != "" {
					return m, fmt.Errorf("the agent's reply failed: %s", reason)
				}
				return m, nil
			}
		}
		if time.Now().After(deadline) {
			ctx, cancel := cli.APIContext(context.Background())
			var diagnostics map[string]any
			err := client.GetJSON(ctx, "/api/chat/sessions/"+url.PathEscape(sessionID)+"/progress?task_id="+url.QueryEscape(taskID), &diagnostics)
			cancel()
			if err != nil {
				return nil, fmt.Errorf("no reply after %s; task diagnostics unavailable: %w; read it later with: multica chat progress --session %s --history", timeout, err, sessionID)
			}
			delete(diagnostics, "progress")
			return diagnostics, fmt.Errorf("no reply after %s; task_status=%s reason=%s runtime=%v; read it later with: multica chat progress --session %s --history", timeout, strVal(diagnostics, "task_status"), strVal(diagnostics, "reason"), diagnostics["runtime"], sessionID)
		}
		time.Sleep(chatReplyPollInterval)
	}
}

func printChatRuntimeWarning(cmd *cobra.Command, out map[string]any) {
	if warning := strVal(out, "warning"); warning != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "Warning:", warning)
	}
}
