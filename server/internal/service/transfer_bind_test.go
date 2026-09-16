package service

import (
	"strings"
	"testing"
)

// The bind rule is pure, so its matrix lives here: the DB-backed tests in
// internal/handler prove the three tiers end to end and the write, while this
// covers the combinations a fixture would only obscure.
func TestMatchTransferRuntimeCandidates(t *testing.T) {
	claudeLocal := transferRuntimeCandidate{
		RuntimeID: "r-claude", Name: "Claude (mac)", Provider: "claude", RuntimeMode: "local",
	}
	claudeLocalOtherHost := transferRuntimeCandidate{
		RuntimeID: "r-claude-2", Name: "Claude (air)", Provider: "claude", RuntimeMode: "local",
	}
	claudeCloud := transferRuntimeCandidate{
		RuntimeID: "r-cloud", Name: "Claude (cloud)", Provider: "claude", RuntimeMode: "cloud",
	}
	claudeProfile := transferRuntimeCandidate{
		RuntimeID: "r-profile", Name: "Claude (gateway)", Provider: "claude", RuntimeMode: "local",
		ProfileName: "corp-gateway",
	}
	codexLocal := transferRuntimeCandidate{
		RuntimeID: "r-codex", Name: "Codex (mac)", Provider: "codex", RuntimeMode: "local",
	}
	all := []transferRuntimeCandidate{claudeLocal, claudeLocalOtherHost, claudeCloud, claudeProfile, codexLocal}

	cases := []struct {
		name        string
		hint        TransferRuntimeHint
		profileName string
		wantIDs     []string
	}{
		{
			name:    "built-in hint matches only built-in runtimes of the same provider and mode",
			hint:    TransferRuntimeHint{SourceRuntimeID: "s1", Provider: "claude", RuntimeMode: "local"},
			wantIDs: []string{"r-claude", "r-claude-2"},
		},
		{
			name:    "a cloud source never matches a local runtime",
			hint:    TransferRuntimeHint{SourceRuntimeID: "s1", Provider: "claude", RuntimeMode: "cloud"},
			wantIDs: []string{"r-cloud"},
		},
		{
			name:    "another provider is never a candidate",
			hint:    TransferRuntimeHint{SourceRuntimeID: "s1", Provider: "gemini", RuntimeMode: "local"},
			wantIDs: []string{},
		},
		{
			name:    "a hint without a provider is not a match-all",
			hint:    TransferRuntimeHint{SourceRuntimeID: "s1"},
			wantIDs: []string{},
		},
		{
			name:        "a profile hint only matches the same custom profile",
			hint:        TransferRuntimeHint{SourceRuntimeID: "s1", Provider: "claude", RuntimeMode: "local"},
			profileName: "corp-gateway",
			wantIDs:     []string{"r-profile"},
		},
		{
			name:        "a built-in hint never matches a custom-profile runtime",
			hint:        TransferRuntimeHint{SourceRuntimeID: "s1", Provider: "claude", RuntimeMode: "local"},
			profileName: "",
			wantIDs:     []string{"r-claude", "r-claude-2"},
		},
		{
			name:        "an unknown profile name matches nothing",
			hint:        TransferRuntimeHint{SourceRuntimeID: "s1", Provider: "claude", RuntimeMode: "local"},
			profileName: "no-such-profile",
			wantIDs:     []string{},
		},
		{
			name:    "mode is not filtered when the source does not declare one",
			hint:    TransferRuntimeHint{SourceRuntimeID: "s1", Provider: "codex"},
			wantIDs: []string{"r-codex"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchTransferRuntimeCandidates(all, tc.hint, tc.profileName)
			ids := make([]string, 0, len(got))
			for _, c := range got {
				ids = append(ids, c.RuntimeID)
			}
			if len(ids) != len(tc.wantIDs) {
				t.Fatalf("ids=%v want=%v", ids, tc.wantIDs)
			}
			for i := range ids {
				if ids[i] != tc.wantIDs[i] {
					t.Fatalf("ids=%v want=%v", ids, tc.wantIDs)
				}
			}
		})
	}
}

func TestTransferNoRuntimeReasonNamesTheMissingRuntime(t *testing.T) {
	reason := transferNoRuntimeReason(
		TransferRuntimeHint{Provider: "claude", RuntimeMode: "local"},
		"corp-gateway",
	)
	for _, want := range []string{"provider=claude", "runtime_mode=local", "profile=corp-gateway", "daemon"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason=%q missing %q", reason, want)
		}
	}
	if empty := transferNoRuntimeReason(TransferRuntimeHint{}, ""); empty == "" {
		t.Fatal("reason must never be empty; a zero-candidate row needs a readable cause")
	}
}
