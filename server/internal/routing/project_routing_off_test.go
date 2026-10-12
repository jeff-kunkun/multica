package routing

import (
	"context"
	"testing"
)

// DENE-1966: a project can turn routing off. Its tickets are left exactly as
// people set them — no pick, no swap, no comment — while every other
// project's tickets keep routing.
func TestProjectRoutingOffSkipsItsTickets(t *testing.T) {
	store := newFakeStore()
	store.issue.ProjectID = "p-off"
	store.issue.ProjectRoutingOff = true
	out, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatal(err)
	}
	if out.Action != ActionSkipped || out.Reason != ReasonProjectRoutingOff {
		t.Fatalf("got %s / %q", out.Action, out.Reason)
	}
	if store.wrote() || store.commentCount() > 0 {
		t.Fatalf("routing touched a ticket in a project with routing off: assigns=%v comments=%v", store.assigns, store.comments)
	}

	on := newFakeStore()
	on.issue.ProjectID = "p-on"
	if _, err := newRouter(on, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatal(err)
	}
	if len(on.assigns) == 0 {
		t.Fatal("a project with routing on stopped routing")
	}
}
