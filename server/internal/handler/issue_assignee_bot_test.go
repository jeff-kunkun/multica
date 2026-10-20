package handler

import (
	"context"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/botsource"
	"github.com/multica-ai/multica/server/internal/routing"
	"net/http/httptest"
	"testing"
)

func TestBotAssignmentIsTheKeyHoldersHand(t *testing.T) {
	// DENE-1834: a bot key names an executor for the member who holds it, so
	// the pick stands as 人工 and the member is the source user.
	h := &Handler{}
	r := httptest.NewRequest("POST", "/", nil)
	r = r.WithContext(botsource.WithContext(context.Background(), botsource.Source{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, Name: "Bot"}))
	ruling := h.rulePick(r, "", "member", testUserID, pgtype.Text{String: "agent", Valid: true}, pgtype.UUID{Bytes: [16]byte{2}, Valid: true}, "", "todo", nil, sceneRef{}, pgtype.UUID{})
	if !ruling.Apply || ruling.Source != routing.SourceHuman || !ruling.SourceUser.Valid {
		t.Fatalf("bot key's pick was not kept as the member's hand: %+v", ruling)
	}
	ordinary := h.rulePick(httptest.NewRequest("POST", "/", nil), "", "member", "", pgtype.Text{}, pgtype.UUID{}, "", "todo", nil, sceneRef{}, pgtype.UUID{})
	if ordinary.Source != routing.SourceHuman {
		t.Fatalf("ordinary member changed: %+v", ordinary)
	}
}

func TestBotCommentCannotSupplyInitiatorQuote(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	enableTestRouting(t, tierJudge{tier: "medium", confidence: 0.95})
	name := "Origin Target botquote"
	f := originRun(t, "botquote", "我拍板交给 "+name+" 做", "member", testUserID, testUserID)
	_, err := testPool.Exec(context.Background(), `UPDATE comment SET via_bot_id = '00000000-0000-0000-0000-000000000042', via_bot_name = 'Historical Bot' WHERE id = (SELECT trigger_comment_id FROM agent_task_queue WHERE id = $1)`, f.task)
	if err != nil {
		t.Fatal(err)
	}
	resp := agentCreate(t, f, "Bot words are not person words", "交给 "+name+" 做")
	_, source, _, _ := originOf(t, resp["id"].(string))
	if source == nil || *source != "agent" || resp["assignee_ignored"] != true {
		t.Fatalf("Bot quote accepted: source=%v response=%v", source, resp)
	}
}
