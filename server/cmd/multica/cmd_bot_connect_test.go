package main

import (
	"strings"
	"testing"
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
