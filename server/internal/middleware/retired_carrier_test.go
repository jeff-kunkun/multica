package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Requirement alignment is retired, but its hidden carrier agents
// (`agent.system_key = 'issue_draft:*'`) and their task tokens can outlive the
// feature. These tests pin that such a token is refused outright instead of
// falling through to an ordinary task token's write access.

// assertScope drives one request through the middleware with `token` and
// reports the status the downstream handler produced, plus the headers it saw.
// A status of http.StatusNoContent is the handler having run.
func assertScope(t *testing.T, queries *db.Queries, method, token string) (int, http.Header) {
	t.Helper()
	return assertScopePath(t, queries, method, "/api/issues", token)
}

// assertScopePath is assertScope for a specific path.
func assertScopePath(t *testing.T, queries *db.Queries, method, path, token string) (int, http.Header) {
	t.Helper()
	var seen http.Header
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	Auth(queries, nil, nil, nil)(next).ServeHTTP(rec, req)
	return rec.Code, seen
}

// scopeRig is one workspace with the two agents these tests compare, wired to
// the real query layer.
type scopeRig struct {
	queries     *db.Queries
	fixture     *testutil.Fixture
	workspaceID string
	userID      string
	runtimeID   string
	carrierID   string
	ordinaryID  string
}

// mintTaskToken queues a task for agentID and returns a live `mat_` token bound
// to it, exactly as the claim path would.
func (rig scopeRig) mintTaskToken(t *testing.T, agentID string, expires string) string {
	t.Helper()
	taskID := rig.fixture.Task(t, agentID, testutil.Cols{
		"runtime_id": rig.runtimeID,
		"status":     "running",
		"started_at": testutil.Raw("now()"),
	})
	raw := "mat_" + uuid.NewString()
	rig.fixture.Insert(t, "task_token", testutil.Cols{
		"token_hash":   auth.HashToken(raw),
		"task_id":      taskID,
		"agent_id":     agentID,
		"workspace_id": rig.workspaceID,
		"user_id":      rig.userID,
		"expires_at":   testutil.Raw(expires),
	})
	return raw
}

// scopeFixture builds the workspace, the runtime and the two agents the scope
// distinguishes: a leftover alignment carrier, and an ordinary agent doing
// ordinary task work.
func scopeFixture(t *testing.T) scopeRig {
	t.Helper()
	pool := openPool(t)
	t.Cleanup(pool.Close)

	fx := testutil.New(pool, "", "")
	userID := fx.User(t, "Task Scope Test", "task-scope-test@multica.ai")
	workspaceID := fx.Workspace(t, "Task Scope Test", "task-scope-test")
	runtimeID := fx.Runtime(t, "scope-test-runtime", testutil.Cols{
		"workspace_id": workspaceID,
		"owner_id":     userID,
	})

	carrierID := fx.Agent(t, ".multica-issue-draft-scope", runtimeID, testutil.Cols{
		"workspace_id": workspaceID,
		"owner_id":     userID,
		"kind":         "system",
		"system_key":   "issue_draft:scope-test-flow",
	})
	ordinaryID := fx.Agent(t, "scope-test-ordinary", runtimeID, testutil.Cols{
		"workspace_id": workspaceID,
		"owner_id":     userID,
	})
	return scopeRig{
		queries:     db.New(pool),
		fixture:     fx,
		workspaceID: workspaceID,
		userID:      userID,
		runtimeID:   runtimeID,
		carrierID:   carrierID,
		ordinaryID:  ordinaryID,
	}
}

func TestRetiredCarrierTaskTokenIsRefused(t *testing.T) {
	rig := scopeFixture(t)
	token := rig.mintTaskToken(t, rig.carrierID, "now() + interval '1 hour'")

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			status, seen := assertScope(t, rig.queries, method, token)
			if status != http.StatusForbidden {
				t.Fatalf("%s with a retired carrier token: status = %d, want %d", method, status, http.StatusForbidden)
			}
			if seen != nil {
				t.Fatal("downstream handler ran for a refused request")
			}
		})
	}
	status, _ := assertScopePath(t, rig.queries, http.MethodPost, "/api/upload-file", token)
	if status != http.StatusForbidden {
		t.Fatalf("upload with a retired carrier token: status = %d, want %d", status, http.StatusForbidden)
	}
}

// The refusal is narrow by construction: an ordinary task's token keeps every
// write it had.
func TestOrdinaryTaskTokenStillWrites(t *testing.T) {
	rig := scopeFixture(t)
	token := rig.mintTaskToken(t, rig.ordinaryID, "now() + interval '1 hour'")

	status, seen := assertScope(t, rig.queries, http.MethodPost, token)
	if status != http.StatusNoContent {
		t.Fatalf("POST with an ordinary task token: status = %d, want %d", status, http.StatusNoContent)
	}
	if got := seen.Get("X-Actor-Source"); got != "task_token" {
		t.Fatalf("X-Actor-Source = %q, want %q", got, "task_token")
	}
}

// system_key is a text column, so the prefix alone is not enough: a
// user-authored agent whose key happens to start with `issue_draft:` keeps its
// access. The kind check is what prevents the match.
func TestUserAuthoredAgentWithCarrierPrefixIsNotRefused(t *testing.T) {
	rig := scopeFixture(t)
	userAgentID := rig.fixture.Agent(t, "scope-test-user-agent", rig.runtimeID, testutil.Cols{
		"workspace_id": rig.workspaceID,
		"owner_id":     rig.userID,
		"kind":         "user",
		"system_key":   "issue_draft:not-a-carrier",
	})
	token := rig.mintTaskToken(t, userAgentID, "now() + interval '1 hour'")

	status, _ := assertScope(t, rig.queries, http.MethodPost, token)
	if status != http.StatusNoContent {
		t.Fatalf("POST with a user-authored agent token: status = %d, want %d", status, http.StatusNoContent)
	}
}

func TestIsRetiredAlignmentCarrier(t *testing.T) {
	text := func(value string) pgtype.Text { return pgtype.Text{String: value, Valid: true} }
	cases := []struct {
		name      string
		kind      string
		systemKey pgtype.Text
		want      bool
	}{
		{name: "hidden alignment carrier", kind: "system", systemKey: text("issue_draft:flow"), want: true},
		{name: "null system key", kind: "system", systemKey: pgtype.Text{}, want: false},
		{name: "empty system key", kind: "system", systemKey: text(""), want: false},
		{name: "agent builder carrier", kind: "system", systemKey: text("agent_builder:flow"), want: false},
		{name: "prefix without separator", kind: "system", systemKey: text("issue_draft"), want: false},
		{name: "user-authored agent with carrier-looking key", kind: "user", systemKey: text("issue_draft:flow"), want: false},
		{name: "empty kind", kind: "", systemKey: text("issue_draft:flow"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRetiredAlignmentCarrier(tc.kind, tc.systemKey); got != tc.want {
				t.Fatalf("isRetiredAlignmentCarrier(%q, %q) = %v, want %v", tc.kind, tc.systemKey.String, got, tc.want)
			}
		})
	}
}
