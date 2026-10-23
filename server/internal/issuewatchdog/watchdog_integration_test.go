package issuewatchdog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestWatchdogEmployeeWakeAndFifteenMinuteCOOEscalation(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDatabase(ctx, t)
	fx, workspaceID, agentID, projectName, issueID := watchdogFixture(t, pool)

	const fakeKey = "fake-receiver-key"
	receiver, received, _ := newFakeReceiver(t, fakeKey, http.StatusAccepted)
	defer receiver.Close()

	bus := events.New()
	h := handler.New(db.New(pool), pool, realtime.NewHub(), bus, nil, nil, nil, analytics.NoopClient{}, handler.Config{})
	cfg := Config{
		Enabled: true, WorkspaceID: workspaceID, WebhookURL: receiver.URL, WebhookKey: fakeKey,
		TickInterval: time.Minute, TodoStartDelay: 10 * time.Minute,
		IdleInterval: 30 * time.Minute, ManagerWait: 15 * time.Minute,
		MaxEmployeeRound: 2, HeartbeatFile: filepath.Join(t.TempDir(), "watchdog", "last-run"),
	}
	w := New(pool, cfg, h.CreateWatchdogNudgeComment)
	base := time.Now().UTC().Add(time.Second).Truncate(time.Second)
	current := base
	w.now = func() time.Time { return current }
	if issue, err := w.loadIssue(ctx, workspaceID, issueID); err != nil {
		t.Fatalf("load watchdog issue: %v", err)
	} else if issue.ManagerName != "CTO·扳手" || issue.ProjectName != projectName {
		t.Fatalf("loaded project/manager = %q / %q", issue.ProjectName, issue.ManagerName)
	}

	if err := w.Run(ctx, base); err != nil {
		t.Fatalf("initial baseline run: %v", err)
	}
	current = base.Add(11 * time.Minute)
	issue, err := w.loadIssue(ctx, workspaceID, issueID)
	if err != nil {
		t.Fatalf("reload issue before first reminder: %v", err)
	}
	if err := w.checkIssue(ctx, issue, current); err != nil {
		t.Fatalf("first employee reminder: %v", err)
	}

	var commentCount, taskCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM comment
WHERE issue_id = $1::uuid AND author_type = 'system'
  AND content LIKE '%mention://agent/%' AND content LIKE '%office-issue-watchdog%'`,
		issueID).Scan(&commentCount); err != nil {
		t.Fatal(err)
	}
	if commentCount != 1 {
		t.Fatalf("issue-local employee reminder comments = %d, want 1", commentCount)
	}
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM agent_task_queue
WHERE issue_id = $1::uuid AND status IN ('queued', 'dispatched', 'running')`,
		issueID).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if taskCount == 0 {
		t.Fatal("the @mention created a comment but did not wake the assigned agent")
	}
	t.Logf("at +11m, the issue has one @employee reminder and %d queued agent run(s)", taskCount)

	fx.Comment(t, issueID, "I am still working", testutil.Cols{
		"author_type": "agent", "author_id": agentID,
	})
	commentID := ""
	if err := pool.QueryRow(ctx, `
SELECT id::text FROM comment
WHERE issue_id = $1::uuid AND author_type = 'agent'
ORDER BY created_at DESC LIMIT 1`, issueID).Scan(&commentID); err != nil {
		t.Fatal(err)
	}
	w.HandleEvent(events.Event{
		Type: protocol.EventCommentCreated, WorkspaceID: workspaceID, ActorType: "agent", ActorID: agentID,
		Payload: map[string]any{"comment": map[string]any{
			"id": commentID, "issue_id": issueID, "author_type": "agent", "author_id": agentID,
			"content": "I am still working", "type": "comment",
		}},
	})
	current = base.Add(25*time.Minute + 59*time.Second)
	if err := w.Run(ctx, current); err != nil {
		t.Fatalf("pre-timeout scan: %v", err)
	}
	if gotEvents := received(); len(gotEvents) != 1 {
		t.Fatalf("COO was alerted before the 15-minute manager window elapsed: %+v", gotEvents)
	}
	t.Log("at +14m59s after the manager alert, no COO escalation was sent")
	current = base.Add(26 * time.Minute)
	if err := w.Run(ctx, current); err != nil {
		t.Fatalf("15-minute manager timeout scan: %v", err)
	}

	gotEvents := received()
	if len(gotEvents) != 2 {
		t.Fatalf("fake receiver got %d events, want manager alert and one COO escalation: %+v", len(gotEvents), gotEvents)
	}
	if gotEvents[0].EventType != "employee_comment" || gotEvents[0].WakeTarget != "manager" {
		t.Fatalf("first event = %+v, want employee-comment manager alert", gotEvents[0])
	}
	if gotEvents[1].EventType != "manager_timeout" || gotEvents[1].WakeTarget != "COO·Grok" {
		t.Fatalf("second event = %+v, want COO escalation", gotEvents[1])
	}
	for _, event := range gotEvents {
		if event.ProjectName != projectName || event.Manager != "CTO·扳手" || event.IssueID != issueID {
			t.Fatalf("webhook omitted project, manager, or issue identity: %+v", event)
		}
		t.Logf("fake receiver: type=%s target=%s issue=%s project=%q manager=%q",
			event.EventType, event.WakeTarget, event.IssueNumber, event.ProjectName, event.Manager)
	}
	if err := w.Run(ctx, current.Add(time.Minute)); err != nil {
		t.Fatalf("dedupe run: %v", err)
	}
	if len(received()) != 2 {
		t.Fatalf("repeated scan resent an event; got %d webhook calls", len(received()))
	}
	t.Log("at +15m after the manager alert, COO received exactly one escalation; repeat scan sent no duplicate")
}

