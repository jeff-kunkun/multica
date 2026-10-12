package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/botsource"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// The second review of DENE-1936: an owner's approval is for the exact content
// of the call (R1), and a PR event keeps the access of the tickets it is linked
// to and of its repository (S2). Real Postgres; NOT an actual Grok endpoint.

// R1: the reviewer's probe, inverted. The owner approves adding Alice as a
// member; the same approval does not let the Bot add Mallory as admin. The
// identical call still goes through, once.
func TestOwnerApprovalIsForTheExactMemberAndRole(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	// Every refused call opens a card, the spent-approval retry included, and
	// every call is audited; none may outlive the test into another's counts.
	var started string
	dbfx.QueryRow(t, `SELECT now()::text`).Scan(&started)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_ask WHERE workspace_id=$1 AND kind='owner_confirmation' AND created_at >= $2::timestamptz`, testWorkspaceID, started)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM bot_write_audit WHERE bot_id=$1`, f.bot.ID)
	})
	var forwarded []string
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			req.Header.Set("X-User-ID", testUserID)
			next.ServeHTTP(w, req.WithContext(botsource.WithContext(req.Context(), botsource.Source{ID: f.bot.ID, Name: "Review Bot"})))
		})
	})
	r.Use(middleware.BotWriteAudit(testHandler.Queries, nil, testHandler))
	r.Post("/api/workspaces/{id}/members", func(w http.ResponseWriter, req *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(req.Body).Decode(&body)
		forwarded = append(forwarded, body["email"]+":"+body["role"])
		w.WriteHeader(http.StatusOK)
	})
	path := "/api/workspaces/" + testWorkspaceID + "/members"
	call := func(body, confirmation string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if confirmation != "" {
			req.Header.Set(middleware.ConfirmationHeader, confirmation)
		}
		r.ServeHTTP(rec, req)
		return rec
	}
	type blocked struct {
		ConfirmationID string `json:"confirmation_id"`
		AskID          string `json:"ask_id"`
	}
	read := func(rec *httptest.ResponseRecorder) blocked {
		t.Helper()
		var b blocked
		_ = json.Unmarshal(rec.Body.Bytes(), &b)
		if rec.Code != http.StatusForbidden || b.ConfirmationID == "" || b.AskID == "" {
			t.Fatalf("want a 403 with a card: %d %s", rec.Code, rec.Body)
		}
		t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM agent_ask WHERE id=$1`, b.AskID) })
		return b
	}

	alice := `{"email":"alice@example.invalid","role":"member"}`
	first := read(call(alice, ""))
	var question string
	dbfx.QueryRow(t, `SELECT questions::text FROM agent_ask WHERE id=$1`, first.AskID).Scan(&question)
	if !strings.Contains(question, "alice@example.invalid") || !strings.Contains(question, "role=member") {
		t.Fatalf("the card must show who and which role is approved: %s", question)
	}
	if code := answerCardAs(t, first.AskID, testUserID, service.OwnerConfirmApprove, nil); code != http.StatusOK {
		t.Fatalf("answer %d", code)
	}

	swapped := call(`{"email":"mallory@example.invalid","role":"admin"}`, first.ConfirmationID)
	other := read(swapped)
	if len(forwarded) != 0 {
		t.Fatalf("Mallory/admin went through on Alice/member's approval: %v", forwarded)
	}
	if other.ConfirmationID == first.ConfirmationID {
		t.Fatal("the changed call must be a new request to the owner")
	}

	if rec := call(alice, first.ConfirmationID); rec.Code != http.StatusOK {
		t.Fatalf("the approved call itself: %d %s", rec.Code, rec.Body)
	}
	if rec := call(alice, first.ConfirmationID); rec.Code != http.StatusForbidden {
		t.Fatalf("a second use: %d", rec.Code)
	}
	if strings.Join(forwarded, ",") != "alice@example.invalid:member" {
		t.Fatalf("forwarded = %v", forwarded)
	}
}

// R1, fourth review: a role named twice in two letter cases means member in one
// order and admin in the other to the real member handler, yet sorts to one
// map. Such a body is refused outright, so no approval can stand for both.
func TestOwnerApprovalRefusesARoleNamedTwice(t *testing.T) {
	asMember := `{"email":"alice@example.invalid","Role":"admin","role":"member"}`
	asAdmin := `{"email":"alice@example.invalid","role":"member","Role":"admin"}`
	for body, want := range map[string]string{asMember: "member", asAdmin: "admin"} {
		var req CreateMemberRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil || req.Role != want {
			t.Fatalf("the hazard itself: %s decodes to %q, want %q", body, req.Role, want)
		}
	}
	f := newHumanNeededFixture(t, true)
	var started string
	dbfx.QueryRow(t, `SELECT now()::text`).Scan(&started)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_ask WHERE workspace_id=$1 AND kind='owner_confirmation' AND created_at >= $2::timestamptz`, testWorkspaceID, started)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM bot_write_audit WHERE bot_id=$1`, f.bot.ID)
	})
	forwarded := 0
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			req.Header.Set("X-User-ID", testUserID)
			next.ServeHTTP(w, req.WithContext(botsource.WithContext(req.Context(), botsource.Source{ID: f.bot.ID, Name: "Review Bot"})))
		})
	})
	r.Use(middleware.BotWriteAudit(testHandler.Queries, nil, testHandler))
	r.Post("/api/workspaces/{id}/members", func(w http.ResponseWriter, _ *http.Request) {
		forwarded++
		w.WriteHeader(http.StatusOK)
	})
	for _, body := range []string{asMember, asAdmin} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/members", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "owner_confirmation_unscannable") {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if forwarded != 0 {
		t.Fatalf("an ambiguous body reached the handler %d times", forwarded)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_ask WHERE workspace_id=$1 AND kind='owner_confirmation' AND created_at >= $2::timestamptz`, testWorkspaceID, started); n != 0 {
		t.Fatalf("an ambiguous body opened %d cards for the owner to approve", n)
	}
}

