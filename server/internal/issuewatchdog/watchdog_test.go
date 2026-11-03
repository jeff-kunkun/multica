package issuewatchdog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
		{"基建管家·扫帚", "【负责经理·扫帚】已处理"},
		{"负责人·手柄", "【负责经理·手柄】已处理"},
	}
	for _, tc := range tests {
		t.Run(tc.manager, func(t *testing.T) {
			if !isManagerReply(tc.content, tc.manager) {
				t.Fatalf("%q was not accepted for %q", tc.content, tc.manager)
			}
		})
	}
}

func TestConfigHasOnlyOneDecisionEngine(t *testing.T) {
	t.Setenv("MULTICA_ISSUE_WATCHDOG_ENABLED", "")
	t.Setenv("MULTICA_ISSUE_WATCHDOG_TICK", "")
	t.Setenv("MULTICA_ISSUE_WATCHDOG_STOP_ROUTING", "true")
	t.Setenv("MULTICA_ISSUE_WATCHDOG_REVIEW_WAKE", "true")
	t.Setenv("MULTICA_ISSUE_WATCHDOG_EMPLOYEE_ROUNDS", "2")
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled || cfg.StopRouting || cfg.ReviewWake || cfg.NoRunWait != 30*time.Minute {
		t.Fatalf("unexpected active config: %+v", cfg)
	}
	t.Setenv("MULTICA_ISSUE_WATCHDOG_ENABLED", "true")
	t.Setenv("MULTICA_ISSUE_WATCHDOG_WORKSPACE_ID", "11111111-2222-3333-4444-555555555555")
	t.Setenv("MULTICA_ISSUE_WATCHDOG_OWNERS_FILE", "")
	if _, err = ConfigFromEnv(); err == nil {
		t.Fatal("missing source accepted")
	}
	path := filepath.Join(t.TempDir(), "owners.json")
	if err = os.WriteFile(path, []byte(`{"project_managers":{"project":"负责人"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTICA_ISSUE_WATCHDOG_OWNERS_FILE", path)
	cfg, err = ConfigFromEnv()
	if err != nil || !cfg.Enabled {
		t.Fatalf("outbox must need no webhook or key: %v", err)
	}
	t.Setenv("MULTICA_ISSUE_WATCHDOG_TICK", "0s")
	if _, err = ConfigFromEnv(); err == nil {
		t.Fatal("zero tick accepted")
	}
}

func TestOwnersAndQuietStates(t *testing.T) {
	table := OwnerTable{ProjectManagers: map[string]string{"p": "项目负责人", "legacy": "COO·Grok"}, Unowned: []string{"legacy"}, Managers: []string{"明确负责人"}}
	for _, tc := range []struct {
		explicit, project, want string
		missing                 bool
	}{
		{"明确负责人", "p", "明确负责人", false}, {"", "p", "项目负责人", false},
		{"COO·Grok", "legacy", registrationOwner, true}, {"", "", registrationOwner, true},
		{"", "unknown", registrationOwner, true},
		{"未知名字", "p", registrationOwner, true},
	} {
		got, missing := table.Resolve(tc.explicit, tc.project)
		if got != tc.want || missing != tc.missing {
			t.Fatalf("route %+v = %s,%v", tc, got, missing)
		}
	}
	for _, issue := range []Issue{{Status: "todo"}, {Status: "backlog"}, {Status: "done"}, {Status: "cancelled"},
		{Status: "blocked", KunLabel: true}, {Status: "in_progress", Title: "待 Kun｜确认"},
		{Status: "in_review", WaitingKun: true}} {
		if stallEligible(issue) {
			t.Fatalf("quiet case eligible: %+v", issue)
		}
	}
	if !stallEligible(Issue{Status: "in_review"}) {
		t.Fatal("review without a run must be checked")
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
