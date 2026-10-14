package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestBotConnectClaimUsable(t *testing.T) {
	full := botConnectClaim{Status: "claimed", Token: "mul_x", BotID: "b", WorkspaceID: "w"}
	if err := full.usable(); err != nil {
		t.Fatalf("full claim: %v", err)
	}
	// A repeat claim after the key is in use answers "claimed" with no key:
	// that must not be saved as a login.
	for _, c := range []botConnectClaim{
		{Status: "claimed"},
		{Status: "claimed", Token: "mul_x", BotID: "b"},
		{Status: "claimed", BotID: "b", WorkspaceID: "w"},
	} {
		err := c.usable()
		if err == nil || !strings.Contains(err.Error(), "没有改动") {
			t.Fatalf("partial claim %+v: got %v", c, err)
		}
	}
	for _, status := range []string{"denied", "expired", "approved"} {
		if err := (botConnectClaim{Status: status, Token: "t", BotID: "b", WorkspaceID: "w"}).usable(); err == nil {
			t.Fatalf("status %s accepted", status)
		}
	}
}

// DENE-2041: without the flag the ask carries no lifetime (an older server
// still accepts it and the key never expires); with it, 0 is sent as never.
func TestBotConnectStartBodyExpiry(t *testing.T) {
	newCmd := func(args ...string) *cobra.Command {
		cmd := &cobra.Command{}
		cmd.Flags().Int("expires-in-days", 0, "")
		if err := cmd.Flags().Parse(args); err != nil {
			t.Fatal(err)
		}
		return cmd
	}
	body, err := botConnectStartBody(newCmd(), "Grok", "grok", "https://bot", "k")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := body["expires_in_days"]; ok {
		t.Fatalf("default ask carries a lifetime: %v", body)
	}
	for args, want := range map[string]int{"0": 0, "30": 30} {
		body, err := botConnectStartBody(newCmd("--expires-in-days", args), "Grok", "grok", "https://bot", "k")
		if err != nil || body["expires_in_days"] != want {
			t.Fatalf("--expires-in-days %s = %v %v, want %d", args, body["expires_in_days"], err, want)
		}
	}
	if _, err := botConnectStartBody(newCmd("--expires-in-days", "-1"), "Grok", "grok", "https://bot", "k"); err == nil {
		t.Fatal("negative lifetime accepted")
	}
}
