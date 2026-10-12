package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// DENE-1933: an @ added to a description is one ask per edit that added it.
// The same edit never pushes twice; taking the @ out and putting it back is a
// second ask, and each carries the line the person was asked about.
func TestDescriptionMentionPushesOncePerAddingEdit(t *testing.T) {
	ctx := context.Background()
	fx := workspaceFixture(t)
	person := fx.User(t, "Bound person", fmt.Sprintf("bound-desc-%d@multica.ai", time.Now().UnixNano()))
	fx.Member(t, testWorkspaceID, person, "member")
	issueID := fx.Issue(t, "Description mention", testutil.Cols{"visibility": "workspace"})
	fx.Cleanup(t, `DELETE FROM inbox_item WHERE issue_id = $1`, issueID)
	fx.Cleanup(t, `DELETE FROM issue_subscriber WHERE issue_id = $1`, issueID)

	queries := db.New(testPool)
	bot, err := queries.CreateBotAccessToken(ctx, db.CreateBotAccessTokenParams{
		UserID: util.MustParseUUID(person), WorkspaceID: util.MustParseUUID(testWorkspaceID),
		Name: "description mention bot", TokenHash: fmt.Sprintf("hash-%d", time.Now().UnixNano()), TokenPrefix: "mbt_desc",
	})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM personal_access_token WHERE id=$1`, bot.ID) })
	fx.Exec(t, `UPDATE bot_wake SET webhook_secret='\x01'::bytea WHERE bot_id=$1`, util.UUIDToString(bot.ID))
	fx.Insert(t, "member_bot_binding", testutil.Cols{
		"workspace_id": testWorkspaceID, "user_id": person, "bot_id": util.UUIDToString(bot.ID), "verified_at": time.Now(),
	})

	bus := newNotificationBus(t, queries)
	edit := func(prev, next, version string) {
		t.Helper()
		// The handler writes the description before it publishes the event.
		if _, err := testPool.Exec(ctx, `UPDATE issue SET description=$1 WHERE id=$2`, next, issueID); err != nil {
			t.Fatalf("write description: %v", err)
		}
		bus.Publish(events.Event{
			Type: protocol.EventIssueUpdated, WorkspaceID: testWorkspaceID, ActorType: "member", ActorID: testUserID,
			Payload: map[string]any{
				"issue": handler.IssueResponse{
					ID: issueID, WorkspaceID: testWorkspaceID, Title: "Description mention", Status: "todo",
					Description: &next, UpdatedAt: version,
				},
				"description_changed": true,
				"prev_description":    &prev,
			},
		})
	}
	mention := "[@Bound](mention://member/" + person + ")"
	pushes := func() []string {
		rows, err := testPool.Query(ctx, `SELECT question FROM human_needed_event WHERE issue_id=$1 AND target_user_id=$2 AND event_type='human_needed' ORDER BY sequence`, issueID, person)
		if err != nil {
			t.Fatalf("list pushes: %v", err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var q string
			if err := rows.Scan(&q); err != nil {
				t.Fatal(err)
			}
			out = append(out, q)
		}
		return out
	}

	first := "Background.\nPlease pick the colour " + mention + " before Friday."
	edit("Background.", first, "2026-10-12T10:00:00Z")
	got := pushes()
	if len(got) != 1 || !strings.Contains(got[0], "pick the colour") {
		t.Fatalf("first edit pushes = %q, want one carrying the question", got)
	}
	// The same edit seen again (a replayed event) is not a second push.
	edit("Background.", first, "2026-10-12T10:00:00Z")
	if got := pushes(); len(got) != 1 {
		t.Fatalf("a replay of the same edit pushed again: %q", got)
	}
	// Taking the @ out does not ask anyone.
	edit(first, "Background.", "2026-10-12T11:00:00Z")
	if got := pushes(); len(got) != 1 {
		t.Fatalf("removing the @ pushed: %q", got)
	}
	// Putting it back later is a new ask carrying what was written then.
	second := "Background.\nNow pick the font " + mention + " instead."
	edit("Background.", second, "2026-10-12T12:00:00Z")
	got = pushes()
	if len(got) != 2 || !strings.Contains(got[1], "pick the font") {
		t.Fatalf("re-adding the @ pushes = %q, want a second carrying the new question", got)
	}
}
