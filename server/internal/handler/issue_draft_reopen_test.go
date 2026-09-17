package handler

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// A confirmed alignment is not necessarily the end of it. These tests cover the
// other half: reopening the same draft starts another round on the SAME group,
// and confirming that round adds only the nodes the payload grew — never a
// second group, never a rewrite of what already exists.
//
// Database-backed for the same reason the first half is: every guarantee here
// is a statement about what the transaction and the partial unique index on
// issue (origin_id) actually did.

// reopenRequest posts to the reopen endpoint. It carries no body: the round a
// draft is on is a property of the row, and the idempotent no-op is the answer
// for a draft whose round is already open.
func reopenRequest(t *testing.T, sessionID string) *http.Request {
	t.Helper()
	return withURLParam(newRequest(http.MethodPost, "/api/issue-drafts/"+sessionID+"/reopen", nil), "sessionId", sessionID)
}

// roundOne confirms a parent plus two children and hands back the response, so
// the tests about the second round start from a group that really exists.
func roundOne(t *testing.T, sessionID string, children ...map[string]any) FinalizeIssueDraftResponse {
	t.Helper()
	saved := saveIssueDraft(t, sessionID, 0, "ready", draftGroupPayload("round one parent", children...))
	var first FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, sessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&first)
	if len(first.Issues) != len(children)+1 {
		t.Fatalf("first round produced %d issues, want %d", len(first.Issues), len(children)+1)
	}
	return first
}

// Reopen the same alignment, add one node to the payload, confirm again: the
// group grows by exactly that node, and every issue already in it keeps its id.
func TestReopenIssueDraftConfirmAddsOnlyTheNewNodes(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	agentID := handlerTestAgentID(t)
	session := startIssueDraftSession(t)
	first := roundOne(t, session.SessionID,
		draftChild("c1", "first child", "todo"),
		draftChild("c2", "second child", "todo"),
	)

	var reopened issueDraftResponse
	testutil.Call(t, testHandler.ReopenIssueDraft, reopenRequest(t, session.SessionID)).
		Want(http.StatusOK).JSON(&reopened)

	if reopened.Status != "ready" {
		t.Fatalf("reopened draft is in %q, want ready — the same conversation has to be continuable", reopened.Status)
	}
	if reopened.FinalizeRound != 1 {
		t.Fatalf("reopen counted the round as %d, want 1", reopened.FinalizeRound)
	}
	if reopened.FinalizedRevision == nil || *reopened.FinalizedRevision != first.Draft.Revision {
		t.Fatalf("reopen recorded finalized_revision = %v, want the revision the first round confirmed (%d)",
			reopened.FinalizedRevision, first.Draft.Revision)
	}
	// Reopening is a lifecycle move, not an edit: the content revision is the
	// optimistic-concurrency token for the draft's fields, and it must stay
	// where the confirmed payload left it.
	if reopened.Revision != first.Draft.Revision {
		t.Fatalf("reopen moved the content revision from %d to %d", first.Draft.Revision, reopened.Revision)
	}
	if reopened.IssueID == nil || *reopened.IssueID != first.IssueID {
		t.Fatalf("reopen dropped the root the draft points at: issue_id = %v, want %s",
			reopened.IssueID, first.IssueID)
	}

	// The increment: the same two keys, carried back unchanged, plus a third —
	// stage 1 and assigned, so it is dispatched on confirm like any other
	// first-stage child.
	firstChild := assignTo(draftChild("c1", "first child", "todo"), agentID)
	firstChild["stage"] = 1
	added := assignTo(draftChild("c3", "third child", "todo"), agentID)
	added["stage"] = 1
	round := saveIssueDraft(t, session.SessionID, reopened.Revision, "ready", draftGroupPayload("round one parent",
		firstChild,
		draftChild("c2", "second child", "todo"),
		added,
	))

	var second FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, round.Revision)).
		Want(http.StatusOK).JSON(&second)

	if len(second.Issues) != 4 {
		t.Fatalf("the second round answered with %d issues, want the whole group of 4", len(second.Issues))
	}
	if second.IssueID != first.IssueID {
		t.Fatalf("the second round built another group: root %s, want %s", second.IssueID, first.IssueID)
	}
	for i, issue := range first.Issues {
		if second.Issues[i].ID != issue.ID {
			t.Fatalf("the second round changed position %d: %s became %s — a follow-up round "+
				"adds nodes, it does not rebuild the group", i, issue.ID, second.Issues[i].ID)
		}
	}
	if got := issueDraftGroupIssueCount(t); got != 4 {
		t.Fatalf("the board holds %d issues from this alignment, want 4", got)
	}
	// The appended child is real work: created under the same root, and
	// dispatched, which is what the post-commit half of the create does.
	if second.Issues[3].ParentIssueID == nil || *second.Issues[3].ParentIssueID != first.IssueID {
		t.Fatalf("the appended child hangs off %v, want the group's root %s",
			second.Issues[3].ParentIssueID, first.IssueID)
	}
	if got := dbfx.Count(t, `
		SELECT COUNT(*) FROM agent_task_queue WHERE issue_id = $1
	`, second.Issues[3].ID); got != 1 {
		t.Fatalf("the appended stage-1 child queued %d tasks, want 1", got)
	}
}

