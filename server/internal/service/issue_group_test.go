package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/entitlement"
	"github.com/multica-ai/multica/server/internal/entitlement/entitlementtest"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// CreateGroup is the one place a parent and its children are written together.
// These tests are all database-backed on purpose: "the group commits as a unit"
// is a statement about what the transaction did, and a mocked query layer could
// only assert that the test's own expectations were called in order.

func groupNode(title string, workspaceID, creatorID pgtype.UUID, originID uuid.UUID) IssueGroupNode {
	return IssueGroupNode{Params: IssueCreateParams{
		WorkspaceID:    workspaceID,
		Title:          title,
		Status:         "todo",
		Priority:       "none",
		CreatorType:    "member",
		CreatorID:      creatorID,
		OriginType:     pgtype.Text{String: "issue_draft", Valid: true},
		OriginID:       pgtype.UUID{Bytes: originID, Valid: true},
		AllowDuplicate: true,
	}}
}

func countIssues(t *testing.T, pool *pgxpool.Pool, workspaceID string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM issue WHERE workspace_id = $1`, workspaceID).Scan(&count); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	return count
}

func countIssuesWithOrigins(t *testing.T, pool *pgxpool.Pool, workspaceID string, origins []uuid.UUID) int {
	t.Helper()
	raw := make([]string, 0, len(origins))
	for _, origin := range origins {
		raw = append(raw, origin.String())
	}
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM issue WHERE workspace_id = $1 AND origin_id = ANY($2::uuid[])`,
		workspaceID, raw).Scan(&count); err != nil {
		t.Fatalf("count group issues: %v", err)
	}
	return count
}

// The headline guarantee: one call writes the parent and every child into one
// commit, linked by parent_issue_id and by nothing else (no foreign key).
func TestCreateGroupCommitsRootAndChildrenTogether(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	queries := db.New(pool)
	workspaceIDString, userIDString, _, _ := seedAttributionFixture(t, pool)
	workspaceID := util.MustParseUUID(workspaceIDString)
	userID := util.MustParseUUID(userIDString)

	rootOrigin := uuid.New()
	childOrigins := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	origins := append([]uuid.UUID{rootOrigin}, childOrigins...)

	titles := []string{"group root", "group child 1", "group child 2", "group child 3"}
	nodes := make([]IssueGroupNode, 0, len(titles))
	for i, title := range titles {
		nodes = append(nodes, groupNode(title, workspaceID, userID, origins[i]))
	}

	svc := NewIssueService(queries, pool, nil, nil, nil)
	result, err := svc.CreateGroup(ctx, IssueGroupParams{Nodes: nodes})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if len(result.Issues) != 4 {
		t.Fatalf("group has %d issues, want 4", len(result.Issues))
	}

	root := result.Issues[0]
	if util.UUIDToString(root.OriginID) != rootOrigin.String() {
		t.Fatalf("root origin_id = %s, want the caller's root origin %s",
			util.UUIDToString(root.OriginID), rootOrigin)
	}

	// Every row is visible to a connection that never saw the transaction.
	if got := countIssuesWithOrigins(t, pool, workspaceIDString, origins); got != 4 {
		t.Fatalf("committed group has %d rows visible outside the transaction, want 4", got)
	}

	// Children hang off the root by application code, and each keeps its own
	// origin: sharing one origin_id would make GetIssueByOrigin (LIMIT 1)
	// return an arbitrary row of the group.
	seen := map[string]bool{}
	for i, child := range result.Issues[1:] {
		if child.ParentIssueID != root.ID {
			t.Fatalf("child %d parent_issue_id = %s, want the root %s",
				i, util.UUIDToString(child.ParentIssueID), util.UUIDToString(root.ID))
		}
		if util.UUIDToString(child.OriginID) != childOrigins[i].String() {
			t.Fatalf("child %d origin_id = %s, want %s", i, util.UUIDToString(child.OriginID), childOrigins[i])
		}
		if seen[util.UUIDToString(child.OriginID)] {
			t.Fatalf("child %d reused an origin_id; every node needs its own", i)
		}
		seen[util.UUIDToString(child.OriginID)] = true
	}

	// Numbers are allocated per node inside the transaction, in payload order,
	// and that is the order ListChildIssues reads the group back in.
	for i := 1; i < len(result.Issues); i++ {
		if result.Issues[i].Number <= result.Issues[i-1].Number {
			t.Fatalf("issue numbers are not ascending in payload order: %d then %d",
				result.Issues[i-1].Number, result.Issues[i].Number)
		}
	}
	readBack := make([]db.Issue, 0, len(titles))
	readBack = append(readBack, root)
	children, err := queries.ListChildIssues(ctx, root.ID)
	if err != nil {
		t.Fatalf("read group back: %v", err)
	}
	readBack = append(readBack, children...)
	if len(readBack) != len(titles) {
		t.Fatalf("reading the group back returned %d issues, want %d", len(readBack), len(titles))
	}
	for i, title := range titles {
		if readBack[i].Title != title {
			t.Fatalf("group read back in a different order: position %d is %q, want %q",
				i, readBack[i].Title, title)
		}
	}
}

