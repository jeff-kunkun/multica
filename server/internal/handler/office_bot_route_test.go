package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/dbactor"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func officeRouteBot(t *testing.T) db.PersonalAccessToken {
	t.Helper()
	_, hash, prefix, err := mintBotToken()
	if err != nil {
		t.Fatal(err)
	}
	bot, err := testHandler.Queries.CreateBotAccessToken(context.Background(), db.CreateBotAccessTokenParams{WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(testUserID), Name: "route fixture", TokenHash: hash, TokenPrefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), "DELETE FROM personal_access_token WHERE id=$1", bot.ID)
	})
	return bot
}
func setOfficeRoute(t *testing.T, project pgtype.UUID, bot db.PersonalAccessToken, channel string) db.OfficeBotRoute {
	t.Helper()
	row, err := testHandler.Queries.UpsertOfficeBotRoute(context.Background(), db.UpsertOfficeBotRouteParams{WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(testUserID), ProjectID: project, BotID: bot.ID, ChannelRef: channel})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), "DELETE FROM office_bot_route WHERE id=$1", row.ID) })
	return row
}

func TestOfficeBotRouteProjectDefaultLegacyAndPush(t *testing.T) {
	f := newOfficeFixture(t)
	ctx := context.Background()
	project := parseUUID(dbfx.Project(t, "routed project"))
	fallback := officeRouteBot(t)
	setOfficeRoute(t, pgtype.UUID{}, fallback, "multica-default-group")
	projectRoute := setOfficeRoute(t, project, f.bot, "multica-project-group")
	// Default routes are unique too: an upsert keeps the same row.
	defaultAgain := setOfficeRoute(t, pgtype.UUID{}, fallback, "multica-default-group")
	routes, err := testHandler.Queries.ListOfficeBotRoutes(ctx, db.ListOfficeBotRoutesParams{WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(testUserID)})
	if err != nil || len(routes) != 2 {
		t.Fatalf("routes=%v err=%v", routes, err)
	}
	if defaultAgain.ProjectID.Valid {
		t.Fatal("default acquired a project")
	}
	resolve := func(p pgtype.UUID) db.OfficeBotRoute {
		row, err := testHandler.Queries.ResolveOfficeBotRoute(ctx, db.ResolveOfficeBotRouteParams{WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(testUserID), ProjectID: p})
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	if got := resolve(project); got.ID != projectRoute.ID {
		t.Fatalf("project did not win: %+v", got)
	}
	if got := resolve(pgtype.UUID{}); got.BotID != fallback.ID {
		t.Fatal("default not selected")
	}
	// A revoked primary Bot cannot capture new events; configured default wins.
	dbfx.Exec(t, "UPDATE personal_access_token SET revoked=true WHERE id=$1", f.bot.ID)
	if got := resolve(project); got.BotID != fallback.ID {
		t.Fatal("revoked primary did not fall back")
	}
	dbfx.Exec(t, "UPDATE personal_access_token SET revoked=false WHERE id=$1", f.bot.ID)
	// A project route works without the legacy single-Bot binding.
	dbfx.Exec(t, "UPDATE member_bot_binding SET revoked_at=now() WHERE id=$1", f.binding)
	issue := ownedIssue(t, "project relay")
	dbfx.Exec(t, "UPDATE issue SET project_id=$2 WHERE id=$1", issue, project)
	peer := summonSecondMember(t)
	asActor(t, dbactor.Actor{Type: "member", ID: peer}, "UPDATE issue SET status='in_review',updated_at=now() WHERE id=$1", issue)
	event := latestOfficeEvent(t, "issue_id=$1 AND event_type='issue.status_changed'", issue)
	f.seal(t)
	delivery, err := testHandler.Queries.GetOfficeDeliveryForBot(ctx, db.GetOfficeDeliveryForBotParams{EventID: parseUUID(event.eventID), BotID: f.bot.ID})
	if err != nil || delivery.RelayTarget != "multica-project-group" || delivery.BindingID.Valid {
		t.Fatalf("delivery=%+v err=%v", delivery, err)
	}
	f.worker.DeliverDue(ctx)
	if f.calls.Load() != 1 {
		t.Fatalf("pushes=%d", f.calls.Load())
	}
	var pushed map[string]any
	if err := json.Unmarshal(f.lastBody.Load().([]byte), &pushed); err != nil {
		t.Fatal(err)
	}
	if pushed["delivery"].(map[string]any)["relay_target"] != "multica-project-group" {
		t.Fatal(pushed)
	}
	code, _ := f.call(t, "GET", "/office-events/"+event.eventID, nil)
	if code != 200 {
		t.Fatalf("event read=%d", code)
	}
	code, _ = f.call(t, "POST", f.botPath()+"/office-events/"+event.eventID+"/receipt", map[string]any{"stage": "relayed", "channel": "telegram", "target": "private", "message_ref": "m1"})
	if code != 400 {
		t.Fatalf("wrong group receipt=%d", code)
	}
	code, _ = f.call(t, "POST", f.botPath()+"/office-events/"+event.eventID+"/receipt", map[string]any{"stage": "relayed", "channel": "multica", "target": "multica-project-group", "message_ref": "m1"})
	if code != 200 {
		t.Fatalf("group receipt=%d", code)
	}
}

func TestOfficeBotRouteFreezeAndLegacyFallback(t *testing.T) {
	f := newOfficeFixture(t)
	ctx := context.Background()
	project := parseUUID(dbfx.Project(t, "freeze project"))
	route := setOfficeRoute(t, project, f.bot, "original-group")
	issue := ownedIssue(t, "freeze relay")
	dbfx.Exec(t, "UPDATE issue SET project_id=$2 WHERE id=$1", issue, project)
	peer := summonSecondMember(t)
	asActor(t, dbactor.Actor{Type: "member", ID: peer}, "UPDATE issue SET status='in_review',updated_at=now() WHERE id=$1", issue)
	event := latestOfficeEvent(t, "issue_id=$1 AND event_type='issue.status_changed'", issue)
	f.seal(t)
	dbfx.Exec(t, "UPDATE office_bot_route SET channel_ref='new-group' WHERE id=$1", route.ID)
	f.worker.DeliverDue(ctx)
	if f.calls.Load() != 0 {
		t.Fatal("changed group received old frozen event")
	}
	status, _, blocked, _ := deliveryStatus(t, event.eventID, uuidToString(f.bot.ID))
	if status != "blocked" || blocked != officeBlockedRoute {
		t.Fatalf("status=%s blocked=%s", status, blocked)
	}
	f.worker.Restore(ctx)
	status, _, _, _ = deliveryStatus(t, event.eventID, uuidToString(f.bot.ID))
	if status != "blocked" {
		t.Fatal("legacy restore stole project delivery")
	}
	dbfx.Exec(t, "DELETE FROM office_bot_route WHERE id=$1", route.ID)
	asActor(t, dbactor.Actor{Type: "member", ID: peer}, "UPDATE issue SET status='done',updated_at=now() WHERE id=$1", issue)
	next := latestOfficeEvent(t, "issue_id=$1 AND event_type='issue.status_changed'", issue)
	f.seal(t)
	d, err := testHandler.Queries.GetOfficeDeliveryForBot(ctx, db.GetOfficeDeliveryForBotParams{EventID: parseUUID(next.eventID), BotID: f.bot.ID})
	if err != nil || d.RelayTarget != "" || !d.BindingID.Valid {
		t.Fatalf("legacy fallback=%+v err=%v", d, err)
	}
}

func TestOfficeBotRouteCRUDOwnershipAndValidation(t *testing.T) {
	if testHandler == nil {
		t.Skip("requires test database")
	}
	bot := officeRouteBot(t)
	router := chi.NewRouter()
	router.Put("/api/workspaces/{id}/office-bot-routes", testHandler.PutOfficeBotRoute)
	router.Get("/api/workspaces/{id}/office-bot-routes", testHandler.ListOfficeBotRoutes)
	router.Delete("/api/workspaces/{id}/office-bot-routes", testHandler.DeleteOfficeBotRoute)
	run := func(method, path, body, user, source string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/workspaces/"+testWorkspaceID+"/office-bot-routes"+path, strings.NewReader(body))
		req.Header.Set("X-User-ID", user)
		req.Header.Set("X-Actor-Source", source)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	body := `{"bot_id":"` + uuidToString(bot.ID) + `","channel_ref":"group"}`
	if w := run("PUT", "", body, testUserID, "task_token"); w.Code != 403 {
		t.Fatalf("agent accepted %d %s", w.Code, w.Body)
	}
	if w := run("PUT", "", body, testUserID, ""); w.Code != 200 {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), "DELETE FROM office_bot_route WHERE bot_id=$1", bot.ID)
	})
	if w := run("GET", "", "", testUserID, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"channel_ref":"group"`) {
		t.Fatalf("list %d %s", w.Code, w.Body)
	}
	foreign := dbfx.Workspace(t, "foreign project workspace", "route-other-"+randomID(), testutil.Cols{"issue_prefix": "RTE"})
	foreignProject := testutil.New(testPool, foreign, testUserID).Project(t, "foreign project")
	wrongProject := `{"bot_id":"` + uuidToString(bot.ID) + `","channel_ref":"group","project_id":"` + foreignProject + `"}`
	if w := run("PUT", "", wrongProject, testUserID, ""); w.Code != 404 {
		t.Fatalf("foreign project accepted %d %s", w.Code, w.Body)
	}
	if w := run("PUT", "", `{"bot_id":"`+uuidToString(bot.ID)+`","channel_ref":" "}`, testUserID, ""); w.Code != 400 {
		t.Fatalf("empty group accepted %d", w.Code)
	}
	peer := summonSecondMember(t)
	dbfx.Exec(t, "UPDATE member SET role='admin' WHERE workspace_id=$1 AND user_id=$2", testWorkspaceID, peer)
	if w := run("PUT", "", body, peer, ""); w.Code != 404 {
		t.Fatalf("foreign bot accepted %d %s", w.Code, w.Body)
	}
	if w := run("DELETE", "", "", testUserID, ""); w.Code != 204 {
		t.Fatalf("delete %d %s", w.Code, w.Body)
	}
}

func TestOfficeBotRouteSuppressesLegacyStreamDuplicate(t *testing.T) {
	f := newOfficeFixture(t)
	project := parseUUID(dbfx.Project(t, "single responsible Bot"))
	primary := officeRouteBot(t)
	setOfficeRoute(t, project, primary, "project-group")
	setActivityScope(t, f.humanNeededFixture, "owner_visible_all")
	issue := ownedIssue(t, "one human relay")
	dbfx.Exec(t, "UPDATE issue SET project_id=$2 WHERE id=$1", issue, project)
	peer := summonSecondMember(t)
	asActor(t, dbactor.Actor{Type: "member", ID: peer}, "UPDATE issue SET status='in_review',updated_at=now() WHERE id=$1", issue)
	event := latestOfficeEvent(t, "issue_id=$1 AND event_type='issue.status_changed'", issue)
	f.seal(t)
	var count int
	dbfx.QueryRow(t, "SELECT count(*) FROM office_event_delivery WHERE event_id=$1 AND target_user_id=$2", event.eventID, testUserID).Scan(&count)
	if count != 1 {
		t.Fatalf("owner got %d deliveries, want exactly one", count)
	}
	d, err := testHandler.Queries.GetOfficeDeliveryForBot(context.Background(), db.GetOfficeDeliveryForBotParams{EventID: parseUUID(event.eventID), BotID: primary.ID})
	if err != nil || d.RelayTarget != "project-group" || !d.MustRelay {
		t.Fatalf("primary=%+v err=%v", d, err)
	}
}

func TestOfficeBotRouteRestoresOldGapAndExactDestination(t *testing.T) {
	f := newOfficeFixture(t)
	ctx := context.Background()
	project := parseUUID(dbfx.Project(t, "route reconnect"))
	dbfx.Exec(t, "UPDATE member_bot_binding SET revoked_at=now() WHERE id=$1", f.binding)
	issue := ownedIssue(t, "gap before route setup")
	dbfx.Exec(t, "UPDATE issue SET project_id=$2 WHERE id=$1", issue, project)
	peer := summonSecondMember(t)
	asActor(t, dbactor.Actor{Type: "member", ID: peer}, "UPDATE issue SET status='in_review',updated_at=now() WHERE id=$1", issue)
	event := latestOfficeEvent(t, "issue_id=$1 AND event_type='issue.status_changed'", issue)
	f.seal(t)
	var unresolved int
	dbfx.QueryRow(t, "SELECT count(*) FROM office_event_gap WHERE event_id=$1 AND resolved_at IS NULL", event.eventID).Scan(&unresolved)
	if unresolved != 1 {
		t.Fatalf("unresolved gaps=%d", unresolved)
	}
	route := setOfficeRoute(t, project, f.bot, "reconnected-group")
	// Independent replicas race to restore one gap. Delivery and resolution
	// must commit together, and subsequent readers see it already resolved.
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); <-start; f.worker.Restore(ctx) }()
	}
	close(start)
	workers.Wait()
	d, err := testHandler.Queries.GetOfficeDeliveryForBot(ctx, db.GetOfficeDeliveryForBotParams{EventID: parseUUID(event.eventID), BotID: f.bot.ID})
	if err != nil || d.RelayTarget != "reconnected-group" || d.Status != "pending" {
		t.Fatalf("restored=%+v err=%v", d, err)
	}
	dbfx.QueryRow(t, "SELECT count(*) FROM office_event_gap WHERE event_id=$1 AND resolved_at IS NULL", event.eventID).Scan(&unresolved)
	if unresolved != 0 {
		t.Fatal("gap not resolved")
	}
	// A route change blocks its frozen destination. Restoring the exact route
	// allows the existing row to resume, without creating a second delivery.
	dbfx.Exec(t, "UPDATE office_bot_route SET channel_ref='different-group' WHERE id=$1", route.ID)
	f.worker.DeliverDue(ctx)
	status, _, blocked, _ := deliveryStatus(t, event.eventID, uuidToString(f.bot.ID))
	if status != "blocked" || blocked != officeBlockedRoute {
		t.Fatalf("%s %s", status, blocked)
	}
	dbfx.Exec(t, "UPDATE office_bot_route SET channel_ref='reconnected-group' WHERE id=$1", route.ID)
	f.worker.Restore(ctx)
	f.worker.DeliverDue(ctx)
	if f.calls.Load() != 1 {
		t.Fatalf("pushes=%d", f.calls.Load())
	}
	// Reconnect again cannot create another row or relay an already sent event
	// to another fallback Bot while its first response may be in flight.
	other := officeRouteBot(t)
	setOfficeRoute(t, pgtype.UUID{}, other, "fallback-group")
	dbfx.Exec(t, "UPDATE office_bot_route SET bot_id=$2 WHERE id=$1", route.ID, other.ID)
	f.worker.Restore(ctx)
	var count int
	dbfx.QueryRow(t, "SELECT count(*) FROM office_event_delivery WHERE event_id=$1 AND target_user_id=$2", event.eventID, testUserID).Scan(&count)
	if count != 1 {
		t.Fatalf("reconnect created %d deliveries", count)
	}
}
