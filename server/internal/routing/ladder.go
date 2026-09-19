package routing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed ladder.json
var ladderJSON []byte

// Tier is one rung of the seat ladder.
type Tier struct {
	Key   string `json:"key"`
	Base  string `json:"base"`
	Label string `json:"label"`
}

// Ladder is the candidate source: the ordered tiers, the known directions, and
// the project -> direction table. It is pure data loaded from ladder.json.
type Ladder struct {
	Tiers      []Tier            `json:"tiers"`
	Directions []string          `json:"directions"`
	Projects   map[string]string `json:"projects"`
}

// DefaultLadder is the shipped ladder. Parsed once at init; a malformed
// ladder.json is a build-time-visible programming error, not a runtime state
// this package has to model, so it panics.
var DefaultLadder = mustLoadLadder()

func mustLoadLadder() Ladder {
	var l Ladder
	if err := json.Unmarshal(ladderJSON, &l); err != nil {
		panic(fmt.Sprintf("routing: ladder.json is malformed: %v", err))
	}
	if len(l.Tiers) == 0 {
		panic("routing: ladder.json declares no tiers")
	}
	return l
}

// Direction resolves an issue's direction from its project name. An unknown or
// empty project yields "", which means the generic rung — routing never guesses
// a direction, because guessing one silently sends work to a seat carrying the
// wrong domain pack.
func (l Ladder) Direction(projectName string) string {
	if projectName == "" {
		return ""
	}
	return l.Projects[projectName]
}

// Seat is one routable agent.
type Seat struct {
	ID        string
	Name      string
	TierKey   string
	TierLabel string
	Direction string
}

// SeatName is the naming convention that links a tier to its
// direction-specialised seat: base name concatenated with the direction.
func SeatName(base, direction string) string {
	return base + direction
}

// Candidates narrows the workspace roster to the seats routing may pick for
// this direction: one seat per tier, in ladder order.
//
// This is the whole of today's filter chain. The chain is where a future
// execution-only model would be inserted — a second stage appended here leaves
// Route's shape and both call sites untouched.
func (l Ladder) Candidates(direction string, roster map[string]Agent) []Seat {
	seats := make([]Seat, 0, len(l.Tiers))
	for _, t := range l.Tiers {
		name := SeatName(t.Base, direction)
		a, ok := roster[name]
		if !ok && direction != "" {
			// A direction with no specialised seat on this rung falls back to
			// the generic seat rather than dropping the rung: losing a rung
			// silently narrows the ladder the judge is choosing from.
			a, ok = roster[t.Base]
			if ok {
				seats = append(seats, Seat{ID: a.ID, Name: a.Name, TierKey: t.Key, TierLabel: t.Label})
				continue
			}
		}
		if !ok {
			continue
		}
		seats = append(seats, Seat{ID: a.ID, Name: a.Name, TierKey: t.Key, TierLabel: t.Label, Direction: direction})
	}
	return seats
}

// Agent is the slice of an agent record routing needs.
type Agent struct {
	ID   string
	Name string
}

// SeatByTier finds the candidate on a named rung.
func SeatByTier(seats []Seat, tierKey string) (Seat, bool) {
	for _, s := range seats {
		if strings.EqualFold(s.TierKey, tierKey) {
			return s, true
		}
	}
	return Seat{}, false
}

// StrongerThan returns the candidate one rung above the given seat, if the
// ladder has one. Used to keep a reviewer from being the seat that did the
// work: reviewing your own output is not review.
func StrongerThan(seats []Seat, seat Seat) (Seat, bool) {
	for i, s := range seats {
		if s.ID == seat.ID {
			if i == 0 {
				return Seat{}, false
			}
			return seats[i-1], true
		}
	}
	return Seat{}, false
}

// TierKeys lists the rung keys in ladder order — the exact value range the
// judge is allowed to answer with.
func (l Ladder) TierKeys() []string {
	keys := make([]string, 0, len(l.Tiers))
	for _, t := range l.Tiers {
		keys = append(keys, t.Key)
	}
	return keys
}
