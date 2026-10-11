package main

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/multica-ai/multica/server/internal/fastmcp"
	"github.com/spf13/cobra"
)

func newMCPCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "mcp", Short: "Expose Multica tools to a local MCP client", Annotations: map[string]string{annotationAccess: "local"}}
	serve := &cobra.Command{
		Use: "serve", Short: "Serve permission-checked activity, history, messaging and attachments over stdio",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{annotationAccess: "local"},
		Long: `Run a local MCP stdio server using the existing Multica API client and login.
The client launches this process and owns its lifetime; stdout is MCP JSON-RPC only.
Supports current discovery and legacy initialize. No listening port or new credentials.
Use existing Web/Desktop Settings > MCP or an agent's MCP tab: command multica,
args ["mcp","serve"]. Sign in on the runtime host through the existing login flow;
managed tasks reuse their injected identity. Never put credentials in tool arguments.

Tools: changes_read, runs_list, issue_get, comments_list, chats_list, chat_history,
comment_create, chat_send, attachment_download. IDs come from changes_read/runs_list.
Reads default to summaries, one bounded page, and never mark chats read. full=true
explicitly requests text. The server rechecks permissions on every request.
Writes can wake agents and cost budget. No automatic retry. ambiguous_write means
the server may have committed: inspect the returned reconciliation read and use
comments_list/chat_history before deciding whether to send again. 429 preserves
retry_after. Clear summaries/cursors on scope_reset or reset_required.
Downloads are verified using server size/SHA256 then saved to a private directory
under --download-dir; the tool cannot choose arbitrary output paths.`,
		Example: "  multica mcp serve\n  multica mcp serve --download-dir ./attachments",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := newAPIClient(cmd)
			if err != nil {
				return err
			}
			dir, _ := cmd.Flags().GetString("download-dir")
			return fastmcp.NewServer(client, dir).Run(cmd.Context(), &mcp.StdioTransport{})
		},
	}
	serve.Flags().String("download-dir", "./attachments", "Local parent directory for verified downloads (not a tool argument)")
	cmd.AddCommand(serve)
	return cmd
}
