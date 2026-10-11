package routing

import (
	"context"
	"errors"
	"testing"
)

func TestPickSeatChoosesFromLadderWithoutWriting(t *testing.T) {
	store := newFakeStore()
	judge := &fakeJudge{verdict: confidentVerdict()}

	pick, err := newRouter(store, judge).PickSeat(context.Background(), "ws", PickRequest{
		Title: "帮我看一个游戏关卡的 bug", Text: "关卡 3 的敌人不刷新", ProjectName: "game",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pick.Seat == nil || pick.Seat.Name != "孙悟空游戏" {
		t.Fatalf("seat = %+v, want 孙悟空游戏 (declined=%q)", pick.Seat, pick.Declined)
	}
	if pick.Reason != PickReasonTier {
		t.Errorf("reason = %q, want %q", pick.Reason, PickReasonTier)
	}
	if pick.Tier != "strong" {
		t.Errorf("tier = %q, want strong", pick.Tier)
	}
	if len(pick.Candidates) == 0 {
		t.Error("candidates are empty; the receipt must list who was considered")
	}
	if store.wrote() || store.commentCount() != 0 {
		t.Error("a ticketless pick must not write a slot or a comment")
	}
}

func TestPickSeatSkipsOfflineRuntime(t *testing.T) {
	store := newFakeStore()
	// The ladder's own pick for this verdict is 孙悟空游戏; its machine is gone.
	store.facts = RoutingFacts{Seats: map[string]SeatSnapshot{
		"a-goku-g": {AgentID: "a-goku-g", Availability: AvailabilityUnreachable},
	}}
	judge := &fakeJudge{verdict: confidentVerdict()}

	pick, err := newRouter(store, judge).PickSeat(context.Background(), "ws", PickRequest{Title: "游戏", ProjectName: "game"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pick.Seat == nil {
		t.Fatalf("no seat picked: %s", pick.Declined)
	}
	if pick.Seat.ID == "a-goku-g" {
		t.Fatalf("picked the offline seat %s", pick.Seat.Name)
	}
	var skipped *PickCandidate
	for i := range pick.Candidates {
		if pick.Candidates[i].ID == "a-goku-g" {
			skipped = &pick.Candidates[i]
		}
	}
	if skipped == nil || skipped.Excluded != AvailabilityUnreachable || skipped.Availability != AvailabilityUnreachable {
		t.Errorf("the offline seat is not reported as skipped: %+v", skipped)
	}
}

func TestPickSeatDeclinesWhenEverySeatIsOffline(t *testing.T) {
	store := newFakeStore()
	seats := map[string]SeatSnapshot{}
	for _, a := range store.roster {
		seats[a.ID] = SeatSnapshot{AgentID: a.ID, Availability: AvailabilityDead}
	}
	store.facts = RoutingFacts{Seats: seats}
	judge := &fakeJudge{verdict: confidentVerdict()}

	pick, err := newRouter(store, judge).PickSeat(context.Background(), "ws", PickRequest{Title: "游戏", ProjectName: "game"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pick.Seat != nil || pick.Declined == "" {
		t.Fatalf("expected a decline, got seat=%+v declined=%q", pick.Seat, pick.Declined)
	}
	if judge.callCount() != 0 {
		t.Error("asked the judge with nobody to pick")
	}
	for _, c := range pick.Candidates {
		if c.Excluded != AvailabilityDead {
			t.Errorf("candidate %s not marked offline: %+v", c.Name, c)
		}
	}
}

func TestPickSeatRefusesWhenRoutingIsOff(t *testing.T) {
	store := newFakeStore()
	store.settings = Settings{}
	_, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).PickSeat(context.Background(), "ws", PickRequest{Title: "x"})
	if !errors.Is(err, ErrRoutingInactive) {
		t.Fatalf("err = %v, want ErrRoutingInactive", err)
	}
}
