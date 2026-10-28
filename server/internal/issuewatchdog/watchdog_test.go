package issuewatchdog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestManagerReplyRequiresAssignedSignatureAndSubstance(t *testing.T) {
	tests := []struct {
		name    string
		content string
		manager string
		want    bool
	}{
		{"bracketed wrench", "[负责经理·扳手] 已安排", "CTO·扳手", true},
		{"Chinese bracketed wrench", "【负责经理·扳手】已安排", "CTO·扳手", true},
		{"plain wrench alias", "负责经理扳手：已安排", "CTO·扳手", true},
		{"other manager", "[负责经理·小喇叭] 已看", "CTO·扳手", false},
		{"signature not at beginning", "我看到了 [负责经理·扳手] 已安排", "CTO·扳手", false},
		{"bare received", "[负责经理·扳手] 收到。", "CTO·扳手", false},
		{"Chinese bracketed received", "【负责经理·扳手】收到。", "CTO·扳手", false},
		{"unmarked received", "收到！", "CTO·扳手", false},
		{"coo signature", "[COO·Grok] 我来处理", "COO·Grok", true},
		{"Chinese bracketed coo signature", "【COO·Grok】我来处理", "COO·Grok", true},
		{"missing signature", "已经处理", "CTO·扳手", false},
		{"generic Grok is not a signature", "Grok 已处理", "COO·Grok", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isManagerReply(tc.content, tc.manager); got != tc.want {
				t.Fatalf("isManagerReply(%q, %q) = %v, want %v", tc.content, tc.manager, got, tc.want)
			}
		})
	}
}

func TestEveryManagerHasAnAssignedSignature(t *testing.T) {
	tests := []struct {
		manager string
		content string
	}{
		{"CTO·扳手", "[负责经理扳手] 已处理"},
		{"内容总监·小喇叭", "[负责经理·小喇叭] 已处理"},
		{"选品经理·放大镜", "负责经理放大镜：已处理"},
		{"论文负责人·学霸", "[负责经理·学霸] 已处理"},
		{"COO·Grok", "[COO·Grok] 已处理"},
	}
	for _, tc := range tests {
		t.Run(tc.manager, func(t *testing.T) {
			if !isManagerReply(tc.content, tc.manager) {
				t.Fatalf("%q was not accepted for %q", tc.content, tc.manager)
			}
		})
	}
}

func TestConfigIsDisabledByDefaultAndRejectsInvalidThresholds(t *testing.T) {
	for _, name := range []string{
		"MULTICA_ISSUE_WATCHDOG_ENABLED", "MULTICA_ISSUE_WATCHDOG_WORKSPACE_ID",
		"MULTICA_ISSUE_WATCHDOG_WEBHOOK_URL", "MULTICA_ISSUE_WATCHDOG_WEBHOOK_KEY",
		"MULTICA_ISSUE_WATCHDOG_TICK", "MULTICA_ISSUE_WATCHDOG_TODO_DELAY",
		"MULTICA_ISSUE_WATCHDOG_IDLE_INTERVAL", "MULTICA_ISSUE_WATCHDOG_MANAGER_WAIT",
		"MULTICA_ISSUE_WATCHDOG_EMPLOYEE_ROUNDS", "MULTICA_ISSUE_WATCHDOG_HEARTBEAT_FILE",
		"MULTICA_ISSUE_WATCHDOG_RUN_AWARE", "MULTICA_ISSUE_WATCHDOG_SILENT_INTERVAL",
	} {
		t.Setenv(name, "")
	}
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled {
		t.Fatal("watchdog must default to disabled")
	}
	if cfg.RunAware {
		t.Fatal("run-aware reminders must default to off so a deploy alone keeps the old behavior")
	}
	if cfg.SilentInterval != 20*time.Minute {
		t.Fatalf("silent-run reminder interval = %s, want 20m", cfg.SilentInterval)
	}
	if cfg.TickInterval != time.Minute {
		t.Fatalf("watchdog scan interval = %s, want 1m", cfg.TickInterval)
	}
	if cfg.TodoStartDelay != 10*time.Minute {
		t.Fatalf("todo start delay = %s, want 10m", cfg.TodoStartDelay)
	}
	if cfg.IdleInterval != 30*time.Minute {
		t.Fatalf("idle reminder interval = %s, want 30m", cfg.IdleInterval)
	}
	if cfg.ManagerWait != 15*time.Minute {
		t.Fatalf("manager reply wait = %s, want 15m", cfg.ManagerWait)
	}

	t.Setenv("MULTICA_ISSUE_WATCHDOG_ENABLED", "true")
	t.Setenv("MULTICA_ISSUE_WATCHDOG_WORKSPACE_ID", "8b82babe-01a5-41f5-b726-622bd77aa839")
	t.Setenv("MULTICA_ISSUE_WATCHDOG_WEBHOOK_URL", "https://receiver.example.invalid/alerts")
	t.Setenv("MULTICA_ISSUE_WATCHDOG_WEBHOOK_KEY", "test-only")
	t.Setenv("MULTICA_ISSUE_WATCHDOG_IDLE_INTERVAL", "not-a-duration")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("invalid threshold was accepted")
	}
	t.Setenv("MULTICA_ISSUE_WATCHDOG_IDLE_INTERVAL", "")

	t.Setenv("MULTICA_ISSUE_WATCHDOG_RUN_AWARE", "true")
	t.Setenv("MULTICA_ISSUE_WATCHDOG_SILENT_INTERVAL", "25m")
	cfg, err = ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RunAware || cfg.SilentInterval != 25*time.Minute {
		t.Fatalf("run-aware config = %v / %s, want on / 25m", cfg.RunAware, cfg.SilentInterval)
	}
	t.Setenv("MULTICA_ISSUE_WATCHDOG_RUN_AWARE", "sometimes")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("non-boolean run-aware switch was accepted")
	}
	t.Setenv("MULTICA_ISSUE_WATCHDOG_RUN_AWARE", "true")
	t.Setenv("MULTICA_ISSUE_WATCHDOG_SILENT_INTERVAL", "0s")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("zero silent-run interval was accepted")
	}
}

