package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The workspace-wide stream (DENE-1936): real local sockets and a disposable
// Postgres; NOT an actual Grok endpoint.

func routeWorkspace(t *testing.T, in service.WorkspaceActivityInput) int {
	t.Helper()
	in.WorkspaceID = parseUUID(testWorkspaceID)
	if in.ActivityType == "" {
		in.ActivityType = service.ActivityWorkspaceWrite
	}
	n, err := service.RouteWorkspaceActivity(context.Background(), testHandler.Queries, in)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func setMemberRole(t *testing.T, role string) {
	t.Helper()
	var old string
	dbfx.QueryRow(t, `SELECT role FROM member WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, testUserID).Scan(&old)
	dbfx.Exec(t, `UPDATE member SET role=$3 WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, testUserID, role)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `UPDATE member SET role=$3 WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, testUserID, old)
	})
}

func projectEvent(actorName string) service.WorkspaceActivityInput {
	return service.WorkspaceActivityInput{
		DedupeKey: "ws:test:" + actorName + time.Now().Format("150405.000000000"),
		Body: service.WorkspaceEventBody{
			Family: service.FamilyProject, Action: "update", Fields: []string{"title"}, ActorKind: "member",
			ActorName: actorName, Route: "PATCH /api/workspaces/{id}/projects/{projectId}",
		},
		ActorType: "member",
	}
}

func TestWorkspaceStreamNeedsTheWholeWorkspaceScope(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	other := summonSecondMember(t)
	in := projectEvent("Peer")
	in.ActorID = parseUUID(other)
	for _, scope := range [][]string{{}, {"owned"}, {"owned", "followed"}} {
		setActivityScope(t, f, scope...)
		in.DedupeKey += "x"
		if n := routeWorkspace(t, in); n != 0 {
			t.Fatalf("scope %v still got %d workspace events", scope, n)
		}
	}
	setActivityScope(t, f, "owner_visible_all")
	in.DedupeKey += "y"
	if n := routeWorkspace(t, in); n != 1 {
		t.Fatalf("the whole-workspace scope queued %d", n)
	}
	// Not confirmed yet: nothing.
	dbfx.Exec(t, `UPDATE member_bot_binding SET verified_at=NULL WHERE id=$1`, f.binding)
	in.DedupeKey += "z"
	if n := routeWorkspace(t, in); n != 0 {
		t.Fatalf("an unconfirmed Bot got %d events", n)
	}
}

func TestWorkspaceStreamSkipsThePersonsOwnDoingsAndTheirBot(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	setActivityScope(t, f, "owner_visible_all")

	own := projectEvent("Me")
	own.ActorID = parseUUID(testUserID)
	if n := routeWorkspace(t, own); n != 0 {
		t.Fatalf("the person's own change was pushed to their Bot (%d)", n)
	}

	viaOwnBot := projectEvent("Bot")
	viaOwnBot.Body.ActorKind = "bot"
	viaOwnBot.Body.ViaBotID = uuidToString(f.bot.ID)
	viaOwnBot.Body.OnBehalfOfUserID = testUserID
	viaOwnBot.ActorID = parseUUID(testUserID)
	if n := routeWorkspace(t, viaOwnBot); n != 0 {
		t.Fatalf("the Bot was told about its own write (%d)", n)
	}

	peer := projectEvent("Peer")
	peer.ActorID = parseUUID(summonSecondMember(t))
	if n := routeWorkspace(t, peer); n != 1 {
		t.Fatalf("someone else's change queued %d", n)
	}
}

func TestWorkspaceStreamHidesManagementFromPlainMembers(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	setActivityScope(t, f, "owner_visible_all")
	setMemberRole(t, "member")

	billing := projectEvent("Peer")
	billing.Body.Family = service.FamilyBilling
	billing.ActorID = parseUUID(summonSecondMember(t))
	if n := routeWorkspace(t, billing); n != 0 {
		t.Fatalf("a member's Bot was sent billing news (%d)", n)
	}
	project := projectEvent("Peer2")
	project.ActorID = billing.ActorID
	if n := routeWorkspace(t, project); n != 1 {
		t.Fatalf("a member's Bot should still see project changes, got %d", n)
	}
}

func TestWorkspaceStreamIsReDecidedAtSendTime(t *testing.T) {
	cases := []struct {
		name   string
		family string
		change func(t *testing.T, f *humanNeededFixture)
	}{
		{"scope narrowed", service.FamilyProject, func(t *testing.T, f *humanNeededFixture) { setActivityScope(t, f, "owned") }},
		{"role dropped below the family", service.FamilyBilling, func(t *testing.T, _ *humanNeededFixture) { setMemberRole(t, "member") }},
		{"binding revoked", service.FamilyProject, func(t *testing.T, f *humanNeededFixture) {
			dbfx.Exec(t, `UPDATE member_bot_binding SET revoked_at=now() WHERE id=$1`, f.binding)
		}},
		{"no longer a member", service.FamilyProject, func(t *testing.T, _ *humanNeededFixture) { setMemberRole(t, "guest") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newHumanNeededFixture(t, true)
			setActivityScope(t, f, "owner_visible_all")
			in := projectEvent("Peer" + c.name)
			in.Body.Family = c.family
			in.ActorID = parseUUID(summonSecondMember(t))
			if n := routeWorkspace(t, in); n != 1 {
				t.Fatalf("queued %d", n)
			}
			c.change(t, f)
			before := f.calls.Load()
			f.worker.ProcessNext(context.Background())
			if f.calls.Load() != before {
				t.Fatal("a row no longer covered was pushed")
			}
		})
	}

	t.Run("still covered is pushed", func(t *testing.T) {
		f := newHumanNeededFixture(t, true)
		setActivityScope(t, f, "owner_visible_all")
		in := projectEvent("PeerOK")
		in.ActorID = parseUUID(summonSecondMember(t))
		routeWorkspace(t, in)
		f.worker.ProcessNext(context.Background())
		if f.calls.Load() != 1 {
			t.Fatalf("calls %d", f.calls.Load())
		}
	})
}

// The payload scan: whatever rode into the event, the body that reaches the Bot
// carries no credential, and no field value.
func TestWorkspaceStreamPayloadCarriesNoCredentials(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	setActivityScope(t, f, "owner_visible_all")
	secrets := []string{"hunter2-super", "sk-abcdefghijklmnopqrstuvwx", "mul_AbCdEfGhIjKlMnOp", "ghp_abcdefghijklmnopqrstuvwxyz0123"}
	in := projectEvent("password: " + secrets[0] + " key " + secrets[1])
	in.Body.Family = service.FamilyAgent
	in.Body.Fields = []string{"instructions", "name"}
	in.Body.ViaBotName = "tok " + secrets[2]
	in.Body.OnBehalfOfName = "gh " + secrets[3]
	in.ActorID = parseUUID(summonSecondMember(t))
	if n := routeWorkspace(t, in); n != 1 {
		t.Fatalf("queued %d", n)
	}
	if ok, err := f.worker.ProcessNext(context.Background()); err != nil || !ok {
		t.Fatalf("process: %v %v", ok, err)
	}
	raw, _ := f.lastBody.Load().([]byte)
	for _, s := range secrets {
		if strings.Contains(string(raw), s) {
			t.Fatalf("a secret left in the body: %s\n%s", s, raw)
		}
	}
	var body struct {
		Events []struct {
			Type     string `json:"type"`
			Activity struct {
				SourceKind string `json:"source_kind"`
				Workspace  struct {
					Family string   `json:"family"`
					Fields []string `json:"fields"`
				} `json:"workspace"`
			} `json:"activity"`
		} `json:"events"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Events) != 1 {
		t.Fatalf("body: %v %s", err, raw)
	}
	e := body.Events[0]
	if e.Type != "activity" || e.Activity.SourceKind != "workspace" || e.Activity.Workspace.Family != service.FamilyAgent ||
		strings.Join(e.Activity.Workspace.Fields, ",") != "instructions,name" {
		t.Fatalf("event: %+v", e)
	}
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
