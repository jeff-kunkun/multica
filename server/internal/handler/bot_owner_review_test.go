package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/botsource"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The review fixes of DENE-1936: the owner's go-ahead is a server-held object
// only the owner can grant (R1), a first connection defaults to the full office
// event scope (R2), and a stopped dangerous call reaches the owner once (R3).
// Real Postgres and local sockets; NOT an actual Grok endpoint.

func agentActor(t *testing.T) string {
	t.Helper()
	rt := dbfx.Runtime(t, "owner review runtime")
	return dbfx.Agent(t, "Owner Review Agent", rt)
}

// R1: the owner's go-ahead.
func ownerConfirmationFor(t *testing.T, f *humanNeededFixture, route string) service.OwnerConfirmation {
	t.Helper()
	req := service.OwnerConfirmationRequest{
		WorkspaceID: parseUUID(testWorkspaceID), BotID: f.bot.ID, BotName: "Fixture Bot", OwnerUserID: parseUUID(testUserID),
		Action: service.DangerDeleteWorkspace, Method: http.MethodDelete, Route: route, Fields: nil,
		ScopeHash: service.OwnerConfirmationScopeHash(http.MethodDelete, route, "", nil),
	}
	res, err := testHandler.EnsureOwnerConfirmation(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_ask WHERE id=$1`, res.AskID)
	})
	return res
}

func spend(t *testing.T, id pgtype.UUID, f *humanNeededFixture, route string) error {
	t.Helper()
	_, err := testHandler.Queries.ConsumeBotActionConfirmation(context.Background(), db.ConsumeBotActionConfirmationParams{
		ID: id, WorkspaceID: parseUUID(testWorkspaceID), BotID: f.bot.ID, OwnerUserID: parseUUID(testUserID),
		ScopeHash: service.OwnerConfirmationScopeHash(http.MethodDelete, route, "", nil),
	})
	return err
}

func answerCardAs(t *testing.T, askID, userID, choice string, ctxFn func(context.Context) context.Context) int {
	t.Helper()
	req := newRequest("POST", "/api/asks/"+askID+"/answer", map[string]any{"answers": map[string]string{"0": choice}})
	req.Header.Set("X-User-ID", userID)
	if ctxFn != nil {
		req = req.WithContext(ctxFn(req.Context()))
	}
	w := httptest.NewRecorder()
	testHandler.AnswerAsk(w, withURLParam(req, "id", askID))
	return w.Code
}

func TestOwnerConfirmationIsOnlyTheOwnersToGive(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	route := "/api/workspaces/" + testWorkspaceID
	res := ownerConfirmationFor(t, f, route)
	ask := uuidToString(res.AskID)

	// Not yet approved: nothing to spend, and a made-up id spends nothing either.
	if err := spend(t, res.ID, f, route); err != pgx.ErrNoRows {
		t.Fatalf("a pending request was spendable: %v", err)
	}
	if err := spend(t, parseUUID("12345678-1234-4234-8234-123456789012"), f, route); err != pgx.ErrNoRows {
		t.Fatalf("a fabricated id was spendable: %v", err)
	}

	// The Bot cannot approve itself: not with its own key, not with its receipt,
	// not as an agent started through it.
	asBot := func(ctx context.Context) context.Context {
		return botsource.WithContext(ctx, botsource.Source{ID: f.bot.ID, Name: "Fixture Bot"})
	}
	if code := answerCardAs(t, ask, testUserID, service.OwnerConfirmApprove, asBot); code != http.StatusForbidden {
		t.Fatalf("a Bot key answering the owner's card: %d", code)
	}
	asAgent := func(ctx context.Context) context.Context {
		return botsource.WithActor(ctx, botsource.Actor{Type: "agent", ID: parseUUID(agentActor(t))})
	}
	if code := answerCardAs(t, ask, testUserID, service.OwnerConfirmApprove, asAgent); code != http.StatusForbidden {
		t.Fatalf("an agent answering the owner's card: %d", code)
	}
	if _, askErr := testHandler.answerAskCore(context.Background(), askAnswer{
		Workspace: parseUUID(testWorkspaceID), AskID: res.AskID, Answers: map[string]string{"0": service.OwnerConfirmApprove},
		Answerer: parseUUID(testUserID), Bot: &f.bot,
	}); askErr == nil || askErr.Code != http.StatusForbidden {
		t.Fatalf("the Bot's own receipt approved the call: %+v", askErr)
	}
	// Another member cannot either.
	if code := answerCardAs(t, ask, summonSecondMember(t), service.OwnerConfirmApprove, nil); code != http.StatusForbidden {
		t.Fatalf("another member answering the owner's card: %d", code)
	}
	if err := spend(t, res.ID, f, route); err != pgx.ErrNoRows {
		t.Fatalf("a refused answer still made it spendable: %v", err)
	}

	// The owner, personally, says yes: it is spendable for that call, once.
	if code := answerCardAs(t, ask, testUserID, service.OwnerConfirmApprove, nil); code != http.StatusOK {
		t.Fatalf("the owner approving: %d", code)
	}
	if err := spend(t, res.ID, f, "/api/workspaces/"+testWorkspaceID+"/members"); err != pgx.ErrNoRows {
		t.Fatalf("an approval was spent on another call: %v", err)
	}
	_, otherHash, otherPrefix, _ := mintBotToken()
	otherBot, err := testHandler.Queries.CreateBotAccessToken(context.Background(), db.CreateBotAccessTokenParams{
		UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), Name: "another bot", TokenHash: otherHash, TokenPrefix: otherPrefix,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM personal_access_token WHERE id=$1`, otherBot.ID)
	})
	if _, err := testHandler.Queries.ConsumeBotActionConfirmation(context.Background(), db.ConsumeBotActionConfirmationParams{
		ID: res.ID, WorkspaceID: parseUUID(testWorkspaceID), BotID: otherBot.ID, OwnerUserID: parseUUID(testUserID),
		ScopeHash: service.OwnerConfirmationScopeHash(http.MethodDelete, route, "", nil),
	}); err != pgx.ErrNoRows {
		t.Fatalf("an approval was spent by another Bot: %v", err)
	}
	if err := spend(t, res.ID, f, route); err != nil {
		t.Fatalf("the approved call could not be spent: %v", err)
	}
	if err := spend(t, res.ID, f, route); err != pgx.ErrNoRows {
		t.Fatalf("an approval was spent twice: %v", err)
	}
}

