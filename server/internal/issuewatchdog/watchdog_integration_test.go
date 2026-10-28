package issuewatchdog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

// The three cases the office asked for (DENE-2667 PR 1): a live run that
// reported 5 minutes ago is left alone, a live run silent for 25 minutes gets a
// project-group reminder and no issue comment, and an assigned issue that never
// started still gets the issue-comment wake.
func TestRunAwareWatchdogRemindsGroupInsteadOfCommentingOnLiveRuns(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDatabase(ctx, t)
	fx, workspaceID, agentID, projectName, seedIssueID := watchdogFixture(t, pool)

	const fakeKey = "run-aware-test-key"
	receiver, received, _ := newFakeReceiver(t, fakeKey, http.StatusAccepted)
	defer receiver.Close()

	h := handler.New(db.New(pool), pool, realtime.NewHub(), events.New(), nil, nil, nil, analytics.NoopClient{}, handler.Config{})
	w := New(pool, Config{
		Enabled: true, WorkspaceID: workspaceID, WebhookURL: receiver.URL, WebhookKey: fakeKey,
		TickInterval: time.Minute, TodoStartDelay: 10 * time.Minute,
		IdleInterval: 30 * time.Minute, ManagerWait: 15 * time.Minute,
		MaxEmployeeRound: 2, HeartbeatFile: filepath.Join(t.TempDir(), "last-run"),
		RunAware: true, SilentInterval: 20 * time.Minute,
	}, h.CreateWatchdogNudgeComment)
	now := time.Now().UTC().Truncate(time.Second)
	w.now = func() time.Time { return now }

	var projectID, runtimeID string
	var properties []byte
	if err := pool.QueryRow(ctx, `SELECT project_id::text, properties FROM issue WHERE id = $1::uuid`,
		seedIssueID).Scan(&projectID, &properties); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT runtime_id::text FROM agent WHERE id = $1::uuid`,
		agentID).Scan(&runtimeID); err != nil {
		t.Fatal(err)
	}
	// The seed issue was assigned just now; keep it out of the scan.
	if _, err := pool.Exec(ctx, `UPDATE issue SET status = 'backlog' WHERE id = $1::uuid`, seedIssueID); err != nil {
		t.Fatal(err)
	}
	newIssue := func(title, status string, cols testutil.Cols) string {
		t.Helper()
		base := testutil.Cols{
			"workspace_id": workspaceID, "project_id": projectID, "properties": json.RawMessage(properties),
			"status": status, "assignee_type": "agent", "assignee_id": agentID,
			"created_at": now.Add(-2 * time.Hour), "updated_at": now.Add(-15 * time.Minute),
		}
		for k, v := range cols {
			base[k] = v
		}
		return fx.Issue(t, title, base)
	}
	liveRun := func(issueID string, lastReport time.Time) string {
		t.Helper()
		taskID := fx.Task(t, agentID, testutil.Cols{
			"issue_id": issueID, "runtime_id": runtimeID, "status": "running",
			"dispatched_at": now.Add(-40 * time.Minute), "started_at": now.Add(-40 * time.Minute),
		})
		fx.Comment(t, issueID, "still on it: tests are running", testutil.Cols{
			"author_type": "agent", "author_id": agentID, "created_at": lastReport,
		})
		return taskID
	}

	reportedID := newIssue("live run, reported 5m ago", "in_progress", testutil.Cols{"first_executed_at": now.Add(-40 * time.Minute)})
	liveRun(reportedID, now.Add(-5*time.Minute))
	silentID := newIssue("live run, silent 25m", "in_progress", testutil.Cols{"first_executed_at": now.Add(-40 * time.Minute)})
	silentTaskID := liveRun(silentID, now.Add(-25*time.Minute))
	notStartedID := newIssue("assigned, never started", "todo", nil)

	// The employee reported through the progress line 5m ago, then a manager
	// overwrote the issue's summary. The history still has the report.
	overwrittenID := newIssue("live run, progress overwritten by someone else", "in_progress",
		testutil.Cols{"first_executed_at": now.Add(-40 * time.Minute)})
	liveRun(overwrittenID, now.Add(-40*time.Minute))
	managerID := "01a17d60-0000-7000-8000-000000000001"
	if _, err := pool.Exec(ctx, `
INSERT INTO issue_progress (workspace_id, issue_id, text, source, author_type, author_id, created_at)
VALUES ($1::uuid, $2::uuid, '跑完单测，正在改接口', 'agent', 'agent', $3::uuid, $4),
       ($1::uuid, $2::uuid, '经理改写了摘要', 'member', 'member', $5::uuid, $6)`,
		workspaceID, overwrittenID, agentID, now.Add(-5*time.Minute), managerID, now.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE issue SET progress_text = '经理改写了摘要', progress_source = 'member',
    progress_author_type = 'member', progress_author_id = $2::uuid, progress_updated_at = $3
WHERE id = $1::uuid`, overwrittenID, managerID, now.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}

	// A bare acknowledgement 3m ago is not a report: the run has been silent
	// since its last substantive comment 25m ago.
	ackID := newIssue("live run, only acknowledged", "in_progress", testutil.Cols{"first_executed_at": now.Add(-40 * time.Minute)})
	ackTaskID := liveRun(ackID, now.Add(-25*time.Minute))
	fx.Comment(t, ackID, "收到。", testutil.Cols{
		"author_type": "agent", "author_id": agentID, "created_at": now.Add(-3 * time.Minute),
	})

	// The silent run's daemon reported a command in flight 30s ago; the
	// acknowledged run's last live report is 10m old, too stale to name
	// (DENE-2667 PR 4).
	liveStatus := func(taskID, issueID, command string, reportedAt time.Time) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
