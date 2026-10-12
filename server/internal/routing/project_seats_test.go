package routing

import (
	"context"
	"strings"
	"testing"
	"time"
)

// DENE-1959: a project's executor allowlist bounds every automatic pick for
// its work. The fake roster's 比克游戏 (weak) and 贝吉塔游戏 (medium) are the
// listed seats; the judge's favourite 孙悟空游戏 (strong) and the strongest
// 布尔玛 seats are not.
var allowlist = []string{"a-piccolo-g", "a-vegeta-g"}

var allowlistNames = map[string]bool{"比克游戏": true, "贝吉塔游戏": true}

func allowlistStore() *fakeStore {
	store := newFakeStore()
	store.issue.ProjectID = "p-ipa"
	store.issue.ProjectSeats = allowlist
	return store
}

func assertOnlyListed(t *testing.T, what string, names []string) {
	t.Helper()
	for _, name := range names {
		if !allowlistNames[name] {
			t.Fatalf("%s went to %q, outside the project's executor allowlist (all: %v)", what, name, names)
		}
	}
}

func TestProjectAdmits(t *testing.T) {
	if !ProjectAdmits(nil, "anyone") || !ProjectAdmits(nil, "") {
		t.Fatal("an empty allowlist must admit every seat")
	}
	if !ProjectAdmits(allowlist, "A-PICCOLO-G") {
		t.Fatal("a listed seat was refused")
	}
	if ProjectAdmits(allowlist, "a-goku-g") {
		t.Fatal("an unlisted seat was admitted")
	}
	if ProjectAdmits(allowlist, "") {
		t.Fatal("a seat with no id must fail closed against an allowlist")
	}
}

// Every refusal a listed seat can have still applies, and a listed seat's
// unknown availability is not a refusal: unknown is not unavailable.
func TestProjectSeatsKeepEveryOtherRefusal(t *testing.T) {
	c := SelectContext{ProjectID: "p-ipa", ProjectSeats: allowlist}
	cases := []struct {
		name  string
		state SeatState
		c     SelectContext
		want  string
	}{
		{"listed", SeatState{ID: "a-piccolo-g"}, c, ""},
		{"listed, unknown availability", SeatState{ID: "a-piccolo-g"}, SelectContext{ProjectID: "p-ipa", ProjectSeats: allowlist, Availability: AvailabilityUnknown}, ""},
		{"unlisted", SeatState{ID: "a-goku-g"}, c, ReasonNotProjectSeat},
		{"listed but archived", SeatState{ID: "a-piccolo-g", Archived: true}, c, ReasonArchived},
		{"listed but disabled", SeatState{ID: "a-piccolo-g", Disabled: true}, c, ReasonDisabled},
		{"listed but no runtime", SeatState{ID: "a-piccolo-g", NoRuntime: true}, c, ReasonNoRuntime},
		{"listed but offline", SeatState{ID: "a-piccolo-g"}, SelectContext{ProjectID: "p-ipa", ProjectSeats: allowlist, Availability: AvailabilityUnreachable}, AvailabilityUnreachable},
		{"listed but out of quota", SeatState{ID: "a-piccolo-g"}, SelectContext{ProjectID: "p-ipa", ProjectSeats: allowlist, Availability: AvailabilityQuotaExhausted}, AvailabilityQuotaExhausted},
		{"listed but breaker open", SeatState{ID: "a-piccolo-g"}, SelectContext{ProjectID: "p-ipa", ProjectSeats: allowlist, BreakerOpen: true}, ReasonBreakerOpen},
	}
	for _, tc := range cases {
		ok, why := SeatSelectable(tc.state, tc.c)
		if ok != (tc.want == "") || why != tc.want {
			t.Errorf("%s: ok=%v why=%q, want %q", tc.name, ok, why, tc.want)
		}
	}
}

// A new ticket in the project goes to a listed seat even when the judge
// names a stronger, unlisted one; the reviewer slot stays inside the list too.
func TestRouteKeepsNewTicketInsideProjectSeats(t *testing.T) {
	store := allowlistStore()
	judge := &fakeJudge{verdict: confidentVerdict()}
	if _, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(store.assigns) != 1 {
		t.Fatalf("assigns = %v, want one listed seat", store.assigns)
	}
	assertOnlyListed(t, "executor", store.assigns)
	assertOnlyListed(t, "reviewer", store.reviewer)
	for _, s := range judge.assignedState().EligibleSeats {
		if !allowlistNames[s.Name] {
			t.Fatalf("the judge was offered %q, outside the allowlist", s.Name)
		}
	}
}

// The same ticket in a project without an allowlist routes as before.
func TestRouteWithoutProjectSeatsIsUnchanged(t *testing.T) {
	store := newFakeStore()
	store.issue.ProjectID = "p-other"
	if _, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空游戏" {
		t.Fatalf("assigns = %v, want the ladder's 孙悟空游戏", store.assigns)
	}
}