func TestOwnerConfirmationExpiresAndNoIsNo(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	route := "/api/workspaces/" + testWorkspaceID

	denied := ownerConfirmationFor(t, f, route)
	if code := answerCardAs(t, uuidToString(denied.AskID), testUserID, service.OwnerConfirmDeny, nil); code != http.StatusOK {
		t.Fatalf("the owner refusing: %d", code)
	}
	if err := spend(t, denied.ID, f, route); err != pgx.ErrNoRows {
		t.Fatalf("a refusal was spendable: %v", err)
	}
	// A refused Bot is told so, not given a fresh card to ask again.
	again, err := testHandler.EnsureOwnerConfirmation(context.Background(), service.OwnerConfirmationRequest{
		WorkspaceID: parseUUID(testWorkspaceID), BotID: f.bot.ID, BotName: "Fixture Bot", OwnerUserID: parseUUID(testUserID),
		Action: service.DangerDeleteWorkspace, Method: http.MethodDelete, Route: route,
		ScopeHash: service.OwnerConfirmationScopeHash(http.MethodDelete, route, "", nil),
	})
	if err != nil || again.ID != denied.ID || again.Status != service.OwnerConfirmDenied {
		t.Fatalf("a refused call should read back as refused: %+v %v", again, err)
	}

	// An approval that ran out is not spendable, and an answer after expiry
	// changes nothing.
	routeB := route + "/members"
	expired := ownerConfirmationFor(t, f, routeB)
	if code := answerCardAs(t, uuidToString(expired.AskID), testUserID, service.OwnerConfirmApprove, nil); code != http.StatusOK {
		t.Fatalf("approving: %d", code)
	}
	dbfx.Exec(t, `UPDATE bot_action_confirmation SET expires_at = now() - interval '1 minute' WHERE id=$1`, expired.ID)
	if err := spend(t, expired.ID, f, routeB); err != pgx.ErrNoRows {
		t.Fatalf("an expired approval was spendable: %v", err)
	}
	routeC := route + "/keys"
	late := ownerConfirmationFor(t, f, routeC)
	dbfx.Exec(t, `UPDATE bot_action_confirmation SET expires_at = now() - interval '1 minute' WHERE id=$1`, late.ID)
	if code := answerCardAs(t, uuidToString(late.AskID), testUserID, service.OwnerConfirmApprove, nil); code != http.StatusGone {
		t.Fatalf("an answer after expiry: %d", code)
	}
	if err := spend(t, late.ID, f, routeC); err != pgx.ErrNoRows {
		t.Fatalf("a late answer made it spendable: %v", err)
	}
}