INSERT INTO task_live_status (task_id, workspace_id, issue_id, agent_id, tool, command,
    tools_in_flight, last_activity_at, reported_at)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'Bash', $5, 1, $6, $6)`,
			taskID, workspaceID, issueID, agentID, command, reportedAt); err != nil {
			t.Fatal(err)
		}
	}
	liveStatus(silentTaskID, silentID, "go test ./internal/... -count=1", now.Add(-30*time.Second))
	liveStatus(ackTaskID, ackID, "make build", now.Add(-10*time.Minute))

	watchdogComments := func(issueID string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `
SELECT count(*) FROM comment
WHERE issue_id = $1::uuid AND author_type = 'system' AND content LIKE '%office-issue-watchdog%'`,
			issueID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	eventsFor := func(issueID string) []webhookEvent {
		var out []webhookEvent
		for _, e := range received() {
			if e.IssueID == issueID {
				out = append(out, e)
			}
		}
		return out
	}

	if err := w.Run(ctx, now); err != nil {
		t.Fatalf("scan: %v", err)
	}

	if n, got := watchdogComments(reportedID), eventsFor(reportedID); n != 0 || len(got) != 0 {
		t.Fatalf("live run that reported 5m ago: comments=%d events=%+v, want none", n, got)
	}
	t.Log("live run, reported 5m ago: no comment, no group reminder")

	if n := watchdogComments(silentID); n != 0 {
		t.Fatalf("live run silent 25m got %d issue comments; a comment would be steered into the run", n)
	}
	silentEvents := eventsFor(silentID)
	if len(silentEvents) != 1 {
		t.Fatalf("live run silent 25m: events=%+v, want one group reminder", silentEvents)
	}
	if e := silentEvents[0]; e.EventType != RunningSilentEventType || e.WakeTarget != "project_group" ||
		e.TaskID != silentTaskID || e.SilentMinutes != 25 || e.ProjectName != projectName || e.Manager != "CTO·扳手" {
		t.Fatalf("group reminder = %+v", e)
	}
	if e := silentEvents[0]; e.CurrentCommand != "go test ./internal/... -count=1" ||
		!strings.HasSuffix(e.Reason, "；当前命令：go test ./internal/... -count=1") {
		t.Fatalf("group reminder does not name the command in flight: command=%q reason=%q", e.CurrentCommand, e.Reason)
	}
	t.Logf("live run, silent 25m: group reminder only (type=%s target=%s silent=%dm project=%q)",
		silentEvents[0].EventType, silentEvents[0].WakeTarget, silentEvents[0].SilentMinutes, silentEvents[0].ProjectName)

	if n, got := watchdogComments(overwrittenID), eventsFor(overwrittenID); n != 0 || len(got) != 0 {
		t.Fatalf("employee progress 5m ago, summary overwritten since: comments=%d events=%+v, want none", n, got)
	}
	t.Log("live run, own progress 5m ago under someone else's summary: no reminder")

	if got := eventsFor(ackID); watchdogComments(ackID) != 0 || len(got) != 1 || got[0].SilentMinutes != 25 {
		t.Fatalf("live run acknowledged 3m ago, last report 25m ago: events=%+v, want one reminder at 25 silent minutes", got)
	} else if got[0].CurrentCommand != "" || strings.Contains(got[0].Reason, "当前命令") {
		t.Fatalf("a 10m-old live report was named as the current command: %+v", got[0])
	}
	t.Log("live run, bare 收到。 3m ago: silence still counted from the 25m-old report")

	if n, got := watchdogComments(notStartedID), eventsFor(notStartedID); n != 1 || len(got) != 0 {
		t.Fatalf("never-started issue: comments=%d events=%+v, want one comment wake", n, got)
	}
	var queued int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM agent_task_queue WHERE issue_id = $1::uuid AND status IN ('queued', 'dispatched', 'deferred')`,
		notStartedID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued == 0 {
		t.Fatal("the never-started issue's comment wake did not queue a run")
	}
	t.Log("assigned, never started: issue-comment wake queued a run")

	// Within the interval the silent run is not reminded again; at the
	// interval it is, and still without an issue comment.
	if err := w.Run(ctx, now.Add(19*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := eventsFor(silentID); len(got) != 1 {
		t.Fatalf("reminded again 19m after the first reminder: %+v", got)
	}
	if err := w.Run(ctx, now.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := eventsFor(silentID); len(got) != 2 || got[1].SilentMinutes != 45 || got[1].CurrentCommand != "" {
		t.Fatalf("second reminder at +20m = %+v, want one more at 45 silent minutes, no stale command", got)
	}
	if n := watchdogComments(silentID); n != 0 {
		t.Fatalf("silent live run collected %d issue comments", n)
	}
	t.Log("silent run: second group reminder exactly 20m after the first, none in between")
}

func TestRunAwareWatchdogIsOffByDefault(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDatabase(ctx, t)
	fx, workspaceID, agentID, _, issueID := watchdogFixture(t, pool)
	var runtimeID string
	if err := pool.QueryRow(ctx, `SELECT runtime_id::text FROM agent WHERE id = $1::uuid`, agentID).Scan(&runtimeID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := pool.Exec(ctx, `
UPDATE issue SET status = 'in_progress', created_at = $2, first_executed_at = $3
WHERE id = $1::uuid`, issueID, now.Add(-2*time.Hour), now.Add(-40*time.Minute)); err != nil {
		t.Fatal(err)
	}
	fx.Task(t, agentID, testutil.Cols{
		"issue_id": issueID, "runtime_id": runtimeID, "status": "running", "started_at": now.Add(-40 * time.Minute),
	})
	var nudges int
	w := New(pool, Config{
		Enabled: true, WorkspaceID: workspaceID, WebhookURL: "http://127.0.0.1:1", WebhookKey: "unused",
		TickInterval: time.Minute, TodoStartDelay: 10 * time.Minute,
		IdleInterval: 30 * time.Minute, ManagerWait: 15 * time.Minute,
		MaxEmployeeRound: 2, HeartbeatFile: filepath.Join(t.TempDir(), "last-run"),
		SilentInterval: 20 * time.Minute,
	}, func(context.Context, string, string, string, string) error {
		nudges++
		return nil
	})
	// The status update above records a status change now, so the idle
	// interval counts from here.
	if err := w.Run(ctx, now.Add(31*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if nudges != 1 {
		t.Fatalf("with RunAware off, an idle in_progress issue was nudged %d times, want the old single comment wake", nudges)
	}
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
