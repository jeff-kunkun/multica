package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/integrations/vcs"
	"github.com/spf13/cobra"
)

var connectionCmd = &cobra.Command{Use: "connection", Short: "Manage Git connections"}
var connectionRepoCmd = &cobra.Command{Use: "repo", Short: "Inspect repository connections"}
var connectionListCmd = &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: runConnectionList}
var connectionAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Register a GitHub or GitLab token connection",
	Long: `The token is read from a file, from stdin (--token-file -), or from the local gh/glab login.
It is never accepted as a command-line flag.

  multica connection add --from-gh --yes
  multica connection add --provider gitlab --token-file ./token.txt

--from-gh and --from-glab register a personal connection for the current member.
An agent may pass --yes; the server then binds the connection to the task initiator
and it only covers repositories that person registered. --workspace registers an
admin-owned connection for the whole instance account instead.`,
	Args: cobra.NoArgs,
	RunE: runConnectionAdd,
}
var connectionTestCmd = &cobra.Command{Use: "test <connection-id>", Args: exactArgs(1), RunE: runConnectionTest}
var connectionRemoveCmd = &cobra.Command{Use: "remove <connection-id>", Args: exactArgs(1), RunE: runConnectionRemove}
var connectionRepoStatusCmd = &cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: runConnectionRepoStatus}

// connectionTokenCommand runs a local credential helper. Tests replace it.
// The arguments are exactly `auth token`; the secret stays on stdout.
var connectionTokenCommand = func(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

// discoverConnectionScope asks the provider which accounts the token covers.
// Tests replace it so the suite never calls the network.
var discoverConnectionScope = func(ctx context.Context, provider, instance, token string) ([]string, error) {
	p, ok := vcs.For(provider)
	if !ok {
		return nil, fmt.Errorf("unsupported provider")
	}
	account, err := p.ValidateToken(ctx, instance, token)
	if err != nil {
		return nil, err
	}
	host := instanceHost(instance)
	if len(account.Covers) == 0 {
		return []string{host + "/*"}, nil
	}
	lines := make([]string, 0, len(account.Covers))
	for _, cover := range account.Covers {
		if cover == "" {
			continue
		}
		lines = append(lines, host+"/"+cover)
	}
	if len(lines) == 0 {
		return []string{host + "/*"}, nil
	}
	return lines, nil
}

func init() {
	for _, c := range []*cobra.Command{connectionListCmd, connectionAddCmd, connectionTestCmd, connectionRemoveCmd, connectionRepoStatusCmd} {
		c.Flags().String("output", "table", "Output format: table or json")
	}
	connectionAddCmd.Flags().String("provider", "", "Provider: github or gitlab")
	connectionAddCmd.Flags().String("instance-url", "", "Provider instance URL (default is the public host)")
	connectionAddCmd.Flags().String("token-file", "", "Read the token from a file, or '-' for stdin. Never pass the token as a flag")
	connectionAddCmd.Flags().Bool("from-gh", false, "Read the token from `gh auth token` and register a personal GitHub connection")
	connectionAddCmd.Flags().Bool("from-glab", false, "Read the token from `glab auth token` and register a personal GitLab connection")
	connectionAddCmd.Flags().Bool("yes", false, "Skip the confirmation prompt. For an agent this binds the task initiator's personal connection")
	connectionAddCmd.Flags().Bool("workspace", false, "Register a workspace connection (admin) instead of a personal one")
	connectionCmd.AddCommand(connectionListCmd, connectionAddCmd, connectionTestCmd, connectionRemoveCmd, connectionRepoCmd)
	connectionRepoCmd.AddCommand(connectionRepoStatusCmd)
	rootCmd.AddCommand(connectionCmd)
}

func connectionClient(cmd *cobra.Command) (*cli.APIClient, string, error) {
	ws, err := requireWorkspaceID(cmd)
	if err != nil {
		return nil, "", err
	}
	c, err := newAPIClient(cmd)
	if err != nil {
		return nil, "", err
	}
	return c, ws, nil
}

func connectionWantsJSON(cmd *cobra.Command) bool {
	o, _ := cmd.Flags().GetString("output")
	return o == "json"
}

func runConnectionList(cmd *cobra.Command, _ []string) error {
	c, ws, err := connectionClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var out map[string]any
	if err = c.GetJSON(ctx, "/api/workspaces/"+ws+"/vcs/connections", &out); err != nil {
		return fmt.Errorf("list connections: %w", err)
	}
	redactConnectionPayload(out)
	if connectionWantsJSON(cmd) {
		return cli.PrintJSON(cmd.OutOrStdout(), out)
	}
	rows, _ := out["connections"].([]any)
	if len(rows) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No connections")
		return nil
	}
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		personal := ""
		if row["personal"] == true {
			personal = " personal"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s%s\n", row["provider"], row["account_login"], row["instance_url"], personal)
		if covers, ok := row["covers"].([]any); ok && len(covers) > 0 {
			parts := make([]string, 0, len(covers))
			for _, cover := range covers {
				parts = append(parts, fmt.Sprint(cover))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  covers: %s\n", strings.Join(parts, ", "))
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "  covers: whole instance")
		}
	}
	return nil
}

