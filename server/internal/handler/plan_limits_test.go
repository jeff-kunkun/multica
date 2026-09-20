package handler

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// TestPlanLimitsForResponseAgesOutWindowlessSnapshots is the DENE-606
// regression. A window-less "exhausted" snapshot carries no reset boundary, so
// nothing in it bounds its own life. Before this rule the API served one until
// something replaced it, and for a runtime whose probe cannot answer (dsh with
// no balance key in the daemon's environment) nothing ever did: the runtime read
// as out of quota indefinitely, and `multica runtime list` reported that to the
// agents choosing where to dispatch work.
func TestPlanLimitsForResponseAgesOutWindowlessSnapshots(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	previous := planLimitsNow
	planLimitsNow = func() time.Time { return now }
	t.Cleanup(func() { planLimitsNow = previous })

	reset := now.Add(20 * time.Minute).Unix()
	used := 100.0
	raw := func(snapshot protocol.PlanLimitsSnapshot) []byte {
		data, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatalf("marshal fixture: %v", err)
		}
		return data
	}

	tests := []struct {
		name string
		raw  []byte
		want bool
	}{
		{
			name: "window-less exhausted is dropped once it has aged out",
			raw: raw(protocol.PlanLimitsSnapshot{
				Provider:   "dsh",
				Status:     protocol.PlanLimitsStatusExhausted,
				ObservedAt: now.Add(-2 * time.Hour).Unix(),
			}),
		},
		{
			name: "window-less exhausted is served while still fresh",
			raw: raw(protocol.PlanLimitsSnapshot{
				Provider:   "dsh",
				Status:     protocol.PlanLimitsStatusExhausted,
				ObservedAt: now.Add(-2 * time.Minute).Unix(),
			}),
			want: true,
		},
		{
			name: "a reported window keeps a stale snapshot meaningful",
			raw: raw(protocol.PlanLimitsSnapshot{
				Provider:   "grok",
				Status:     protocol.PlanLimitsStatusExhausted,
				ObservedAt: now.Add(-6 * time.Hour).Unix(),
				Windows:    []protocol.PlanLimitWindow{{Name: "credits", UsedPercent: &used, ResetsAt: &reset}},
			}),
			want: true,
		},
		{name: "no stored snapshot", raw: nil},
		{name: "unreadable snapshot", raw: []byte("{")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := planLimitsForResponse(tc.raw)
			if (got != nil) != tc.want {
				t.Fatalf("snapshot = %+v, want present=%v", got, tc.want)
			}
		})
	}
}

func TestValidatePlanLimitsSnapshot(t *testing.T) {
	t.Parallel()

	used := 42.0
	minutes := int64(300)
	reset := int64(1_800_000_000)
	valid := func() *protocol.PlanLimitsSnapshot {
		return &protocol.PlanLimitsSnapshot{
			Provider:   "codex",
			Status:     protocol.PlanLimitsStatusAvailable,
			ObservedAt: 1_700_000_000,
			Windows: []protocol.PlanLimitWindow{{
				Name:          "primary",
				UsedPercent:   &used,
				WindowMinutes: &minutes,
				ResetsAt:      &reset,
			}},
		}
	}

	tests := []struct {
		name     string
		mutate   func(*protocol.PlanLimitsSnapshot)
		provider string
		wantErr  bool
	}{
		{name: "valid", provider: "codex"},
		{name: "provider mismatch", provider: "claude", wantErr: true},
		{name: "missing observation", provider: "codex", mutate: func(s *protocol.PlanLimitsSnapshot) { s.ObservedAt = 0 }, wantErr: true},
		{name: "unknown status", provider: "codex", mutate: func(s *protocol.PlanLimitsSnapshot) { s.Status = "unknown" }, wantErr: true},
		{name: "available without windows", provider: "codex", mutate: func(s *protocol.PlanLimitsSnapshot) { s.Windows = nil }, wantErr: true},
		{name: "invalid percent", provider: "codex", mutate: func(s *protocol.PlanLimitsSnapshot) { value := 101.0; s.Windows[0].UsedPercent = &value }, wantErr: true},
		{name: "duplicate window", provider: "codex", mutate: func(s *protocol.PlanLimitsSnapshot) { s.Windows = append(s.Windows, s.Windows[0]) }, wantErr: true},
		{name: "exhausted without window", provider: "codex", mutate: func(s *protocol.PlanLimitsSnapshot) { s.Status = protocol.PlanLimitsStatusExhausted; s.Windows = nil }},
		{name: "remaining balance window", provider: "dsh", mutate: func(s *protocol.PlanLimitsSnapshot) {
			s.Provider = "dsh"
			remaining := 12.5
			s.Windows = []protocol.PlanLimitWindow{{Name: "balance_cny", Remaining: &remaining}}
		}},
		{name: "negative remaining", provider: "dsh", mutate: func(s *protocol.PlanLimitsSnapshot) {
			s.Provider = "dsh"
			remaining := -1.0
			s.Windows = []protocol.PlanLimitWindow{{Name: "balance_cny", Remaining: &remaining}}
		}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := valid()
			if tc.mutate != nil {
				tc.mutate(snapshot)
			}
			_, err := validatePlanLimitsSnapshot(snapshot, tc.provider)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