// Confirming the same round twice is the same answer twice, and reopening a
// draft that already has an open round does not count it again.
func TestReopenIssueDraftRoundAndConfirmAreIdempotent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	session := startIssueDraftSession(t)
	first := roundOne(t, session.SessionID,
		draftChild("c1", "first child", "todo"),
		draftChild("c2", "second child", "todo"),
	)

	var reopened issueDraftResponse
	testutil.Call(t, testHandler.ReopenIssueDraft, reopenRequest(t, session.SessionID)).
		Want(http.StatusOK).JSON(&reopened)
	round := saveIssueDraft(t, session.SessionID, reopened.Revision, "ready", draftGroupPayload("round one parent",
		draftChild("c1", "first child", "todo"),
		draftChild("c2", "second child", "todo"),
		draftChild("c3", "third child", "todo"),
	))

	var second FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, round.Revision)).
		Want(http.StatusOK).JSON(&second)

	var again FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, round.Revision)).
		Want(http.StatusOK).JSON(&again)

	if len(again.Issues) != len(second.Issues) {
		t.Fatalf("the repeated confirm answered with %d issues, want the same %d", len(again.Issues), len(second.Issues))
	}
	for i := range second.Issues {
		if again.Issues[i].ID != second.Issues[i].ID {
			t.Fatalf("the repeated confirm disagrees at position %d: %s vs %s",
				i, again.Issues[i].ID, second.Issues[i].ID)
		}
	}
	if got := issueDraftGroupIssueCount(t); got != 4 {
		t.Fatalf("a repeated confirm left %d issues, want 4", got)
	}
	var storedRound int32
	dbfx.QueryRow(t, `SELECT finalize_round FROM issue_draft WHERE chat_session_id = $1`, session.SessionID).Scan(&storedRound)
	if storedRound != 1 {
		t.Fatalf("draft recorded round %d, want 1", storedRound)
	}

	// A retried reopen — a lost response, a double click — lands on the
	// completed row the confirm just produced. It must not advance the round a
	// second time.
	var reopenedAgain issueDraftResponse
	testutil.Call(t, testHandler.ReopenIssueDraft, reopenRequest(t, session.SessionID)).
		Want(http.StatusOK).JSON(&reopenedAgain)
	if reopenedAgain.FinalizeRound != 2 {
		t.Fatalf("the second reopen counted round %d, want 2", reopenedAgain.FinalizeRound)
	}
	var repeated issueDraftResponse
	testutil.Call(t, testHandler.ReopenIssueDraft, reopenRequest(t, session.SessionID)).
		Want(http.StatusOK).JSON(&repeated)
	if repeated.FinalizeRound != 2 {
		t.Fatalf("reopening an already-open round counted it again: round %d, want 2", repeated.FinalizeRound)
	}
	if repeated.Status != "ready" {
		t.Fatalf("reopening an already-open round left status %q, want ready", repeated.Status)
	}
	if repeated.Revision != reopenedAgain.Revision {
		t.Fatalf("reopening an already-open round moved the revision from %d to %d",
			reopenedAgain.Revision, repeated.Revision)
	}
	if first.Issues[0].ID == "" {
		t.Fatal("the first round produced no root")
	}
}

