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
