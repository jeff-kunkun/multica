package handler

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func builtinStatus(c db.Issue) (string, error) { return c.Status, nil }

func watchdogNow() time.Time {
	return time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
}

func childAt(stage int32, status string, at time.Time) db.Issue {
	c := child(stage, status)
	c.LastActivityAt = pgtype.Timestamptz{Time: at, Valid: true}
	return c
}

func childCloseAt(stage int32, status string, at time.Time) db.Issue {
	c := childAt(stage, status, at)
	raw, _ := json.Marshal(map[string]string{closeprotocol.KeyAt: at.Format(time.RFC3339)})
	c.Metadata = raw
	return c
}

func TestClassifyScanA_HitBarrierWhenPreviousStageDoneNextBacklog(t *testing.T) {
	now := watchdogNow()
	children := []db.Issue{
		child(1, "done"), child(1, "cancelled"),
		child(2, "backlog"), child(2, "backlog"),
	}
	hit, err := classifyScanA(children, builtinStatus, now)
	if err != nil {
		t.Fatal(err)
	}
	if hit.Kind != scanABarrier {
		t.Fatalf("kind = %q, want %s", hit.Kind, scanABarrier)
	}
}

func TestClassifyScanA_HitBarrierWhenEveryStageTerminal(t *testing.T) {
	hit, err := classifyScanA([]db.Issue{child(1, "done"), child(2, "cancelled")}, builtinStatus, watchdogNow())
	if err != nil {
		t.Fatal(err)
	}
	if hit.Kind != scanABarrier {
		t.Fatalf("kind = %q, want %s", hit.Kind, scanABarrier)
	}
}

func TestClassifyScanA_MissWhenLowestStageStillInProgress(t *testing.T) {
	children := []db.Issue{
		child(1, "done"), child(1, "in_progress"),
		child(2, "backlog"),
	}
	hit, err := classifyScanA(children, builtinStatus, watchdogNow())
	if err != nil {
		t.Fatal(err)
	}
	if !hit.empty() {
		t.Fatalf("in_progress sibling must not hit, got %+v", hit)
	}
}

func TestClassifyScanA_HitStalledReviewWhenAllInReviewAndStale(t *testing.T) {
	now := watchdogNow()
	stale := now.Add(-31 * time.Minute)
	children := []db.Issue{
		childCloseAt(1, "in_review", stale),
		childAt(1, "blocked", stale),
		child(2, "backlog"),
	}
	hit, err := classifyScanA(children, builtinStatus, now)
	if err != nil {
		t.Fatal(err)
	}
	if hit.Kind != scanAReview {
		t.Fatalf("kind = %q, want %s", hit.Kind, scanAReview)
	}
}

func TestClassifyScanA_MissStalledReviewWhenCloseAtIsFresh(t *testing.T) {
	now := watchdogNow()
	children := []db.Issue{
		childCloseAt(1, "in_review", now.Add(-5*time.Minute)),
		child(2, "backlog"),
	}
	hit, err := classifyScanA(children, builtinStatus, now)
	if err != nil {
		t.Fatal(err)
	}
	if !hit.empty() {
		t.Fatalf("fresh close.at must not hit scan A review, got %+v", hit)
	}
}

func TestClassifyScanA_UnstagedInReviewIsScanAReview(t *testing.T) {
	now := watchdogNow()
	stale := now.Add(-31 * time.Minute)
	hit, err := classifyScanA([]db.Issue{childAt(0, "in_review", stale)}, builtinStatus, now)
	if err != nil {
		t.Fatal(err)
	}
	if hit.Kind != scanAReview {
		t.Fatalf("kind = %q, want %s", hit.Kind, scanAReview)
	}
}

func TestClassifyScanB_HitAndMiss(t *testing.T) {
	now := watchdogNow()
	stale := now.Add(-31 * time.Minute)
	fresh := now.Add(-5 * time.Minute)

	hit, err := classifyScanB(childAt(0, "in_progress", stale), builtinStatus, now)
	if err != nil {
		t.Fatal(err)
	}
	if hit.Kind != scanBIdle {
		t.Fatalf("stale in_progress must hit, got %+v", hit)
	}

	hit, err = classifyScanB(childAt(0, "in_progress", fresh), builtinStatus, now)
	if err != nil {
		t.Fatal(err)
	}
	if !hit.empty() {
		t.Fatalf("fresh in_progress must miss, got %+v", hit)
	}

	hit, err = classifyScanB(childAt(0, "in_review", stale), builtinStatus, now)
	if err != nil {
		t.Fatal(err)
	}
	if !hit.empty() {
		t.Fatalf("in_review is excluded from scan B, got %+v", hit)
	}

	hit, err = classifyScanB(childAt(0, "blocked", stale), builtinStatus, now)
	if err != nil {
		t.Fatal(err)
	}
	if !hit.empty() {
		t.Fatalf("blocked is excluded from scan B, got %+v", hit)
	}
}

func TestClassifyScanDWaitingOn_HitAndMiss(t *testing.T) {
	if got := classifyScanDWaitingOn("in_review", "done"); got.Kind != scanDWait {
		t.Fatalf("waiter in_review + waited done must hit, got %+v", got)
	}
	if got := classifyScanDWaitingOn("blocked", "cancelled"); got.Kind != scanDWait {
		t.Fatalf("waiter blocked + waited cancelled must hit, got %+v", got)
	}
	if got := classifyScanDWaitingOn("in_review", "in_progress"); !got.empty() {
		t.Fatalf("waited-on still open must miss, got %+v", got)
	}
	if got := classifyScanDWaitingOn("done", "done"); !got.empty() {
		t.Fatalf("terminal waiter must miss, got %+v", got)
	}
	if got := classifyScanDWaitingOn("backlog", "done"); !got.empty() {
		t.Fatalf("backlog waiter must miss, got %+v", got)
	}
}

func TestClassifyScanDMention_HitAndMiss(t *testing.T) {
	now := watchdogNow()
	stale := now.Add(-31 * time.Minute).Format(time.RFC3339)
	fresh := now.Add(-5 * time.Minute).Format(time.RFC3339)
	meta := map[string]string{
		closeprotocol.KeyWakeAction:  closeprotocol.WakeMention,
		closeprotocol.KeyNextOwnerID: "1cbd7845-acbd-47d7-b0ea-582ec3d9f01f",
		closeprotocol.KeyAt:          stale,
	}
	if got := classifyScanDMention(meta, now); got.Kind != scanDMention {
		t.Fatalf("stale mention wake must hit, got %+v", got)
	}
	meta[closeprotocol.KeyAt] = fresh
	if got := classifyScanDMention(meta, now); !got.empty() {
		t.Fatalf("fresh close.at must miss, got %+v", got)
	}
	meta[closeprotocol.KeyAt] = stale
	meta[closeprotocol.KeyWakeAction] = closeprotocol.WakeNone
	if got := classifyScanDMention(meta, now); !got.empty() {
		t.Fatalf("wake_action=none must miss, got %+v", got)
	}
}

func TestParentWatchdogEligible(t *testing.T) {
	if parentWatchdogEligible(issuestatus.Done) || parentWatchdogEligible(issuestatus.Cancelled) || parentWatchdogEligible(issuestatus.Backlog) {
		t.Fatal("done/cancelled/backlog parents are not scan A candidates")
	}
	if !parentWatchdogEligible(issuestatus.InProgress) || !parentWatchdogEligible(issuestatus.InReview) {
		t.Fatal("in_progress/in_review parents remain eligible")
	}
}
