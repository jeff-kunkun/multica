package workspacelink

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestManagedChatOwnSessionsOnly(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	link := w.link(t, w.shared)
	w.setManaged(t, link, true)
	// The originator is a member of both, unlike the old standalone delivery.
	w.fx.Member(t, util.UUIDToString(w.source), util.UUIDToString(w.vOwner), "member")
	run := w.run(t, w.vOwner, "running")
	fx := testutil.New(w.fx.Pool, util.UUIDToString(w.source), util.UUIDToString(w.vOwner))
	runtime := fx.Runtime(t, "chat-rt")
	agent := fx.Agent(t, "chat-agent", runtime)
	own := fx.ChatSession(t, agent)
	other := fx.ChatSession(t, agent, testutil.Cols{"creator_id": util.UUIDToString(w.srcOwner), "visibility": "workspace"})
	token := map[string]string{"X-Actor-Source": "task_token", "X-Workspace-ID": util.UUIDToString(w.viewer), "X-Agent-ID": util.UUIDToString(run.agent), "X-Task-ID": util.UUIDToString(run.task), LinkedWorkspaceHeader: w.sourceSlug}
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/chat/directory"}, {"GET", "/api/chat/sessions/" + own}, {"GET", "/api/chat/sessions/" + own + "/messages"}, {"POST", "/api/chat/sessions/" + own + "/messages"},
	} {
		rec, headers := gateRequest(t, w.svc, c.method, c.path, token, 200, `{}`)
		if rec.Code != 200 || headers.user != util.UUIDToString(w.vOwner) {
			t.Fatalf("%s %s: %d", c.method, c.path, rec.Code)
		}
	}
	if n := fx.Count(t, `SELECT count(*) FROM workspace_link_audit WHERE link_id=$1 AND action='managed_write' AND detail->>'chat_id'=$2`, link, own); n != 1 {
		t.Fatalf("chat audit=%d", n)
	}
	for _, path := range []string{"/api/chat/sessions/" + other, "/api/chat/sessions/" + other + "/messages", "/api/chat/sessions/missing/messages"} {
		rec, _ := gateRequest(t, w.svc, http.MethodGet, path, token, 200, `{}`)
		if rec.Code != 403 {
			t.Fatalf("non-owned GET %s=%d", path, rec.Code)
		}
	}
	if rec, _ := gateRequest(t, w.svc, "POST", "/api/chat/sessions/"+other+"/messages", token, 200, `{}`); rec.Code != 403 {
		t.Fatal("other chat writable")
	}
	w.setManaged(t, link, false)
	if rec, _ := gateRequest(t, w.svc, "POST", "/api/chat/sessions/"+own+"/messages", token, 200, `{}`); rec.Code != 403 {
		t.Fatal("managed off writable")
	}
	w.setManaged(t, link, true)
	if err := w.svc.Revoke(ctx, w.source, w.srcOwner, owner(), link); err != nil {
		t.Fatal(err)
	}
	if rec, _ := gateRequest(t, w.svc, "POST", "/api/chat/sessions/"+own+"/messages", token, 200, `{}`); rec.Code != 403 {
		t.Fatal("revoked writable")
	}
}

func TestNewLinkManagedDefaultsWithoutChangingOldRows(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	old, err := w.svc.q.CreateWorkspaceLink(ctx, db.CreateWorkspaceLinkParams{SourceWorkspaceID: w.source2, TargetWorkspaceID: w.viewer, CreatedBy: w.src2Owner})
	if err != nil {
		t.Fatal(err)
	}
	created, err := w.svc.Create(ctx, w.source, w.srcOwner, owner(), w.viewerSlug, []pgtype.UUID{w.shared})
	if err != nil || !created.Managed {
		t.Fatalf("new default=%+v %v", created, err)
	}
	oldAfter, err := w.svc.q.GetWorkspaceLink(ctx, old.ID)
	if err != nil || oldAfter.Managed {
		t.Fatal("old row changed")
	}
	if err := w.svc.Revoke(ctx, w.source, w.srcOwner, owner(), uid(t, created.ID)); err != nil {
		t.Fatal(err)
	}
	off, err := w.svc.Create(ctx, w.source, w.srcOwner, owner(), w.viewerSlug, []pgtype.UUID{w.shared}, false)
	if err != nil || off.Managed {
		t.Fatalf("explicit false=%+v %v", off, err)
	}
}
