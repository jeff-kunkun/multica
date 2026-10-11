package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

// Display metadata only: this cache is never consulted to choose, refresh or
// replace a credential. The fingerprint isolates profiles, servers and keys.
type credentialDisplay struct {
	Kind        string  `json:"key_type"`
	ID          string  `json:"key_id,omitempty"`
	Name        string  `json:"key_name,omitempty"`
	WorkspaceID string  `json:"workspace_id,omitempty"`
	ExpiresAt   *string `json:"expires_at"`
	Known       bool    `json:"expiry_known"`
}

func credentialDisplayPath(cmd *cobra.Command, server, token string) string {
	dir, err := cli.ProfileDir(resolveProfile(cmd))
	if err != nil {
		return ""
	}
	return filepath.Join(dir, fmt.Sprintf("auth-status-%x.json", sha256.Sum256([]byte(server+"\n"+token))))
}

func readCredentialDisplay(cmd *cobra.Command, server, token string) credentialDisplay {
	var d credentialDisplay
	if data, err := os.ReadFile(credentialDisplayPath(cmd, server, token)); err == nil {
		_ = json.Unmarshal(data, &d)
	}
	if d.Kind == "" {
		d.Kind = "unknown"
	}
	return d
}

func saveCredentialDisplay(cmd *cobra.Command, server, token string, d credentialDisplay) {
	path := credentialDisplayPath(cmd, server, token)
	if path == "" {
		return
	}
	data, err := json.Marshal(d)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o700) == nil {
		_ = os.WriteFile(path, data, 0o600)
	}
}

func matchingCredential(rows []map[string]any, token, kind, wsID string) (credentialDisplay, bool) {
	var matches []map[string]any
	for _, row := range rows {
		prefix := strVal(row, "token_prefix")
		if len(prefix) >= 12 && strings.HasPrefix(token, prefix) {
			matches = append(matches, row)
		}
	}
	if len(matches) != 1 {
		return credentialDisplay{}, false
	}
	row := matches[0]
	d := credentialDisplay{Kind: kind, ID: strVal(row, "id"), Name: strVal(row, "name"), WorkspaceID: wsID}
	if raw, present := row["expires_at"]; present {
		d.Known = true
		if value, ok := raw.(string); ok {
			d.ExpiresAt = &value
		}
	}
	return d, true
}

func loadCredentialDisplay(ctx context.Context, cmd *cobra.Command, client *cli.APIClient, token string) (credentialDisplay, error) {
	if strings.HasPrefix(token, "mat_") {
		return credentialDisplay{Kind: "task"}, nil
	}
	if strings.HasPrefix(token, "mcn_") {
		return credentialDisplay{Kind: "cloud_node"}, nil
	}
	if !strings.HasPrefix(token, "mul_") {
		return credentialDisplay{Kind: "session"}, nil
	}
	var personal []map[string]any
	if err := client.GetJSON(ctx, "/api/tokens", &personal); err != nil {
		return credentialDisplay{}, err
	}
	if d, ok := matchingCredential(personal, token, "personal", ""); ok {
		return d, nil
	}
	workspaceID := resolveWorkspaceID(cmd)
	workspaces := []workspaceSummary{{ID: workspaceID}}
	if workspaceID == "" {
		if err := client.GetJSON(ctx, "/api/workspaces", &workspaces); err != nil {
			return credentialDisplay{}, err
		}
	}
	for _, ws := range workspaces {
		var bots []map[string]any
		if err := client.GetJSON(ctx, botsPath(ws.ID), &bots); err != nil {
			return credentialDisplay{}, err
		}
		if d, ok := matchingCredential(bots, token, "bot", ws.ID); ok {
			return d, nil
		}
	}
	return credentialDisplay{Kind: "unknown"}, nil
}

func credentialRenewalHint(d credentialDisplay) string {
	if d.Kind == "bot" {
		id := d.ID
		if id == "" {
			id = "<bot-id>"
		}
		ws := d.WorkspaceID
		if ws == "" {
			ws = "<workspace-id>"
		}
		return fmt.Sprintf("From an administrator's authenticated session (without MULTICA_TOKEN), run `multica bot key %s --workspace %s --profile <admin-profile>`, then install the new Bot key. This session keeps its selected identity.", id, ws)
	}
	if d.Kind == "task" {
		return "Ask the task owner to restart this task for a fresh task credential."
	}
	if d.Kind == "personal" || d.Kind == "session" {
		return "Run `multica login` to renew the personal credential; MULTICA_TOKEN, when set, must also be updated."
	}
	return "For a Bot key, an administrator must run `multica bot key <bot-id> --workspace <workspace-id> --profile <admin-profile>` from a session without MULTICA_TOKEN. For a personal key, run `multica login`. The selected key is retained; no other identity is tried."
}

