package routing

import (
	"context"
	"strings"
	"testing"
)

// DENE-1834 最强档门槛: the top rung only when its seat is not tight and no
// strong or medium seat can take the work; the comment says 为什么非她不可.

func strongestStore(usage string) *fakeStore {
	store := newFakeStore()
	// Creator facts put the table on strong, so a confident strongest from
	// the judge is a one-rung raise the ladder accepts.
	withCreatorFacts(store, strongFacts)
	store.roster = map[string]Agent{
		"布尔玛": {ID: "bulma", Name: "布尔玛", Tier: "strongest", RuntimeMode: "claude-code", Usage: usage},
		"孙悟空": {ID: "goku", Name: "孙悟空", Tier: "strong", RuntimeMode: "codex"},
		"克林":  {ID: "krillin", Name: "克林", Tier: "medium", RuntimeMode: "codex"},
		"拉蒂兹": {ID: "raditz", Name: "拉蒂兹", Tier: "weak", RuntimeMode: "codex"},
	}
	return store
}

func strongestVerdict() Verdict {
	v := confidentVerdict()
	v.ExecutorTier, v.ExecutorConfidence = "strongest", 1
	return v
}

func assignmentBody(store *fakeStore) string {
	return strings.Join(store.comments[KindAssignment], "\n")
}

func TestStrongestYieldsToAStrongSeat(t *testing.T) {
	store := strongestStore(UsageAmple)
	out, err := newRouter(store, &fakeJudge{verdict: strongestVerdict()}).Route(context.Background(), "ws", "issue-1")
	if err != nil || out.ExecutorWritten == nil || out.ExecutorWritten.ID != "goku" {
		t.Fatalf("out = %+v err = %v, want the strong seat", out.ExecutorWritten, err)
	}
	if !out.StrongestGate.Moved || out.StrongestGate.To != "strong" {
		t.Fatalf("gate = %+v", out.StrongestGate)
	}
	if body := assignmentBody(store); !strings.Contains(body, "最强档门槛：强档有合适席位 孙悟空，不派最强档，改派强档") {
		t.Fatalf("comment:\n%s", body)
	}
}

func TestStrongestStaysWhenMiddleRungsHaveNobody(t *testing.T) {
	store := strongestStore(UsageAmple)
	store.facts = RoutingFacts{Seats: map[string]SeatSnapshot{
		"goku":    {AgentID: "goku", Availability: AvailabilityDead},
		"krillin": {AgentID: "krillin", Availability: AvailabilityQuotaExhausted},
	}}
	out, err := newRouter(store, &fakeJudge{verdict: strongestVerdict()}).Route(context.Background(), "ws", "issue-1")
	if err != nil || out.ExecutorWritten == nil || out.ExecutorWritten.ID != "bulma" {
		t.Fatalf("out = %+v err = %v, want the strongest seat kept", out.ExecutorWritten, err)
	}
	if out.StrongestGate.Moved {
		t.Fatalf("gate = %+v", out.StrongestGate)
	}
	body := assignmentBody(store)
	for _, want := range []string{"最强档门槛：为什么非她不可：强档 孙悟空 离线，中档 克林 额度用尽；布尔玛 额度充足"} {
		if !strings.Contains(body, want) {
			t.Fatalf("comment lacks %q:\n%s", want, body)
		}
	}
	// No seat on a middle rung at all is said as such.
	store2 := strongestStore(UsageAmple)
	delete(store2.roster, "孙悟空")
	delete(store2.roster, "克林")
	out2, _ := newRouter(store2, &fakeJudge{verdict: strongestVerdict()}).Route(context.Background(), "ws", "issue-1")
	if out2.ExecutorWritten == nil || out2.ExecutorWritten.ID != "bulma" || !strings.Contains(out2.StrongestGate.Why, "强档没有席位，中档没有席位") {
		t.Fatalf("out = %+v gate = %+v", out2.ExecutorWritten, out2.StrongestGate)
	}
}

func TestStrongestTightCannotSkipToWeakOrForceDispatch(t *testing.T) {
	store := strongestStore(UsageTight)
	delete(store.roster, "孙悟空")
	delete(store.roster, "克林")
	out, err := newRouter(store, &fakeJudge{verdict: strongestVerdict()}).Route(context.Background(), "ws", "issue-1")
	if err != nil || out.ExecutorWritten != nil || out.Action != ActionSkipped {
		t.Fatalf("%+v %v", out, err)
	}
	if !strings.Contains(assignmentBody(store), "最强档不作兜底") {
		t.Fatal(assignmentBody(store))
	}
}

func TestStrongestGateRespectsLabelAndSwitch(t *testing.T) {
	store := strongestStore(UsageAmple)
	store.issue.Labels = []string{"最强"}
	out, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1")
	if err != nil || out.ExecutorWritten == nil || out.ExecutorWritten.ID != "bulma" || out.StrongestGate.Checked {
		t.Fatalf("labelled: out = %+v gate = %+v err = %v", out.ExecutorWritten, out.StrongestGate, err)
	}
	store2 := strongestStore(UsageAmple)
	store2.settings.StrongestGate = boolPtr(false)
	out2, _ := newRouter(store2, &fakeJudge{verdict: strongestVerdict()}).Route(context.Background(), "ws", "issue-1")
	if out2.ExecutorWritten == nil || out2.ExecutorWritten.ID != "bulma" || out2.StrongestGate.Checked {
		t.Fatalf("switched off: out = %+v gate = %+v", out2.ExecutorWritten, out2.StrongestGate)
	}
}

func TestPickSeatAppliesStrongestGate(t *testing.T) {
	// A chat has no creator facts: the analysis role answers, and the table
	// lands on strong so the judge's strongest is an accepted raise.
	store := &cachingStore{fakeStore: strongestStore(UsageAmple)}
	store.settings = analysisAndJudge()
	r := newRouter(store, &fakeJudge{verdict: strongestVerdict()})
	r.Analyst = &fakeAnalyst{record: AnalysisRecord{Facts: strongFacts, Source: FactsFromAnalysis}}
	pick, err := r.PickSeat(context.Background(), "ws", PickRequest{Title: "重构调度核心", Text: "跨三个模块"})
	if err != nil || pick.Seat == nil || pick.Seat.ID != "goku" || !pick.StrongestGate.Moved {
		t.Fatalf("pick = %+v err = %v", pick, err)
	}
	if !strings.Contains(pick.Detail, "最强档门槛") {
		t.Fatalf("detail = %q", pick.Detail)
	}
}
