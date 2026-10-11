package handler

import (
	"context"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/botsource"
	"github.com/multica-ai/multica/server/internal/routing"
	"net/http/httptest"
	"testing"
)

func TestBotAssignmentIsNotHumanOverride(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest("POST", "/", nil)
	r = r.WithContext(botsource.WithContext(context.Background(), botsource.Source{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, Name: "Bot"}))
	ruling := h.rulePick(r, "", "member", "", pgtype.Text{}, pgtype.UUID{}, "", "todo", nil, sceneRef{})
	if ruling.Source != routing.SourceAgent || ruling.SourceUser.Valid {
		t.Fatalf("Bot assignment impersonated person: %+v", ruling)
	}
	ordinary := h.rulePick(httptest.NewRequest("POST", "/", nil), "", "member", "", pgtype.Text{}, pgtype.UUID{}, "", "todo", nil, sceneRef{})
	if ordinary.Source != routing.SourceHuman {
		t.Fatalf("ordinary member changed: %+v", ordinary)
	}
}

func TestBotCommentCannotSupplyInitiatorQuote(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	enableDraftSuggestRouting(t, tierJudge{tier: "medium", confidence: 0.95})
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
