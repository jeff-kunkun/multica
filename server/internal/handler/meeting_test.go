package handler

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func insertTestWorkspaceBot(t *testing.T, workspaceID string) {
	t.Helper()
	dbfx.Insert(t, "workspace_bot", testutil.Cols{
		"workspace_id": workspaceID,
		"user_id":      uuid.NewString(),
		"name":         "PM Bot",
		"created_by":   testUserID,
	})
}

// Unread belongs to the Bot: once a workspace has a project-manager Bot, only
// mentions written by a member light the unread counts.
func TestUnreadInboxCountsOnlyMemberMentionsWhenWorkspaceHasBot(t *testing.T) {
	workspaceID := dbfx.Workspace(t, "Bot unread", "bot-unread-"+uuid.NewString())
	dbfx.Member(t, workspaceID, testUserID, "owner")
	dbfx.Cleanup(t, `DELETE FROM inbox_item WHERE workspace_id = $1`, workspaceID)

	base := time.Now().UTC().Add(-time.Hour)
	item := func(issueID, typ, actorType string, at time.Time) {
		dbfx.Insert(t, "inbox_item", testutil.Cols{
			"workspace_id":   workspaceID,
			"recipient_type": "member",
			"recipient_id":   testUserID,
			"type":           typ,
			"severity":       "info",
			"issue_id":       issueID,
			"title":          typ,
			"actor_type":     actorType,
			"actor_id":       uuid.NewString(),
			"created_at":     at,
		})
	}
	issue := func(title string) string {
		return dbfx.Issue(t, title, testutil.Cols{"workspace_id": workspaceID})
	}
	item(issue("status"), "status_changed", "agent", base)
	item(issue("member mention"), "mentioned", "member", base)
	item(issue("agent mention"), "mentioned", "agent", base)
	// An older member mention under a newer status change on the same issue:
	// with a Bot the status change does not qualify, so the mention is the
	// issue's newest counted item and keeps it unread.
	mixed := issue("mixed")
	item(mixed, "mentioned", "member", base)
	item(mixed, "status_changed", "agent", base.Add(time.Minute))

	count := func() int64 {
		t.Helper()
		var out struct {
			Count int64 `json:"count"`
		}
		testutil.Call(t, inboxWorkspaceHandler(testHandler.CountUnreadInbox),
			inboxRequest(http.MethodGet, "/api/inbox/unread-count", workspaceID)).
			Want(http.StatusOK).JSON(&out)
		return out.Count
	}
	summary := func() int64 {
		t.Helper()
		var rows []InboxWorkspaceUnreadResponse
		testutil.Call(t, testHandler.UnreadInboxSummary,
			inboxRequest(http.MethodGet, "/api/inbox/unread-summary", workspaceID)).
			Want(http.StatusOK).JSON(&rows)
		for _, row := range rows {
			if row.WorkspaceID == workspaceID {
				return row.Count
			}
		}
		return 0
	}

	if got := count(); got != 5 {
		t.Fatalf("no bot: unread count = %d, want 5", got)
	}
	if got := summary(); got != 4 {
		t.Fatalf("no bot: unread summary = %d, want 4", got)
	}

	insertTestWorkspaceBot(t, workspaceID)
	if got := count(); got != 2 {
		t.Fatalf("with bot: unread count = %d, want 2", got)
	}
	if got := summary(); got != 2 {
		t.Fatalf("with bot: unread summary = %d, want 2", got)
	}
}

