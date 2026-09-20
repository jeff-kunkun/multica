package handler

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// reviewerOptions is the merge that keeps a provisioned slot usable as the
// roster changes. The rule it has to hold is that existing option ids survive
// — issue rows store the id, not the name, so a regenerated id silently blanks
// the reviewer on every ticket already holding that value.
func TestReviewerOptionsPreservesExistingIDs(t *testing.T) {
	existing := []PropertyOption{
		{ID: "11111111-1111-1111-1111-111111111111", Name: routing.OptionNoReview, Color: reviewerNoReviewColor},
		{ID: "22222222-2222-2222-2222-222222222222", Name: "孙悟空", Color: reviewerSeatColor},
	}
	out := reviewerOptions([]string{"孙悟空", "贝吉塔"}, existing)

	if len(out) != 4 {
		t.Fatalf("want 4 options (2 existing + 交给人 + 贝吉塔), got %d: %+v", len(out), out)
	}
	if out[0].ID != existing[0].ID || out[1].ID != existing[1].ID {
		t.Fatalf("existing option ids were not preserved: %+v", out[:2])
	}
	byName := map[string]PropertyOption{}
	for _, o := range out {
		byName[o.Name] = o
	}
	for _, want := range []string{routing.OptionNoReview, routing.OptionHuman, "孙悟空", "贝吉塔"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("option %q missing from merged list: %+v", want, out)
		}
	}
	if byName["贝吉塔"].ID == "" {
		t.Error("newly added option got no id")
	}
}

// A second merge with the same inputs must be a no-op, because a merge that
// keeps changing is a merge that writes the property on every routed ticket.
func TestReviewerOptionsIsStable(t *testing.T) {
	first := reviewerOptions([]string{"孙悟空"}, nil)
	second := reviewerOptions([]string{"孙悟空"}, first)
	if len(second) != len(first) {
		t.Fatalf("second merge changed the option count: %d -> %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID || first[i].Name != second[i].Name {
			t.Fatalf("option %d changed across merges: %+v -> %+v", i, first[i], second[i])
		}
	}
}

// The two non-seat answers are what let the slot close: "needs no review" has
// to be a value rather than an empty slot, or routing re-judges the ticket on
// every later status change.
func TestReviewerOptionsAlwaysCarriesTheNonSeatAnswers(t *testing.T) {
	out := reviewerOptions(nil, nil)
	if len(out) != 2 {
		t.Fatalf("want exactly the two fixed options with an empty roster, got %+v", out)
	}
	if out[0].Name != routing.OptionNoReview || out[1].Name != routing.OptionHuman {
		t.Fatalf("fixed options wrong or reordered: %+v", out)
	}
}

func TestEnsureReviewerProperty(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	store := routingStore{h: testHandler}
	wsID, err := util.ParseUUID(testWorkspaceID)
	if err != nil {
		t.Fatalf("parse workspace id: %v", err)
	}

	clear := func() {
		testPool.Exec(ctx, `DELETE FROM issue_property WHERE workspace_id = $1 AND name = $2`,
			wsID, ReviewerPropertyName)
	}
	clear()
	t.Cleanup(clear)

	// First call provisions the slot — nobody had to open the settings page.
	prop, ok, err := store.ensureReviewerProperty(ctx, wsID)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !ok {
		t.Fatal("reviewer slot was not provisioned")
	}
	for _, want := range []string{routing.OptionNoReview, routing.OptionHuman} {
		if prop.Options[want] == "" {
			t.Errorf("provisioned slot has no %q option: %+v", want, prop.Options)
		}
	}

	// Second call is idempotent: same property, no duplicate definition.
	again, ok, err := store.ensureReviewerProperty(ctx, wsID)
	if err != nil || !ok {
		t.Fatalf("second ensure: ok=%v err=%v", ok, err)
	}
	if again.ID != prop.ID {
		t.Fatalf("ensure created a second definition: %s then %s", prop.ID, again.ID)
	}
	if again.Options[routing.OptionHuman] != prop.Options[routing.OptionHuman] {
		t.Error("option id changed on the second ensure; issue values would be orphaned")
	}

	// Archiving the definition is how a workspace turns the reviewer half of
	// routing off. It must not be resurrected by the next routed ticket.
	propID, err := util.ParseUUID(prop.ID)
	if err != nil {
		t.Fatalf("parse property id: %v", err)
	}
	if _, err := testHandler.Queries.UpdateIssueProperty(ctx, db.UpdateIssuePropertyParams{
		ID: propID, WorkspaceID: wsID, ArchivedSet: true, ArchivedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		t.Fatalf("archive property: %v", err)
	}
	if _, ok, err := store.ensureReviewerProperty(ctx, wsID); err != nil || ok {
		t.Fatalf("archived slot should read as absent: ok=%v err=%v", ok, err)
	}
	var count int
	if err := testPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM issue_property WHERE workspace_id = $1 AND name = $2`,
		wsID, ReviewerPropertyName,
	).Scan(&count); err != nil {
		t.Fatalf("count definitions: %v", err)
	}
	if count != 1 {
		t.Fatalf("archived slot was re-provisioned: %d definitions named %q", count, ReviewerPropertyName)
	}
}
