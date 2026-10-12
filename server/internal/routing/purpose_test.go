package routing

import "testing"

func TestUnifiedPurposeSelection(t *testing.T) {
	for _, purpose := range []Purpose{PurposeDo, PurposeConsult, PurposeReview} {
		t.Run(string(purpose), func(t *testing.T) {
			l := DefaultLadder.For(Settings{})
			roster := reviewPolicyRoster()
			a := roster["天津饭"]
			a.State.DispatchMode = "mention_only"
			roster["天津饭"] = a
			a = roster["布尔玛"]
			a.Usage = UsageTight
			roster["布尔玛"] = a
			r := newRouter(newFakeStore(), nil)
			in := ticketRoute{Ticket: Issue{AssigneeID: "krillin", AssigneeType: "agent"}, TargetTier: "medium", ExplicitTier: true, Roster: roster, Candidates: l.assignmentCandidates(GenericScene, roster), Facts: &Facts{Category: "前端"}}
			got := r.routeTicket(l, Settings{}, in, purpose)
			if len(got.Seats) == 0 || got.Seats[0].ID != "trunks" {
				t.Fatalf("DENE-1916: %+v", got)
			}
			// No same-tier candidate: higher adjacent wins before lower adjacent.
			delete(roster, "天津饭")
			delete(roster, "特兰克斯")
			in.Roster = roster
			in.Candidates = l.assignmentCandidates(GenericScene, roster)
			got = r.routeTicket(l, Settings{}, in, purpose)
			if len(got.Seats) == 0 || got.Seats[0].TierKey != "strong" {
				t.Fatalf("adjacent: %+v", got)
			}
			// Never borrow strongest as fallback, even with ample quota.
			delete(roster, "孙悟空")
			delete(roster, "孙悟饭")
			a = roster["布尔玛"]
			a.Usage = UsageAmple
			roster["布尔玛"] = a
			in.TargetTier = "strong"
			in.Ticket.AssigneeID = "trunks"
			in.Roster = roster
			in.Candidates = l.assignmentCandidates(GenericScene, roster)
			// Keep the executor's tier known but not auto-pickable.
			roster["特兰克斯"] = Agent{ID: "trunks", Name: "特兰克斯", Tier: "medium", State: SeatState{DispatchMode: "mention_only"}}
			got = r.routeTicket(l, Settings{}, in, purpose)
			if len(got.Seats) > 0 {
				t.Fatalf("strongest fallback: %+v", got)
			}
		})
	}
}
func TestConsultAndReviewMediumUseStrongNotBulma(t *testing.T) {
	l := DefaultLadder
	roster := reviewPolicyRoster()
	r := newRouter(newFakeStore(), nil)
	for _, p := range []Purpose{PurposeConsult, PurposeReview} {
		got := r.routeTicket(l, Settings{}, ticketRoute{Ticket: Issue{AssigneeID: "trunks"}, Roster: roster, Candidates: l.assignmentCandidates(GenericScene, roster)}, p)
		if len(got.Seats) == 0 || got.Seats[0].TierKey != "strong" || got.Seats[0].ID == "trunks" || got.Seats[0].ID == "bulma" {
			t.Fatalf("%s %+v", p, got)
		}
	}
}

func TestStrongExecutorPurposeOrder(t *testing.T) {
	for _, purpose := range []Purpose{PurposeDo, PurposeConsult, PurposeReview} {
		for _, tc := range []struct {
			name  string
			peer  bool
			usage string
			want  string
		}{
			{"peer before ample strongest", true, UsageAmple, "gohan"},
			{"explicit strongest only after peers unavailable", false, UsageAmple, "bulma"},
			{"tight strongest never used", false, UsageTight, ""},
			{"unknown strongest quota never used", false, "unknown", ""},
		} {
			t.Run(string(purpose)+"/"+tc.name, func(t *testing.T) {
				roster := reviewPolicyRoster()
				delete(roster, "特兰克斯")
				delete(roster, "天津饭")
				delete(roster, "克林")
				if !tc.peer {
					delete(roster, "孙悟饭")
				}
				a := roster["布尔玛"]
				a.Usage = tc.usage
				roster["布尔玛"] = a
				l := DefaultLadder.For(Settings{})
				r := newRouter(newFakeStore(), nil)
				got := r.routeTicket(l, Settings{}, ticketRoute{Ticket: Issue{AssigneeID: "goku"}, TargetTier: "strong", ExplicitTier: true, Roster: roster, Candidates: l.assignmentCandidates(GenericScene, roster)}, purpose)
				want := tc.want
				if purpose == PurposeDo {
					want = "goku"
				} // Dispatch is unaffected by the consult/review peer preference.
				id := ""
				if len(got.Seats) > 0 {
					id = got.Seats[0].ID
				}
				if id != want {
					t.Fatalf("got=%+v want=%s", got, want)
				}
			})
		}
	}
}

func TestFallbackUsesTierNotSeatName(t *testing.T) {
	for _, name := range []string{"布尔玛", "其他席位"} {
		for _, tier := range []string{"medium", "strongest"} {
			got := DefaultLadder.fallbackSafe(Seat{ID: "seat", Name: name, TierKey: tier})
			if got != (tier == "medium") {
				t.Fatalf("name=%s tier=%s allowed=%v", name, tier, got)
			}
		}
	}
}
