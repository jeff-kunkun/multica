package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestAllChildrenTerminalRequiresChildrenAndEveryChildTerminal(t *testing.T) {
	terminal := func(issue db.Issue) bool { return issue.Status == "done" || issue.Status == "cancelled" }
	if allChildrenTerminal(nil, terminal) {
		t.Fatal("empty child set must not auto-close a parent")
	}
	children := []db.Issue{{Status: "done"}, {Status: "in_progress"}}
	if allChildrenTerminal(children, terminal) {
		t.Fatal("open child must keep parent open")
	}
	children[1].Status = "cancelled"
	if !allChildrenTerminal(children, terminal) {
		t.Fatal("all terminal children should close the barrier")
	}
}

func TestStallStringAndBoolReadOnlyMetadata(t *testing.T) {
	meta := map[string]any{stallActionKey: "announced", stallCandidateKey: true}
	if stallString(meta, stallActionKey) != stallActionAnnounced {
		t.Fatal("action metadata was not read")
	}
	if !stallBool(meta, stallCandidateKey) {
		t.Fatal("candidate metadata was not read")
	}
}

func TestParentAutoCompletionEligibilityRequiresQuietSafeParent(t *testing.T) {
	base := func() bool { return parentAutoCompletionEligible("in_progress", false, false, false, 2, true) }
	if !base() {
		t.Fatal("a quiet parent with terminal children should be eligible")
	}
	for name, got := range map[string]bool{
		"paused":      parentAutoCompletionEligible("in_progress", true, false, false, 2, true),
		"active run":  parentAutoCompletionEligible("in_progress", false, true, false, 2, true),
		"linked PR":   parentAutoCompletionEligible("in_progress", false, false, true, 2, true),
		"open child":  parentAutoCompletionEligible("in_progress", false, false, false, 2, false),
		"no children": parentAutoCompletionEligible("in_progress", false, false, false, 0, true),
		"backlog":     parentAutoCompletionEligible("backlog", false, false, false, 2, true),
	} {
		if got {
			t.Fatalf("%s parent must not be auto-completed", name)
		}
	}
}

func TestSweepStallActionsCompletesQuietParentAndUndoRestoresStatus(t *testing.T) {
	parent := dbfx.Issue(t, "quiet parent", testutil.Cols{"status": "in_progress", "last_activity_at": time.Now().UTC().Add(-37 * time.Hour)})
	dbfx.Issue(t, "finished child", testutil.Cols{"status": "done", "parent_issue_id": parent})

	if _, err := testHandler.SweepStallActions(t.Context()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	var status string
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, parent).Scan(&status)
	if status != "done" {
		t.Fatalf("parent status = %q, want done", status)
	}

	req := withURLParam(inboxRequest(http.MethodPost, "/api/issues/"+parent+"/stall/undo", testWorkspaceID), "id", parent)
	rr := httptest.NewRecorder()
	inboxWorkspaceHandler(testHandler.UndoStallAction).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("undo status = %d, body=%s", rr.Code, rr.Body.String())
	}
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, parent).Scan(&status)
	if status != "in_progress" {
		t.Fatalf("restored parent status = %q, want in_progress", status)
	}
}

func TestSweepStallActionsAnnouncementKeepAndExpiry(t *testing.T) {
	kept := dbfx.Issue(t, "candidate kept", testutil.Cols{"status": "in_progress", "last_activity_at": time.Now().UTC().Add(-37 * time.Hour), "metadata": rawJSON(`{"stall.candidate":"true"}`)})
	if _, err := testHandler.SweepStallActions(t.Context()); err != nil {
		t.Fatalf("candidate sweep: %v", err)
	}
	var action string
	dbfx.QueryRow(t, `SELECT metadata->>'stall.action' FROM issue WHERE id = $1`, kept).Scan(&action)
	if action != stallActionAnnounced {
		t.Fatalf("candidate action = %q, want announced", action)
	}

	req := withURLParam(inboxRequest(http.MethodPost, "/api/issues/"+kept+"/stall/keep", testWorkspaceID), "id", kept)
	rr := httptest.NewRecorder()
	inboxWorkspaceHandler(testHandler.KeepStallAction).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("keep status = %d, body=%s", rr.Code, rr.Body.String())
	}
	dbfx.QueryRow(t, `SELECT metadata->>'stall.action' FROM issue WHERE id = $1`, kept).Scan(&action)
	if action != stallActionKept {
		t.Fatalf("kept action = %q, want kept", action)
	}

	expired := dbfx.Issue(t, "candidate expired", testutil.Cols{"status": "in_progress", "last_activity_at": time.Now().UTC().Add(-37 * time.Hour), "metadata": rawJSON(`{"stall.candidate":"true"}`)})
	if _, err := testHandler.SweepStallActions(t.Context()); err != nil {
		t.Fatalf("second candidate sweep: %v", err)
	}
	dbfx.Exec(t, `UPDATE issue SET metadata = jsonb_set(metadata, ARRAY['stall.review_until'], to_jsonb($2::text), true) WHERE id = $1`, expired, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339))
	if _, err := testHandler.SweepStallActions(t.Context()); err != nil {
		t.Fatalf("expiry sweep: %v", err)
	}
	var status string
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, expired).Scan(&status)
	if status != "cancelled" {
		t.Fatalf("expired status = %q, want cancelled", status)
	}
}

func rawJSON(value string) any { return testutil.Raw("'" + value + "'::jsonb") }
