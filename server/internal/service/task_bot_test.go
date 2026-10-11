package service

import (
	"context"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/botsource"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"testing"
)

func TestBotDirectRunAndPersistedCommentProvenance(t *testing.T) {
	user := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	bot := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	other := pgtype.UUID{Bytes: [16]byte{3}, Valid: true}
	ctx := botsource.WithContext(context.Background(), botsource.Source{ID: other, Name: "Current Bot"})
	s := &TaskService{}
	got := s.attributionFromComment(ctx, db.Comment{AuthorType: "member", AuthorID: user, ViaBotID: bot, ViaBotName: pgtype.Text{String: "Historical Bot", Valid: true}}, attribution.SourceCommentSource)
	if got.Source != attribution.SourceViaBot || got.ViaBotID != bot || got.ViaBotName.String != "Historical Bot" || got.UserID != user {
		t.Fatalf("comment used request rather than persisted origin: %+v", got)
	}
	got = s.attributionFromComment(ctx, db.Comment{AuthorType: "member", AuthorID: user}, attribution.SourceCommentSource)
	if got.Source != attribution.SourceDirectHuman || got.ViaBotID.Valid {
		t.Fatalf("Bot request tainted human comment: %+v", got)
	}
	got = directRequestAttribution(ctx, user, attribution.EvidenceChat, pgtype.UUID{})
	if got.Source != attribution.SourceViaBot || got.ViaBotID != other || got.UserID != user {
		t.Fatalf("direct Bot request lost identity: %+v", got)
	}
	got = s.attributionForIssueTask(ctx, db.Issue{CreatorType: "member", CreatorID: other}, pgtype.UUID{}, attribution.SourceCommentSource, user)
	if got.Source != attribution.SourceViaBot || got.UserID != user || got.ViaBotID != other {
		t.Fatalf("action attributed to issue creator: %+v", got)
	}
}

// Background wakes must keep the creation provenance even after the request
// context has gone away; a later person's own action starts a new human root.
func TestBotPersistedIssueSourceSurvivesBackgroundWake(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	_, creatorID, _, issueID := seedAttributionFixture(t, pool)
	botID := pgtype.UUID{Bytes: [16]byte{0x42}, Valid: true}
	if _, err := pool.Exec(ctx, `UPDATE issue SET via_bot_id=$2, via_bot_name=$3 WHERE id=$1`, issueID, botID, "Historical Bot"); err != nil {
		t.Fatal(err)
	}
	q := db.New(pool)
	issue, err := q.GetIssue(ctx, util.MustParseUUID(issueID))
	if err != nil {
		t.Fatal(err)
	}
	if issue.ViaBotID != botID || issue.ViaBotName.String != "Historical Bot" {
		t.Fatalf("issue source did not persist: %+v", issue)
	}
	svc := &TaskService{Queries: q}
	got := svc.attributionForIssueTask(ctx, issue, pgtype.UUID{}, attribution.SourceCommentSource, pgtype.UUID{})
	if got.Source != attribution.SourceViaBot || got.ViaBotID != botID || got.ViaBotName.String != "Historical Bot" || got.UserID != util.MustParseUUID(creatorID) {
		t.Fatalf("background wake lost persisted Bot source: %+v", got)
	}
	actor := pgtype.UUID{Bytes: [16]byte{0x43}, Valid: true}
	got = svc.attributionForIssueTask(ctx, issue, pgtype.UUID{}, attribution.SourceCommentSource, actor)
	if got.Source != attribution.SourceDirectHuman || got.UserID != actor || got.AccountableUserID != actor || got.ViaBotID.Valid || got.ViaBotName.Valid {
		t.Fatalf("direct person did not replace prior Bot provenance: %+v", got)
	}
}
