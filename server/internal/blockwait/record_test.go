package blockwait

import (
	"strings"
	"testing"
	"time"
)

func TestRecordRequiresOneKind(t *testing.T) {
	if (Record{}).Structured() {
		t.Fatal("empty record is not a block")
	}
	if !(Record{BlockedBy: []string{"DENE-806"}}).Structured() {
		t.Fatal("blocked_by should count")
	}
	if !(Record{HasWakeAt: true, WakeAt: time.Now()}).Structured() {
		t.Fatal("wake_at should count")
	}
	if (Record{WaitCondition: "publisher caught up"}).Structured() {
		t.Fatal("a condition without a deadline is not enough")
	}
	if !(Record{WaitCondition: "publisher caught up", HasWaitTimeout: true, WaitTimeout: time.Now()}).Structured() {
		t.Fatal("condition plus deadline should count")
	}
	if !(Record{NeedsHuman: "00000000-0000-0000-0000-000000000001"}).Structured() {
		t.Fatal("needs_human should count")
	}
}

func TestSuggestFromWaitingComment(t *testing.T) {
	got := SuggestFromComments([]string{"买入这步卡住，等 DENE-806 修好再继续。"})
	if len(got.BlockedBy) != 1 || got.BlockedBy[0] != "DENE-806" {
		t.Fatalf("suggestion = %#v", got)
	}
	if !strings.Contains(got.Hint, "--blocked-by DENE-806") {
		t.Fatalf("hint = %q", got.Hint)
	}
	if SuggestFromComments([]string{"没有提到票号"}).Hint != "" {
		t.Fatal("plain comment must not invent a blocker")
	}
}

func TestMergeRejectsBadClock(t *testing.T) {
	_, err := Merge(nil, Input{WakeAt: "tomorrow"})
	if err == nil {
		t.Fatal("expected wake_at error")
	}
}

func TestWaitingOnMetadataCountsAsStructured(t *testing.T) {
	r := ParseMetadata(map[string]any{"close.waiting_on": "DENE-806"})
	if !r.Structured() || r.BlockedBy[0] != "DENE-806" {
		t.Fatalf("record = %#v", r)
	}
}

func TestWakeIdempotency(t *testing.T) {
	if AlreadyWoken("DENE-1,DENE-2", "DENE-2") != true {
		t.Fatal("expected already woken")
	}
	if AlreadyWoken("DENE-80", "DENE-806") {
		t.Fatal("DENE-80 must not match DENE-806")
	}
	if MarkWoken("DENE-1", "DENE-1") != "DENE-1" {
		t.Fatal("mark must not duplicate")
	}
}

func TestPatrolWakesAtClockAndPicksUpUnstructured(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	due := DecidePatrol(PatrolInput{
		Status: "blocked",
		Record: Record{HasWakeAt: true, WakeAt: now.Add(-time.Minute)},
		Now:    now,
	})
	if due.Action != ActionWake {
		t.Fatalf("due wake_at action = %s", due.Action)
	}
	future := DecidePatrol(PatrolInput{
		Status: "blocked",
		Quiet:  2 * time.Hour,
		Record: Record{HasWakeAt: true, WakeAt: now.Add(10 * time.Minute)},
		Now:    now,
	})
	if future.Action != ActionHold {
		t.Fatalf("future wake_at must wait, got %s", future.Action)
	}
	bare := DecidePatrol(PatrolInput{
		Status: "blocked",
		Quiet:  QuietAfter,
		Now:    now,
	})
	if bare.Action != ActionWake {
		t.Fatalf("unstructured blocked action = %s", bare.Action)
	}
	open := DecidePatrol(PatrolInput{
		Status:   "blocked",
		Quiet:    2 * time.Hour,
		Record:   Record{BlockedBy: []string{"DENE-806"}},
		Blockers: []BlockerView{{Ref: "DENE-806", Status: "in_progress"}},
		Now:      now,
	})
	if open.Action != ActionHold {
		t.Fatalf("open blocker must hold, got %s", open.Action)
	}
	cleared := DecidePatrol(PatrolInput{
		Status:   "blocked",
		Record:   Record{BlockedBy: []string{"DENE-806"}},
		Blockers: []BlockerView{{Ref: "DENE-806", Status: "done"}},
		Now:      now,
	})
	if cleared.Action != ActionWake {
		t.Fatalf("cleared blocker action = %s", cleared.Action)
	}
}

func TestAcceptancePassPhrases(t *testing.T) {
	if !IsAcceptancePass("验收通过，等待合并流程。") {
		t.Fatal("the DENE-806 wording is a pass")
	}
	if IsAcceptancePass("验收不通过，需要修改。") {
		t.Fatal("a rejection must not pass")
	}
	if IsAcceptancePass("暂不合并，检查是红的。") {
		t.Fatal("an explicit hold must not pass")
	}
}

func TestReleaseDoesNotStayInReview(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	if DecideRelease(nil, now).Action != ReleaseDone {
		t.Fatal("no PR should close")
	}
	clean := DecideRelease([]PRSnapshot{{Number: 12, State: "open", Mergeable: "clean", Checks: "success"}}, now)
	if clean.Action != ReleaseMerge {
		t.Fatalf("clean PR action = %s", clean.Action)
	}
	dirty := DecideRelease([]PRSnapshot{{Number: 12, State: "open", Mergeable: "dirty", URL: "https://example/pull/12"}}, now)
	if dirty.Action != ReleaseBlock || !dirty.Record.Structured() {
		t.Fatalf("conflict = %#v", dirty)
	}
	if !strings.Contains(dirty.Reason, "不继续停在待验收") {
		t.Fatalf("reason = %q", dirty.Reason)
	}
}

func TestDownstreamNoticeDoesNotAskForRedispatch(t *testing.T) {
	got := DownstreamFailureNotice("DENE-806", "abc", "被平台中断")
	if strings.Contains(got, "mention://agent/") {
		t.Fatalf("notice must not wake an agent: %s", got)
	}
	if !strings.Contains(got, "不用你去重派") || !strings.Contains(got, "DENE-806") {
		t.Fatalf("notice = %s", got)
	}
}
