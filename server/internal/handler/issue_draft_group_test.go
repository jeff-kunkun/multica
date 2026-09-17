package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/entitlement"
	"github.com/multica-ai/multica/server/internal/entitlement/entitlementtest"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
)

// An alignment confirm produces a GROUP. These tests are database-backed
// because every guarantee at stake — one group per alignment under
// concurrency, children that roll back with their parent, identity derived
// from the conversation rather than from the client — is a statement about what
// the transaction and the partial unique index actually did.

// cleanupIssueDraftGroup is cleanupIssueDraftCarriers plus the agent tasks the
// group's nodes enqueued. agent_task_queue carries no foreign key (repo rule),
// so nothing prunes it when the issue goes away — and the tasks have to be
// registered AFTER the carriers cleanup so they run BEFORE it (t.Cleanup is
// LIFO) while the issues they point at still exist.
func cleanupIssueDraftGroup(t *testing.T) {
	t.Helper()
	cleanupIssueDraftCarriers(t)
	dbfx.Cleanup(t, `
		DELETE FROM agent_task_queue
		WHERE issue_id IN (SELECT id FROM issue WHERE workspace_id = $1 AND origin_type = 'issue_draft')
	`, testWorkspaceID)
}

// draftChild builds one sub-issue the way the preview panel does: a stable key
// plus the fields the alignment settled on.
func draftChild(key, title, status string) map[string]any {
	return map[string]any{
		"key":         key,
		"title":       title,
		"description": "",
		"status":      status,
		"priority":    "medium",
	}
}

func assignTo(child map[string]any, agentID string) map[string]any {
	child["assignee_type"] = "agent"
	child["assignee_id"] = agentID
	return child
}

// draftGroupPayload is the flattened root fields plus the children array.
func draftGroupPayload(title string, children ...map[string]any) map[string]any {
	payload := map[string]any{
		"title":       title,
		"description": "agreed in conversation",
		"status":      "todo",
		"priority":    "medium",
	}
	if len(children) > 0 {
		payload["children"] = children
	}
	return payload
}

// issueDraftGroupIssueCount counts the issues an alignment produced. Every node
// of a group carries origin_type = 'issue_draft', so this is the group's size on
// the board.
func issueDraftGroupIssueCount(t *testing.T) int {
	t.Helper()
	return dbfx.Count(t, `
		SELECT COUNT(*) FROM issue WHERE workspace_id = $1 AND origin_type = 'issue_draft'
	`, testWorkspaceID)
}

// reopenIssueDraft puts a completed draft back in the state a crashed or racing
// confirm leaves behind: still `ready`, no issue recorded, and — when the
// caller passes replacement children — a payload that no longer matches what
// the first confirm created.
//
// It is deliberately NOT the /reopen endpoint. That endpoint starts a new round
// (finalize_round+1) on a group that exists, and the confirm that follows is
// allowed to append; this helper reproduces the two states a first round has to
// survive, where the group exists but no round was ever opened on it: the
// process that died between the commit and the record, and the save that
// re-keyed every child while a confirm was in flight. Both must adopt the group
// whole, so the round counter must stay 0 here — that is the point of the
// fixture.
func reopenIssueDraft(t *testing.T, sessionID string, draft map[string]any) int64 {
	t.Helper()
	raw, err := json.Marshal(draft)
	if err != nil {
		t.Fatalf("encode replacement draft: %v", err)
	}
	dbfx.Exec(t, `
		UPDATE issue_draft
		SET status = 'ready', issue_id = NULL, revision = revision + 1, draft = $2::jsonb
		WHERE chat_session_id = $1
	`, sessionID, string(raw))
	var revision int64
	dbfx.QueryRow(t, `SELECT revision FROM issue_draft WHERE chat_session_id = $1`, sessionID).Scan(&revision)
	return revision
}