// When no listed seat can work, routing declines and says so; it never
// reaches for an idle seat outside the list.
func TestRouteDeclinesWhenEveryListedSeatIsOut(t *testing.T) {
	for _, availability := range []string{AvailabilityUnreachable, AvailabilityQuotaExhausted, AvailabilityDisabled} {
		store := allowlistStore()
		store.facts = RoutingFacts{Seats: map[string]SeatSnapshot{
			"a-piccolo-g": {AgentID: "a-piccolo-g", Availability: availability},
			"a-vegeta-g":  {AgentID: "a-vegeta-g", Availability: availability},
		}}
		if _, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1"); err != nil {
			t.Fatalf("%s: route: %v", availability, err)
		}
		if len(store.assigns) != 0 {
			t.Fatalf("%s: assigns = %v, want nobody", availability, store.assigns)
		}
		body := strings.Join(store.comments[KindAssignment], "\n")
		if !strings.Contains(body, "未派出") || !strings.Contains(body, "名单外席位不兜底") {
			t.Fatalf("%s: decline comment does not name the allowlist:\n%s", availability, body)
		}
	}

	// Listed seats archived or switched off leave the roster entirely.
	store := allowlistStore()
	for name, a := range store.roster {
		if allowlistNames[name] {
			a.State.Archived = true
			store.roster[name] = a
		}
	}
	if _, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("archived: route: %v", err)
	}
	if len(store.assigns) != 0 {
		t.Fatalf("archived: assigns = %v, want nobody", store.assigns)
	}
}

// A ticket already held by an unlisted seat is not taken away from it.
func TestProjectSeatsDoNotReplaceAnExistingExecutor(t *testing.T) {
	store := allowlistStore()
	store.issue.Status = "in_progress"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-goku-g"
	if _, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(store.assigns) != 0 {
		t.Fatalf("assigns = %v, an existing executor was replaced", store.assigns)
	}
}

// A new chat in the project opens with a listed seat, or declines.
func TestPickSeatStaysInsideProjectSeats(t *testing.T) {
	store := newFakeStore()
	req := PickRequest{Title: "看一下", ProjectID: "p-ipa", ProjectName: "game", ProjectSeats: allowlist}
	pick, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).PickSeat(context.Background(), "ws", req)
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if pick.Seat == nil || !allowlistNames[pick.Seat.Name] {
		t.Fatalf("seat = %+v (declined %q), want a listed seat", pick.Seat, pick.Declined)
	}
	for _, c := range pick.Candidates {
		if !allowlistNames[c.Name] {
			t.Fatalf("candidate %q is outside the allowlist", c.Name)
		}
	}

	store.facts = RoutingFacts{Seats: map[string]SeatSnapshot{
		"a-piccolo-g": {AgentID: "a-piccolo-g", Availability: AvailabilityDead},
		"a-vegeta-g":  {AgentID: "a-vegeta-g", Availability: AvailabilityDead},
	}}
	pick, err = newRouter(store, &fakeJudge{verdict: confidentVerdict()}).PickSeat(context.Background(), "ws", req)
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if pick.Seat != nil || !strings.Contains(pick.Declined, "allowlist") {
		t.Fatalf("seat = %+v declined = %q, want a decline naming the allowlist", pick.Seat, pick.Declined)
	}
}

// 接着做 does not hand the ticket to the previous stage's unlisted executor.
func TestContinuationStaysInsideProjectSeats(t *testing.T) {
	store := continuationStore(true) // previous executor: 布尔玛游戏, unlisted
	store.issue.ProjectID = "p-ipa"
	store.issue.ProjectSeats = allowlist
	if _, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(store.assigns) != 1 {
		t.Fatalf("assigns = %v, want one listed seat", store.assigns)
	}
	assertOnlyListed(t, "continuation", store.assigns)
}

// 升档 never climbs out of the list: from the top listed seat it stays put.
func TestEscalateStaysInsideProjectSeats(t *testing.T) {
	store := allowlistStore()
	store.issue.ConsultCount = 1
	store.issue.Status = "in_progress"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-piccolo-g"
	esc, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Escalate(context.Background(), "ws", "issue-1", "要改三个服务")
	if err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if !esc.Changed || esc.To != "贝吉塔游戏" {
		t.Fatalf("escalation = %+v, want one rung up to the listed 贝吉塔游戏", esc)
	}

	store = allowlistStore()
	store.issue.ConsultCount = 1
	store.issue.Status = "in_progress"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-vegeta-g"
	esc, err = newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Escalate(context.Background(), "ws", "issue-1", "还是太难")
	if err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if esc.Changed || len(store.handoffs) != 0 {
		t.Fatalf("escalation = %+v handoffs = %v, want nothing moved past the list", esc, store.handoffs)
	}
}

