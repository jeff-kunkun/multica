package blockwait

import (
	"strings"
	"testing"
	"time"
)

const ownerID = "11111111-1111-1111-1111-111111111111"

// Every machine stop the close gate can hit keeps the ticket in progress on a
// clock the first time: checks running, checks red, a conflict, a failed merge.
func TestMachineWaitStaysInProgressWithWake(t *testing.T) {
	now := time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		pr   PRSnapshot
		kind string
	}{
		{"checks pending", PRSnapshot{Number: 1, State: "open", Mergeable: "unstable", Checks: "PENDING", RunningChecks: 2, URL: "u1"}, MachineChecksPending},
		{"checks red", PRSnapshot{Number: 2, State: "open", Mergeable: "unstable", Checks: "FAILURE", FailedChecks: []string{"test"}, URL: "u2"}, MachineChecksRed},
		{"required check running", PRSnapshot{Number: 5, State: "open", Mergeable: "blocked", Checks: "PENDING", RunningChecks: 1, URL: "u5"}, MachineChecksPending},
		{"conflict", PRSnapshot{Number: 3, State: "open", Mergeable: "dirty", Checks: "SUCCESS", URL: "u3"}, MachineConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decisions := []Decision{DecideClose([]PRSnapshot{tc.pr}, now)}
			if tc.kind != MachineChecksPending {
				// A pass tries the merge on running checks; a refused merge
				// is the merge_failed case below.
				decisions = append(decisions, DecideRelease([]PRSnapshot{tc.pr}, now))
			}
			for _, d := range decisions {
				if d.Action != ReleaseWait {
					t.Fatalf("action = %q, want wait", d.Action)
				}
				if d.Machine != tc.kind {
					t.Fatalf("machine = %q, want %q", d.Machine, tc.kind)
				}
				if strings.Contains(d.Reason, "阻塞") {
					t.Fatalf("reason still says 阻塞: %q", d.Reason)
				}
				assertParkedInProgress(t, ParkMachineWait(nil, d.Machine, d.Record.WaitCondition, ownerID, now), now)
			}
		})
	}
	t.Run("merge failed", func(t *testing.T) {
		assertParkedInProgress(t, ParkMachineWait(nil, MachineMergeFailed, "合并 u4 没有成功", ownerID, now), now)
	})
}

func assertParkedInProgress(t *testing.T, park MachinePark, now time.Time) {
	t.Helper()
	if park.Status != "in_progress" || park.Escalated {
		t.Fatalf("park = %+v, want in_progress", park)
	}
	if !park.Record.HasWakeAt || !park.Record.WakeAt.Equal(now.Add(QuietAfter)) {
		t.Fatalf("wake = %v, want %v", park.Record.WakeAt, now.Add(QuietAfter))
	}
	if park.Record.NeedsHuman != "" {
		t.Fatalf("needs_human = %q on a machine wait", park.Record.NeedsHuman)
	}
	if !strings.Contains(park.Note, "票保持进行中") || strings.Contains(park.Note, "阻塞") {
		t.Fatalf("note = %q", park.Note)
	}
	if park.Rounds != 1 {
		t.Fatalf("rounds = %d, want 1", park.Rounds)
	}
}

// The same stop surviving MachineEscalateAfter rounds asks the owner.
func TestMachineWaitEscalatesAfterRepeatedRounds(t *testing.T) {
	now := time.Now()
	meta := map[string]any{}
	var park MachinePark
	for round := 1; round <= MachineEscalateAfter; round++ {
		park = ParkMachineWait(meta, MachineChecksRed, "检查是红的", ownerID, now)
		if park.Rounds != round {
			t.Fatalf("round %d: rounds = %d", round, park.Rounds)
		}
		if round < MachineEscalateAfter && park.Status != "in_progress" {
			t.Fatalf("round %d: status = %q, want in_progress", round, park.Status)
		}
		for k, v := range park.Pairs() {
			meta[k] = v
		}
	}
	if park.Status != "blocked" || !park.Escalated {
		t.Fatalf("final park = %+v, want blocked", park)
	}
	if park.Record.NeedsHuman != ownerID || park.Record.HasWakeAt {
		t.Fatalf("record = %+v, want needs_human owner and no clock", park.Record)
	}
	if !park.Record.Structured() {
		t.Fatal("escalated record is not structured")
	}
}

// A different stop is progress: the counter starts over.
func TestMachineWaitKindChangeResetsRounds(t *testing.T) {
	meta := map[string]any{KeyMachineKind: MachineChecksPending, KeyMachineRounds: "2"}
	park := ParkMachineWait(meta, MachineChecksRed, "检查是红的", ownerID, time.Now())
	if park.Rounds != 1 || park.Status != "in_progress" {
		t.Fatalf("park = %+v, want round 1 in_progress", park)
	}
}

// With nobody to ask, the ticket keeps waiting in progress.
func TestMachineWaitWithoutOwnerKeepsWaiting(t *testing.T) {
	meta := map[string]any{KeyMachineKind: MachineConflict, KeyMachineRounds: "5"}
	park := ParkMachineWait(meta, MachineConflict, "和主线冲突", "", time.Now())
	if park.Status != "in_progress" || park.Escalated || park.Rounds != 6 {
		t.Fatalf("park = %+v, want in_progress round 6", park)
	}
}
