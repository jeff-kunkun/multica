package handler

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func urgentTestRuntime(id byte, owner byte, provider, status string, seenAgo time.Duration, now time.Time) db.AgentRuntime {
	rt := db.AgentRuntime{
		ID:       pgtype.UUID{Bytes: [16]byte{id}, Valid: true},
		Provider: provider,
		Status:   status,
		Name:     provider + " (host)",
	}
	if owner != 0 {
		rt.OwnerID = pgtype.UUID{Bytes: [16]byte{owner}, Valid: true}
	}
	if seenAgo >= 0 {
		rt.LastSeenAt = pgtype.Timestamptz{Time: now.Add(-seenAgo), Valid: true}
	}
	return rt
}

// The one rule for which runtime may stand in for another (DENE-1844): same
// owner and same provider, never itself. Shared by agent update and urgent
// dispatch, so a single table pins both surfaces.
func TestFallbackRuntimeCompatible(t *testing.T) {
	now := time.Now()
	primary := urgentTestRuntime(1, 9, "claude", "online", 0, now)
	cases := []struct {
		name     string
		fallback db.AgentRuntime
		wantErr  bool
	}{
		{"same owner same provider", urgentTestRuntime(2, 9, "claude", "offline", 0, now), false},
		{"itself", primary, true},
		{"other owner", urgentTestRuntime(2, 7, "claude", "online", 0, now), true},
		{"other provider", urgentTestRuntime(2, 9, "codex", "online", 0, now), true},
		{"ownerless fallback", urgentTestRuntime(2, 0, "claude", "online", 0, now), true},
	}
	for _, tc := range cases {
		err := fallbackRuntimeCompatible(primary, tc.fallback)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
	}
	ownerless := urgentTestRuntime(1, 0, "claude", "online", 0, now)
	if err := fallbackRuntimeCompatible(ownerless, urgentTestRuntime(2, 0, "claude", "online", 0, now)); err == nil {
		t.Error("two ownerless runtimes must not pair: nil-owner equality is not ownership")
	}
}

// runtimeCanClaim must agree with the ClaimAgentTask fence: online AND a
// heartbeat inside the freshness window; a stale "online" row cannot claim.
func TestRuntimeCanClaimMirrorsClaimFence(t *testing.T) {
	now := time.Now()
	if !runtimeCanClaim(urgentTestRuntime(1, 9, "claude", "online", 10*time.Second, now), now) {
		t.Error("fresh online runtime should be claimable")
	}
	if runtimeCanClaim(urgentTestRuntime(1, 9, "claude", "offline", 10*time.Second, now), now) {
		t.Error("offline runtime must not be claimable")
	}
	if runtimeCanClaim(urgentTestRuntime(1, 9, "claude", "online", 10*time.Minute, now), now) {
		t.Error("online runtime with a stale heartbeat must not be claimable")
	}
	noSeen := urgentTestRuntime(1, 9, "claude", "online", -1, now)
	if runtimeCanClaim(noSeen, now) {
		t.Error("runtime with no last_seen_at and no updated_at must not be claimable")
	}
	noSeen.UpdatedAt = pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true}
	if !runtimeCanClaim(noSeen, now) {
		t.Error("updated_at stands in for a missing last_seen_at, as in the SQL fence")
	}
}

func TestUrgentRuntimeOfflineReasonNamesRuntimeAndLastSeen(t *testing.T) {
	now := time.Date(2026, 10, 11, 9, 0, 10, 0, time.UTC)
	rt := urgentTestRuntime(1, 9, "Claude", "offline", 0, now)
	rt.Name = "Claude (MacBook-Pro.local)"
	got := urgentRuntimeOfflineReason(rt)
	want := "runtime_offline: Claude (MacBook-Pro.local) last_seen 2026-10-11T09:00:10Z"
	if got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
}
