package handler

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/botsource"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestBotWakeReportScopesBotAuditAndRetainsDeletedTargets(t *testing.T) {
	requireDB(t)
	f := newBotChainChatFixture(t)
	other := newBotChainChatFixture(t)
	root := f.session
	privateOwner := dbfx.User(t, "Private audit owner", "private-audit-owner@example.invalid")
	privateChat := dbfx.ChatSession(t, f.agent, testutil.Cols{"workspace_id": testWorkspaceID, "creator_id": privateOwner, "visibility": "private"})
	privateBlock := dbfx.Insert(t, "bot_wake_block", testutil.Cols{"workspace_id": testWorkspaceID, "bot_id": f.bot.ID, "agent_id": f.agent, "root_id": privateChat, "chat_session_id": privateChat, "limit_value": 4})
	own := dbfx.Insert(t, "bot_wake_block", testutil.Cols{"workspace_id": testWorkspaceID, "bot_id": f.bot.ID, "agent_id": f.agent, "root_id": root, "chat_session_id": f.session, "limit_value": 4})
	hidden := dbfx.Insert(t, "bot_wake_block", testutil.Cols{"workspace_id": testWorkspaceID, "bot_id": other.bot.ID, "agent_id": other.agent, "root_id": other.session, "chat_session_id": other.session, "limit_value": 4})
	dbfx.Insert(t, "bot_wake_block", testutil.Cols{"workspace_id": testWorkspaceID, "root_id": root, "limit_value": 4})
	r := newRequest("GET", "/overview", nil)
	r = r.WithContext(botsource.WithContext(r.Context(), botsource.Source{ID: f.bot.ID, Name: f.bot.Name}))
	got := testHandler.botWakeChainReport(r, testWorkspaceID)
	if !got.AuditVisible || got.AuditReadFailed || got.BlockedLast24h == nil || *got.BlockedLast24h != 1 || len(got.RecentBlocks) != 1 || got.RecentBlocks[0].ID != own {
		t.Fatalf("Bot audit scope = %+v", got)
	}
	for _, record := range got.RecentBlocks {
		if record.ID == hidden || record.ID == privateBlock {
			t.Fatal("other Bot's block disclosed")
		}
	}
	// An ordinary task without Bot provenance cannot inherit its owner's admin
	// access to private chat identifiers in the audit.
	r = newRequest("GET", "/overview", nil)
	r.Header.Set("X-Actor-Source", "task_token")
	r = r.WithContext(botsource.WithContext(r.Context(), botsource.Source{ID: f.bot.ID, Name: f.bot.Name}))
	got = testHandler.botWakeChainReport(r, testWorkspaceID)
	if got.AuditVisible || got.BlockedLast24h != nil || len(got.RecentBlocks) != 0 {
		t.Fatalf("ordinary task read audit: %+v", got)
	}
	// NULL Bot/target references are historical records, not a scan failure.
	r = newRequest("GET", "/overview", nil)
	member, err := testHandler.getWorkspaceMember(context.Background(), testUserID, testWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if roleAllowed(member.Role, "owner", "admin") {
		got = testHandler.botWakeChainReport(r, testWorkspaceID)
		if !got.AuditVisible || got.AuditReadFailed || len(got.RecentBlocks) < 3 {
			t.Fatalf("admin historical audit = %+v", got)
		}
		for _, record := range got.RecentBlocks {
			if record.ID == privateBlock {
				t.Fatal("admin audit exposed another person’s private chat")
			}
		}
	}
}
