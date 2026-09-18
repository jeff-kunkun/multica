package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func transferImportStrPtr(v string) *string { return &v }

// DENE-442: an assignee with no counterpart on the target used to be written as
// an empty assignee — "unassigned is the honest empty". A real import left 28
// tasks owned by nobody, so the owner reversed that call: the fallback is the
// importer. The fallback stays a write-path decision, so the same test pins
// what the contract still refuses: a queued task, an activity row, or any drift
// in the issue number and timestamps.
func TestTransferIssueUnmappedAssigneeFallsBackToImporter(t *testing.T) {
	fixture := newTransferOptionsFixture(t, nil)
	fx, env := fixture.fx, fixture.env
	ctx := context.Background()

	// A second target member the source creator maps onto, so the fallback
	// assignee is a different subscriber than the creator instead of collapsing
	// into the creator's own row.
	creatorEmail := fmt.Sprintf("xfer-assignee-creator-%d@example.com", time.Now().UnixNano())
	creatorID := fx.User(t, "xfer-assignee-creator", creatorEmail)
	fx.Member(t, fixture.workspace, creatorID, "member")

	issueSource := uuid.NewString()
	creatorSource := uuid.NewString()
	missingAssignee := uuid.NewString()
	targetIssueID := TransferIssueID(fixture.workspace, issueSource).String()
	fx.Cleanup(t, `DELETE FROM issue WHERE workspace_id = $1`, fixture.workspace)
	fx.Cleanup(t, `DELETE FROM comment WHERE workspace_id = $1`, fixture.workspace)

	const (
		createdAt = "2026-09-01T10:00:00Z"
		updatedAt = "2026-09-02T11:30:00Z"
	)
	dry := false
	report, err := ImportTransferIssues(ctx, env, TransferIssuesRequest{
		Refs: TransferRefs{
			Issues:  map[string]TransferIssueRef{issueSource: {Number: 7}},
			Members: map[string]TransferMemberRef{creatorSource: {Email: creatorEmail}},
		},
		Issues: []TransferIssueRow{{
			SourceID: issueSource,
			Number:   7,
			Title:    "assigned to an identity the target never heard of",
			Status:   "todo",
			Priority: "high",
			// An agent the bundle carries no ref for is the "source row was
			// deleted before the export" shape, which is exactly the case that
			// used to land unassigned.
			AssigneeType:   transferImportStrPtr("agent"),
			AssigneeID:     &missingAssignee,
			CreatorType:    "member",
			CreatorID:      creatorSource,
			CreatedAt:      createdAt,
			UpdatedAt:      updatedAt,
			LastActivityAt: updatedAt,
		}},
		DryRun:   &dry,
		Finalize: true,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	if len(report.AssigneeUnmapped) != 1 {
		t.Fatalf("assignee_unmapped=%v, want exactly the one unmappable assignee", report.AssigneeUnmapped)
	}
	degraded := report.AssigneeUnmapped[0]
	if degraded.Resolution != "importer" || degraded.RefID != missingAssignee || degraded.RefType != "agent" {
		t.Fatalf("degradation row=%+v, want {ref_type: agent, ref_id: %s, resolution: importer}", degraded, missingAssignee)
	}
	if len(report.CreatorUnmapped) != 0 {
		t.Fatalf("creator_unmapped=%v, want none: the creator maps by email", report.CreatorUnmapped)
	}

	importerID := uuidString(env.ImporterID)
	var assigneeType, assigneeID string
	fx.QueryRow(t, `SELECT assignee_type, assignee_id::text FROM issue WHERE id = $1`, targetIssueID).
		Scan(&assigneeType, &assigneeID)
	if assigneeType != "member" || assigneeID != importerID {
		t.Fatalf("assignee=(%s,%s), want (member,%s): the fallback is the importer, never empty", assigneeType, assigneeID, importerID)
	}

	var number int32
	var created, updated time.Time
	fx.QueryRow(t, `SELECT number, created_at, updated_at FROM issue WHERE id = $1`, targetIssueID).
		Scan(&number, &created, &updated)
	if number != 7 {
		t.Fatalf("number=%d, want the source number 7", number)
	}
	if created.UTC().Format(time.RFC3339) != createdAt || updated.UTC().Format(time.RFC3339) != updatedAt {
		t.Fatalf("timestamps=(%s,%s), want (%s,%s): the import never restamps a row",
			created.UTC().Format(time.RFC3339), updated.UTC().Format(time.RFC3339), createdAt, updatedAt)
	}

	if n := fx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, targetIssueID); n != 0 {
		t.Fatalf("agent_task_queue rows=%d, want 0: re-pointing an assignee must not enqueue a run", n)
	}
	if n := fx.Count(t, `SELECT count(*) FROM activity_log WHERE issue_id = $1`, targetIssueID); n != 0 {
		t.Fatalf("activity_log rows=%d, want 0: the import path publishes no events", n)
	}

	subscribers := map[string]string{}
	rows, err := fx.Pool.Query(ctx, `SELECT reason, user_id::text FROM issue_subscriber WHERE issue_id = $1`, targetIssueID)
	if err != nil {
		t.Fatalf("read subscribers: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var reason, userID string
		if err := rows.Scan(&reason, &userID); err != nil {
			t.Fatalf("scan subscriber: %v", err)
		}
		subscribers[reason] = userID
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read subscribers: %v", err)
	}
	if len(subscribers) != 2 {
		t.Fatalf("subscribers=%v, want one creator row and one assignee row", subscribers)
	}
	if subscribers["creator"] != creatorID {
		t.Fatalf("creator subscriber=%q, want %s", subscribers["creator"], creatorID)
	}
	if subscribers["assignee"] != importerID {
		t.Fatalf("assignee subscriber=%q, want the importer %s: the subscriber rebuild follows the fallback value",
			subscribers["assignee"], importerID)
	}
}

// DENE-442: the export indexed system agents by system_key, which is the
// target-side lookup key and not the source-side identity, so every assignee,
// comment author and mention pointing at the source workspace's built-in agent
// missed the index and degraded. The refs index is keyed by the source agent
// uuid now; the system_key it carries still decides which target agent wins.
func TestTransferIssueSystemAgentResolvesBySourceID(t *testing.T) {
	fixture := newTransferOptionsFixture(t, nil)
	fx, env := fixture.fx, fixture.env
	ctx := context.Background()

	systemAgentID := fx.Agent(t, "Mika", "", testutil.Cols{"kind": "system", "system_key": "mika"})

	var importerEmail string
	fx.QueryRow(t, `SELECT email FROM "user" WHERE id = $1`, uuidString(env.ImporterID)).Scan(&importerEmail)

	issueSource := uuid.NewString()
	commentSource := uuid.NewString()
	systemAgentSource := uuid.NewString()
	targetIssueID := TransferIssueID(fixture.workspace, issueSource).String()
	fx.Cleanup(t, `DELETE FROM issue WHERE workspace_id = $1`, fixture.workspace)
	fx.Cleanup(t, `DELETE FROM comment WHERE workspace_id = $1`, fixture.workspace)

	const (
		createdAt = "2026-09-03T09:00:00Z"
		updatedAt = "2026-09-03T09:05:00Z"
	)

	// The refs travel export → import exactly as a real bundle carries them, so
	// the index the exporter builds is the one this import resolves against.
	files, err := ExportFromSource(ctx, &fakeTransferSource{payloads: map[string]any{
		"/api/me":              map[string]any{"id": "user-1", "email": importerEmail},
		"/api/workspaces":      []map[string]any{{"id": "ws-1", "slug": "src", "name": "Src"}},
		"/api/workspaces/ws-1": map[string]any{"id": "ws-1", "slug": "src", "name": "Src"},
		"/api/agents": []map[string]any{{
			"id": systemAgentSource, "system_key": "mika", "name": "Mika", "instructions": "hi",
		}},
	}}, TransferExportOpts{Include: []string{"config"}, WorkspaceRef: "src"})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	refs := files.Manifest.Refs
	refs.Issues = map[string]TransferIssueRef{issueSource: {Number: 12}}
	refs.Members = map[string]TransferMemberRef{uuidString(env.ImporterID): {Email: importerEmail}}

	dry := false
	report, err := ImportTransferIssues(ctx, env, TransferIssuesRequest{
		Refs: refs,
		Issues: []TransferIssueRow{{
			SourceID:       issueSource,
			Number:         12,
			Title:          "assigned to the source workspace's built-in agent",
			Status:         "todo",
			Priority:       "none",
			AssigneeType:   transferImportStrPtr("agent"),
			AssigneeID:     &systemAgentSource,
			CreatorType:    "member",
			CreatorID:      uuidString(env.ImporterID),
			CreatedAt:      createdAt,
			UpdatedAt:      updatedAt,
			LastActivityAt: updatedAt,
		}},
		Comments: []TransferCommentRow{{
			SourceID:   commentSource,
			IssueID:    issueSource,
			AuthorType: "agent",
			AuthorID:   systemAgentSource,
			Content:    "handing this back to [@Mika](mention://agent/" + systemAgentSource + ")",
			Type:       "comment",
			CreatedAt:  createdAt,
			UpdatedAt:  updatedAt,
		}},
		DryRun:   &dry,
		Finalize: true,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	if len(report.AssigneeUnmapped) != 0 || len(report.AuthorUnmapped) != 0 || len(report.MentionUnmapped) != 0 {
		t.Fatalf("degradations: assignee=%v author=%v mention=%v, want none: the source id resolves through refs.system_agents",
			report.AssigneeUnmapped, report.AuthorUnmapped, report.MentionUnmapped)
	}

	var assigneeType, assigneeID string
	fx.QueryRow(t, `SELECT assignee_type, assignee_id::text FROM issue WHERE id = $1`, targetIssueID).
		Scan(&assigneeType, &assigneeID)
	if assigneeType != "agent" || assigneeID != systemAgentID {
		t.Fatalf("assignee=(%s,%s), want (agent,%s): the same system_key on the target is the mapping",
			assigneeType, assigneeID, systemAgentID)
	}

	var authorType, authorID, content string
	fx.QueryRow(t, `SELECT author_type, author_id::text, content FROM comment WHERE id = $1`, TransferCommentID(fixture.workspace, commentSource).String()).
		Scan(&authorType, &authorID, &content)
	if authorType != "agent" || authorID != systemAgentID {
		t.Fatalf("comment author=(%s,%s), want (agent,%s)", authorType, authorID, systemAgentID)
	}
	if !strings.Contains(content, "mention://agent/"+systemAgentID) {
		t.Fatalf("comment content=%q, want the mention rewritten onto %s", content, systemAgentID)
	}
	if strings.Contains(content, systemAgentSource) {
		t.Fatalf("comment content=%q still points at the source agent id", content)
	}
}
