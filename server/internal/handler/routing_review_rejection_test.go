package handler

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type reviewReturnStore struct {
	routingStore
	targets []string
}

func (s *reviewReturnStore) RoutingFacts(context.Context, string, []string, []string) (routing.RoutingFacts, error) {
	return routing.RoutingFacts{Seats: map[string]routing.SeatSnapshot{}}, nil
}
func (s *reviewReturnStore) Handoff(ctx context.Context, ws, id, kind, target string) error {
	s.targets = append(s.targets, target)
	_, err := s.h.Queries.ReassignIssue(ctx, db.ReassignIssueParams{ID: parseUUID(id), WorkspaceID: parseUUID(ws), AssigneeType: kind, AssigneeID: parseUUID(target)})
	return err
}
func TestReviewTwoHoldsEscalateAndKeepReviewer(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	enableTestRouting(t, tierJudge{tier: "medium", confidence: 1})
	executor := createHandlerTestAgent(t, "review-return-executor", []byte("[]"))
	reviewer := createHandlerTestAgent(t, "review-return-reviewer", []byte("[]"))
	stronger := createHandlerTestAgent(t, "review-return-stronger", []byte("[]"))
	dbfx.Exec(t, "UPDATE agent SET routing_tier='medium' WHERE id=$1", executor)
	dbfx.Exec(t, "UPDATE agent SET routing_tier='strong' WHERE id=$1 OR id=$2", reviewer, stronger)
	id := dbfx.Issue(t, "two rejected reviews", testutil.Cols{"status": "in_review", "assignee_type": "agent", "assignee_id": reviewer, "reviewer_type": "agent", "reviewer_id": reviewer})
	evidence := dbfx.Comment(t, id, "delivery", testutil.Cols{"author_type": "agent", "author_id": executor})
	meta, _ := json.Marshal(map[string]any{closeprotocol.KeyEvidenceCommentID: evidence})
	dbfx.Exec(t, "UPDATE issue SET metadata=$2 WHERE id=$1", id, meta)
	store := &reviewReturnStore{routingStore: routingStore{h: testHandler}}
	testHandler.Routing = routing.New(store, nil)
	ctx := context.Background()
	for round := 1; round <= 2; round++ {
		commentID := dbfx.Comment(t, id, "verdict: hold\nFix the failing behavior.", testutil.Cols{"author_type": "agent", "author_id": reviewer})
		comment, err := testHandler.Queries.GetComment(ctx, parseUUID(commentID))
		if err != nil {
			t.Fatal(err)
		}
		if err = testHandler.applyReviewRejection(ctx, comment); err != nil {
			t.Fatal(err)
		}
		// A duplicate event must not consume another return.
		if err = testHandler.applyReviewRejection(ctx, comment); err != nil {
			t.Fatal(err)
		}
		got, err := testHandler.Queries.GetIssue(ctx, parseUUID(id))
		if err != nil {
			t.Fatal(err)
		}
		want := executor
		if round == 2 {
			want = stronger
		}
		if uuidToString(got.AssigneeID) != want || uuidToString(got.ReviewerID) != reviewer || got.Status != "in_progress" {
			t.Fatalf("round %d: %+v", round, got)
		}
		if round == 1 {
			dbfx.Exec(t, "UPDATE issue SET status='in_review',assignee_id=$2 WHERE id=$1", id, reviewer)
		}
	}
	if len(store.targets) != 2 {
		t.Fatalf("handoffs=%v", store.targets)
	}
}
