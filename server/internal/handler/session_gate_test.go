package handler

import (
	"context"
	"net/http"
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

// The gate at the claim: a cold, thick session is set aside — no session id,
// the working directory kept — and every other quadrant resumes as before.
func TestClaimTask_ColdThickSessionStartsFresh(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cases := []struct {
		name      string
		endedAgo  string
		tokens    any // nil: no usage row carries the size
		wantFresh bool
	}{
		{"hot thin", "10 minutes", int64(40_000), false},
		{"hot thick", "10 minutes", int64(180_000), false},
		{"cold thin", "3 hours", int64(40_000), false},
		{"cold thick", "3 hours", int64(180_000), true},
		{"cold, size missing", "3 hours", nil, false},
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
					started_at, completed_at, session_id, work_dir
				)
				VALUES ($1, $2, $3, 'completed', 0,
				        now() - interval '`+tc.endedAgo+`' - interval '5 minutes', now() - interval '`+tc.endedAgo+`',
				        'gate-session', '/tmp/gate-workdir')
				RETURNING id
			`, agentID, runtimeID, issueID).Scan(&priorID)
			if tc.tokens != nil {
				dbfx.Exec(t, `INSERT INTO task_usage (task_id, provider, model, last_context_tokens) VALUES ($1, 'claude', 'claude-opus-5', $2)`, priorID, tc.tokens)
			} else {
				dbfx.Exec(t, `INSERT INTO task_usage (task_id, provider, model) VALUES ($1, 'claude', 'claude-opus-5')`, priorID)
			}
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
				return
			}
			if task.PriorSessionID != "gate-session" {
				t.Fatalf("PriorSessionID = %q, want gate-session resumed", task.PriorSessionID)
			}
		})
	}
}

func TestSessionResumeMaxContextTokens(t *testing.T) {
	for settings, want := range map[string]int64{
		``:         0,
		`{}`:       0,
		`not json`: 0,
		`{"session_resume_max_context_tokens":150000}`:  150_000,
		`{"session_resume_max_context_tokens":0}`:       0,
		`{"session_resume_max_context_tokens":-1}`:      0,
		`{"session_resume_max_context_tokens":2000001}`: 0,
		`{"session_resume_max_context_tokens":"5"}`:     0,
		`{"session_resume_max_context_tokens":2.5}`:     0,
	} {
		if got := sessionResumeMaxContextTokens([]byte(settings)); got != want {
			t.Errorf("sessionResumeMaxContextTokens(%s) = %d, want %d", settings, got, want)
		}
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

// DENE-2756: a follow-up resumes the last session unless the workspace set a
// context limit and the session's latest run, compaction included, ended past
// it; then a new session starts with the old directory and last report. The
// limit reads every runtime that reports a size, while the cold gate still
// reads Claude's only, so Codex's default is unchanged.
func TestClaimTask_SessionContextLimit(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	var stored []byte
	dbfx.QueryRow(t, `SELECT settings FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&stored)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `UPDATE workspace SET settings = $1 WHERE id = $2`, stored, testWorkspaceID)
	})
	cases := []struct {
		name        string
		limit       any // nil: never set
		provider    string
		olderTokens any // the session's earlier run
		tokens      any // the session's latest run; nil: no size reported
		endedAgo    string
		wantFresh   bool
	}{
		{"off by default", nil, "claude", nil, int64(500_000), "10 minutes", false},
		{"off at 0", 0, "codex", nil, int64(500_000), "10 minutes", false},
		{"below the limit", 150_000, "claude", nil, int64(120_000), "10 minutes", false},
		{"compacted below the limit", 150_000, "codex", int64(400_000), int64(90_000), "10 minutes", false},
		{"size missing", 150_000, "codex", nil, nil, "10 minutes", false},
		{"Claude past the limit", 150_000, "claude", nil, int64(180_000), "10 minutes", true},
		{"Codex past the limit", 150_000, "codex", int64(90_000), int64(180_000), "10 minutes", true},
		{"Codex cold and thick, limit off", nil, "codex", nil, int64(180_000), "3 hours", false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.limit == nil {
				dbfx.Exec(t, `UPDATE workspace SET settings = settings - 'session_resume_max_context_tokens' WHERE id = $1`, testWorkspaceID)
			} else {
				dbfx.Exec(t, `UPDATE workspace SET settings = jsonb_set(settings, '{session_resume_max_context_tokens}', to_jsonb($1::int)) WHERE id = $2`, tc.limit, testWorkspaceID)
			}
			agentID, runtimeID, daemonID := createRuntimeGuardAgent(t, ctx)
			issueID := dbfx.Issue(t, "context limit "+tc.name, testutil.Cols{"status": "in_progress", "number": 92756 + i})
			for n, size := range []any{tc.olderTokens, tc.tokens} {
				var runID string
				dbfx.QueryRow(t, `
					INSERT INTO agent_task_queue (
						agent_id, runtime_id, issue_id, status, priority,
						started_at, completed_at, session_id, work_dir, result
					)
					VALUES ($1, $2, $3, 'completed', 0,
					        now() - interval '`+tc.endedAgo+`' - make_interval(mins => 20 - 10 * $4::int),
					        now() - interval '`+tc.endedAgo+`' - make_interval(mins => 15 - 15 * $4::int),
					        'limit-session', '/tmp/limit-workdir', jsonb_build_object('output', 'report ' || $4::int::text))
					RETURNING id
				`, agentID, runtimeID, issueID, n).Scan(&runID)
				dbfx.Exec(t, `INSERT INTO task_usage (task_id, provider, model, last_context_tokens) VALUES ($1, $2, 'model-x', $3)`, runID, tc.provider, size)
			}
			commentID := dbfx.Comment(t, issueID, "one more follow-up")
			dbfx.Exec(t, `INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, trigger_comment_id, status, priority) VALUES ($1, $2, $3, $4, 'queued', 0)`, agentID, runtimeID, issueID, commentID)

			task := claimTaskForRuntimeGuard(t, runtimeID, daemonID)
			if task.PriorWorkDir != "/tmp/limit-workdir" {
				t.Fatalf("PriorWorkDir = %q, want the old directory kept", task.PriorWorkDir)
			}
			if !tc.wantFresh {
				if task.PriorSessionID != "limit-session" || task.SessionHandoff != nil {
					t.Fatalf("PriorSessionID = %q, SessionHandoff = %+v; want limit-session resumed", task.PriorSessionID, task.SessionHandoff)
				}
				return
			}
			if task.PriorSessionID != "" {
				t.Fatalf("PriorSessionID = %q, want a new session", task.PriorSessionID)
			}
			h := task.SessionHandoff
			if h == nil || h.Reason != sessionHandoffContextLimit || h.ContextTokens != tc.tokens.(int64) || h.LastOutput != "report 1" {
				t.Fatalf("SessionHandoff = %+v, want context_limit at %v with the latest report", h, tc.tokens)
			}
		})
	}
}