// 换人兜底 (same-tier substitution / seat relay) on a project roster only
// offers listed seats.
func TestSubstituteSeatStaysInsideProjectSeats(t *testing.T) {
	roster := ForProject(map[string]Agent{
		"孙悟饭": {ID: "gohan", Name: "孙悟饭"},
		"布尔玛": {ID: "bulma", Name: "布尔玛"},
		"孙悟空": {ID: "goku", Name: "孙悟空"},
	}, "p-ipa", []string{"gohan", "goku"})
	got, down, ok := SubstituteSeat(DefaultLadder, Seat{ID: "gohan", Name: "孙悟饭", TierKey: "strongest"}, roster, nil, GenericScene)
	if !ok || !down || got.ID != "goku" {
		t.Fatalf("substitute = %+v down=%v ok=%v, want the listed 孙悟空 one tier down, never 布尔玛", got, down, ok)
	}
	roster = ForProject(roster, "p-ipa", []string{"gohan"})
	if got, _, ok := SubstituteSeat(DefaultLadder, Seat{ID: "gohan", Name: "孙悟饭", TierKey: "strongest"}, roster, nil, GenericScene); ok {
		t.Fatalf("substitute = %+v, want none: the only listed seat is the one that failed", got)
	}
}

// offListReviewStore is a ticket awaiting acceptance whose reviewer seat was
// picked before the project had an allowlist and is not on it now.
func offListReviewStore(seats []string) *fakeStore {
	store := allowlistStore()
	store.issue.ProjectSeats = seats
	store.issue.Status = "in_review"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-piccolo-g"
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerAgent, ID: "a-goku-g", Name: "孙悟空游戏"}
	store.issue.LastActivityAt = time.Now().Add(-7 * 24 * time.Hour)
	store.workspaces = []string{"ws"}
	store.staleIDs = []string{"issue-1"}
	return store
}

func allComments(store *fakeStore) string {
	var parts []string
	for _, body := range store.comments {
		parts = append(parts, body...)
	}
	return strings.Join(parts, "\n")
}

func assertOffListReviewerNotWoken(t *testing.T, what string, store *fakeStore) {
	t.Helper()
	for _, h := range store.handoffs {
		if h == "agent:a-goku-g" {
			t.Fatalf("%s woke the unlisted reviewer: %v", what, store.handoffs)
		}
	}
}

// Every automatic acceptance wake (handoff, failed-review retry, stale wake)
// checks the allowlist as it stands now: a reviewer picked before the list
// existed is not woken, a listed seat that did not work on the ticket covers
// it, and the cover does not hand the review back later.
func TestOffListReviewerIsCoveredInsideProjectSeats(t *testing.T) {
	ctx := context.Background()
	wakes := map[string]func(*Router) error{
		"acceptance handoff": func(r *Router) error { _, err := r.Route(ctx, "ws", "issue-1"); return err },
		"failed review":      func(r *Router) error { _, err := r.CoverFailedReview(ctx, "ws", "issue-1", false); return err },
		"forced failed review": func(r *Router) error {
			_, err := r.CoverFailedReview(ctx, "ws", "issue-1", true)
			return err
		},
		"stale wake": func(r *Router) error { _, err := r.RouteStale(ctx, "ws", "issue-1"); return err },
	}
	for what, wake := range wakes {
		t.Run(what, func(t *testing.T) {
			store := offListReviewStore(allowlist)
			judge := &fakeJudge{verdict: confidentVerdict(), stale: StaleDecision{Action: StaleWake, Confidence: 0.95}}
			if err := wake(newRouter(store, judge)); err != nil {
				t.Fatalf("wake: %v", err)
			}
			assertOffListReviewerNotWoken(t, what, store)
			if len(store.handoffs) != 1 || store.handoffs[0] != "agent:a-vegeta-g" {
				t.Fatalf("handoffs = %v, want the listed 贝吉塔游戏 (the executor 比克游戏 cannot review itself)", store.handoffs)
			}
			if len(store.relays) != 1 || store.relays[0].Designated {
				t.Fatalf("relays = %+v, want one cover that never hands the review back", store.relays)
			}
			if !strings.Contains(allComments(store), "不在本项目的执行人名单里") {
				t.Fatalf("no note names the allowlist:\n%s", allComments(store))
			}
		})

		t.Run(what+", nobody listed can review", func(t *testing.T) {
			store := offListReviewStore([]string{"a-piccolo-g"})
			judge := &fakeJudge{verdict: confidentVerdict(), stale: StaleDecision{Action: StaleWake, Confidence: 0.95}}
			if err := wake(newRouter(store, judge)); err != nil {
				t.Fatalf("wake: %v", err)
			}
			if len(store.handoffs) != 0 {
				t.Fatalf("handoffs = %v, want nobody woken outside the list", store.handoffs)
			}
			if !strings.Contains(allComments(store), "名单外席位不兜底") {
				t.Fatalf("the stuck note does not name the allowlist:\n%s", allComments(store))
			}
		})
	}
}

// A listed reviewer is still woken as before.
func TestListedReviewerIsStillWoken(t *testing.T) {
	store := offListReviewStore(allowlist)
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerAgent, ID: "a-vegeta-g", Name: "贝吉塔游戏"}
	if _, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(store.handoffs) != 1 || store.handoffs[0] != "agent:a-vegeta-g" || len(store.relays) != 0 {
		t.Fatalf("handoffs = %v relays = %+v, want the listed reviewer woken directly", store.handoffs, store.relays)
	}
}
