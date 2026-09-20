// The routing gateway key rules. Nothing here touches the database: every
// rule under test is a pure transformation of a settings payload, which is
// what makes them cheap enough to pin exhaustively.
package handler

import (
	"encoding/json"
	"strings"
	"testing"
)

func boxedHandler(t *testing.T) *Handler {
	t.Helper()
	box, err := NewRoutingSecretBox("deployment-jwt-secret")
	if err != nil {
		t.Fatalf("NewRoutingSecretBox: %v", err)
	}
	return &Handler{RoutingSecrets: box}
}

func settingsWith(routingBlock map[string]any) map[string]any {
	return map[string]any{
		"github_enabled": true,
		"routing":        routingBlock,
	}
}

func routingBlockOf(t *testing.T, settings any) map[string]any {
	t.Helper()
	root, ok := settings.(map[string]any)
	if !ok {
		t.Fatalf("settings is not an object: %#v", settings)
	}
	block, ok := root["routing"].(map[string]any)
	if !ok {
		t.Fatalf("routing block missing: %#v", root)
	}
	return block
}

// TestSealedKeyRoundTrips is the base case: what goes in comes back out, and
// the stored form is not the plaintext.
func TestSealedKeyRoundTrips(t *testing.T) {
	h := boxedHandler(t)
	sealed, ok := h.sealRoutingKey("sk-live-abc123")
	if !ok {
		t.Fatal("sealRoutingKey refused a key on a boxed deployment")
	}
	if strings.Contains(sealed, "sk-live-abc123") {
		t.Fatalf("sealed value contains the plaintext: %q", sealed)
	}
	if got := h.openRoutingKey(sealed); got != "sk-live-abc123" {
		t.Fatalf("openRoutingKey = %q, want the original key", got)
	}
}

// TestKeySealedUnderAnotherDeploymentOpensEmpty covers a restored database or
// a rotated secret. The key must read as absent, not as garbage: an empty
// string sends routing to the deployment gateway, while garbage would be
// dialled as if it were a credential.
func TestKeySealedUnderAnotherDeploymentOpensEmpty(t *testing.T) {
	sealed, ok := boxedHandler(t).sealRoutingKey("sk-live-abc123")
	if !ok {
		t.Fatal("seal failed")
	}
	other, err := NewRoutingSecretBox("a-completely-different-secret")
	if err != nil {
		t.Fatalf("NewRoutingSecretBox: %v", err)
	}
	if got := (&Handler{RoutingSecrets: other}).openRoutingKey(sealed); got != "" {
		t.Fatalf("openRoutingKey under a foreign secret = %q, want empty", got)
	}
	if got := (&Handler{}).openRoutingKey(sealed); got != "" {
		t.Fatalf("openRoutingKey with no box = %q, want empty", got)
	}
	if got := (&Handler{RoutingSecrets: other}).openRoutingKey("not base64 at all !!"); got != "" {
		t.Fatalf("openRoutingKey on junk = %q, want empty", got)
	}
}

// TestRedactionStripsTheSealedKey is the rule that keeps the credential off
// the wire. It also asserts the rest of the payload survives: a redaction that
// ate the switch or the model would turn every settings read into a workspace
// that looks unconfigured.
func TestRedactionStripsTheSealedKey(t *testing.T) {
	in := settingsWith(map[string]any{
		"enabled":     true,
		"model":       "gpt-5.6-luna",
		"base_url":    "https://gw.example/v1",
		"api_key_enc": "c2VhbGVk",
	})
	out := redactRoutingSettings(in)
	block := routingBlockOf(t, out)
	if _, present := block["api_key_enc"]; present {
		t.Fatalf("sealed key survived redaction: %#v", block)
	}
	if block["enabled"] != true || block["model"] != "gpt-5.6-luna" || block["base_url"] != "https://gw.example/v1" {
		t.Fatalf("redaction dropped non-secret fields: %#v", block)
	}
	if root, _ := out.(map[string]any); root["github_enabled"] != true {
		t.Fatalf("redaction touched an unrelated settings key: %#v", out)
	}
	// The caller's own map must be unchanged: responses are redacted while
	// other code in the same request may still be reading the settings.
	if _, present := in["routing"].(map[string]any)["api_key_enc"]; !present {
		t.Fatal("redaction mutated the input settings")
	}
}

