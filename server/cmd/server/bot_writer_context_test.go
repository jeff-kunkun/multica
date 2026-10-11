package main

import (
	"context"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"testing"
)

// Exercise the trusted SQL contract consumed by outbox triggers in the same
// statement, then prove a later system write does not reuse the previous actor.
func TestBotWriterContextReachesTrigger(t *testing.T) {
	ctx := context.Background()
	issueID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() { cleanupTestIssue(t, issueID) })
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `CREATE TEMP TABLE bot_source_probe(actor_type text, actor_id text, bot_id text, bot_name text);
 CREATE FUNCTION pg_temp.capture_bot_source() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 INSERT INTO bot_source_probe VALUES (current_setting('multica.actor_type',true),current_setting('multica.actor_id',true),current_setting('multica.via_bot_id',true),current_setting('multica.via_bot_name',true));
 RETURN NEW; END $$;
 CREATE TRIGGER bot_source_probe_trigger AFTER UPDATE OF status ON issue FOR EACH ROW EXECUTE FUNCTION pg_temp.capture_bot_source();`)
	if err != nil {
		t.Fatal(err)
	}
	bot := util.MustParseUUID("10000000-0000-4000-8000-000000000094")
	actor := util.MustParseUUID("10000000-0000-4000-8000-000000000095")
	q := db.New(tx)
	p := db.UpdateIssueStatusParams{ID: util.MustParseUUID(issueID), WorkspaceID: util.MustParseUUID(testWorkspaceID), Status: "in_progress", ActorType: pgtype.Text{String: "member", Valid: true}, ActorID: actor, ViaBotID: bot, ViaBotName: pgtype.Text{String: "Grok", Valid: true}}
	if _, err = q.UpdateIssueStatus(ctx, p); err != nil {
		t.Fatal(err)
	}
	var at, aid, bid, bn string
	if err = tx.QueryRow(ctx, `SELECT actor_type,actor_id,bot_id,bot_name FROM bot_source_probe`).Scan(&at, &aid, &bid, &bn); err != nil {
		t.Fatal(err)
	}
	if at != "member" || aid != util.UUIDToString(actor) || bid != util.UUIDToString(bot) || bn != "Grok" {
		t.Fatalf("trigger lost trusted writer: %q %q %q %q", at, aid, bid, bn)
	}
	if _, err = tx.Exec(ctx, `TRUNCATE bot_source_probe`); err != nil {
		t.Fatal(err)
	}
	if _, err = q.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: p.ID, WorkspaceID: p.WorkspaceID, Status: "todo"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `SELECT actor_type,actor_id,bot_id,bot_name FROM bot_source_probe`).Scan(&at, &aid, &bid, &bn); err != nil {
		t.Fatal(err)
	}
	if at != "" || aid != "" || bid != "" || bn != "" {
		t.Fatalf("system write reused Bot source: %q %q %q %q", at, aid, bid, bn)
	}
}