func readConnectionToken(cmd *cobra.Command) (string, error) {
	file, _ := cmd.Flags().GetString("token-file")
	gh, _ := cmd.Flags().GetBool("from-gh")
	glab, _ := cmd.Flags().GetBool("from-glab")
	n := 0
	if gh {
		n++
	}
	if glab {
		n++
	}
	if file != "" {
		n++
	}
	if n != 1 {
		return "", fmt.Errorf("pass exactly one of --token-file, --from-gh, or --from-glab")
	}
	if gh || glab {
		name := "gh"
		if glab {
			name = "glab"
		}
		b, err := connectionTokenCommand(name, "auth", "token")
		if err != nil {
			return "", fmt.Errorf("read %s login failed", name)
		}
		token := strings.TrimSpace(string(b))
		if token == "" {
			return "", fmt.Errorf("%s login is empty", name)
		}
		return token, nil
	}
	var r io.Reader = cmd.InOrStdin()
	if file != "-" {
		f, err := os.Open(file)
		if err != nil {
			return "", err
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", fmt.Errorf("token is empty")
	}
	return token, nil
}

func confirmConnection(cmd *cobra.Command, scope []string, personal bool) error {
	label := "将登记为你的个人连接，覆盖："
	if !personal {
		label = "将登记为工作区连接，覆盖："
	}
	fmt.Fprintln(cmd.ErrOrStderr(), label)
	for _, line := range scope {
		fmt.Fprintf(cmd.ErrOrStderr(), "- %s\n", line)
	}
	yes, _ := cmd.Flags().GetBool("yes")
	if yes {
		return nil
	}
	fmt.Fprint(cmd.ErrOrStderr(), "继续？输入 y 确认：")
	answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if strings.ToLower(strings.TrimSpace(answer)) != "y" {
		return fmt.Errorf("cancelled")
	}
	return nil
}

func runConnectionAdd(cmd *cobra.Command, _ []string) error {
	c, ws, err := connectionClient(cmd)
	if err != nil {
		return err
	}
	provider, _ := cmd.Flags().GetString("provider")
	gh, _ := cmd.Flags().GetBool("from-gh")
	glab, _ := cmd.Flags().GetBool("from-glab")
	if provider == "" {
		switch {
		case gh:
			provider = "github"
		case glab:
			provider = "gitlab"
		}
	}
	if provider == "" {
		return fmt.Errorf("--provider is required")
	}
	token, err := readConnectionToken(cmd)
	if err != nil {
		return err
	}
	instance, _ := cmd.Flags().GetString("instance-url")
	if instance == "" {
		if provider == "gitlab" || glab {
			instance = "https://gitlab.com"
		} else {
			instance = "https://github.com"
		}
	}
	workspaceWide, _ := cmd.Flags().GetBool("workspace")
	agentYes := false
	if yes, _ := cmd.Flags().GetBool("yes"); yes && strings.TrimSpace(os.Getenv("MULTICA_AGENT_ID")) != "" {
		agentYes = true
	}
	if agentYes && workspaceWide {
		return redactToken(fmt.Errorf("--workspace cannot be combined with an agent --yes"), token)
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	scope, err := discoverConnectionScope(ctx, provider, instance, token)
	if err != nil {
		return redactToken(fmt.Errorf("could not read which accounts this login covers: %w", err), token)
	}
	personal := !workspaceWide
	if err = confirmConnection(cmd, scope, personal); err != nil {
		return err
	}
	body := map[string]any{
		"provider":     provider,
		"instance_url": instance,
		"access_token": token,
		"personal":     personal,
		"agent_yes":    agentYes,
	}
	var out map[string]any
	if err = c.PostJSON(ctx, "/api/workspaces/"+ws+"/vcs/connections", body, &out); err != nil {
		return redactToken(fmt.Errorf("add connection: %w", err), token)
	}
	redactConnectionPayload(out)
	if connectionWantsJSON(cmd) {
		return cli.PrintJSON(cmd.OutOrStdout(), out)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "connected %s (%s)\n", out["account_login"], out["id"])
	return nil
}

func runConnectionTest(cmd *cobra.Command, args []string) error {
	c, ws, err := connectionClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var out map[string]any
	if err = c.PostJSON(ctx, "/api/workspaces/"+ws+"/vcs/connections/"+args[0]+"/test", map[string]any{}, &out); err != nil {
		return fmt.Errorf("test connection: %w", err)
	}
	redactConnectionPayload(out)
	if connectionWantsJSON(cmd) {
		return cli.PrintJSON(cmd.OutOrStdout(), out)
	}
	if out["ok"] == true {
		fmt.Fprintf(cmd.OutOrStdout(), "ok  %s\n", out["account_login"])
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "failed  %s\n", out["error"])
	return nil
}

func runConnectionRemove(cmd *cobra.Command, args []string) error {
	c, ws, err := connectionClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	if err = c.DeleteJSON(ctx, "/api/workspaces/"+ws+"/vcs/connections/"+args[0]); err != nil {
		return fmt.Errorf("remove connection: %w", err)
	}
	if connectionWantsJSON(cmd) {
		return cli.PrintJSON(cmd.OutOrStdout(), map[string]any{"removed": args[0]})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", args[0])
	return nil
}

func runConnectionRepoStatus(cmd *cobra.Command, _ []string) error {
	c, ws, err := connectionClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var out map[string]any
	if err = c.GetJSON(ctx, "/api/workspaces/"+ws+"/vcs/connections/repo-status", &out); err != nil {
		return fmt.Errorf("repo status: %w", err)
	}
	if connectionWantsJSON(cmd) {
		return cli.PrintJSON(cmd.OutOrStdout(), out)
	}
	rows, _ := out["repos"].([]any)
	if len(rows) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No repositories")
		return nil
	}
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s  %s\n", row["repo_key"], row["status"], row["matched_by"], row["account_login"])
	}
	return nil
}

func instanceHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
	}
	return u.Hostname()
}

func redactToken(err error, token string) error {
	if err == nil || token == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), token, "[redacted]")
	return errors.New(msg)
}

func redactConnectionPayload(v any) {
	switch val := v.(type) {
	case map[string]any:
		delete(val, "access_token")
		for _, child := range val {
			redactConnectionPayload(child)
		}
	case []any:
		for _, child := range val {
			redactConnectionPayload(child)
		}
	}
}
