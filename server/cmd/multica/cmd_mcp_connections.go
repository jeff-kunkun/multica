package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

func newMCPConnectionsCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "connections", Short: "Manage remote MCP authorizations without changing bot keys"}
	list := &cobra.Command{Use: "list", Short: "List remote MCP connections in this workspace", Args: cobra.NoArgs, Annotations: map[string]string{annotationAccess: "read"}, RunE: func(cmd *cobra.Command, _ []string) error {
		client, ws, ctx, cancel, err := botClient(cmd)
		if err != nil {
			return err
		}
		defer cancel()
		var out []map[string]any
		if err := client.GetJSON(ctx, mcpConnectionsPath(ws), &out); err != nil {
			return err
		}
		return cli.PrintJSON(cmd.OutOrStdout(), out)
	}}
	create := &cobra.Command{Use: "create", Short: "Issue a short-lived, single-use authorization code", Args: cobra.NoArgs, Annotations: map[string]string{annotationAccess: "write"}, Long: `Issue one authorization code for a named bot connection. Owner/admin permission is checked by the server.
Deliver the code privately to the plugin; it exchanges the code once at exchange_url.
This does not rotate bot keys or replace any existing login. The code is printed once.`, RunE: func(cmd *cobra.Command, _ []string) error {
		name, _ := cmd.Flags().GetString("name")
		bot, _ := cmd.Flags().GetString("bot")
		if strings.TrimSpace(name) == "" || strings.TrimSpace(bot) == "" {
			return fmt.Errorf("--name and --bot (bot UUID) are required")
		}
		client, ws, ctx, cancel, err := botClient(cmd)
		if err != nil {
			return err
		}
		defer cancel()
		var out map[string]any
		if err := client.PostJSON(ctx, mcpConnectionsPath(ws), map[string]string{"name": strings.TrimSpace(name), "bot_id": strings.TrimSpace(bot)}, &out); err != nil {
			return err
		}
		return cli.PrintJSON(cmd.OutOrStdout(), out)
	}}
	create.Flags().String("name", "", "Connection name (required)")
	create.Flags().String("bot", "", "Existing bot UUID (required; its key is unchanged)")
	revoke := &cobra.Command{Use: "revoke <connection-id>", Short: "Revoke this connection only", Args: cobra.ExactArgs(1), Annotations: map[string]string{annotationAccess: "write"}, RunE: func(cmd *cobra.Command, args []string) error {
		client, ws, ctx, cancel, err := botClient(cmd)
		if err != nil {
			return err
		}
		defer cancel()
		if err := client.DeleteJSON(ctx, mcpConnectionsPath(ws)+"/"+url.PathEscape(args[0])); err != nil {
			return err
		}
		return cli.PrintJSON(cmd.OutOrStdout(), map[string]any{"id": args[0], "revoked": true})
	}}
	for _, c := range []*cobra.Command{list, create, revoke} {
		c.Flags().String("output", "json", "Output format: json")
		cmd.AddCommand(c)
	}
	return cmd
}

func mcpConnectionsPath(ws string) string {
	return "/api/workspaces/" + url.PathEscape(ws) + "/mcp-connections"
}

func newMCPConnectCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "connect", Short: "Exchange a one-time code for remote MCP client configuration", Args: cobra.NoArgs, Annotations: map[string]string{annotationAccess: "local"}, Long: `Read a one-time code from stdin and exchange it with the explicitly trusted server.
Only HTTPS is allowed, except HTTP on localhost for local testing. Redirects are refused.
Prints MCP client JSON containing a new bearer token to stdout; treat that output as a secret.
No credentials are saved and the existing Multica login and bot keys are unchanged.
External plugins may instead POST {"code":"..."} once to the supplied exchange_url
and store the returned access_token in their own secret storage. Do not retry an
ambiguous exchange automatically; request a new one-time code if the response was lost.`, Example: "  multica mcp connect --code-stdin --exchange-url https://test.example.com/api/mcp/exchange < private-code.txt", RunE: runMCPConnect}
	cmd.Flags().Bool("code-stdin", false, "Read the authorization code from stdin (required)")
	cmd.Flags().String("exchange-url", "", "Explicitly trusted server exchange URL ending in /api/mcp/exchange (required)")
	cmd.Flags().String("output", "json", "Output format: json")
	return cmd
}

func validateMCPExchangeURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/api/mcp/exchange" {
		return nil, fmt.Errorf("--exchange-url must be an explicit trusted URL ending in /api/mcp/exchange without credentials, query or fragment")
	}
	local := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return nil, fmt.Errorf("--exchange-url requires HTTPS (HTTP allowed only on localhost)")
	}
	return u, nil
}

func runMCPConnect(cmd *cobra.Command, _ []string) error {
	stdin, _ := cmd.Flags().GetBool("code-stdin")
	if !stdin {
		return fmt.Errorf("--code-stdin is required; never pass authorization codes as command arguments")
	}
	raw, _ := cmd.Flags().GetString("exchange-url")
	trusted, err := validateMCPExchangeURL(raw)
	if err != nil {
		return err
	}
	code, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 4097))
	if err != nil || len(code) > 4096 || strings.TrimSpace(string(code)) == "" {
		return fmt.Errorf("stdin must contain a non-empty authorization code of at most 4096 bytes")
	}
	body, _ := json.Marshal(map[string]string{"code": strings.TrimSpace(string(code))})
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, trusted.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("cannot prepare authorization exchange")
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("authorization exchange failed; no automatic retry (check the trusted server)")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("authorization exchange failed (HTTP %d); no automatic retry", resp.StatusCode)
	}
	var result struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Endpoint    string `json:"endpoint"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&result) != nil || result.AccessToken == "" || !strings.EqualFold(result.TokenType, "Bearer") {
		return fmt.Errorf("invalid authorization exchange response")
	}
	endpoint, err := url.Parse(result.Endpoint)
	if err != nil || endpoint.Scheme != trusted.Scheme || endpoint.Host != trusted.Host || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return fmt.Errorf("exchange returned an untrusted MCP endpoint")
	}
	return cli.PrintJSON(cmd.OutOrStdout(), map[string]any{"mcpServers": map[string]any{"multica": map[string]any{"url": result.Endpoint, "headers": map[string]string{"Authorization": "Bearer " + result.AccessToken}}}})
}
