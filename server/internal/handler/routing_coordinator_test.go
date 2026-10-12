package handler

import (
	"context"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"strings"
	"testing"
)

type coordinatorRoutingStore struct {
	routingStore
	roster      map[string]routing.Agent
	unavailable bool
}

func (s coordinatorRoutingStore) Roster(context.Context, string) (map[string]routing.Agent, error) {
	return s.roster, nil
}
func (s coordinatorRoutingStore) RoutingFacts(context.Context, string, []string, []string) (routing.RoutingFacts, error) {
	facts := routing.RoutingFacts{Seats: map[string]routing.SeatSnapshot{}}
	for _, a := range s.roster {
		if s.unavailable {
			facts.Seats[a.ID] = routing.SeatSnapshot{AgentID: a.ID, Availability: routing.AvailabilityDead}
		}
	}
	return facts, nil
}
func TestNoSeatRoutesNotifyAndSubscribeGrok(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	_, hash, prefix, err := mintBotToken()
	if err != nil {
		t.Fatal(err)
	}
	bot, err := testHandler.Queries.CreateBotAccessToken(ctx, db.CreateBotAccessTokenParams{UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), Name: "Coordinator Grok", TokenHash: hash, TokenPrefix: prefix, BotType: pgtype.Text{String: "grok", Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), "DELETE FROM personal_access_token WHERE id=$1", bot.ID) })
	enableDraftSuggestRouting(t, tierJudge{tier: "medium", confidence: 1})
	for _, tc := range []struct {
		name           string
		empty, offline bool
	}{{"no routable seats", true, false}, {"all seats offline", false, true}, {"only strongest remains", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			id := dbfx.Issue(t, tc.name, testutil.Cols{"status": "todo"})
			agent := createHandlerTestAgent(t, "coordinator-seat", []byte("[]"))
			store := coordinatorRoutingStore{routingStore: routingStore{h: testHandler}, roster: map[string]routing.Agent{}, unavailable: tc.offline}
			if !tc.empty {
				store.roster["top"] = routing.Agent{ID: agent, Name: "top", Tier: "strongest", Usage: routing.UsageAmple}
			}
			router := routing.New(store, tierJudge{tier: "medium", confidence: 1})
			out, err := router.Route(ctx, testWorkspaceID, id)
			if err != nil {
				t.Fatal(err)
			}
			if !out.Commented || out.ExecutorWritten != nil {
				t.Fatalf("outcome=%+v", out)
			}
			_, err = router.Route(ctx, testWorkspaceID, id)
			if err != nil {
				t.Fatal(err)
			}
			var body string
			if err = testPool.QueryRow(ctx, "SELECT content FROM comment WHERE issue_id=$1 AND routing_kind='assignment'", id).Scan(&body); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(body, "mention://bot/"+uuidToString(bot.ID)) {
				t.Fatalf("missing Grok mention: %s", body)
			}
			var events, watches int
			if err = testPool.QueryRow(ctx, "SELECT count(*) FROM bot_wake_event WHERE issue_id=$1 AND bot_id=$2 AND event_type='mention'", id, bot.ID).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if err = testPool.QueryRow(ctx, "SELECT count(*) FROM bot_wake_watch WHERE issue_id=$1 AND bot_id=$2", id, bot.ID).Scan(&watches); err != nil {
				t.Fatal(err)
			}
			if events != 1 || watches != 1 {
				t.Fatalf("events=%d watches=%d", events, watches)
			}
		})
	}
}
