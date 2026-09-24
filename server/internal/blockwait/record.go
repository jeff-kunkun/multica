// Package blockwait is the structured "what is this issue waiting on" record
// and the decisions that bring a stalled issue back (DENE-850).
//
// It does not retry a server-cancelled run (DENE-813), relay a spent quota
// seat (DENE-836), or replace a disabled seat (DENE-848). It records the wait,
// wakes the waiter when the blocker clears, and patrols issues whose wait is
// missing or already due. Acceptance-pass merge is a decision here; the
// handler performs the GitHub call.
package blockwait

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	KeyBlockedBy     = "block.blocked_by"
	KeyWakeAt        = "block.wake_at"
	KeyWaitCondition = "block.wait_condition"
	KeyWaitProbe     = "block.wait_probe"
	KeyWaitTimeout   = "block.wait_timeout"
	KeyNeedsHuman    = "block.needs_human"
	KeyWokenBy       = "block.woken_by"
	KeyPatrolAt      = "block.patrol_at"
	KeyReleased      = "block.released"

	// QuietAfter is how long a blocked or in-review issue with nobody running
	// may sit before the patrol picks it up.
	QuietAfter = 30 * time.Minute

	ReleasedPass = "pass"
)

// Record is the wait written when an issue becomes blocked. At least one of
// the four kinds must be present: another issue, a clock, a probe with a
// deadline, or a specific person.
type Record struct {
	BlockedBy      []string
	WakeAt         time.Time
	HasWakeAt      bool
	WaitCondition  string
	WaitProbe      string
	WaitTimeout    time.Time
	HasWaitTimeout bool
	NeedsHuman     string
}

// Structured reports whether the record is enough to enter blocked.
func (r Record) Structured() bool {
	if len(r.BlockedBy) > 0 || r.HasWakeAt || strings.TrimSpace(r.NeedsHuman) != "" {
		return true
	}
	return strings.TrimSpace(r.WaitCondition) != "" && r.HasWaitTimeout
}

// Input is the block fields carried on the status change itself.
type Input struct {
	BlockedBy     string
	WakeAt        string
	WaitCondition string
	WaitProbe     string
	WaitTimeout   string
	NeedsHuman    string
}

// Empty reports whether the caller sent no block fields.
func (in Input) Empty() bool {
	return strings.TrimSpace(in.BlockedBy) == "" &&
		strings.TrimSpace(in.WakeAt) == "" &&
		strings.TrimSpace(in.WaitCondition) == "" &&
		strings.TrimSpace(in.WaitProbe) == "" &&
		strings.TrimSpace(in.WaitTimeout) == "" &&
		strings.TrimSpace(in.NeedsHuman) == ""
}