// The node id is the whole identity model: it has to be reproducible from the
// conversation alone, and the root has to keep being the chat session id, or
// every existing draft row loses the one lookup that finds it.
func TestIssueDraftNodeIDIsDerivedFromSessionAndKey(t *testing.T) {
	session := util.MustParseUUID("11111111-1111-7111-8111-111111111111")
	other := util.MustParseUUID("22222222-2222-7222-8222-222222222222")

	// The root IS the session id. Migration 486, GetIssueByOrigin and
	// issue_draft.issue_id all rest on this.
	if got := issueDraftNodeID(session, ""); got != session {
		t.Fatalf("root node id = %s, want the chat session itself", uuidToString(got))
	}

	// Golden values: they pin the namespace and the UUIDv5 construction. The
	// namespace is half the input of every node id already minted, so changing
	// it would give every existing group a second identity.
	for key, want := range map[string]string{
		"c1": "c6b0d5f3-6ca6-5dcb-9a14-a49e0cd64e02",
		"c2": "b7d92042-0404-5871-be7a-b2c8f5335460",
		"c3": "f3dcd233-68c1-5533-8612-588b3ff578ba",
	} {
		if got := uuidToString(issueDraftNodeID(session, key)); got != want {
			t.Fatalf("node %q derived %s, want %s — the derivation or its namespace changed, "+
				"which would rebuild every existing group on the next confirm", key, got, want)
		}
	}

	// Reproducible: the same (session, key) always lands on the same node, which
	// is what makes a retried confirm adopt instead of duplicate.
	if a, b := issueDraftNodeID(session, "c1"), issueDraftNodeID(session, "c1"); a != b {
		t.Fatalf("the same (session, key) derived two ids: %s vs %s", uuidToString(a), uuidToString(b))
	}
	// Session-scoped: a client cannot construct a node id that belongs to
	// someone else's alignment.
	if a, b := issueDraftNodeID(session, "c1"), issueDraftNodeID(other, "c1"); a == b {
		t.Fatal("the chat session does not participate in derivation; a node id could point at another draft")
	}
	if a, b := issueDraftNodeID(session, "c1"), issueDraftNodeID(session, "c2"); a == b {
		t.Fatal("two different child keys derived the same node id")
	}
}

// A payload without children is a group with one node — not a legacy branch.
// Its result has to be field-for-field what a lone issue always was.
func TestFinalizeIssueDraftSingleNodeMatchesTheSingleIssueShape(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", map[string]any{
		"title":       "No children is one node",
		"description": "the shape every draft had before groups existed",
		"status":      "todo",
		"priority":    "medium",
	})

	var finalized FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&finalized)

	if len(finalized.Issues) != 1 {
		t.Fatalf("a payload without children produced %d issues, want exactly the single node", len(finalized.Issues))
	}
	only := finalized.Issues[0]
	if only.ID != finalized.IssueID {
		t.Fatalf("the group's only row is %s but issue_id says %s", only.ID, finalized.IssueID)
	}
	if only.ParentIssueID != nil {
		t.Fatalf("the single node has a parent: %v", *only.ParentIssueID)
	}
	if only.Identifier == "" {
		t.Fatal("the confirmation row has no identifier; people are shown numbers, not UUIDs")
	}
	if finalized.Draft.IssueID == nil || *finalized.Draft.IssueID != finalized.IssueID {
		t.Fatalf("draft issue_id = %v, want %s — this is the field a client navigates to "+
			"and a crashed confirm recovers from", finalized.Draft.IssueID, finalized.IssueID)
	}
	var originID string
	dbfx.QueryRow(t, `SELECT origin_id FROM issue WHERE id = $1`, finalized.IssueID).Scan(&originID)
	if originID != session.SessionID {
		t.Fatalf("single node origin_id = %s, want the chat session %s", originID, session.SessionID)
	}

	// The repeat confirm answers with the same one-node group.
	var again FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&again)
	if len(again.Issues) != 1 || again.Issues[0].ID != only.ID {
		t.Fatalf("repeat confirm returned %d issues (%+v), want the same single node", len(again.Issues), again.Issues)
	}
}