func TestWriteHeartbeatReplacesFileWithUTCStamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "last-run")
	now := time.Date(2026, 10, 23, 12, 34, 56, 789, time.FixedZone("test", -5*60*60))
	if err := writeHeartbeat(path, now); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := now.UTC().Format(time.RFC3339Nano) + "\n"
	if string(got) != want {
		t.Fatalf("heartbeat = %q, want %q", got, want)
	}
	if err := writeHeartbeat(path, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != now.Add(time.Minute).UTC().Format(time.RFC3339Nano)+"\n" {
		t.Fatalf("heartbeat was not atomically refreshed: %q", got)
	}
}

func TestFirstScanUsesExistingProgressTimestamp(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDatabase(ctx, t)
	_, workspaceID, _, _, issueID := watchdogFixture(t, pool)
	base := time.Now().UTC().Add(time.Second).Truncate(time.Second)
	if _, err := pool.Exec(ctx, `
UPDATE issue SET status = 'in_progress', created_at = $2
WHERE id = $1::uuid`, issueID, base.Add(-45*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
DELETE FROM office_event
WHERE issue_id = $1::uuid AND event_type = 'issue.status_changed'`, issueID); err != nil {
		t.Fatal(err)
	}

	var nudges int
	w := New(pool, Config{
		Enabled: true, WorkspaceID: workspaceID, TickInterval: time.Minute,
		TodoStartDelay: 10 * time.Minute, IdleInterval: 30 * time.Minute,
		ManagerWait: 15 * time.Minute, MaxEmployeeRound: 2,
		HeartbeatFile: filepath.Join(t.TempDir(), "last-run"),
	}, func(context.Context, string, string, string, string) error {
		nudges++
		return nil
	})
	w.now = func() time.Time { return base }
	issue, err := w.loadIssue(ctx, workspaceID, issueID)
	if err != nil {
		t.Fatal(err)
	}
	if !issue.ProgressAt.Before(base.Add(-30 * time.Minute)) {
		t.Fatalf("fixture progress timestamp = %s, want older than %s", issue.ProgressAt, base.Add(-30*time.Minute))
	}
	if err := w.Run(ctx, base); err != nil {
		t.Fatal(err)
	}
	if nudges != 1 {
		t.Fatalf("first scan nudges = %d, want immediate reminder for 45-minute idle issue", nudges)
	}
}

func TestWebhookPostsOnceWithBearerKeyAndProject(t *testing.T) {
	const key = "fake-test-secret"
	received := make(chan webhookEvent, 1)
	requests := make(chan int, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- 1
		if got := r.Header.Get("Authorization"); got != "Bearer "+key {
			t.Errorf("authorization = %q", got)
		}
		var event webhookEvent
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		received <- event
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	w := New(nil, Config{WebhookURL: server.URL, WebhookKey: key}, nil)
	event := webhookEvent{
		EventID: "test-event", EventType: "employee_unresponsive",
		IssueID: "issue-1", IssueNumber: "TST-1", Title: "test title",
		Status: "in_progress", Employee: "Codex", Manager: "CTO·扳手",
		ProjectName: "Agent mono仓库管理", WakeTarget: "manager", Reason: "test",
	}
	if err := w.post(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	got := <-received
	if got.ProjectName != event.ProjectName || got.Manager != event.Manager || got.WakeTarget != "manager" {
		t.Fatalf("payload = %+v", got)
	}
	select {
	case <-requests:
	default:
		t.Fatal("webhook receiver did not see a request")
	}
}
