package routing

import "strings"

// 最强档门槛 (DENE-1834): the top rung is the scarce one. Kun's rule for it is
// that the strongest seat takes a ticket only when its quota is not tight AND
// no seat on the strong or medium rung can take the work — and the comment
// must then say 为什么非她不可. A person's tier label and the upshift switch
// are a person's own call and bypass the gate.

// StrongestGate is what the gate did to one pick.
type StrongestGate struct {
	// Checked is true when the pick had landed on the top rung and the gate
	// looked at it. Moved is true when it went down; To is the rung it went
	// to. Why is the one sentence for the comment.
	Checked bool   `json:"checked,omitempty"`
	Moved   bool   `json:"moved,omitempty"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Tight   bool   `json:"tight,omitempty"`
	Why     string `json:"why,omitempty"`
}

// Line renders the gate's answer for the decision comment and the receipt.
func (g StrongestGate) Line(l Ladder) string {
	if !g.Checked {
		return ""
	}
	if g.Moved {
		return "最强档门槛：" + g.Why + "，不派最强档，改派" + tierLabel(l, g.To) + "档"
	}
	return "最强档门槛：" + g.Why
}

// middleTiers is the rungs the gate asks about before letting the top rung
// take the ticket: everything between the top and the bottom. A two-rung
// ladder has only the bottom to ask.
func (l Ladder) middleTiers() []Tier {
	if len(l.Tiers) >= 3 {
		return l.Tiers[1 : len(l.Tiers)-1]
	}
	if len(l.Tiers) == 2 {
		return l.Tiers[1:]
	}
	return nil
}

// gateStrongest applies the rule to the seat the ladder picked. candidates
// is the one-per-rung list, eligible the part of it that can take work right
// now, snaps the availability words for the sentence; roster gives the seat's
// usage tag. The seat returned is the one to write.
func (l Ladder) gateStrongest(seat Seat, candidates, eligible []Seat, roster map[string]Agent, snaps map[string]SeatSnapshot) (Seat, StrongestGate) {
	g := StrongestGate{}
	if len(l.Tiers) < 2 || seat.ID == "" || seat.TierKey != l.Tiers[0].Key || seat.Upshifted {
		return seat, g
	}
	g.Checked, g.From = true, seat.TierKey
	agent, _ := agentByID(roster, seat.ID)
	g.Tight = agent.Usage == UsageTight
	for _, t := range l.middleTiers() {
		alt, ok := SeatByTier(eligible, t.Key)
		if !ok {
			continue
		}
		g.Moved, g.To = true, alt.TierKey
		if g.Tight {
			g.Why = seat.Name + " 额度紧张"
		} else {
			g.Why = tierLabel(l, alt.TierKey) + "档有合适席位 " + alt.Name
		}
		return alt, g
	}
	if g.Tight {
		// Not even the middle rungs: a tight top seat still yields to any
		// lower seat that can take the work.
		for _, t := range l.Tiers[1:] {
			if alt, ok := SeatByTier(eligible, t.Key); ok {
				g.Moved, g.To = true, alt.TierKey
				g.Why = seat.Name + " 额度紧张，" + l.middleAbsence(candidates, eligible, snaps)
				return alt, g
			}
		}
		g.Why = seat.Name + " 额度紧张，但没有别的可用席位，只能派她"
		return seat, g
	}
	g.Why = "为什么非她不可：" + l.middleAbsence(candidates, eligible, snaps) + "；" + seat.Name + " 额度" + UsageLabel(agent.Usage)
	return seat, g
}

// middleAbsence says, rung by rung, why the strong and medium rungs gave
// nobody: no seat on the rung at all, or the seat there cannot take work
// (with the availability word when the snapshot has one).
func (l Ladder) middleAbsence(candidates, eligible []Seat, snaps map[string]SeatSnapshot) string {
	parts := make([]string, 0, 2)
	for _, t := range l.middleTiers() {
		c, ok := SeatByTier(candidates, t.Key)
		switch {
		case !ok:
			parts = append(parts, tierLabel(l, t.Key)+"档没有席位")
		case seatIn(eligible, c):
			parts = append(parts, tierLabel(l, t.Key)+"档 "+c.Name+" 被分工规则排除")
		default:
			why := "不可用"
			if snap, found := snaps[c.ID]; found && snap.Availability != "" {
				why = AvailabilityLabel(snap.Availability)
			}
			parts = append(parts, tierLabel(l, t.Key)+"档 "+c.Name+" "+why)
		}
	}
	return strings.Join(parts, "，")
}

// AvailabilityLabel is the availability word in the comment's language.
func AvailabilityLabel(availability string) string {
	switch availability {
	case AvailabilityDead, AvailabilityUnreachable:
		return "离线"
	case AvailabilityQuotaExhausted:
		return "额度用尽"
	case AvailabilityDisabled:
		return "已停用"
	case AvailabilityArchived:
		return "已归档"
	case AvailabilityCancelled:
		return "已取消"
	case AvailabilityAvailable:
		return "可用"
	default:
		return "不可用"
	}
}
