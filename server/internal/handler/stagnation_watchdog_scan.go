package handler

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/closeprotocol"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Protocol §7 (DENE-233): four compensation scans. Hit detection is pure so
// tests can cover both sides without a database. The sweeper applies the
// shared (issue, agent) active-task idempotency and posts the comment.

const watchdogStaleAfter = 30 * time.Minute

const (
	watchdogMarkerABarrier = "watchdog:scan-a-barrier"
	watchdogMarkerAReview  = "watchdog:scan-a-review"
	watchdogMarkerB        = "watchdog:scan-b"
	watchdogMarkerC        = "watchdog:scan-c"
	watchdogMarkerDWait    = "watchdog:scan-d-waiting-on"
	watchdogMarkerDMention = "watchdog:scan-d-mention"
)

const (
	wakeFailCreateComment = "create_system_comment"
	wakeFailEnqueueAgent  = "enqueue_parent_agent"
	wakeFailEnqueueSquad  = "enqueue_parent_squad_leader"
	wakeFailLoadParent    = "load_parent"
	wakeFailListSiblings  = "list_siblings"
)

type watchdogScanKind string

const (
	scanABarrier watchdogScanKind = "a_barrier_closed_no_runner"
	scanAReview  watchdogScanKind = "a_stalled_review"
	scanBIdle    watchdogScanKind = "b_idle_in_progress"
	scanCFailure watchdogScanKind = "c_child_done_failure"
	scanDWait    watchdogScanKind = "d_waiting_on_resolved"
	scanDMention watchdogScanKind = "d_mention_not_enqueued"
)

type watchdogHit struct {
	Kind   watchdogScanKind
	Marker string
	Reason string
}

func (h watchdogHit) empty() bool { return h.Kind == "" }

type stageBucket struct {
	stage int32 // 0 = implicit unstaged set
	kids  []db.Issue
}

func groupWatchdogStages(children []db.Issue) []stageBucket {
	if !siblingsAreStaged(children) {
		return []stageBucket{{stage: 0, kids: children}}
	}
	byStage := map[int32][]db.Issue{}
	var order []int32
	for _, c := range children {
		if !c.Stage.Valid {
			continue
		}
		s := c.Stage.Int32
		if _, ok := byStage[s]; !ok {
			order = append(order, s)
		}
		byStage[s] = append(byStage[s], c)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	out := make([]stageBucket, 0, len(order))
	for _, s := range order {
		out = append(out, stageBucket{stage: s, kids: byStage[s]})
	}
	return out
}

func classifyScanA(children []db.Issue, effective func(db.Issue) (string, error), now time.Time) (watchdogHit, error) {
	buckets := groupWatchdogStages(children)
	if len(buckets) == 0 {
		return watchdogHit{}, nil
	}
	statuses := make([][]string, len(buckets))
	for i, b := range buckets {
		statuses[i] = make([]string, len(b.kids))
		for j, c := range b.kids {
			st, err := effective(c)
			if err != nil {
				return watchdogHit{}, err
			}
			statuses[i][j] = st
		}
	}

	for i, b := range buckets {
		if allTerminalStatuses(statuses[i]) {
			continue
		}
		if allReviewOrBlocked(statuses[i]) {
			if allChildrenStale(b.kids, now) {
				return watchdogHit{
					Kind:   scanAReview,
					Marker: watchdogMarkerAReview,
					Reason: "lowest open stage is all in_review/blocked and stale; mention the parent dispatcher — do not auto-done",
				}, nil
			}
			return watchdogHit{}, nil
		}
		if allBacklogStatuses(statuses[i]) && i > 0 && allTerminalStatuses(statuses[i-1]) {
			return watchdogHit{
				Kind:   scanABarrier,
				Marker: watchdogMarkerABarrier,
				Reason: "previous stage is fully terminal and the next stage is still backlog; parent should have been woken",
			}, nil
		}
		return watchdogHit{}, nil
	}

	return watchdogHit{
		Kind:   scanABarrier,
		Marker: watchdogMarkerABarrier,
		Reason: "every stage is fully terminal; parent should have been woken to wrap up",
	}, nil
}

func classifyScanB(issue db.Issue, effective func(db.Issue) (string, error), now time.Time) (watchdogHit, error) {
	status, err := effective(issue)
	if err != nil {
		return watchdogHit{}, err
	}
	if status != "in_progress" {
		return watchdogHit{}, nil
	}
	if !activityOlderThan(issue, now.Add(-watchdogStaleAfter)) {
		return watchdogHit{}, nil
	}
	return watchdogHit{
		Kind:   scanBIdle,
		Marker: watchdogMarkerB,
		Reason: "in_progress with no recent activity; mention the current assignee",
	}, nil
}

func classifyScanDWaitingOn(waiterStatus, waitedStatus string) watchdogHit {
	if isTerminalChildStatus(waiterStatus) || waiterStatus == "backlog" {
		return watchdogHit{}
	}
	if !isTerminalChildStatus(waitedStatus) {
		return watchdogHit{}
	}
	return watchdogHit{
		Kind:   scanDWait,
		Marker: watchdogMarkerDWait,
		Reason: "close.waiting_on points at a terminal issue; mention this issue's assignee",
	}
}

func classifyScanDMention(meta map[string]string, now time.Time) watchdogHit {
	if meta[closeprotocol.KeyWakeAction] != closeprotocol.WakeMention {
		return watchdogHit{}
	}
	if strings.TrimSpace(meta[closeprotocol.KeyNextOwnerID]) == "" {
		return watchdogHit{}
	}
	at := strings.TrimSpace(meta[closeprotocol.KeyAt])
	if at == "" {
		return watchdogHit{}
	}
	parsed, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return watchdogHit{}
	}
	if !parsed.Before(now.Add(-watchdogStaleAfter)) {
		return watchdogHit{}
	}
	return watchdogHit{
		Kind:   scanDMention,
		Marker: watchdogMarkerDMention,
		Reason: "close.wake_action=mention is older than 30m and next_owner has no active run",
	}
}