var (
	identifierRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-[0-9]+$`)
	uuidRE       = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	waitMention  = regexp.MustCompile(`(?:等|等待|waiting on|blocked by|depends on)[^\n]{0,40}\b([A-Za-z][A-Za-z0-9]*-[0-9]+)\b`)
)

// ParseMetadata reads a block record out of issue metadata. close.waiting_on
// counts as a blocker so a wait recorded before this ticket still qualifies.
func ParseMetadata(meta map[string]any) Record {
	var r Record
	r.BlockedBy = splitTokens(MetaString(meta, KeyBlockedBy))
	if w := MetaString(meta, "close.waiting_on"); w != "" {
		r.BlockedBy = appendUnique(r.BlockedBy, w)
	}
	if raw := MetaString(meta, KeyWakeAt); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			r.WakeAt = t
			r.HasWakeAt = true
		}
	}
	r.WaitCondition = MetaString(meta, KeyWaitCondition)
	r.WaitProbe = MetaString(meta, KeyWaitProbe)
	if raw := MetaString(meta, KeyWaitTimeout); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			r.WaitTimeout = t
			r.HasWaitTimeout = true
		}
	}
	r.NeedsHuman = MetaString(meta, KeyNeedsHuman)
	return r
}

// Merge overlays a status-change input onto the record already stored.
// Malformed clocks and identifiers return an error instead of a partial record.
func Merge(existing map[string]any, in Input) (Record, error) {
	r := ParseMetadata(existing)
	if tokens := splitTokens(in.BlockedBy); len(tokens) > 0 {
		for _, token := range tokens {
			if !identifierRE.MatchString(token) && !uuidRE.MatchString(token) {
				return Record{}, fmt.Errorf("blocked_by %q 不是票号或 UUID", token)
			}
			r.BlockedBy = appendUnique(r.BlockedBy, token)
		}
	}
	if raw := strings.TrimSpace(in.WakeAt); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return Record{}, fmt.Errorf("wake_at 必须是 RFC3339 时间")
		}
		r.WakeAt = t.UTC()
		r.HasWakeAt = true
	}
	if v := strings.TrimSpace(in.WaitCondition); v != "" {
		r.WaitCondition = v
	}
	if v := strings.TrimSpace(in.WaitProbe); v != "" {
		r.WaitProbe = v
	}
	if raw := strings.TrimSpace(in.WaitTimeout); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return Record{}, fmt.Errorf("wait_timeout 必须是 RFC3339 时间")
		}
		r.WaitTimeout = t.UTC()
		r.HasWaitTimeout = true
	}
	if v := strings.TrimSpace(in.NeedsHuman); v != "" {
		if !uuidRE.MatchString(v) {
			return Record{}, fmt.Errorf("needs_human 必须是成员 UUID")
		}
		r.NeedsHuman = v
	}
	return r, nil
}

// Pairs is the metadata to write for this record. The first blocker is also
// mirrored to close.waiting_on so the existing wake path keeps matching.
func (r Record) Pairs() map[string]string {
	out := map[string]string{}
	if len(r.BlockedBy) > 0 {
		out[KeyBlockedBy] = strings.Join(r.BlockedBy, ",")
		out["close.waiting_on"] = r.BlockedBy[0]
	}
	if r.HasWakeAt {
		out[KeyWakeAt] = r.WakeAt.UTC().Format(time.RFC3339)
	}
	if strings.TrimSpace(r.WaitCondition) != "" {
		out[KeyWaitCondition] = strings.TrimSpace(r.WaitCondition)
	}
	if strings.TrimSpace(r.WaitProbe) != "" {
		out[KeyWaitProbe] = strings.TrimSpace(r.WaitProbe)
	}
	if r.HasWaitTimeout {
		out[KeyWaitTimeout] = r.WaitTimeout.UTC().Format(time.RFC3339)
	}
	if strings.TrimSpace(r.NeedsHuman) != "" {
		out[KeyNeedsHuman] = strings.TrimSpace(r.NeedsHuman)
	}
	return out
}

// Suggestion is a block the platform can see in recent comments but will not
// apply on its own.
type Suggestion struct {
	BlockedBy []string
	Hint      string
}

// SuggestFromComments finds "等 DENE-123" style waits. The hint tells the
// caller how to retry the status change. The platform does not apply it.
func SuggestFromComments(bodies []string) Suggestion {
	var found []string
	seen := map[string]bool{}
	for _, body := range bodies {
		for _, m := range waitMention.FindAllStringSubmatch(body, -1) {
			if len(m) < 2 || seen[m[1]] {
				continue
			}
			seen[m[1]] = true
			found = append(found, m[1])
		}
	}
	if len(found) == 0 {
		return Suggestion{}
	}
	return Suggestion{
		BlockedBy: found,
		Hint:      fmt.Sprintf("评论里像是在等 %s。可以这样补：multica issue status <id> blocked --blocked-by %s", strings.Join(found, "、"), strings.Join(found, ",")),
	}
}

// Rejection is the error text when an agent tries to enter blocked with no record.
func Rejection(suggestion Suggestion) string {
	msg := "切到 blocked 必须写明在等什么，至少一种：--blocked-by（挡路的票）、--wake-at（到点复查）、--wait-condition 配 --wait-timeout（可探测的条件）、--needs-human（要哪个人）。"
	if suggestion.Hint != "" {
		msg += suggestion.Hint
	}
	return msg
}

// AlreadyWoken reports whether this waiter was already woken for the completed issue.
func AlreadyWoken(existing, completed string) bool {
	completed = strings.TrimSpace(completed)
	if completed == "" {
		return false
	}
	for _, token := range splitTokens(existing) {
		if token == completed {
			return true
		}
	}
	return false
}

// MarkWoken appends the completed issue to the idempotency list.
func MarkWoken(existing, completed string) string {
	completed = strings.TrimSpace(completed)
	if completed == "" || AlreadyWoken(existing, completed) {
		return existing
	}
	if strings.TrimSpace(existing) == "" {
		return completed
	}
	return existing + "," + completed
}

// DownstreamFailureNotice is the informational comment on the upstream issue.
// It names the child and does not ask anyone to re-dispatch.
func DownstreamFailureNotice(childIdentifier, childID, reason string) string {
	if strings.TrimSpace(reason) == "" {
		reason = "运行失败"
	}
	return fmt.Sprintf(
		"下游 [%s](mention://issue/%s) %s。平台会按自己的重试或到点叫醒处理，这张票不用你去重派。",
		childIdentifier, childID, reason,
	)
}

// MetaString reads one primitive metadata value as text.
func MetaString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	v, ok := meta[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

func splitTokens(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = appendUnique(out, part)
	}
	return out
}

func appendUnique(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}