// The whole point: one confirm produces a parent and its children, linked by
// parent_issue_id, each node with its own origin and its own stage.
func TestFinalizeIssueDraftCreatesRootAndChildrenAsOneGroup(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	agentID := handlerTestAgentID(t)
	first := assignTo(draftChild("c1", "stage one backend", "todo"), agentID)
	first["stage"] = 1
	second := assignTo(draftChild("c2", "stage one frontend", "todo"), agentID)
	second["stage"] = 1
	third := assignTo(draftChild("c3", "stage two verification", "backlog"), agentID)
	third["stage"] = 2

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready",
		draftGroupPayload("group parent", first, second, third))

	var finalized FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&finalized)

	if got := issueDraftGroupIssueCount(t); got != 4 {
		t.Fatalf("the group has %d issues, want the root plus 3 children", got)
	}
	if len(finalized.Issues) != 4 {
		t.Fatalf("the response carries %d issues, want the whole group of 4", len(finalized.Issues))
	}
	root := finalized.Issues[0]
	if root.ID != finalized.IssueID {
		t.Fatalf("root row %s is not issue_id %s", root.ID, finalized.IssueID)
	}
	if root.ParentIssueID != nil {
		t.Fatalf("the root hangs off a parent: %v", *root.ParentIssueID)
	}
	// The payload's flat fields describe the root, and nothing assigns it: the
	// group's work is done by the children.
	if root.AssigneeID != nil || root.AssigneeType != nil {
		t.Fatalf("the root carries an assignee (%v/%v); confirming a group must not "+
			"start a run for the container", root.AssigneeType, root.AssigneeID)
	}

	var rootOrigin string
	dbfx.QueryRow(t, `SELECT origin_id FROM issue WHERE id = $1`, root.ID).Scan(&rootOrigin)
	if rootOrigin != session.SessionID {
		t.Fatalf("root origin_id = %s, want the chat session %s", rootOrigin, session.SessionID)
	}

	// Children: parented to the root, each with the origin its (session, key)
	// derives, and each carrying the stage the alignment settled on.
	wantStages := []int{1, 1, 2}
	wantTitles := []string{"stage one backend", "stage one frontend", "stage two verification"}
	wantKeys := []string{"c1", "c2", "c3"}
	seenOrigins := map[string]bool{}
	for i, child := range finalized.Issues[1:] {
		if child.ParentIssueID == nil || *child.ParentIssueID != root.ID {
			t.Fatalf("child %d is not parented to the root: %v", i, child.ParentIssueID)
		}
		if child.Stage == nil || int(*child.Stage) != wantStages[i] {
			t.Fatalf("child %d stage = %v, want %d", i, child.Stage, wantStages[i])
		}
		if child.Title != wantTitles[i] {
			t.Fatalf("child %d title = %q, want %q", i, child.Title, wantTitles[i])
		}
		var originID string
		dbfx.QueryRow(t, `SELECT origin_id FROM issue WHERE id = $1`, child.ID).Scan(&originID)
		want := uuidToString(issueDraftNodeID(util.MustParseUUID(session.SessionID), wantKeys[i]))
		if originID != want {
			t.Fatalf("child %d origin_id = %s, want the derived node id %s", i, originID, want)
		}
		if seenOrigins[originID] {
			t.Fatalf("child %d shares an origin_id; a group must never share one, "+
				"or GetIssueByOrigin (LIMIT 1) returns an arbitrary member", i)
		}
		seenOrigins[originID] = true
	}
}

