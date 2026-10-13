package mirror

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestPGSourceReadsWorkspace(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping live-Postgres mirror test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	f := testutil.New(pool, "", "")
	userID := f.User(t, "kunkunya", "mirror-test@multica.ai")
	wsID := f.Workspace(t, "Mirror", "mirror-test", testutil.Cols{"issue_prefix": "MIR"})
	f = testutil.New(pool, wsID, userID)
	f.Member(t, wsID, userID, "owner")
	runtimeID := f.Runtime(t, "rt")
	agentID := f.Agent(t, "孙悟空", runtimeID)
	projectID := f.Project(t, "魔改", testutil.Cols{"description": "d"})
	parentID := f.Issue(t, "父票", testutil.Cols{"project_id": projectID, "assignee_type": "agent", "assignee_id": agentID})
	childID := f.Issue(t, "子票", testutil.Cols{"parent_issue_id": parentID, "assignee_type": "member", "assignee_id": userID})
	commentID := f.Comment(t, parentID, "hi", testutil.Cols{"author_type": "agent", "author_id": agentID})
	goneID := f.Comment(t, parentID, "gone", testutil.Cols{"deleted_at": time.Now()})

	src, err := NewPGSource(ctx, pool, "mirror-test", "t1")
	if err != nil {
		t.Fatal(err)
	}
	v, err := src.Versions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(v[KindProject]) != 1 || len(v[KindIssue]) != 2 || len(v[KindComment]) != 1 {
		t.Fatalf("versions = %v", v)
	}
	if _, ok := v[KindComment][goneID]; ok {
		t.Fatal("soft-deleted comment listed")
	}
	p, err := src.Project(ctx, projectID)
	if err != nil || p.Title != "魔改" || p.Description != "d" {
		t.Fatalf("project = %+v, %v", p, err)
	}
	parent, err := src.Issue(ctx, parentID)
	if err != nil || parent.Assignee != "孙悟空" || parent.ProjectID != projectID || parent.ParentID != "" || parent.Identifier[:4] != "MIR-" {
		t.Fatalf("parent = %+v, %v", parent, err)
	}
	child, err := src.Issue(ctx, childID)
	if err != nil || child.Assignee != "kunkunya" || child.ParentID != parentID {
		t.Fatalf("child = %+v, %v", child, err)
	}
	c, err := src.Comment(ctx, commentID)
	if err != nil || c.Author != "孙悟空" || c.Engine != "handler_test_runtime" || c.IssueID != parentID || c.Content != "hi" {
		t.Fatalf("comment = %+v, %v", c, err)
	}

	at := time.Now().UTC().Truncate(time.Microsecond)
	if err := src.SaveLink(ctx, Link{Kind: KindIssue, LocalID: parentID, RemoteID: "r1", SourceUpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := src.SaveLink(ctx, Link{Kind: KindIssue, LocalID: parentID, RemoteID: "r2", SourceUpdatedAt: at, Inbound: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM mirror_link WHERE target = 't1'`) })
	links, err := src.Links(ctx)
	if err != nil || len(links) != 1 || links[0].RemoteID != "r2" || !links[0].SourceUpdatedAt.Equal(at) || !links[0].Inbound {
		t.Fatalf("links = %+v, %v", links, err)
	}
	other, _ := NewPGSource(ctx, pool, wsID, "t2")
	if l, _ := other.Links(ctx); len(l) != 0 {
		t.Fatalf("links leaked across targets: %+v", l)
	}
	if err := src.SaveLink(ctx, Link{Kind: kindCursor, LocalID: cursorID, SourceUpdatedAt: at}); err != nil {
		t.Fatalf("cursor row: %v", err)
	}
	if err := src.DeleteLink(ctx, kindCursor, cursorID); err != nil {
		t.Fatal(err)
	}
	if err := src.DeleteLink(ctx, KindIssue, parentID); err != nil {
		t.Fatal(err)
	}
	if l, _ := src.Links(ctx); len(l) != 0 {
		t.Fatalf("link not deleted: %+v", l)
	}
}