func TestWorkspaceSessionResumeEndpoint(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	var stored []byte
	dbfx.QueryRow(t, `SELECT settings FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&stored)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `UPDATE workspace SET settings = $1 WHERE id = $2`, stored, testWorkspaceID)
	})
	dbfx.Exec(t, `UPDATE workspace SET settings = settings - 'session_resume_max_context_tokens' WHERE id = $1`, testWorkspaceID)

	path := "/api/workspaces/" + testWorkspaceID + "/session-resume"
	get := func() int64 {
		t.Helper()
		var out workspaceSessionResumeResponse
		testutil.Call(t, testHandler.GetWorkspaceSessionResume, withURLParam(newRequest(http.MethodGet, path, nil), "id", testWorkspaceID)).Want(http.StatusOK).JSON(&out)
		return out.MaxContextTokens
	}
	put := func(body any, mutate func(*http.Request)) *http.Request {
		req := withURLParam(newRequest(http.MethodPut, path, body), "id", testWorkspaceID)
		if mutate != nil {
			mutate(req)
		}
		return req
	}

	if got := get(); got != 0 {
		t.Fatalf("unset limit reads %d, want 0 (off)", got)
	}
	for _, body := range []any{
		map[string]any{},
		map[string]any{"max_context_tokens": -1},
		map[string]any{"max_context_tokens": 2_000_001},
		map[string]any{"max_context_tokens": "150000"},
		map[string]any{"max_context_tokens": 1.5},
	} {
		testutil.Call(t, testHandler.UpdateWorkspaceSessionResume, put(body, nil)).Want(http.StatusBadRequest)
	}
	testutil.Call(t, testHandler.UpdateWorkspaceSessionResume, put(map[string]any{"max_context_tokens": 150_000}, func(r *http.Request) {
		r.Header.Set("X-Actor-Source", "task_token")
	})).Want(http.StatusForbidden)
	if got := get(); got != 0 {
		t.Fatalf("refused writes changed the limit to %d", got)
	}

	testutil.Call(t, testHandler.UpdateWorkspaceSessionResume, put(map[string]any{"max_context_tokens": 150_000}, nil)).Want(http.StatusOK)
	if got := get(); got != 150_000 {
		t.Fatalf("limit = %d after PUT 150000", got)
	}
	// The generic settings write carries the stored limit forward whatever it sends.
	req := withURLParam(newRequest(http.MethodPut, "/api/workspaces/"+testWorkspaceID, map[string]any{
		"settings": map[string]any{"session_resume_max_context_tokens": 50},
	}), "id", testWorkspaceID)
	testutil.Call(t, testHandler.UpdateWorkspace, req).Want(http.StatusOK)
	if got := get(); got != 150_000 {
		t.Fatalf("generic settings write changed the limit to %d", got)
	}
	testutil.Call(t, testHandler.UpdateWorkspaceSessionResume, put(map[string]any{"max_context_tokens": 0}, nil)).Want(http.StatusOK)
	if got := get(); got != 0 {
		t.Fatalf("limit = %d after PUT 0", got)
	}
}