func allTerminalStatuses(statuses []string) bool {
	if len(statuses) == 0 {
		return false
	}
	for _, s := range statuses {
		if !isTerminalChildStatus(s) {
			return false
		}
	}
	return true
}

func allReviewOrBlocked(statuses []string) bool {
	if len(statuses) == 0 {
		return false
	}
	for _, s := range statuses {
		if s != "in_review" && s != "blocked" {
			return false
		}
	}
	return true
}

func allBacklogStatuses(statuses []string) bool {
	if len(statuses) == 0 {
		return false
	}
	for _, s := range statuses {
		if s != "backlog" {
			return false
		}
	}
	return true
}

func allChildrenStale(children []db.Issue, now time.Time) bool {
	cutoff := now.Add(-watchdogStaleAfter)
	for _, c := range children {
		if !childStaleBefore(c, cutoff) {
			return false
		}
	}
	return true
}

func childStaleBefore(c db.Issue, cutoff time.Time) bool {
	if t, ok := closeAtFromMetadata(c.Metadata); ok {
		return t.Before(cutoff)
	}
	return activityOlderThan(c, cutoff)
}

func activityOlderThan(issue db.Issue, cutoff time.Time) bool {
	if issue.LastActivityAt.Valid {
		return issue.LastActivityAt.Time.Before(cutoff)
	}
	if issue.UpdatedAt.Valid {
		return issue.UpdatedAt.Time.Before(cutoff)
	}
	return true
}

func closeAtFromMetadata(raw []byte) (time.Time, bool) {
	at := closeMetaString(parseIssueMetadata(raw), closeprotocol.KeyAt)
	if at == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func closeMetadataStrings(raw []byte) map[string]string {
	src := parseIssueMetadata(raw)
	out := make(map[string]string, len(closeprotocol.Keys))
	for _, k := range closeprotocol.Keys {
		out[k] = closeMetaString(src, k)
	}
	return out
}

func closeMetaString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	v, ok := meta[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", t), "0"), ".")
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(t)
	}
}

func parentWatchdogEligible(status string) bool {
	switch status {
	case "done", "cancelled", "backlog":
		return false
	}
	return true
}
