package routing

import (
	"strings"
	"testing"
)

func TestFrontendMediumRelaxesSeries(t *testing.T) {
	store, analyst := dispatchStore(t, "前端", nil)
	store.roster = map[string]Agent{
		"布尔玛":  {ID: "bulma", Name: "布尔玛", Tier: "strongest", RuntimeMode: "claude-code", Usage: UsageTight},
		"天津饭":  {ID: "tien", Name: "天津饭", Tier: "medium", RuntimeMode: "claude-code", State: SeatState{DispatchMode: "mention_only"}},
		"特兰克斯": {ID: "trunks", Name: "特兰克斯", Tier: "medium", RuntimeMode: "codex"},
	}
	analyst.record.Facts.Scope = ScopeModule
	v := strongVerdict()
	v.ExecutorTier = "medium"
	out := routeWith(t, store, &fakeJudge{verdict: v}, analyst)
	if out.ExecutorWritten == nil {
		t.Fatal("no executor")
	}
	t.Logf("selected=%s tier=%s", out.ExecutorWritten.Name, out.ExecutorWritten.TierKey)
	if out.ExecutorWritten.ID != "trunks" {
		t.Fatalf("want 特兰克斯; got %+v", out.ExecutorWritten)
	}
	body := strings.Join(store.comments[KindAssignment], "\n")
	if strings.Contains(body, "100% 低于") {
		t.Fatal(body)
	}
}