func credentialExpiry(d credentialDisplay, now time.Time) string {
	if !d.Known {
		return "unknown"
	}
	if d.ExpiresAt == nil {
		return "never"
	}
	expires, err := time.Parse(time.RFC3339, *d.ExpiresAt)
	if err != nil {
		return "unknown"
	}
	if !expires.After(now) {
		return "expired"
	}
	if expires.Sub(now) <= 7*24*time.Hour {
		return "expiring_soon"
	}
	return "valid"
}

func runCredentialStatus(cmd *cobra.Command, token, server string) error {
	client := cli.NewAPIClient(server, "", token)
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	d := readCredentialDisplay(cmd, server, token)
	if strings.HasPrefix(token, "mat_") {
		d = credentialDisplay{Kind: "task"}
	}
	var me map[string]any
	err := client.GetJSON(ctx, "/api/me", &me)
	cached := true
	if err == nil {
		fresh, metadataErr := loadCredentialDisplay(ctx, cmd, client, token)
		if metadataErr == nil {
			d = fresh
			cached = false
			saveCredentialDisplay(cmd, server, token, d)
		} else {
			fmt.Fprintf(cmd.ErrOrStderr(), "Could not read key metadata: %v\n", metadataErr)
		}
	}
	expiry := credentialExpiry(d, time.Now())
	status := "authenticated"
	if err != nil {
		status = "unverified"
	}
	var httpErr *cli.HTTPError
	rejected := errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnauthorized
	if rejected {
		status = "rejected"
	}
	source := "profile"
	if strings.TrimSpace(os.Getenv("MULTICA_TOKEN")) != "" {
		source = "MULTICA_TOKEN"
	}
	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		out := map[string]any{"server": server, "status": status, "source": source, "key": d, "expiry_status": expiry, "metadata_cached": cached, "renewal_command": credentialRenewalHint(d)}
		if me != nil {
			out["user"] = map[string]any{"name": me["name"], "email": me["email"]}
		}
		if printErr := cli.PrintJSON(cmd.OutOrStdout(), out); printErr != nil {
			return printErr
		}
	} else {
		expires := "unknown"
		if d.Known {
			expires = "never"
		}
		if d.ExpiresAt != nil {
			expires = *d.ExpiresAt
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Server:  %s\nUser:    %s (%s)\nKey:     %s %s (%s)\nSource:  %s\nExpires: %s (%s)\nStatus:  %s\n", server, strVal(me, "name"), strVal(me, "email"), d.Kind, d.Name, d.ID, source, expires, expiry, status)
		if cached {
			fmt.Fprintln(cmd.ErrOrStderr(), "Key metadata is cached or unknown; it does not authenticate this session.")
		}
	}
	if rejected {
		return cli.WithUserMessage("Selected key is invalid, expired or revoked. "+credentialRenewalHint(d), err)
	}
	if err != nil {
		return err
	}
	if expiry == "expired" || expiry == "expiring_soon" {
		fmt.Fprintf(cmd.ErrOrStderr(), "Key %s. %s\n", expiry, credentialRenewalHint(d))
	}
	return nil
}

// Adds the Bot remedy to ordinary CLI failures without changing how the HTTP
// client or daemon chooses a token. Transport and task-token errors stay intact.
func authFailureHint(cmd *cobra.Command, err error) error {
	var httpErr *cli.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusUnauthorized || httpErr.TaskScoped {
		return err
	}
	var displayErr *cli.UserMessageError
	if errors.As(err, &displayErr) {
		return err
	}
	token := resolveToken(cmd)
	if !strings.HasPrefix(token, "mul_") {
		return err
	}
	d := readCredentialDisplay(cmd, resolveServerURL(cmd), token)
	return cli.WithUserMessage("Selected key was rejected (invalid, expired or revoked). "+credentialRenewalHint(d), err)
}