// A node that already owns an issue is adopted, never rewritten: the group is
// real work by the time a round is added to it, edited by people and agents,
// and a follow-up round is not grounds for overwriting that.
func TestReopenIssueDraftDoesNotRewriteNodesThatAlreadyExist(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	session := startIssueDraftSession(t)
	first := roundOne(t, session.SessionID,
		draftChild("c1", "first child", "todo"),
		draftChild("c2", "second child", "todo"),
	)

	// Somebody renamed the child on the board after it was created.
	const edited = "renamed by hand after the round"
	dbfx.Exec(t, `UPDATE issue SET title = $1 WHERE id = $2`, edited, first.Issues[2].ID)

	var reopened issueDraftResponse
	testutil.Call(t, testHandler.ReopenIssueDraft, reopenRequest(t, session.SessionID)).
		Want(http.StatusOK).JSON(&reopened)
	// The payload still carries the title the alignment settled on, because the
	// draft is not where the rename happened.
	round := saveIssueDraft(t, session.SessionID, reopened.Revision, "ready", draftGroupPayload("round one parent",
		draftChild("c1", "first child", "todo"),
		draftChild("c2", "second child", "todo"),
		draftChild("c3", "third child", "todo"),
	))

	var second FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, round.Revision)).
		Want(http.StatusOK).JSON(&second)

	var title string
	dbfx.QueryRow(t, `SELECT title FROM issue WHERE id = $1`, first.Issues[2].ID).Scan(&title)
	if title != edited {
		t.Fatalf("the round rewrote an existing node: title = %q, want the hand edit %q", title, edited)
	}
	if len(second.Issues) != 4 {
		t.Fatalf("the round answered with %d issues, want 4", len(second.Issues))
	}
}

// Reopening a draft that never confirmed anything is a no-op, and the confirm
// that follows behaves exactly as it did before rounds existed.
func TestReopenIssueDraftOnAnOpenDraftIsANoOp(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	session := startIssueDraftSession(t)

	// Still 'draft': never even offered for confirmation.
	var untouched issueDraftResponse
	testutil.Call(t, testHandler.ReopenIssueDraft, reopenRequest(t, session.SessionID)).
		Want(http.StatusOK).JSON(&untouched)
	if untouched.Status != "draft" || untouched.FinalizeRound != 0 {
		t.Fatalf("reopen changed a never-confirmed draft: status %q, round %d", untouched.Status, untouched.FinalizeRound)
	}

	saved := saveIssueDraft(t, session.SessionID, untouched.Revision, "ready", draftGroupPayload("open parent",
		draftChild("c1", "first child", "todo"),
		draftChild("c2", "second child", "todo"),
	))

	var open issueDraftResponse
	testutil.Call(t, testHandler.ReopenIssueDraft, reopenRequest(t, session.SessionID)).
		Want(http.StatusOK).JSON(&open)
	if open.Status != "ready" || open.FinalizeRound != 0 || open.FinalizedRevision != nil {
		t.Fatalf("reopen counted a round on a draft that never confirmed one: status %q, round %d, finalized_revision %v",
			open.Status, open.FinalizeRound, open.FinalizedRevision)
	}

	var finalized FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&finalized)
	if len(finalized.Issues) != 3 {
		t.Fatalf("the confirm after a no-op reopen produced %d issues, want 3", len(finalized.Issues))
	}
	if got := issueDraftGroupIssueCount(t); got != 3 {
		t.Fatalf("the board holds %d issues from this alignment, want 3", got)
	}
	for _, issue := range finalized.Issues {
		if issue.ID != finalized.Issues[0].ID && issue.ParentIssueID == nil {
			t.Fatalf("child %s has no parent: %+v", issue.ID, issue)
		}
	}
}

// An abandoned alignment stays discarded: reopening is not an undo for a
// conversation somebody explicitly threw away.
func TestReopenIssueDraftRefusesAnAbandonedDraft(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	session := startIssueDraftSession(t)
	saveIssueDraft(t, session.SessionID, 0, "ready", draftGroupPayload("abandoned parent"))

	testutil.Call(t, testHandler.AbandonIssueDraft, withURLParam(
		newRequest(http.MethodPost, "/api/issue-drafts/"+session.SessionID+"/abandon", nil), "sessionId", session.SessionID)).
		Want(http.StatusOK)

	testutil.Call(t, testHandler.ReopenIssueDraft, reopenRequest(t, session.SessionID)).
		Want(http.StatusConflict)

	var status string
	dbfx.QueryRow(t, `SELECT status FROM issue_draft WHERE chat_session_id = $1`, session.SessionID).Scan(&status)
	if status != "abandoned" {
		t.Fatalf("the refused reopen left the draft in %q, want abandoned", status)
	}
}

