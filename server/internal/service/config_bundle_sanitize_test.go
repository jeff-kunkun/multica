package service

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSanitizeBundleSecrets_NullsDenylistedKeys(t *testing.T) {
	bundle := &ConfigBundle{
		Format:        ConfigBundleFormat,
		SchemaVersion: ConfigBundleSchemaVersion,
		Entities: ConfigEntities{
			Agents: []ConfigAgent{{
				SourceID:      "a1",
				Name:          "bot",
				RuntimeConfig: json.RawMessage(`{"gateway":{"url":"http://x","token":"SHOULD_GO"},"mode":"gateway"}`),
			}},
		},
	}
	sanitizeBundleSecrets(bundle)
	raw, _ := json.Marshal(bundle)
	if strings.Contains(string(raw), "SHOULD_GO") {
		t.Fatalf("denylist left a token value in the bundle: %s", raw)
	}
	found := false
	for _, s := range bundle.SecretsOmitted {
		if s.Reason == secretReasonDenylist {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a key_name_denylist secrets_omitted entry")
	}
}

func TestSanitizeBundleSecrets_NullsCompoundKeyNames(t *testing.T) {
	bundle := &ConfigBundle{
		Format:        ConfigBundleFormat,
		SchemaVersion: ConfigBundleSchemaVersion,
		Entities: ConfigEntities{
			Workspace: &ConfigWorkspace{
				Settings: json.RawMessage(`{"always_redact_env":true,"access_token":"LEAK_A","nested":{"GITHUB_TOKEN":"LEAK_B"}}`),
			},
			Skills: []ConfigSkill{{
				SourceID: "s1", Name: "sk",
				Config: json.RawMessage(`{"clientSecret":"LEAK_C","x-api-key":"LEAK_D","private_key":"LEAK_E","max_tokens":4096}`),
			}},
		},
	}
	sanitizeBundleSecrets(bundle)
	raw, _ := json.Marshal(bundle)
	for _, leak := range []string{"LEAK_A", "LEAK_B", "LEAK_C", "LEAK_D", "LEAK_E"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("compound secret key %s survived: %s", leak, raw)
		}
	}
	if !strings.Contains(string(raw), `"max_tokens":4096`) {
		t.Fatalf("max_tokens must not be treated as a secret: %s", raw)
	}
}

func TestSecretKeyName(t *testing.T) {
	for key, want := range map[string]bool{
		"token": true, "access_token": true, "GITHUB_TOKEN": true, "clientSecret": true,
		"x-api-key": true, "apiKey": true, "credentials": true, "Authorization": true,
		"max_tokens": false, "tokenizer": false, "key": false, "url": false, "has_custom_env": false,
		"secrets_omitted": false,
	} {
		if got := secretKeyName(key); got != want {
			t.Errorf("secretKeyName(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestBundleContainsSecret_RejectsNonNullCustomEnv(t *testing.T) {
	bundle := &ConfigBundle{
		Entities: ConfigEntities{
			Agents: []ConfigAgent{{
				Name:      "bot",
				CustomEnv: json.RawMessage(`{"K":"v"}`),
			}},
		},
	}
	if err := bundleContainsSecret(bundle); err == nil {
		t.Fatal("expected secret rejection")
	}
}

func TestBundleContainsSecret_AllowsNullPlaceholders(t *testing.T) {
	bundle := &ConfigBundle{
		Entities: ConfigEntities{
			Agents: []ConfigAgent{{
				Name:      "bot",
				CustomEnv: json.RawMessage("null"),
				McpConfig: json.RawMessage("null"),
			}},
		},
	}
	if err := bundleContainsSecret(bundle); err != nil {
		t.Fatalf("null placeholders should be allowed: %v", err)
	}
}

func TestRedactSecretArgs_MasksFlagValues(t *testing.T) {
	raw := []byte(`["--model","opus","--api-key","LEAK_A","--token=LEAK_B","GITHUB_TOKEN=LEAK_C","--max-tokens","4096","--verbose"]`)
	out, masked := redactSecretArgs(raw)
	if masked != 3 {
		t.Fatalf("masked = %d, want 3: %s", masked, out)
	}
	for _, leak := range []string{"LEAK_A", "LEAK_B", "LEAK_C"} {
		if strings.Contains(string(out), leak) {
			t.Fatalf("secret arg %s survived: %s", leak, out)
		}
	}
	for _, keep := range []string{`"opus"`, `"4096"`, `"--verbose"`} {
		if !strings.Contains(string(out), keep) {
			t.Fatalf("non-secret arg %s was lost: %s", keep, out)
		}
	}
}

func TestRestoreSecretArgs_KeepsTargetValueOrDropsPair(t *testing.T) {
	imported, _ := redactSecretArgs([]byte(`["--api-key","SRC","--token=SRC","--model","opus"]`))

	restored := string(restoreSecretArgs(imported, []byte(`["--api-key","TGT_A","--token=TGT_B"]`)))
	if restored != `["--api-key","TGT_A","--token=TGT_B","--model","opus"]` {
		t.Fatalf("overwrite must keep target secrets: %s", restored)
	}

	created := string(restoreSecretArgs(imported, nil))
	if created != `["--model","opus"]` {
		t.Fatalf("new agent must drop masked pairs: %s", created)
	}
	if strings.Contains(created, secretArgPlaceholder) {
		t.Fatalf("placeholder leaked into agent args: %s", created)
	}
}

func TestKeepTargetGatewayToken_BundleWithoutGateway(t *testing.T) {
	out := keepTargetGatewayToken(json.RawMessage(`{"mode":"local"}`), []byte(`{"gateway":{"url":"http://x","token":"TGT"}}`))
	if !strings.Contains(string(out), `"token":"TGT"`) {
		t.Fatalf("overwrite erased the target gateway token: %s", out)
	}
}