// The confirmed group is the authority on what runs: stage 1 is started, later
// stages are parked in backlog with their assignee already bound. The server
// adds no branch for this — the payload's status IS the policy, and the
// existing backlog skip does the rest.
func TestFinalizeIssueDraftGroupStartsOnlyTheFirstStage(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	agentID := handlerTestAgentID(t)
	first := assignTo(draftChild("s1", "started now", "todo"), agentID)
	first["stage"] = 1
	parked := assignTo(draftChild("s2", "parked until promoted", "backlog"), agentID)
	parked["stage"] = 2

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready",
		draftGroupPayload("staged parent", first, parked))

	var finalized FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&finalized)

	statusByTitle := map[string]string{}
	for _, issue := range finalized.Issues {
		var status string
		dbfx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, issue.ID).Scan(&status)
		statusByTitle[issue.Title] = status
	}
	if statusByTitle["started now"] != "todo" {
		t.Fatalf("stage 1 child landed in %q, want todo", statusByTitle["started now"])
	}
	if statusByTitle["parked until promoted"] != "backlog" {
		t.Fatalf("stage 2 child landed in %q, want backlog so it does not run", statusByTitle["parked until promoted"])
	}

	queued := func(title string) int {
		return dbfx.Count(t, `
			SELECT COUNT(*) FROM agent_task_queue
			WHERE issue_id IN (SELECT id FROM issue WHERE workspace_id = $1 AND title = $2)
		`, testWorkspaceID, title)
	}
	if got := queued("started now"); got != 1 {
		t.Fatalf("stage 1 child queued %d tasks, want 1 — the backlog comparison below is "+
			"only meaningful if the started child really enqueued", got)
	}
	if got := queued("parked until promoted"); got != 0 {
		t.Fatalf("stage 2 child queued %d tasks; a parked stage must not start work", got)
	}
}

// A confirmed alignment answers with the same group every time it is confirmed.
func TestFinalizeIssueDraftGroupIsIdempotent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", draftGroupPayload("idempotent parent",
		draftChild("c1", "first child", "todo"),
		draftChild("c2", "second child", "todo"),
	))

	var first FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&first)

	var again FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&again)

	if again.IssueID != first.IssueID {
		t.Fatalf("retried confirm returned a different root: %s vs %s", again.IssueID, first.IssueID)
	}
	if len(again.Issues) != len(first.Issues) {
		t.Fatalf("retried confirm returned %d issues, want the same %d", len(again.Issues), len(first.Issues))
	}
	for i := range first.Issues {
		if again.Issues[i].ID != first.Issues[i].ID {
			t.Fatalf("retried confirm disagrees at position %d: %s vs %s",
				i, again.Issues[i].ID, first.Issues[i].ID)
		}
	}
	if got := issueDraftGroupIssueCount(t); got != 3 {
		t.Fatalf("two confirms produced %d issues, want 3", got)
	}
}

// The case revision cannot defend against: between being admitted and creating,
// a confirm's payload was replaced with a whole new set of child keys. Only the
// root's id — the chat session id — can stop the second confirm from building a
// second group.
func TestFinalizeIssueDraftGroupIsNotDuplicatedByReKeyedChildren(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", draftGroupPayload("rekey parent",
		draftChild("c1", "the child that was agreed", "todo"),
		draftChild("c2", "the other agreed child", "todo"),
	))

	var first FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&first)
	if len(first.Issues) != 3 {
		t.Fatalf("first confirm produced %d issues, want 3", len(first.Issues))
	}

	// A save that replaced every child key, at a revision the next confirm will
	// find perfectly acceptable. §3.3 timeline B.
	revision := reopenIssueDraft(t, session.SessionID, draftGroupPayload("rekey parent",
		draftChild("x1", "a child nobody agreed to", "todo"),
		draftChild("x2", "another child nobody agreed to", "todo"),
		draftChild("x3", "a third child nobody agreed to", "todo"),
	))

	var second FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, revision)).
		Want(http.StatusOK).JSON(&second)

	if second.IssueID != first.IssueID {
		t.Fatalf("the re-keyed confirm built a second group: root %s, want %s", second.IssueID, first.IssueID)
	}
	if len(second.Issues) != 3 {
		t.Fatalf("the re-keyed confirm answered with %d issues, want the first group's 3", len(second.Issues))
	}
	if got := issueDraftGroupIssueCount(t); got != 3 {
		t.Fatalf("the board holds %d issues from this alignment, want 3 — re-keying must "+
			"adopt the group that exists, never add to it", got)
	}
	for _, title := range []string{"a child nobody agreed to", "another child nobody agreed to", "a third child nobody agreed to"} {
		if got := dbfx.Count(t, `SELECT COUNT(*) FROM issue WHERE workspace_id = $1 AND title = $2`, testWorkspaceID, title); got != 0 {
			t.Fatalf("a re-keyed child was created anyway: %q", title)
		}
	}
}

