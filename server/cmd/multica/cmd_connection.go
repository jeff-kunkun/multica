package main

import (
	"bufio"
	"context"
	"fmt"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
	"io"
	"os"
	"os/exec"
	"strings"
)

var connectionCmd = &cobra.Command{Use: "connection", Short: "Manage Git connections"}
var connectionRepoCmd = &cobra.Command{Use: "repo", Short: "Inspect repository connections"}
var connectionListCmd = &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: runConnectionList}
var connectionAddCmd = &cobra.Command{Use: "add", Args: cobra.NoArgs, RunE: runConnectionAdd}
var connectionTestCmd = &cobra.Command{Use: "test <connection-id>", Args: exactArgs(1), RunE: runConnectionTest}
var connectionRemoveCmd = &cobra.Command{Use: "remove <connection-id>", Args: exactArgs(1), RunE: runConnectionRemove}
var connectionRepoStatusCmd = &cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: runConnectionRepoStatus}

func init() {
	for _, c := range []*cobra.Command{connectionListCmd, connectionAddCmd, connectionTestCmd, connectionRemoveCmd, connectionRepoStatusCmd} {
		c.Flags().String("output", "table", "Output format: table or json")
	}
	connectionAddCmd.Flags().String("provider", "", "Provider: github or gitlab")
	connectionAddCmd.Flags().String("instance-url", "", "Provider instance URL (default is provider public host)")
	connectionAddCmd.Flags().String("token-file", "", "Read token from file, or '-' for stdin")
	connectionAddCmd.Flags().Bool("from-gh", false, "Read the token from gh auth token")
	connectionAddCmd.Flags().Bool("from-glab", false, "Read the token from glab auth token")
	connectionAddCmd.Flags().Bool("yes", false, "Confirm without prompting (agent use is limited by the server)")
	connectionCmd.AddCommand(connectionListCmd, connectionAddCmd, connectionTestCmd, connectionRemoveCmd, connectionRepoCmd)
	connectionRepoCmd.AddCommand(connectionRepoStatusCmd)
	rootCmd.AddCommand(connectionCmd)
}
func connectionClient(cmd *cobra.Command) (*cli.APIClient, string, error) {
	ws, e := requireWorkspaceID(cmd)
	if e != nil {
		return nil, "", e
	}
	c, e := newAPIClient(cmd)
	return c, ws, e
}
func connectionOutput(cmd *cobra.Command, v any) error {
	o, _ := cmd.Flags().GetString("output")
	if o == "json" {
		return cli.PrintJSON(cmd.OutOrStdout(), v)
	}
	return nil
}
func runConnectionList(cmd *cobra.Command, _ []string) error {
	c, ws, e := connectionClient(cmd)
	if e != nil {
		return e
	}
	ctx, cn := cli.APIContext(context.Background())
	defer cn()
	var out any
	if e = c.GetJSON(ctx, "/api/workspaces/"+ws+"/vcs/connections", &out); e != nil {
		return fmt.Errorf("list connections: %w", e)
	}
	if o, _ := cmd.Flags().GetString("output"); o == "json" {
		return cli.PrintJSON(cmd.OutOrStdout(), out)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Connections")
	return nil
}
func readConnectionToken(cmd *cobra.Command) (string, error) {
	file, _ := cmd.Flags().GetString("token-file")
	gh, _ := cmd.Flags().GetBool("from-gh")
	glab, _ := cmd.Flags().GetBool("from-glab")
	if gh && glab {
		return "", fmt.Errorf("choose only one of --from-gh or --from-glab")
	}
	if gh || glab {
		name := "gh"
		args := []string{"auth", "token"}
		if glab {
			name = "glab"
			args = []string{"auth", "token"}
		}
		b, e := exec.Command(name, args...).Output()
		if e != nil {
			return "", fmt.Errorf("read %s login: %w", name, e)
		}
		return strings.TrimSpace(string(b)), nil
	}
	if file == "" {
		return "", fmt.Errorf("token is required via --token-file, --from-gh, or --from-glab")
	}
	var r io.Reader = os.Stdin
	if file != "-" {
		f, e := os.Open(file)
		if e != nil {
			return "", e
		}
		defer f.Close()
		r = f
	}
	b, e := io.ReadAll(r)
	if e != nil {
		return "", e
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		return "", fmt.Errorf("token is empty")
	}
	return t, nil
}
func confirmConnection(cmd *cobra.Command, scope string) error {
	y, _ := cmd.Flags().GetBool("yes")
	if y {
		return nil
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "This will connect %s. Continue? [y/N] ", scope)
	s, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if strings.ToLower(strings.TrimSpace(s)) != "y" {
		return fmt.Errorf("cancelled")
	}
	return nil
}
func runConnectionAdd(cmd *cobra.Command, _ []string) error {
	c, ws, e := connectionClient(cmd)
	if e != nil {
		return e
	}
	p, _ := cmd.Flags().GetString("provider")
	gh, _ := cmd.Flags().GetBool("from-gh")
	glab, _ := cmd.Flags().GetBool("from-glab")
	if p == "" {
		if gh {
			p = "github"
		}
		if glab {
			p = "gitlab"
		}
	}
	if p == "" {
		return fmt.Errorf("--provider is required")
	}
	token, e := readConnectionToken(cmd)
	if e != nil {
		return e
	}
	if e = confirmConnection(cmd, p+" account in this workspace"); e != nil {
		return e
	}
	inst, _ := cmd.Flags().GetString("instance-url")
	if inst == "" {
		if p == "gitlab" {
			inst = "https://gitlab.com"
		} else {
			inst = "https://github.com"
		}
	}
	ctx, cn := cli.APIContext(context.Background())
	defer cn()
	var out any
	body := map[string]any{"provider": p, "instance_url": inst, "access_token": token}
	if e = c.PostJSON(ctx, "/api/workspaces/"+ws+"/vcs/connections", body, &out); e != nil {
		return fmt.Errorf("add connection: %w", e)
	}
	return connectionOutput(cmd, out)
}
func runConnectionTest(cmd *cobra.Command, args []string) error {
	c, ws, e := connectionClient(cmd)
	if e != nil {
		return e
	}
	ctx, cn := cli.APIContext(context.Background())
	defer cn()
	var out any
	e = c.PostJSON(ctx, "/api/workspaces/"+ws+"/vcs/connections/"+args[0]+"/test", map[string]any{}, &out)
	if e != nil {
		return fmt.Errorf("test connection: %w", e)
	}
	return connectionOutput(cmd, out)
}
func runConnectionRemove(cmd *cobra.Command, args []string) error {
	c, ws, e := connectionClient(cmd)
	if e != nil {
		return e
	}
	ctx, cn := cli.APIContext(context.Background())
	defer cn()
	if e = c.DeleteJSON(ctx, "/api/workspaces/"+ws+"/vcs/connections/"+args[0]); e != nil {
		return fmt.Errorf("remove connection: %w", e)
	}
	return connectionOutput(cmd, map[string]any{"removed": args[0]})
}
func runConnectionRepoStatus(cmd *cobra.Command, _ []string) error {
	c, ws, e := connectionClient(cmd)
	if e != nil {
		return e
	}
	ctx, cn := cli.APIContext(context.Background())
	defer cn()
	var out any
	e = c.GetJSON(ctx, "/api/workspaces/"+ws+"/github/coverage", &out)
	if e != nil {
		return fmt.Errorf("repository connection status: %w", e)
	}
	return connectionOutput(cmd, out)
}
