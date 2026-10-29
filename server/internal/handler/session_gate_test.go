package handler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// DENE-1331: the cold-and-thick gate in its four quadrants, plus the missing
// size that must never trip it.
func TestSessionColdAndThick(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	thin, thick := int64(40_000), int64(180_000)
	hot, cold := now.Add(-10*time.Minute), now.Add(-3*time.Hour)
	cases := []struct {
		name   string
		ended  time.Time
		tokens *int64
		want   bool
	}{
		{"hot thin", hot, &thin, false},
		{"hot thick", hot, &thick, false},
		{"cold thin", cold, &thin, false},
		{"cold thick", cold, &thick, true},
		{"cold, size unknown", cold, nil, false},
		{"end unknown", time.Time{}, &thick, false},
	}
	for _, tc := range cases {
		if got := sessionColdAndThick(tc.ended, tc.tokens, now); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The gate at the claim: a cold, thick Claude session is set aside — no
// session id, the working directory kept, the old session's last report handed
// over — and every other case resumes as before. Codex reports its size too,
// but the gate reads Claude's only, so Codex's default is unchanged.
func TestClaimTask_ColdThickSessionStartsFresh(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cases := []struct {
		name      string
		provider  string
		endedAgo  string
		tokens    any // nil: no usage row carries the size
		wantFresh bool
	}{
		{"hot thin", "claude", "10 minutes", int64(40_000), false},
		{"hot thick", "claude", "10 minutes", int64(180_000), false},
		{"cold thin", "claude", "3 hours", int64(40_000), false},
		{"cold thick", "claude", "3 hours", int64(180_000), true},
		{"cold, size missing", "claude", "3 hours", nil, false},
		{"Codex cold thick", "codex", "3 hours", int64(180_000), false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			agentID, runtimeID, daemonID := createRuntimeGuardAgent(t, ctx)
			issueID := dbfx.Issue(t, "cold thick "+tc.name, testutil.Cols{"status": "in_progress", "number": 91331 + i})
			var priorID string
			dbfx.QueryRow(t, `
				INSERT INTO agent_task_queue (
					agent_id, runtime_id, issue_id, status, priority,
					started_at, completed_at, session_id, work_dir, result
				)
				VALUES ($1, $2, $3, 'completed', 0,
				        now() - interval '`+tc.endedAgo+`' - interval '5 minutes', now() - interval '`+tc.endedAgo+`',
				        'gate-session', '/tmp/gate-workdir', '{"output":"last report"}')
				RETURNING id
			`, agentID, runtimeID, issueID).Scan(&priorID)
			dbfx.Exec(t, `INSERT INTO task_usage (task_id, provider, model, last_context_tokens) VALUES ($1, $2, 'model-x', $3)`, priorID, tc.provider, tc.tokens)
			commentID := dbfx.Comment(t, issueID, "continue the prior issue session")
			dbfx.Exec(t, `INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, trigger_comment_id, status, priority) VALUES ($1, $2, $3, $4, 'queued', 0)`, agentID, runtimeID, issueID, commentID)

			task := claimTaskForRuntimeGuard(t, runtimeID, daemonID)
			if task.PriorWorkDir != "/tmp/gate-workdir" {
				t.Fatalf("PriorWorkDir = %q, want the old directory kept", task.PriorWorkDir)
			}
			if tc.wantFresh {
				if task.PriorSessionID != "" {
					t.Fatalf("PriorSessionID = %q, want a new session", task.PriorSessionID)
				}
				if h := task.SessionHandoff; h == nil || h.Reason != sessionHandoffCold || h.LastOutput != "last report" {
					t.Fatalf("SessionHandoff = %+v, want cold with the last report", h)
				}
				return
			}
			if task.PriorSessionID != "gate-session" || task.SessionHandoff != nil {
				t.Fatalf("PriorSessionID = %q, SessionHandoff = %+v; want gate-session resumed", task.PriorSessionID, task.SessionHandoff)
			}
		})
	}
}

func TestClipHandoffOutput(t *testing.T) {
	if got := clipHandoffOutput("  done  "); got != "done" {
		t.Fatalf("short report = %q", got)
	}
	long := strings.Repeat("好", sessionHandoffOutputRunes+50)
	got := clipHandoffOutput(long)
	if !strings.HasSuffix(got, "…") || len([]rune(got)) != sessionHandoffOutputRunes+1 {
		t.Fatalf("long report kept %d runes", len([]rune(got)))
	}
}