// The list is the only row an open alignment page reads, so a continuation has
// to be able to say which round it is on from the list alone. It used to be
// unable to: `ListIssueDraftsByCreator` did not select the round columns, and
// the page POSTed /reopen — a write — purely to read its own round back
// (DENE-416).
func TestListIssueDraftsCarriesTheRoundOfAContinuation(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	session := startIssueDraftSession(t)
	first := roundOne(t, session.SessionID, draftChild("c1", "first child", "todo"))

	var reopened issueDraftResponse
	testutil.Call(t, testHandler.ReopenIssueDraft, reopenRequest(t, session.SessionID)).
		Want(http.StatusOK).JSON(&reopened)

	var listed ListIssueDraftsResponse
	testutil.Call(t, testHandler.ListIssueDrafts, newRequest(http.MethodGet, "/api/issue-drafts", nil)).
		Want(http.StatusOK).JSON(&listed)

	var row *IssueDraftSummary
	for i := range listed.Drafts {
		if listed.Drafts[i].ChatSessionID == session.SessionID {
			row = &listed.Drafts[i]
			break
		}
	}
	if row == nil {
		t.Fatal("the reopened alignment is missing from the unfinished list")
	}
	if row.FinalizeRound != 1 {
		t.Fatalf("the listed row reports round %d, want 1 — a continuation cannot say which round it is on",
			row.FinalizeRound)
	}
	if row.FinalizedRevision == nil || *row.FinalizedRevision != first.Draft.Revision {
		t.Fatalf("the listed row reports finalized_revision %v, want the revision the first round confirmed (%d)",
			row.FinalizedRevision, first.Draft.Revision)
	}
}

// A node's issue can leave the group — moved under a sibling, re-parented onto
// another epic, detached to the top level — and it still owns its origin. The
// round that follows has to recognise it as an existing node anyway.
//
// Reading the group by parent does not: it would report the moved node as new,
// the insert would collide on the partial unique index over issue (origin_id),
// and the whole round's transaction would roll back — so the nodes the round
// genuinely added would silently never be created, and every later confirm
// would fail the same way. Ownership is therefore asked by origin, which is the
// key that index is on.
func TestReopenIssueDraftSeesNodesMovedOutOfTheGroup(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	session := startIssueDraftSession(t)
	first := roundOne(t, session.SessionID,
		draftChild("c1", "first child", "todo"),
		draftChild("c2", "second child", "todo"),
	)

	// Somebody re-parents the first child under its sibling.
	dbfx.Exec(t, `UPDATE issue SET parent_issue_id = $1 WHERE id = $2`, first.Issues[2].ID, first.Issues[1].ID)

	var reopened issueDraftResponse
	testutil.Call(t, testHandler.ReopenIssueDraft, reopenRequest(t, session.SessionID)).
		Want(http.StatusOK).JSON(&reopened)
	round := saveIssueDraft(t, session.SessionID, reopened.Revision, "ready", draftGroupPayload("round one parent",
		draftChild("c1", "first child", "todo"),
		draftChild("c2", "second child", "todo"),
		draftChild("c3", "third child", "todo"),
	))

	var second FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, round.Revision)).
		Want(http.StatusOK).JSON(&second)

	// Exactly one new issue: the moved node is adopted, not built again.
	if got := issueDraftGroupIssueCount(t); got != 4 {
		t.Fatalf("the round left %d issues from this alignment, want 4 — the node it added "+
			"was lost to a collision with the node that had moved out of the group", got)
	}
	var movedParent, movedID string
	dbfx.QueryRow(t, `SELECT id::text, parent_issue_id::text FROM issue WHERE id = $1`,
		first.Issues[1].ID).Scan(&movedID, &movedParent)
	if movedParent != first.Issues[2].ID {
		t.Fatalf("the round moved the node back under the root: parent = %s, want %s — "+
			"a follow-up round adopts what exists, it does not re-file it",
			movedParent, first.Issues[2].ID)
	}
}
