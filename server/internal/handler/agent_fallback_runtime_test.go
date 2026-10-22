package handler

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func fallbackTestRuntime(id byte, owner byte, provider string) db.AgentRuntime {
	rt := db.AgentRuntime{
		ID:       pgtype.UUID{Bytes: [16]byte{id}, Valid: true},
		Provider: provider,
	}
	if owner != 0 {
		rt.OwnerID = pgtype.UUID{Bytes: [16]byte{owner}, Valid: true}
	}
	return rt
}

// The one rule for which runtime may stand in for another: same owner and
// same provider, never itself.
func TestFallbackRuntimeCompatible(t *testing.T) {
	primary := fallbackTestRuntime(1, 9, "claude")
	cases := []struct {
		name     string
		fallback db.AgentRuntime
		wantErr  bool
	}{
		{"same owner same provider", fallbackTestRuntime(2, 9, "claude"), false},
		{"itself", primary, true},
		{"other owner", fallbackTestRuntime(2, 7, "claude"), true},
		{"other provider", fallbackTestRuntime(2, 9, "codex"), true},
		{"ownerless fallback", fallbackTestRuntime(2, 0, "claude"), true},
	}
	for _, tc := range cases {
		err := fallbackRuntimeCompatible(primary, tc.fallback)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
	}
	ownerless := fallbackTestRuntime(1, 0, "claude")
	if err := fallbackRuntimeCompatible(ownerless, fallbackTestRuntime(2, 0, "claude")); err == nil {
		t.Error("two ownerless runtimes must not pair: nil-owner equality is not ownership")
	}
}