func prEvent(title, repo string, linked ...string) service.WorkspaceActivityInput {
	owner, name, _ := strings.Cut(repo, "/")
	in, _ := service.PullRequestActivity(parseUUID(testWorkspaceID), service.ActivityPRUpdated, "updated", map[string]any{
		"pull_request": map[string]any{
			"id": uuid.NewString(), "number": 7, "state": "open", "title": title,
			"repo_owner": owner, "repo_name": name, "html_url": "https://github.com/" + repo + "/pull/7",
		},
		"linked_issue_ids": linked,
	})
	in.Visible = testHandler.TargetVisibility()
	return in
}

func routePR(t *testing.T, in service.WorkspaceActivityInput) int {
	t.Helper()
	n, err := service.RouteWorkspaceActivity(context.Background(), testHandler.Queries, in)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// S2: a PR event is told only to a person who can open every ticket it is linked
// to and its repository; one linked to no ticket is told to nobody.
func TestPREventKeepsTheAccessOfItsTicketsAndRepository(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	setActivityScope(t, f, "owner_visible_all")
	setMemberRole(t, "member")
	peer := summonSecondMember(t)
	hidden := dbfx.Issue(t, "Private work", testutil.Cols{"visibility": "private", "creator_type": "member", "creator_id": peer})
	open := dbfx.Issue(t, "Open work", testutil.Cols{"visibility": "workspace"})

	t.Run("linked to a private ticket", func(t *testing.T) {
		if n := routePR(t, prEvent("Confidential acquisition work", "acme/app", hidden)); n != 0 {
			t.Fatalf("queued %d", n)
		}
	})
	t.Run("linked to an open and a private ticket", func(t *testing.T) {
		if n := routePR(t, prEvent("Mixed", "acme/app", open, hidden)); n != 0 {
			t.Fatalf("queued %d", n)
		}
	})
	t.Run("linked to no ticket", func(t *testing.T) {
		if n := routePR(t, prEvent("Unlinked", "acme/app")); n != 0 {
			t.Fatalf("queued %d", n)
		}
	})
	t.Run("in a restricted repository", func(t *testing.T) {
		var old string
		dbfx.QueryRow(t, `SELECT COALESCE(repos::text, '[]') FROM workspace WHERE id=$1`, testWorkspaceID).Scan(&old)
		t.Cleanup(func() {
			_, _ = testPool.Exec(context.Background(), `UPDATE workspace SET repos=$2::jsonb WHERE id=$1`, testWorkspaceID, old)
		})
		repos, _ := json.Marshal([]map[string]string{{"url": "https://github.com/secret-org/vault", "visibility": "private", "created_by": peer}})
		dbfx.Exec(t, `UPDATE workspace SET repos=$2::jsonb WHERE id=$1`, testWorkspaceID, string(repos))
		if n := routePR(t, prEvent("Vault change", "secret-org/vault", open)); n != 0 {
			t.Fatalf("queued %d", n)
		}
		if n := routePR(t, prEvent("Elsewhere", "acme/app", open)); n != 1 {
			t.Fatalf("a PR of an unrestricted repository on an open ticket queued %d", n)
		}
	})

	t.Run("access lost after queueing", func(t *testing.T) {
		call := activityCaller(t, f)
		listed := func() int {
			t.Helper()
			code, data := call("GET", "/activity")
			var out struct {
				Events []map[string]any `json:"events"`
			}
			if code != 200 || json.Unmarshal(data, &out) != nil {
				t.Fatalf("list: %d %s", code, data)
			}
			return len(out.Events)
		}
		goes := dbfx.Issue(t, "Goes private", testutil.Cols{"visibility": "workspace"})
		dbfx.Exec(t, `DELETE FROM human_needed_event WHERE bot_id=$1`, f.bot.ID)
		if n := routePR(t, prEvent("Soon hidden", "acme/app", open, goes)); n != 1 {
			t.Fatalf("queued %d", n)
		}
		if listed() != 1 {
			t.Fatal("the PR row should be listed while every linked ticket is open")
		}
		dbfx.Exec(t, `UPDATE issue SET visibility='private', creator_type='member', creator_id=$2 WHERE id=$1`, goes, peer)
		if listed() != 0 {
			t.Fatal("the pull list served a PR whose second ticket went private")
		}
		before := f.calls.Load()
		f.worker.ProcessNext(context.Background())
		if f.calls.Load() != before {
			t.Fatal("a PR whose second ticket went private was pushed")
		}
	})
}