// TestUnrelatedSettingsWritePreservesTheKey is the failure this whole file
// exists to prevent. The client was never sent the key, so it cannot send it
// back; a write that took the payload literally would silently delete it and
// routing would stop with nothing on screen to explain why.
func TestUnrelatedSettingsWritePreservesTheKey(t *testing.T) {
	h := boxedHandler(t)
	sealed, _ := h.sealRoutingKey("sk-live-abc123")
	stored, _ := json.Marshal(settingsWith(map[string]any{
		"enabled": true, "model": "m", "base_url": "https://gw.example/v1", "api_key_enc": sealed,
	}))

	incoming := settingsWith(map[string]any{
		"enabled": true, "model": "m", "base_url": "https://gw.example/v1",
	})
	merged, ok := h.applyRoutingSecret(incoming, stored)
	if !ok {
		t.Fatal("applyRoutingSecret refused a write it should have accepted")
	}
	if got := routingBlockOf(t, merged)["api_key_enc"]; got != sealed {
		t.Fatalf("api_key_enc = %v, want the stored ciphertext carried forward", got)
	}
}

// TestNewKeyReplacesTheStoredOne — and the plaintext field never reaches
// storage in any form.
func TestNewKeyReplacesTheStoredOne(t *testing.T) {
	h := boxedHandler(t)
	oldSealed, _ := h.sealRoutingKey("sk-old")
	stored, _ := json.Marshal(settingsWith(map[string]any{"api_key_enc": oldSealed}))

	merged, ok := h.applyRoutingSecret(settingsWith(map[string]any{
		"enabled": true, "model": "m", "api_key": "sk-new",
	}), stored)
	if !ok {
		t.Fatal("applyRoutingSecret refused a new key")
	}
	block := routingBlockOf(t, merged)
	if _, present := block["api_key"]; present {
		t.Fatalf("plaintext api_key reached storage: %#v", block)
	}
	sealed, _ := block["api_key_enc"].(string)
	if sealed == "" || sealed == oldSealed {
		t.Fatalf("api_key_enc was not replaced: %q", sealed)
	}
	if got := h.openRoutingKey(sealed); got != "sk-new" {
		t.Fatalf("stored key opens to %q, want sk-new", got)
	}
	encoded, _ := json.Marshal(merged)
	if strings.Contains(string(encoded), "sk-new") {
		t.Fatalf("serialised settings carried the plaintext: %s", encoded)
	}
}

// TestEmptyKeyClearsIt — the only way to remove a stored key, and it has to be
// distinguishable from "this write is about something else".
func TestEmptyKeyClearsIt(t *testing.T) {
	h := boxedHandler(t)
	sealed, _ := h.sealRoutingKey("sk-old")
	stored, _ := json.Marshal(settingsWith(map[string]any{"api_key_enc": sealed}))

	merged, ok := h.applyRoutingSecret(settingsWith(map[string]any{
		"enabled": true, "model": "m", "api_key": "",
	}), stored)
	if !ok {
		t.Fatal("applyRoutingSecret refused a clear")
	}
	block := routingBlockOf(t, merged)
	if _, present := block["api_key_enc"]; present {
		t.Fatalf("an explicit clear left a key behind: %#v", block)
	}
}

// TestDeploymentWithoutABoxRefusesAKey. Accepting and dropping it would show a
// saved-looking form over a key that was never stored.
func TestDeploymentWithoutABoxRefusesAKey(t *testing.T) {
	h := &Handler{}
	if _, ok := h.applyRoutingSecret(settingsWith(map[string]any{"api_key": "sk-live"}), nil); ok {
		t.Fatal("a deployment with no secretbox accepted a routing key")
	}
	// A write that carries no key at all is still fine on such a deployment:
	// the switch, the model and the threshold have nothing to do with the box.
	if _, ok := h.applyRoutingSecret(settingsWith(map[string]any{"enabled": true}), nil); !ok {
		t.Fatal("a deployment with no secretbox refused a key-free settings write")
	}
}

// TestGarbageStoredSettingsCannotSmuggleAValue — an unparseable stored column
// contributes nothing rather than an attacker-shaped string.
func TestGarbageStoredSettingsCannotSmuggleAValue(t *testing.T) {
	if got := storedRoutingSealedKey([]byte("{not json")); got != "" {
		t.Fatalf("storedRoutingSealedKey on junk = %q", got)
	}
	if got := storedRoutingSealedKey(nil); got != "" {
		t.Fatalf("storedRoutingSealedKey on nil = %q", got)
	}
}
