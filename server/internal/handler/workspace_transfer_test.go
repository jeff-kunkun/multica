package handler

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
)

func transferReq(method, path, wsID string, body any) *http.Request {
	return testutil.WithURLParams(
		testutil.WithHeaders(testutil.JSONRequest(method, path, body), "X-User-ID", testUserID),
		"id", wsID,
	)
}

func TestTransferConfig_RejectsSecret(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	dry := false
	body := map[string]any{
		"dry_run":     dry,
		"on_conflict": "skip",
		"config": map[string]any{
			"format":         service.ConfigBundleFormat,
			"schema_version": 1,
			"bundle_id":      uuid.NewString(),
			"exported_at":    "2026-09-15T00:00:00Z",
			"source":         map[string]any{"workspace_id": uuid.NewString(), "slug": "x", "name": "x", "exported_by": testUserID},
			"entities": map[string]any{
				"agents": []map[string]any{{
					"source_id":  uuid.NewString(),
					"name":       "leaky-xfer",
					"custom_env": map[string]string{"K": "v"},
				}},
			},
		},
	}
	resp := testutil.Call(t, testHandler.ImportWorkspaceTransferConfig, transferReq("POST", "/api/workspaces/"+dst+"/transfer/config", dst, body))
	resp.Want(http.StatusBadRequest)
	if resp.Map()["code"] != "config_bundle_contains_secret" {
		t.Fatalf("code=%v body=%s", resp.Map()["code"], resp.Text())
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent WHERE workspace_id = $1 AND name = 'leaky-xfer'`, dst); n != 0 {
		t.Fatal("reject-secret wrote an agent")
	}
}

func TestTransferConfig_AgentActorForbidden(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	agentID := dbfx.Agent(t, "XferActor-"+uuid.NewString()[:6], "", testutil.Cols{"workspace_id": dst})
	dry := true
	req := transferReq("POST", "/api/workspaces/"+dst+"/transfer/config", dst, map[string]any{
		"dry_run": dry,
		"config": map[string]any{
			"format": service.ConfigBundleFormat, "schema_version": 1,
			"bundle_id": uuid.NewString(), "exported_at": "2026-09-15T00:00:00Z",
			"source":   map[string]any{"workspace_id": uuid.NewString(), "slug": "x", "name": "x", "exported_by": testUserID},
			"entities": map[string]any{},
		},
	})
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Actor-Source", "task_token")
	testutil.Call(t, testHandler.ImportWorkspaceTransferConfig, req).Want(http.StatusForbidden)
}

func TestTransferConversations_IdempotentAndNoTaskEnqueue(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	agentName := "XferBot-" + uuid.NewString()[:6]
	dbfx.Agent(t, agentName, "", testutil.Cols{"workspace_id": dst, "visibility": "workspace"})
	srcAgent := uuid.NewString()
	srcSess := uuid.NewString()
	srcMsg := uuid.NewString()
	dry := false
	body := map[string]any{
		"dry_run": dry,
		"refs": map[string]any{
			"agents": map[string]any{srcAgent: map[string]any{"name": agentName}},
		},
		"sessions": []map[string]any{{
			"source_id": srcSess, "agent_id": srcAgent, "title": "hello",
			"status": "active", "created_at": "2026-09-01T00:00:00Z", "updated_at": "2026-09-01T00:01:00Z",
		}},
		"messages": []map[string]any{{
			"source_id": srcMsg, "chat_session_id": srcSess, "role": "user",
			"message_kind": "message", "content": "hi", "created_at": "2026-09-01T00:00:01Z",
			"attachment_ids": []string{},
		}},
	}
	path := "/api/workspaces/" + dst + "/transfer/conversations"
	testutil.Call(t, testHandler.ImportWorkspaceTransferConversations, transferReq("POST", path, dst, body)).Want(http.StatusOK)
	testutil.Call(t, testHandler.ImportWorkspaceTransferConversations, transferReq("POST", path, dst, body)).Want(http.StatusOK)

	targetSess := service.TransferChatSessionID(dst, srcSess).String()
	if n := dbfx.Count(t, `SELECT count(*) FROM chat_session WHERE workspace_id = $1 AND id = $2`, dst, targetSess); n != 1 {
		t.Fatalf("sessions=%d want 1", n)
	}
	targetMsg := service.TransferChatMessageID(dst, srcMsg).String()
	if n := dbfx.Count(t, `SELECT count(*) FROM chat_message WHERE id = $1`, targetMsg); n != 1 {
		t.Fatalf("messages=%d want 1", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE chat_session_id = $1`, targetSess); n != 0 {
		t.Fatalf("task queue rows=%d want 0", n)
	}
	var created time.Time
	dbfx.QueryRow(t, `SELECT created_at FROM chat_session WHERE id = $1`, targetSess).Scan(&created)
	if created.UTC().Format("2006-01-02") != "2026-09-01" {
		t.Fatalf("created_at=%v, historical timestamp not preserved", created)
	}
}