// R3: a stopped dangerous call reaches the owner's own Bot once, however often
// the Bot retries.
func TestBlockedDangerousCallReachesTheOwnersBotOnce(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	setActivityScope(t, f, "owner_visible_all")
	route := "/api/workspaces/" + testWorkspaceID
	first := ownerConfirmationFor(t, f, route)
	for i := 0; i < 3; i++ {
		again, err := testHandler.EnsureOwnerConfirmation(context.Background(), service.OwnerConfirmationRequest{
			WorkspaceID: parseUUID(testWorkspaceID), BotID: f.bot.ID, BotName: "Fixture Bot", OwnerUserID: parseUUID(testUserID),
			Action: service.DangerDeleteWorkspace, Method: http.MethodDelete, Route: route,
			ScopeHash: service.OwnerConfirmationScopeHash(http.MethodDelete, route, "", nil),
		})
		if err != nil || again.ID != first.ID || again.Created {
			t.Fatalf("retry %d opened another request: %+v %v", i, again, err)
		}
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM human_needed_event WHERE bot_id=$1 AND ask_id=$2`, f.bot.ID, first.AskID); n != 1 {
		t.Fatalf("the owner's Bot was told %d times, want once", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_ask WHERE workspace_id=$1 AND kind='owner_confirmation' AND status='open' AND recipient_id=$2`, testWorkspaceID, testUserID); n != 1 {
		t.Fatalf("open cards = %d, want 1", n)
	}
	if ok, err := f.worker.ProcessNext(context.Background()); err != nil || !ok || f.calls.Load() != 1 {
		t.Fatalf("delivery: ok=%v err=%v calls=%d", ok, err, f.calls.Load())
	}
}

// R2: connecting a Bot for the first time turns the full stream on; an explicit
// off stays off and an existing choice is left alone.
func TestFirstBotBindingDefaultsToTheFullStream(t *testing.T) {
	f := newHumanNeededFixture(t, true)
	put := func(body map[string]any) BotBindingResponse {
		t.Helper()
		w := httptest.NewRecorder()
		testHandler.PutBotBinding(w, withURLParam(newRequest(http.MethodPut, "/api/workspaces/"+testWorkspaceID+"/me/bot-binding", body), "id", testWorkspaceID))
		if w.Code != 200 {
			t.Fatalf("PUT %v -> %d %s", body, w.Code, w.Body.String())
		}
		var resp BotBindingResponse
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return resp
	}
	unbind := func() {
		dbfx.Exec(t, `DELETE FROM member_bot_binding WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, testUserID)
	}
	botID := uuidToString(f.bot.ID)

	unbind()
	if resp := put(map[string]any{"bot_id": botID}); strings.Join(resp.ActivityScope, ",") != service.ActivityScopeAll {
		t.Fatalf("first connection scope = %v, want the full stream", resp.ActivityScope)
	}
	// Saving again, or changing something else, keeps what is there.
	if resp := put(map[string]any{"mode": "mirror"}); strings.Join(resp.ActivityScope, ",") != service.ActivityScopeAll {
		t.Fatalf("re-save changed the scope: %v", resp.ActivityScope)
	}
	// Switching it off is a choice, and later saves keep it.
	if resp := put(map[string]any{"activity_scope": []string{}}); len(resp.ActivityScope) != 0 {
		t.Fatalf("explicit off = %v", resp.ActivityScope)
	}
	if resp := put(map[string]any{"bot_id": botID}); len(resp.ActivityScope) != 0 {
		t.Fatalf("an explicit off came back on: %v", resp.ActivityScope)
	}

	// An explicit empty list on the very first connection is off too.
	unbind()
	if resp := put(map[string]any{"bot_id": botID, "activity_scope": []string{}}); len(resp.ActivityScope) != 0 {
		t.Fatalf("first connection with an explicit off = %v", resp.ActivityScope)
	}

	// A connection that already chose something else is left as it is.
	unbind()
	dbfx.Exec(t, `INSERT INTO member_bot_binding (workspace_id, user_id, bot_id, activity_scope) VALUES ($1,$2,$3,'{owned}')`, testWorkspaceID, testUserID, f.bot.ID)
	if resp := put(map[string]any{"bot_id": botID}); strings.Join(resp.ActivityScope, ",") != "owned" {
		t.Fatalf("an existing choice was overwritten: %v", resp.ActivityScope)
	}
}