// A group is all-or-nothing. The failure is injected on the LAST node so the
// root and the earlier children have already been inserted when it happens:
// what the assertions look at is whether they were rolled back.
func TestCreateGroupRollsBackEveryNodeWhenOneFails(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	queries := db.New(pool)
	workspaceIDString, userIDString, _, _ := seedAttributionFixture(t, pool)
	workspaceID := util.MustParseUUID(workspaceIDString)
	userID := util.MustParseUUID(userIDString)

	before := countIssues(t, pool, workspaceIDString)

	origins := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	nodes := make([]IssueGroupNode, 0, len(origins))
	for i, origin := range origins {
		nodes = append(nodes, groupNode(fmt.Sprintf("doomed group %d", i), workspaceID, userID, origin))
	}
	// A project from nowhere: createInTx refuses it, and it only reaches that
	// check after the earlier nodes have already been written.
	nodes[3].Params.ProjectID = pgtype.UUID{Bytes: uuid.New(), Valid: true}

	svc := NewIssueService(queries, pool, nil, nil, nil)
	if _, err := svc.CreateGroup(ctx, IssueGroupParams{Nodes: nodes}); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("CreateGroup with a foreign project = %v, want ErrProjectNotFound", err)
	}

	if got := countIssuesWithOrigins(t, pool, workspaceIDString, origins); got != 0 {
		t.Fatalf("%d of the group's issues survived the rollback; a half-created group is worse than none", got)
	}
	if after := countIssues(t, pool, workspaceIDString); after != before {
		t.Fatalf("workspace issue count went %d -> %d across a rolled-back group", before, after)
	}
}

// The quota is checked per node inside the group transaction, so a group that
// only half fits must produce nothing at all.
func TestCreateGroupCreatesNothingWhenTheLimitCannotCoverIt(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	queries := db.New(pool)
	workspaceIDString, userIDString, _, _ := seedAttributionFixture(t, pool)
	workspaceID := util.MustParseUUID(workspaceIDString)
	userID := util.MustParseUUID(userIDString)

	// Room for two, asking for four.
	limit := countIssues(t, pool, workspaceIDString) + 2
	stub := entitlementtest.New()
	stub.Set(uuid.UUID(workspaceID.Bytes), entitlement.GateIssueCount, entitlement.Decision{
		Gate:           entitlement.Gate{Action: entitlement.ActionEnforce, Limit: &limit},
		PolicyRevision: 21,
	})
	svc := NewIssueService(queries, pool, nil, nil, nil)
	svc.Entitlements = stub

	origins := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	nodes := make([]IssueGroupNode, 0, len(origins))
	for i, origin := range origins {
		nodes = append(nodes, groupNode(fmt.Sprintf("over quota group %d", i), workspaceID, userID, origin))
	}

	_, err := svc.CreateGroup(ctx, IssueGroupParams{Nodes: nodes})
	var limitErr *IssueLimitReachedError
	if !errors.As(err, &limitErr) {
		t.Fatalf("CreateGroup past the limit = %v, want IssueLimitReachedError", err)
	}
	if limitErr.Limit != int64(limit) || limitErr.PolicyRevision != 21 {
		t.Fatalf("limit error = %v, want the Cloud instruction to survive", err)
	}
	if got := countIssuesWithOrigins(t, pool, workspaceIDString, origins); got != 0 {
		t.Fatalf("%d issues were created by a group that could not fit the quota", got)
	}
}

func TestCreateGroupRejectsAnEmptyGroup(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	svc := NewIssueService(db.New(pool), pool, nil, nil, nil)

	if _, err := svc.CreateGroup(context.Background(), IssueGroupParams{}); err == nil {
		t.Fatal("CreateGroup with no nodes succeeded; there is nothing it could legitimately create")
	}
}
