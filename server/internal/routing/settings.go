// Package routing decides who should be holding an issue right now.
//
// It is an if function. Every time an issue is created or changes status, the
// server calls Route once, and Route answers one question: given this status,
// who should hold this ticket. That framing fixes three properties the rest of
// this package depends on:
//
//   - An if function has no side effects of its own. The model is asked for a
//     branch and a confidence, never for prose, an action, or control flow.
//     Every write is done by deterministic code outside the model call.
//   - An if function can always fall through to else. Disabled, unconfigured,
//     model unreachable, breaker open, confidence below the threshold — all of
//     them take the same else branch, which is exactly the behaviour this
//     deployment had before the package existed.
//   - An if function is more reliable with fewer branches. Anything already
//     known is resolved deterministically before the model is asked, so the
//     model only ever answers the part nobody knows.
//
// Route never advances status. Status is a fact about the work, and only the
// seat doing the work knows it; a model that could write status would move
// tickets to "in review" with the work undone, and that mistake is invisible
// on the board.
package routing

import (
	"encoding/json"
	"math"
)

// SettingsKey is the key under which routing configuration lives inside the
// workspace `settings` JSONB column. The column is shared with other product
// settings, so everything this package owns is nested under one key.
const SettingsKey = "routing"

// DefaultConfidenceThreshold is the threshold applied when settings carry no
// explicit one. Below it, the corresponding slot is left empty rather than
// filled with a guess.
const DefaultConfidenceThreshold = 0.70

// Settings is the whole routing configuration: an on/off switch, the model the
// judge runs on, and the confidence threshold. Deliberately three fields and
// no credentials — the model's credentials belong to the existing LLM
// configuration, and this package never reads or stores a key.
//
// The JSON field names are the cross-surface contract. The desktop settings
// section writes exactly these three names, and changing one is a breaking
// change for any workspace already configured.
type Settings struct {
	Enabled bool `json:"enabled"`
	// Model is a model identifier passed through to the server-internal LLM
	// layer. Empty while the switch is on is the "incomplete" state: somebody
	// flipped the toggle and stopped, and the product must say so rather than
	// look enabled.
	Model string `json:"model"`
	// ConfidenceThreshold is the floor a verdict must clear before its answer
	// is written to a slot. Zero or out of range means "unset" and yields
	// DefaultConfidenceThreshold; it is never treated as "accept everything".
	ConfidenceThreshold float64 `json:"confidence_threshold"`
}

// State is what the settings section shows and what Route branches on. It is
// derived, never stored: a stored copy would drift from the fields above.
type State string

const (
	// StateOff — the switch is off. This is the default for every workspace
	// that has never touched the section.
	StateOff State = "off"
	// StateIncomplete — the switch is on but no model was chosen. Behaves
	// exactly like StateOff; it exists so the UI can say why nothing happens.
	StateIncomplete State = "incomplete"
	// StateEnabled — switch on, model chosen. Routing does its work.
	StateEnabled State = "enabled"
	// StateIneffective — configured, but the model is currently unusable
	// (rejected, unreachable, deleted, or the breaker is cooling down).
	// Behaves exactly like StateOff, and the reason is shown in settings only.
	// It is never derived from the stored fields alone; Route supplies it.
	StateIneffective State = "ineffective"
)

// Active reports whether routing should do anything at all. Only StateEnabled
// is active: the other three states are the pre-existing code path.
func (s State) Active() bool { return s == StateEnabled }

// ParseSettings reads the routing block out of a workspace `settings` payload.
// A missing block, a malformed one, or a null yields the zero Settings, which
// is StateOff — a workspace whose settings JSON cannot be parsed must not
// start routing tickets.
func ParseSettings(raw []byte) Settings {
	if len(raw) == 0 {
		return Settings{}
	}
	var envelope struct {
		Routing *Settings `json:"routing"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Routing == nil {
		return Settings{}
	}
	return *envelope.Routing
}

// State classifies the stored fields. It can only return StateOff,
// StateIncomplete, or StateEnabled: StateIneffective depends on live model
// health, which the stored fields cannot know.
func (s Settings) State() State {
	if !s.Enabled {
		return StateOff
	}
	if s.Model == "" {
		return StateIncomplete
	}
	return StateEnabled
}

// Threshold returns the effective confidence floor. An unset, negative,
// non-finite, or above-1 value falls back to the default rather than being
// clamped silently to something that would accept every verdict.
func (s Settings) Threshold() float64 {
	t := s.ConfidenceThreshold
	if math.IsNaN(t) || math.IsInf(t, 0) || t <= 0 || t > 1 {
		return DefaultConfidenceThreshold
	}
	return t
}