// A confirm whose process died after the commit but before it recorded the
// group must recover into that group, not build a second one.
func TestFinalizeIssueDraftGroupAdoptsTheGroupAfterACrashedConfirm(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", draftGroupPayload("crash parent",
		draftChild("c1", "committed before the crash", "todo"),
	))

	var first FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&first)

	// Same payload, same revision, draft never completed: exactly the state a
	// process that died between step 2 and step 3 leaves behind.
	revision := reopenIssueDraft(t, session.SessionID,
		draftGroupPayload("crash parent", draftChild("c1", "committed before the crash", "todo")))

	var recovered FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, revision)).
		Want(http.StatusOK).JSON(&recovered)

	if recovered.IssueID != first.IssueID {
		t.Fatalf("recovery built a second group: root %s, want %s", recovered.IssueID, first.IssueID)
	}
	if len(recovered.Issues) != 2 {
		t.Fatalf("recovery answered with %d issues, want the group's 2", len(recovered.Issues))
	}
	if recovered.Draft.IssueID == nil || *recovered.Draft.IssueID != first.IssueID {
		t.Fatalf("recovery did not point the draft at the group: issue_id = %v", recovered.Draft.IssueID)
	}
	if got := issueDraftGroupIssueCount(t); got != 2 {
		t.Fatalf("recovery left %d issues, want 2", got)
	}
}

// Two tabs confirming the same revision at the same moment. Both must be told
// about the same group; the loser adopts, it does not create.
func TestFinalizeIssueDraftGroupConcurrentConfirmsCreateOneGroup(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", draftGroupPayload("concurrent parent",
		draftChild("c1", "concurrent child one", "todo"),
		draftChild("c2", "concurrent child two", "todo"),
	))

	const attempts = 4
	results := make([]FinalizeIssueDraftResponse, attempts)
	codes := make([]int, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res := testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision))
			codes[i] = res.Code
			if res.Code == http.StatusOK {
				res.JSON(&results[i])
			}
		}(i)
	}
	wg.Wait()

	if got := issueDraftGroupIssueCount(t); got != 3 {
		t.Fatalf("%d concurrent confirms produced %d issues, want exactly one group of 3", attempts, got)
	}
	root := ""
	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("concurrent confirm %d returned %d; every repeat of an accepted confirm must succeed", i, code)
		}
		if len(results[i].Issues) != 3 {
			t.Fatalf("concurrent confirm %d answered with %d issues, want the whole group of 3", i, len(results[i].Issues))
		}
		if root == "" {
			root = results[i].IssueID
			continue
		}
		if results[i].IssueID != root {
			t.Fatalf("concurrent confirms disagreed on the group: %s vs %s", root, results[i].IssueID)
		}
	}
}