func TestChatUnreadIsZeroWhenWorkspaceHasBot(t *testing.T) {
	agentID := createHandlerTestAgent(t, "ChatBotUnreadAgent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	dbfx.Exec(t, `INSERT INTO chat_message (chat_session_id, role, content) VALUES ($1, 'assistant', 'unread reply')`, sessionID)
	dbfx.Exec(t, `UPDATE chat_session SET last_read_at = 'epoch' WHERE id = $1`, sessionID)

	unreadOf := func(path string) (int, bool) {
		t.Helper()
		var resp []ChatSessionResponse
		testutil.Call(t, testHandler.ListChatSessions, withChatTestWorkspaceCtx(t, newRequest(http.MethodGet, path, nil))).
			Want(http.StatusOK).JSON(&resp)
		for _, s := range resp {
			if s.ID == sessionID {
				return s.UnreadCount, s.HasUnread
			}
		}
		t.Fatalf("session %s not in %s", sessionID, path)
		return 0, false
	}

	for _, path := range []string{"/api/chat/sessions", "/api/chat/sessions?status=all"} {
		if n, has := unreadOf(path); n != 1 || !has {
			t.Fatalf("no bot %s: unread = %d/%v, want 1/true", path, n, has)
		}
	}
	insertTestWorkspaceBot(t, testWorkspaceID)
	for _, path := range []string{"/api/chat/sessions", "/api/chat/sessions?status=all"} {
		if n, has := unreadOf(path); n != 0 || has {
			t.Fatalf("with bot %s: unread = %d/%v, want 0/false", path, n, has)
		}
	}
}

func TestCreateIssueMeetingStampsOrigin(t *testing.T) {
	var meeting IssueResponse
	testutil.Call(t, testHandler.CreateIssue, newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":   "meeting " + t.Name(),
		"meeting": true,
	})).Want(http.StatusCreated).JSON(&meeting)
	dbfx.Cleanup(t, `DELETE FROM issue WHERE id = $1`, meeting.ID)
	if meeting.OriginType == nil || *meeting.OriginType != "meeting" {
		t.Fatalf("origin_type = %v, want meeting", meeting.OriginType)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue WHERE id = $1 AND origin_type = 'meeting' AND origin_id IS NULL`, meeting.ID); n != 1 {
		t.Fatalf("stored meeting origin rows = %d, want 1", n)
	}

	var plain IssueResponse
	testutil.Call(t, testHandler.CreateIssue, newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": "plain " + t.Name(),
	})).Want(http.StatusCreated).JSON(&plain)
	dbfx.Cleanup(t, `DELETE FROM issue WHERE id = $1`, plain.ID)
	if plain.OriginType != nil {
		t.Fatalf("plain issue origin_type = %v, want none", *plain.OriginType)
	}
}

func TestListMeetings(t *testing.T) {
	// A meeting with no plan yet.
	var meeting IssueResponse
	testutil.Call(t, testHandler.CreateIssue, newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":   "meeting " + t.Name(),
		"meeting": true,
	})).Want(http.StatusCreated).JSON(&meeting)
	dbfx.Cleanup(t, `DELETE FROM issue WHERE id = $1`, meeting.ID)

	// An ordinary issue with neither origin nor plan stays out.
	var plain IssueResponse
	testutil.Call(t, testHandler.CreateIssue, newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": "plain " + t.Name(),
	})).Want(http.StatusCreated).JSON(&plain)
	dbfx.Cleanup(t, `DELETE FROM issue WHERE id = $1`, plain.ID)

	// An ordinary issue with an approved plan whose first step is done.
	s := newPlanTestSetup(t)
	proposePlan(t, s.issueID, s.proposeBody(), http.StatusCreated)
	var approved struct {
		Issues []IssueResponse `json:"issues"`
	}
	testutil.Call(t, testHandler.ApprovePlan, approveRequest(s.issueID, 1)).Want(http.StatusOK).JSON(&approved)
	if len(approved.Issues) != 2 {
		t.Fatalf("approval created %d step issues, want 2", len(approved.Issues))
	}
	dbfx.Exec(t, `UPDATE issue SET status = 'done' WHERE id = $1`, approved.Issues[0].ID)
	// A newer proposal after approval is the latest plan; progress still
	// reads the approved version.
	proposePlan(t, s.issueID, s.proposeBody(), http.StatusCreated)

	var out struct {
		Meetings []MeetingResponse `json:"meetings"`
	}
	testutil.Call(t, testHandler.ListMeetings, newRequest(http.MethodGet, "/api/meetings", nil)).
		Want(http.StatusOK).JSON(&out)

	byID := map[string]MeetingResponse{}
	order := map[string]int{}
	for i, m := range out.Meetings {
		byID[m.Issue.ID] = m
		order[m.Issue.ID] = i
	}
	if _, ok := byID[plain.ID]; ok {
		t.Fatal("plain issue without plan listed as a meeting")
	}
	m, ok := byID[meeting.ID]
	if !ok {
		t.Fatal("meeting-origin issue missing")
	}
	if m.Plan != nil || m.StepsTotal != 0 || m.StepsDone != 0 {
		t.Fatalf("planless meeting = plan %v steps %d/%d", m.Plan, m.StepsDone, m.StepsTotal)
	}
	if m.Issue.OriginType == nil || *m.Issue.OriginType != "meeting" || m.Issue.Identifier == "" {
		t.Fatalf("meeting issue = %+v", m.Issue)
	}
	p, ok := byID[s.issueID]
	if !ok {
		t.Fatal("issue with a plan missing")
	}
	if p.Plan == nil || p.Plan.Version != 2 || p.Plan.Status != "proposed" {
		t.Fatalf("latest plan = %+v, want v2 proposed", p.Plan)
	}
	if p.StepsTotal != 2 || p.StepsDone != 1 {
		t.Fatalf("steps = %d/%d, want 1/2", p.StepsDone, p.StepsTotal)
	}
	// The plan issue had the newest activity (its latest plan).
	if order[s.issueID] > order[meeting.ID] {
		t.Fatalf("plan issue listed after the older meeting: %v", order)
	}
}
