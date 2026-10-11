package workspacelink

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
)

func TestViewerUpdatesProjectsAndAuditPagination(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	link := w.link(t, w.shared)
	ids := []pgtype.UUID{w.unticked}
	_, err := w.svc.Update(ctx, w.viewer, w.vAdmin, admin(), link, Patch{ProjectIDs: &ids})
	wantStatus(t, err, 403)
	w.fx.Member(t, util.UUIDToString(w.source), util.UUIDToString(w.vAdmin), "owner")
	updated, err := w.svc.Update(ctx, w.viewer, w.vAdmin, admin(), link, Patch{ProjectIDs: &ids})
	if err != nil || len(updated.Projects) != 1 || updated.Projects[0].ID != util.UUIDToString(w.unticked) {
		t.Fatalf("update: %+v %v", updated, err)
	}
	if n := w.fx.Count(t, `SELECT count(*) FROM workspace_link_audit WHERE link_id=$1 AND action='update_projects'`, link); n != 2 {
		t.Fatalf("audit rows=%d", n)
	}
	private := []pgtype.UUID{w.private}
	_, err = w.svc.Update(ctx, w.viewer, w.vAdmin, admin(), link, Patch{ProjectIDs: &private})
	wantStatus(t, err, 400)
	// One-row pages include both rows even though they share a transaction timestamp.
	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := w.svc.AuditPage(ctx, w.viewer, owner(), Page{Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Entries {
			if seen[e.ID] {
				t.Fatal("duplicate cursor row")
			}
			seen[e.ID] = true
			if e.PeerWorkspaceName != "source" {
				t.Fatalf("peer=%q", e.PeerWorkspaceName)
			}
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if len(seen) != 4 {
		t.Fatalf("audit count=%d", len(seen))
	}
	_, err = w.svc.AuditPage(ctx, w.viewer, owner(), Page{Cursor: "garbage"})
	wantStatus(t, err, 400)
}

func TestPendingLinkCannotEnableManaged(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	// Explicitly off: new offers default on, but an off pending offer still
	// cannot enable managed access before acceptance.
	l, err := w.svc.Create(ctx, w.source, w.srcOwner, owner(), w.viewerSlug, []pgtype.UUID{w.shared}, false)
	if err != nil {
		t.Fatal(err)
	}
	on := true
	_, err = w.svc.Update(ctx, w.source, w.srcOwner, owner(), uid(t, l.ID), Patch{Managed: &on})
	wantStatus(t, err, 400)
	if w.canSet(t, w.source, w.srcOwner, owner()) {
		t.Fatal("pending managed switch enabled")
	}
}