func TestTransferConversations_TwoWorkspacesDoNotCollide(t *testing.T) {
	_, dstA := setupConfigWorkspaces(t)
	_, dstB := setupConfigWorkspaces(t)
	name := "SharedBot-" + uuid.NewString()[:6]
	dbfx.Agent(t, name, "", testutil.Cols{"workspace_id": dstA, "visibility": "workspace"})
	dbfx.Agent(t, name, "", testutil.Cols{"workspace_id": dstB, "visibility": "workspace"})
	srcAgent := uuid.NewString()
	srcSess := uuid.NewString()
	dry := false
	body := map[string]any{
		"dry_run": dry,
		"refs":    map[string]any{"agents": map[string]any{srcAgent: map[string]any{"name": name}}},
		"sessions": []map[string]any{{
			"source_id": srcSess, "agent_id": srcAgent, "title": "t",
			"status": "active", "created_at": "2026-09-01T00:00:00Z", "updated_at": "2026-09-01T00:00:00Z",
		}},
		"messages": []map[string]any{},
	}
	testutil.Call(t, testHandler.ImportWorkspaceTransferConversations, transferReq("POST", "/api/workspaces/"+dstA+"/transfer/conversations", dstA, body)).Want(http.StatusOK)
	testutil.Call(t, testHandler.ImportWorkspaceTransferConversations, transferReq("POST", "/api/workspaces/"+dstB+"/transfer/conversations", dstB, body)).Want(http.StatusOK)
	idA := service.TransferChatSessionID(dstA, srcSess).String()
	idB := service.TransferChatSessionID(dstB, srcSess).String()
	if idA == idB {
		t.Fatal("target ids collided across workspaces")
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM chat_session WHERE id = $1`, idA); n != 1 {
		t.Fatalf("dstA sessions=%d", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM chat_session WHERE id = $1`, idB); n != 1 {
		t.Fatalf("dstB sessions=%d", n)
	}
}

func TestTransferConversations_UnmappedAgentSkipped(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	srcSess := uuid.NewString()
	srcAgent := uuid.NewString()
	dry := false
	resp := testutil.Call(t, testHandler.ImportWorkspaceTransferConversations, transferReq("POST", "/api/workspaces/"+dst+"/transfer/conversations", dst, map[string]any{
		"dry_run": dry,
		"refs":    map[string]any{"agents": map[string]any{srcAgent: map[string]any{"name": "does-not-exist"}}},
		"sessions": []map[string]any{{
			"source_id": srcSess, "agent_id": srcAgent, "title": "t",
			"status": "active", "created_at": "2026-09-01T00:00:00Z", "updated_at": "2026-09-01T00:00:00Z",
		}},
		"messages": []map[string]any{{
			"source_id": uuid.NewString(), "chat_session_id": srcSess, "role": "user",
			"content": "hi", "created_at": "2026-09-01T00:00:01Z", "attachment_ids": []string{},
		}},
	})).Want(http.StatusOK)
	var report service.TransferConversationsReport
	resp.JSON(&report)
	if report.SessionsSkipped != 1 {
		t.Fatalf("sessions_skipped=%d body=%s", report.SessionsSkipped, resp.Text())
	}
	if len(report.AgentUnmapped) != 1 {
		t.Fatalf("agent_unmapped=%d", len(report.AgentUnmapped))
	}
	targetSess := service.TransferChatSessionID(dst, srcSess).String()
	if n := dbfx.Count(t, `SELECT count(*) FROM chat_session WHERE id = $1`, targetSess); n != 0 {
		t.Fatal("unmapped agent still created a session")
	}
}

