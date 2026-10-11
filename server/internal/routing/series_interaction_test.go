package routing

import (
	"context"
	"strings"
	"testing"
)

func TestSeriesPreservesSelectionProvenance(t *testing.T) {
	for _, mode := range []string{"load", "continuation"} {
		for _, preferred := range []string{"", "Codex", "Claude"} {
			t.Run(mode+"/preferred="+preferred, func(t *testing.T) {
				store := newFakeStore()
				store.issue.Reviewer = ReviewerRef{Kind: ReviewerNoReview}
				store.roster = map[string]Agent{
					"a-busy": {ID: "a", Name: "a-busy", Tier: "strong", RuntimeMode: "codex"},
					"b-idle": {ID: "b", Name: "b-idle", Tier: "strong", RuntimeMode: "codex"},
				}
				if preferred == "Claude" {
					a := store.roster["a-busy"]
					a.Series = "Claude"
					store.roster[a.Name] = a
				}
				if mode == "load" {
					store.settings.PreferIdle = true
					store.facts = RoutingFacts{Running: map[string]int{"a": 2, "b": 0}}
				} else {
					store.settings.PreferContinuation = true
					store.issue.Related = []RelatedTicket{{Identifier: "DENE-previous", Relation: RelationPreviousStage, ExecutorID: "b"}}
				}
				verdict := confidentVerdict()
				verdict.PreferredSeries = preferred
				out, err := newRouter(store, &fakeJudge{verdict: verdict}).Route(context.Background(), "ws", "issue-1")
				if err != nil {
					t.Fatal(err)
				}
				body := strings.Join(store.comments[KindAssignment], "\n")
				if out.ExecutorWritten == nil {
					t.Fatal("no executor")
				}
				t.Logf("selected=%s; continues=%q; balanced=%q; comment:\n%s", out.ExecutorWritten.Name, out.ExecutorWritten.Continues, out.ExecutorWritten.Balanced, body)
				if preferred == "Claude" {
					if out.ExecutorWritten.ID != "a" || out.ExecutorWritten.Continues != "" || out.ExecutorWritten.Balanced != "" {
						t.Fatalf("series switch retained stale selection: %+v", out.ExecutorWritten)
					}
					if !strings.Contains(body, "**为什么是他**：档位") || !strings.Contains(body, "系列偏好：Claude；同档可用席位 a-busy") || strings.Contains(body, "**接着做**") || strings.Contains(body, "**负载**") {
						t.Fatalf("series switch has inconsistent provenance:\n%s", body)
					}
					return
				}
				if mode == "load" && (out.ExecutorWritten.Balanced == "" || !strings.Contains(body, "**为什么是他**：负载 —— "+out.ExecutorWritten.Balanced)) {
					t.Fatalf("matching series lost load provenance:\n%s", body)
				}
				if mode == "continuation" && (out.ExecutorWritten.Continues == "" || !strings.Contains(body, "**为什么是他**：接着做")) {
					t.Fatalf("matching series lost continuation provenance:\n%s", body)
				}
				if out.ExecutorWritten.ID != "b" {
					t.Fatalf("already matching Codex series lost %s choice: got %s, want b-idle", mode, out.ExecutorWritten.Name)
				}
			})
		}
	}
}
