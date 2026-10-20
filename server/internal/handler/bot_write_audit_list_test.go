package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The bot-write audit (DENE-1936): real local sockets and a disposable
// Postgres; NOT an actual Grok endpoint.

func setMemberRole(t *testing.T, role string) {
	t.Helper()
	var old string
	dbfx.QueryRow(t, `SELECT role FROM member WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, testUserID).Scan(&old)
	dbfx.Exec(t, `UPDATE member SET role=$3 WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, testUserID, role)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `UPDATE member SET role=$3 WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, testUserID, old)
	})
}

func TestOnBehalfOfIsFilledFromTheBotKeyByTheDatabase(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	issue := dbfx.Issue(t, "On behalf of")
	var id string
	err := testPool.QueryRow(context.Background(),
		`INSERT INTO activity_log (workspace_id, issue_id, actor_type, actor_id, action, details, via_bot_id, via_bot_name)
		 VALUES ($1,$2,'member',$3,'issue.updated','{}',$4,'bot') RETURNING id`,
		testWorkspaceID, issue, testUserID, uuidToString(f.bot.ID)).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	var onBehalf, plain *string
	dbfx.QueryRow(t, `SELECT on_behalf_of_user_id::text FROM activity_log WHERE id=$1`, id).Scan(&onBehalf)
	if onBehalf == nil || *onBehalf != testUserID {
		t.Fatalf("on_behalf_of_user_id = %v, want the key's owner", onBehalf)
	}
	// A write that did not come through a Bot is not marked as one.
	var plainID string
	if err := testPool.QueryRow(context.Background(),
		`INSERT INTO activity_log (workspace_id, issue_id, actor_type, actor_id, action, details) VALUES ($1,$2,'member',$3,'issue.updated','{}') RETURNING id`,
		testWorkspaceID, issue, testUserID).Scan(&plainID); err != nil {
		t.Fatal(err)
	}
	dbfx.QueryRow(t, `SELECT on_behalf_of_user_id::text FROM activity_log WHERE id=$1`, plainID).Scan(&plain)
	if plain != nil {
		t.Fatalf("a person's own write was marked as on behalf of %s", *plain)
	}
}

func TestBotWriteListShowsAdminsEverythingAndOthersOnlyTheirOwn(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	other := summonSecondMember(t)
	ctx := context.Background()
	insert := func(owner string, route string) {
		t.Helper()
		_, err := testHandler.Queries.InsertBotWriteAudit(ctx, db.InsertBotWriteAuditParams{
			WorkspaceID: parseUUID(testWorkspaceID), BotID: f.bot.ID, BotName: "Fixture Bot", OnBehalfOfUserID: parseUUID(owner),
			Method: "PATCH", Route: route, Family: service.ClassifyWorkspaceRoute(route), Fields: []string{"title"},
			StatusCode: 200, Outcome: "done",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = testPool.Exec(ctx, `DELETE FROM bot_write_audit WHERE bot_id=$1`, f.bot.ID) })
	insert(testUserID, "/api/workspaces/{id}/projects/{projectId}")
	insert(other, "/api/workspaces/{id}/agents/{agentId}")

	router := chi.NewRouter()
	router.Use(middleware.Auth(testHandler.Queries, nil, nil, nil))
	router.Route("/api/workspaces/{id}", func(r chi.Router) {
		r.Use(middleware.RequireWorkspaceMemberFromURL(testHandler.Queries, "id"))
		r.Get("/bot-writes", testHandler.ListBotWriteAudit)
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	list := func(query string) []BotWriteAuditResponse {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+"/api/workspaces/"+testWorkspaceID+"/bot-writes"+query, nil)
		req.Header.Set("Authorization", "Bearer "+f.token)
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out struct {
			Writes []BotWriteAuditResponse `json:"writes"`
		}
		if res.StatusCode != 200 {
			t.Fatalf("status %d", res.StatusCode)
		}
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out.Writes
	}

	all := list("")
	if len(all) != 2 || !strings.HasPrefix(all[0].Label, "Bot〈Fixture Bot〉代表〈") {
		t.Fatalf("an owner should see both writes with the label: %+v", all)
	}
	if mine := list("?mine=1"); len(mine) != 1 || mine[0].OnBehalfOfUserID != testUserID {
		t.Fatalf("mine=1 = %+v", mine)
	}
	setMemberRole(t, "member")
	own := list("")
	if len(own) != 1 || own[0].OnBehalfOfUserID != testUserID {
		t.Fatalf("a plain member must only see writes made in their own name: %+v", own)
	}
}