func TestTransferAttachment_Idempotent(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	agentName := "AttBot-" + uuid.NewString()[:6]
	dbfx.Agent(t, agentName, "", testutil.Cols{"workspace_id": dst, "visibility": "workspace"})
	srcAgent := uuid.NewString()
	srcSess := uuid.NewString()
	srcMsg := uuid.NewString()
	srcAtt := uuid.NewString()
	dry := false
	testutil.Call(t, testHandler.ImportWorkspaceTransferConversations, transferReq("POST", "/api/workspaces/"+dst+"/transfer/conversations", dst, map[string]any{
		"dry_run": dry,
		"refs":    map[string]any{"agents": map[string]any{srcAgent: map[string]any{"name": agentName}}},
		"sessions": []map[string]any{{
			"source_id": srcSess, "agent_id": srcAgent, "title": "t",
			"status": "active", "created_at": "2026-09-01T00:00:00Z", "updated_at": "2026-09-01T00:00:00Z",
		}},
		"messages": []map[string]any{{
			"source_id": srcMsg, "chat_session_id": srcSess, "role": "user",
			"content": "hi", "created_at": "2026-09-01T00:00:01Z", "attachment_ids": []string{srcAtt},
		}},
	})).Want(http.StatusOK)

	orig := testHandler.Storage
	store := &mockStorage{}
	testHandler.Storage = store
	defer func() { testHandler.Storage = orig }()

	meta := service.TransferAttachmentMeta{
		SourceID: srcAtt, ChatSessionID: &srcSess, ChatMessageID: &srcMsg,
		Filename: "note.txt", ContentType: "text/plain", SizeBytes: 5,
		CreatedAt: "2026-09-01T00:00:01Z",
	}
	metaJSON, _ := json.Marshal(meta)
	makeAttReq := func() *http.Request {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("meta", string(metaJSON))
		part, _ := mw.CreateFormFile("file", "note.txt")
		_, _ = part.Write([]byte("hello"))
		_ = mw.Close()
		req := httptest.NewRequest("POST", "/api/workspaces/"+dst+"/transfer/attachments", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		return testutil.WithURLParams(testutil.WithHeaders(req, "X-User-ID", testUserID), "id", dst)
	}
	testutil.Call(t, testHandler.ImportWorkspaceTransferAttachment, makeAttReq()).Want(http.StatusOK)
	testutil.Call(t, testHandler.ImportWorkspaceTransferAttachment, makeAttReq()).Want(http.StatusOK)
	target := service.TransferAttachmentID(dst, srcAtt).String()
	if n := dbfx.Count(t, `SELECT count(*) FROM attachment WHERE id = $1`, target); n != 1 {
		t.Fatalf("attachments=%d want 1", n)
	}
}

func TestTransferIDsMatchService(t *testing.T) {
	ws := uuid.NewString()
	src := uuid.NewString()
	if service.TransferChatSessionID(ws, src) == service.TransferChatMessageID(ws, src) {
		t.Fatal("namespaces collided")
	}
	_ = util.MustParseUUID(ws)
}
