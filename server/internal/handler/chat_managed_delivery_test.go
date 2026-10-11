package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workspacelink"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestManagedChatSendOwnSourceChat(t *testing.T) {
	ctx := context.Background()
	tag := uuid.NewString()[:8]
	source := dbfx.Workspace(t, "managed source", "managed-src-"+tag)
	viewer := dbfx.Workspace(t, "managed viewer", "managed-view-"+tag)
	owner := dbfx.User(t, "managed originator", "managed-owner-"+tag+"@test.invalid")
	other := dbfx.User(t, "other owner", "managed-other-"+tag+"@test.invalid")
	dbfx.Member(t, source, owner, "owner")
	dbfx.Member(t, viewer, owner, "owner")
	dbfx.Member(t, source, other, "member")
	srcRT := dbfx.Runtime(t, "managed-src-"+tag, testutil.Cols{"workspace_id": source, "owner_id": owner})
	viewRT := dbfx.Runtime(t, "managed-view-"+tag, testutil.Cols{"workspace_id": viewer, "owner_id": owner})
	srcAgent := dbfx.Agent(t, "receiver", srcRT, testutil.Cols{"workspace_id": source, "owner_id": owner})
	viewAgent := dbfx.Agent(t, "sender", viewRT, testutil.Cols{"workspace_id": viewer, "owner_id": owner})
	chat := dbfx.ChatSession(t, srcAgent, testutil.Cols{"workspace_id": source, "creator_id": owner})
	otherChat := dbfx.ChatSession(t, srcAgent, testutil.Cols{"workspace_id": source, "creator_id": other, "visibility": "project", "explicitly_created_at": testutil.Raw("now()")})
	dbfx.Exec(t, `INSERT INTO resource_share (workspace_id,resource_type,resource_id,member_id,added_by,access) VALUES ($1,'chat_session',$2,$3,$4,'speak')`, source, otherChat, owner, other)
	task := dbfx.Task(t, viewAgent, testutil.Cols{"runtime_id": viewRT, "status": "running", "originator_user_id": owner, "accountable_user_id": owner, "originator_source": "direct_human"})
	project := dbfx.Project(t, "Managed project", testutil.Cols{"workspace_id": source, "visibility": "workspace"})
	srcCall := linkCaller{ws: source, user: owner}
	rec := srcCall.do(t, http.MethodPost, "/api/workspace-links", fmt.Sprintf(`{"target_slug":%q,"project_ids":[%q]}`, "managed-view-"+tag, project))
	if rec.Code != 201 {
		t.Fatalf("create %d %s", rec.Code, rec.Body)
	}
	linkID := decodeField(t, rec, "id")
	linkPath := "/api/workspace-links/" + linkID
	if w := (linkCaller{ws: viewer, user: owner}).do(t, http.MethodPatch, linkPath, `{"accept":true}`); w.Code != 200 {
		t.Fatalf("accept %d %s", w.Code, w.Body)
	}
	router := chi.NewRouter()
	router.Use(testHandler.workspaceLinks().Managed)
	router.Use(middleware.GuestReadOnly(testHandler.Queries))
	router.Use(middleware.RequireWorkspaceMember(testHandler.Queries))
	router.Post("/api/chat/sessions/{sessionId}/messages", testHandler.SendChatMessage)
	router.Get("/api/chat/sessions/{sessionId}/messages", testHandler.ListChatMessages)
	router.Get("/api/chat/directory", testHandler.ListChatDirectory)
	send := func(dest, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/chat/sessions/"+dest+"/messages", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Workspace-ID", viewer)
		r.Header.Set("X-User-ID", owner)
		r.Header.Set("X-Actor-Source", "task_token")
		r.Header.Set("X-Agent-ID", viewAgent)
		r.Header.Set("X-Task-ID", task)
		r.Header.Set(workspacelink.LinkedWorkspaceHeader, source)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := srcCall.do(t, http.MethodPatch, linkPath, `{"managed":false}`); w.Code != 200 {
		t.Fatalf("disable %d %s", w.Code, w.Body)
	}
	if w := send(chat, `{"content":"hello"}`); w.Code != 403 {
		t.Fatalf("disabled %d %s", w.Code, w.Body)
	}
	if w := srcCall.do(t, http.MethodPatch, linkPath, `{"managed":true}`); w.Code != 200 {
		t.Fatalf("enable %d %s", w.Code, w.Body)
	}
	if w := send(otherChat, `{"content":"hello"}`); w.Code != 403 {
		t.Fatalf("other creator %d %s", w.Code, w.Body)
	}
	if w := send(chat, `{"content":"hello","mode":"steer"}`); w.Code != 400 {
		t.Fatalf("steer %d %s", w.Code, w.Body)
	}
	w := send(chat, `{"content":"hello from viewer","mode":"queue"}`)
	if w.Code != 201 {
		t.Fatalf("managed send %d %s", w.Code, w.Body)
	}
	messageID := decodeField(t, w, "message_id")
	if !strings.Contains(w.Body.String(), `"message_kind":"linked_delivery"`) {
		t.Fatalf("response kind %s", w.Body)
	}
	rows, err := testHandler.Queries.ListChatMessages(ctx, util.MustParseUUID(chat))
	if err != nil || len(rows) != 1 {
		t.Fatalf("messages %+v %v", rows, err)
	}
	m := rows[0]
	if util.UUIDToString(m.ID) != messageID || m.MessageKind != protocol.ChatMessageKindLinkedDelivery || m.SenderUserID.Valid || !m.TaskID.Valid || !strings.HasPrefix(m.Content, "managed originator 经 managed viewer·sender 代发") {
		t.Fatalf("provenance %+v", m)
	}
	wake, err := testHandler.Queries.GetAgentTask(ctx, m.TaskID)
	if err != nil || wake.OriginatorUserID != util.MustParseUUID(owner) || wake.OriginatorSource.String != "workspace_link_delivery" || wake.ChatInputTaskID != wake.ID {
		t.Fatalf("wake %+v %v", wake, err)
	}
	sessions, err := testHandler.Queries.ListChatSessionsByCreator(ctx, db.ListChatSessionsByCreatorParams{WorkspaceID: util.MustParseUUID(source), ViewerID: util.MustParseUUID(owner)})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range sessions {
		if row.ID == m.ChatSessionID {
			found = true
			if row.UnreadCount != 1 {
				t.Fatalf("creator unread=%d", row.UnreadCount)
			}
		}
	}
	if !found {
		t.Fatal("chat absent")
	}
	// Sharing normally admits this source chat; the managed directory narrows
	// it to chats the originator started, never shared conversations.
	for _, managed := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodGet, "/api/chat/directory?all_projects=true", nil)
		req.Header.Set("X-User-ID", owner)
		req.Header.Set("X-Workspace-ID", source)
		if managed {
			req.Header.Set("X-Workspace-ID", viewer)
			req.Header.Set("X-Actor-Source", "task_token")
			req.Header.Set("X-Agent-ID", viewAgent)
			req.Header.Set("X-Task-ID", task)
			req.Header.Set(workspacelink.LinkedWorkspaceHeader, source)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != 200 || !strings.Contains(response.Body.String(), chat) {
			t.Fatalf("directory managed=%v: %d %s", managed, response.Code, response.Body)
		}
		if strings.Contains(response.Body.String(), otherChat) == managed {
			t.Fatalf("directory other shared chat managed=%v: %s", managed, response.Body)
		}
	}
	read := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/"+chat+"/messages", nil)
	read.Header.Set("X-User-ID", owner)
	read.Header.Set("X-Workspace-ID", viewer)
	read.Header.Set("X-Actor-Source", "task_token")
	read.Header.Set("X-Agent-ID", viewAgent)
	read.Header.Set("X-Task-ID", task)
	read.Header.Set(workspacelink.LinkedWorkspaceHeader, source)
	readResponse := httptest.NewRecorder()
	router.ServeHTTP(readResponse, read)
	if readResponse.Code != 200 || !strings.Contains(readResponse.Body.String(), "EXTERNAL PASTED CONTENT") {
		t.Fatalf("managed read lost external trust: %d %s", readResponse.Code, readResponse.Body)
	}
	var audits int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM workspace_link_audit WHERE link_id=$1 AND action='managed_write' AND detail->>'route'='chat.send'`, linkID).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit count=%d %v", audits, err)
	}
}