// A payload is validated before anything is created, so a client mistake costs
// nothing. Each case here asserts both halves: the readable error, and an empty
// board.
func TestFinalizeIssueDraftGroupRejectsMalformedChildren(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	twentyOne := make([]map[string]any, 0, 21)
	for i := range 21 {
		twentyOne = append(twentyOne, draftChild(fmt.Sprintf("k%d", i), "child", "todo"))
	}

	cases := []struct {
		name    string
		payload map[string]any
		message string
	}{
		{
			name: "duplicate key",
			payload: draftGroupPayload("dup keys",
				draftChild("c1", "first", "todo"),
				draftChild("c1", "second", "todo")),
			message: "duplicate sub-issue key",
		},
		{
			// The keys are trimmed before anything else, so a key that only
			// differs by whitespace is the same node — and would derive the
			// same origin_id inside the group transaction.
			name: "keys that collide once trimmed",
			payload: draftGroupPayload("trimmed keys",
				draftChild("c1", "first", "todo"),
				draftChild("  c1  ", "second", "todo")),
			message: "duplicate sub-issue key",
		},
		{
			name: "missing key",
			payload: draftGroupPayload("no key",
				draftChild("  ", "unnamed", "todo")),
			message: "sub-issue key is required",
		},
		{
			name:    "too many children",
			payload: draftGroupPayload("too many", twentyOne...),
			message: "draft has too many sub-issues",
		},
		{
			name: "stage below one",
			payload: draftGroupPayload("stage zero",
				map[string]any{"key": "c1", "title": "zero stage", "status": "todo", "priority": "medium", "stage": 0}),
			message: "invalid sub-issue stage",
		},
		{
			name: "negative stage",
			payload: draftGroupPayload("stage negative",
				map[string]any{"key": "c1", "title": "negative stage", "status": "todo", "priority": "medium", "stage": -1}),
			message: "invalid sub-issue stage",
		},
		{
			name: "stage beyond the bound",
			payload: draftGroupPayload("stage high",
				map[string]any{"key": "c1", "title": "high stage", "status": "todo", "priority": "medium", "stage": 21}),
			message: "invalid sub-issue stage",
		},
		{
			name: "child without a title",
			payload: draftGroupPayload("child without title",
				draftChild("c1", "", "todo")),
			message: "draft title is required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cleanupIssueDraftGroup(t)

			session := startIssueDraftSession(t)
			saved := saveIssueDraft(t, session.SessionID, 0, "ready", tc.payload)

			res := testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
				Want(http.StatusBadRequest)
			if !strings.Contains(res.Body.String(), tc.message) {
				t.Fatalf("error body %q does not name the problem (%q)", res.Body.String(), tc.message)
			}
			if got := issueDraftGroupIssueCount(t); got != 0 {
				t.Fatalf("a rejected payload still created %d issues; validation has to happen "+
					"before the first insert, not inside the group transaction", got)
			}
		})
	}
}

// A group must fail whole when one node's assignee is not invocable by the
// caller. The check runs before the transaction, so nothing is created — this
// is the same door the ordinary create path closes.
func TestFinalizeIssueDraftGroupRejectsUnauthorizedChildAssignee(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	agentID := handlerTestAgentID(t)
	privateAgentID, _, _ := privateAgentTestFixture(t)

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", draftGroupPayload("unauthorized child",
		assignTo(draftChild("c1", "allowed child", "todo"), agentID),
		assignTo(draftChild("c2", "child the caller cannot dispatch", "todo"), privateAgentID),
	))

	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusForbidden)

	if got := issueDraftGroupIssueCount(t); got != 0 {
		t.Fatalf("a forbidden assignee on the last child still created %d issues; the group "+
			"transaction must never start", got)
	}
}

// The quota is per node and the group is atomic, so a group that cannot fit
// creates nothing. It is asserted through the handler because the error has to
// reach the client as 402, not as a generic failure.
func TestFinalizeIssueDraftGroupCreatesNothingWhenTheQuotaCannotCoverIt(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	// Room for two, asking for four.
	limit := dbfx.Count(t, `SELECT COUNT(*) FROM issue WHERE workspace_id = $1`, testWorkspaceID) + 2
	stub := entitlementtest.New()
	stub.Set(uuid.MustParse(testWorkspaceID), entitlement.GateIssueCount, entitlement.Decision{
		Gate:           entitlement.Gate{Action: entitlement.ActionEnforce, Limit: &limit},
		PolicyRevision: 34,
	})
	priorProvider := testHandler.IssueService.Entitlements
	testHandler.IssueService.Entitlements = stub
	t.Cleanup(func() { testHandler.IssueService.Entitlements = priorProvider })

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", draftGroupPayload("over quota parent",
		draftChild("c1", "quota child one", "todo"),
		draftChild("c2", "quota child two", "todo"),
		draftChild("c3", "quota child three", "todo"),
	))

	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusPaymentRequired)

	if got := issueDraftGroupIssueCount(t); got != 0 {
		t.Fatalf("a group that did not fit the quota created %d issues; a half-created group "+
			"is worse than none", got)
	}
}