func TestMissingManagerPropertyFallsBackToWrench(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDatabase(ctx, t)
	_, workspaceID, _, _, issueID := watchdogFixture(t, pool)
	if _, err := pool.Exec(ctx, `UPDATE issue SET properties = '{}'::jsonb WHERE id = $1::uuid`, issueID); err != nil {
		t.Fatal(err)
	}
	w := New(pool, Config{Enabled: true, WorkspaceID: workspaceID}, nil)
	issue, err := w.loadIssue(ctx, workspaceID, issueID)
	if err != nil {
		t.Fatal(err)
	}
	if issue.ManagerName != "CTO·扳手" {
		t.Fatalf("empty manager property resolved to %q, want CTO·扳手", issue.ManagerName)
	}
}

func TestWatchdogTwoIdleRoundsEscalateManagerOnlyOnce(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDatabase(ctx, t)
	_, workspaceID, _, projectName, issueID := watchdogFixture(t, pool)
	if _, err := pool.Exec(ctx, `UPDATE issue SET status = 'in_progress' WHERE id = $1::uuid`, issueID); err != nil {
		t.Fatal(err)
	}

	const fakeKey = "idle-round-test-key"
	receiver, received, _ := newFakeReceiver(t, fakeKey, http.StatusAccepted)
	defer receiver.Close()

	cfg := Config{
		Enabled: true, WorkspaceID: workspaceID, WebhookURL: receiver.URL, WebhookKey: fakeKey,
		TickInterval: time.Minute, TodoStartDelay: 10 * time.Minute,
		IdleInterval: 30 * time.Minute, ManagerWait: 15 * time.Minute,
		MaxEmployeeRound: 2, HeartbeatFile: filepath.Join(t.TempDir(), "last-run"),
	}
	var nudgeRounds int
	w := New(pool, cfg, func(context.Context, string, string, string, string) error {
		nudgeRounds++
		return nil
	})
	base := time.Now().UTC().Add(time.Second).Truncate(time.Second)
	w.now = func() time.Time { return base }

	if err := w.Run(ctx, base); err != nil {
		t.Fatal(err)
	}
	if err := w.Run(ctx, base.Add(31*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := w.Run(ctx, base.Add(61*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if nudgeRounds != 2 {
		t.Fatalf("employee wake rounds = %d, want 2", nudgeRounds)
	}
	t.Log("an idle in_progress issue woke its employee at +30m and +60m")
	gotEvents := received()
	if len(gotEvents) != 1 || gotEvents[0].EventType != "employee_unresponsive" ||
		gotEvents[0].WakeTarget != "manager" || gotEvents[0].ProjectName != projectName {
		t.Fatalf("manager escalation events = %+v", gotEvents)
	}
	if err := w.Run(ctx, base.Add(90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	gotEvents = received()
	if len(gotEvents) != 2 || gotEvents[1].EventType != "manager_timeout" ||
		gotEvents[1].WakeTarget != "COO·Grok" || nudgeRounds != 2 {
		t.Fatalf("two-round cap or 15-minute COO escalation failed: events=%+v nudges=%d", gotEvents, nudgeRounds)
	}
	t.Log("after two unanswered employee wake rounds, the manager was alerted once and COO escalation followed")
}

func TestSignedManagerReplyResolvesWaitAndStatusTransitionNotifiesOnce(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDatabase(ctx, t)
	_, workspaceID, _, projectName, issueID := watchdogFixture(t, pool)
	if _, err := pool.Exec(ctx, `UPDATE issue SET status = 'in_review' WHERE id = $1::uuid`, issueID); err != nil {
		t.Fatal(err)
	}
	const fakeKey = "status-manager-test-key"
	receiver, received, _ := newFakeReceiver(t, fakeKey, http.StatusAccepted)
	defer receiver.Close()
	cfg := Config{
		Enabled: true, WorkspaceID: workspaceID, WebhookURL: receiver.URL, WebhookKey: fakeKey,
		TickInterval: time.Minute, TodoStartDelay: 10 * time.Minute,
		IdleInterval: 30 * time.Minute, ManagerWait: 15 * time.Minute,
		MaxEmployeeRound: 2, HeartbeatFile: filepath.Join(t.TempDir(), "last-run"),
	}
	w := New(pool, cfg, nil)
	base := time.Now().UTC().Add(time.Second).Truncate(time.Second)
	current := base
	w.now = func() time.Time { return current }
	busEvent := events.Event{
		Type: protocol.EventIssueUpdated, WorkspaceID: workspaceID,
		Payload: map[string]any{
			"status_changed": true,
			"issue":          map[string]any{"id": issueID},
		},
	}
	w.HandleEvent(busEvent)
	w.HandleEvent(busEvent)
	if got := received(); len(got) != 1 || got[0].EventType != "status_in_review" || got[0].ProjectName != projectName {
		t.Fatalf("status event calls = %+v", got)
	}
	for _, status := range []string{"blocked", "done"} {
		if _, err := pool.Exec(ctx, `UPDATE issue SET status = $2 WHERE id = $1::uuid`, issueID, status); err != nil {
			t.Fatal(err)
		}
		w.HandleEvent(busEvent)
		w.HandleEvent(busEvent)
	}
	gotStatusEvents := received()
	if len(gotStatusEvents) != 3 || gotStatusEvents[1].EventType != "status_blocked" ||
		gotStatusEvents[2].EventType != "status_done" {
		t.Fatalf("status notifications should fire once for review, blocked, and done: %+v", gotStatusEvents)
	}
	t.Log("in_review, blocked, and done each produced one manager webhook event")

	memberID := testUserIDForWorkspace(t, pool, workspaceID)
	fx := testutil.New(pool, workspaceID, "")
	postManagerComment := func(content string) {
		t.Helper()
		comment := map[string]any{
			"id": uuid.NewString(), "issue_id": issueID, "author_type": "member",
			"author_id": memberID, "type": "comment", "content": content,
		}
		commentID := fx.Insert(t, "comment", testutil.Cols{
			"id": comment["id"], "issue_id": issueID, "workspace_id": workspaceID,
			"author_type": "member", "author_id": memberID, "content": content, "type": "comment",
		})
		comment["id"] = commentID
		w.HandleEvent(events.Event{
			Type: protocol.EventCommentCreated, WorkspaceID: workspaceID, ActorType: "member",
			Payload: map[string]any{"comment": comment},
		})
	}
	postManagerComment("收到")
	current = base.Add(14*time.Minute + 59*time.Second)
	if err := w.Run(ctx, current); err != nil {
		t.Fatal(err)
	}
	if got := received(); len(got) != 3 {
		t.Fatalf("plain acknowledgment incorrectly resolved the manager wait: %+v", got)
	}
	t.Log("a bare received acknowledgment did not resolve the manager wait")
	postManagerComment("【负责经理·扳手】已安排处理")
	current = base.Add(15 * time.Minute)
	if err := w.Run(ctx, current); err != nil {
		t.Fatal(err)
	}
	got := received()
	if len(got) != 3 {
		t.Fatalf("valid manager reply did not resolve the wait: webhook calls=%+v", got)
	}
	t.Log("a signed, substantive manager reply before the deadline prevented COO escalation")
}

func TestWebhookFailureIsNotRetriedForTheSameEvent(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDatabase(ctx, t)
	_, workspaceID, _, _, issueID := watchdogFixture(t, pool)
	const fakeKey = "failed-attempt-test-key"
	receiver, _, callCount := newFakeReceiver(t, fakeKey, http.StatusServiceUnavailable)
	defer receiver.Close()
	w := New(pool, Config{
		Enabled: true, WorkspaceID: workspaceID, WebhookURL: receiver.URL, WebhookKey: fakeKey,
		TickInterval: time.Minute, TodoStartDelay: 10 * time.Minute,
		IdleInterval: 30 * time.Minute, ManagerWait: 15 * time.Minute,
		MaxEmployeeRound: 2, HeartbeatFile: filepath.Join(t.TempDir(), "last-run"),
	}, nil)
	issue, err := w.loadIssue(ctx, workspaceID, issueID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := w.notifyManager(ctx, issue, "status_done", "same-failing-event", "test failure", "manager", now); err == nil {
		t.Fatal("non-success response was accepted")
	}
	if err := w.notifyManager(ctx, issue, "status_done", "same-failing-event", "test failure", "manager", now); err != nil {
		t.Fatalf("deduped repeat should be a no-op, got %v", err)
	}
	if got := callCount(); got != 1 {
		t.Fatalf("failed event made %d HTTP attempts, want exactly one", got)
	}
	var outcome string
	if err := pool.QueryRow(ctx, `
SELECT outcome FROM office_issue_watchdog_attempt
WHERE issue_id = $1::uuid AND event_key = 'same-failing-event'`, issueID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "failed" {
		t.Fatalf("failed attempt outcome = %q", outcome)
	}
	t.Logf("failed webhook outcome=%s; repeat event produced no second HTTP attempt", outcome)
}

func newFakeReceiver(t *testing.T, key string, status int) (*httptest.Server, func() []webhookEvent, func() int) {
	t.Helper()
	var (
		mu        sync.Mutex
		delivered []webhookEvent
		calls     int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+key {
			t.Errorf("Authorization header = %q", got)
		}
		var event webhookEvent
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Errorf("decode fake receiver payload: %v", err)
		}
		mu.Lock()
		calls++
		delivered = append(delivered, event)
		mu.Unlock()
		w.WriteHeader(status)
	}))
	events := func() []webhookEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]webhookEvent(nil), delivered...)
	}
	callCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
	return server, events, callCount
}

func watchdogFixture(t *testing.T, pool *pgxpool.Pool) (*testutil.Fixture, string, string, string, string) {
	t.Helper()
	fx := testutil.New(pool, "", "")
	userID := fx.Insert(t, "user", testutil.Cols{
		"name": "Watchdog Test", "email": "watchdog-" + uuid.NewString() + "@example.invalid",
	})
	workspaceID := fx.Insert(t, "workspace", testutil.Cols{
		"name": "Issue Watchdog Test", "slug": "issue-watchdog-" + uuid.NewString(), "issue_prefix": "TST",
	})
	fx.WorkspaceID, fx.UserID = workspaceID, userID
	fx.Member(t, workspaceID, userID, "owner")
	runtimeID := fx.Runtime(t, "Watchdog test runtime")
	agentID := fx.Agent(t, "Watchdog employee", runtimeID)
	projectName := "Watchdog project " + uuid.NewString()
	projectID := fx.Project(t, projectName)
	optionID := uuid.NewString()
	propertyID := fx.Insert(t, "issue_property", testutil.Cols{
		"workspace_id": workspaceID, "name": "负责经理", "type": "select",
		"config": json.RawMessage(fmt.Sprintf(`{"options":[{"id":%q,"name":"CTO·扳手"}]}`, optionID)),
	})
	properties, err := json.Marshal(map[string]string{propertyID: optionID})
	if err != nil {
		t.Fatal(err)
	}
	issueID := fx.Issue(t, "Watchdog full cycle", testutil.Cols{
		"workspace_id": workspaceID, "project_id": projectID,
		"status": "todo", "assignee_type": "agent", "assignee_id": agentID,
		"properties": properties,
	})
	return fx, workspaceID, agentID, projectName, issueID
}

func testUserIDForWorkspace(t *testing.T, pool *pgxpool.Pool, workspaceID string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `
SELECT user_id::text FROM member WHERE workspace_id = $1::uuid LIMIT 1`, workspaceID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
