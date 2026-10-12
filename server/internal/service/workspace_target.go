package service

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// What a write was aimed at (DENE-1936 review). A write that touches a private
// ticket, a restricted project or somebody's chat must not be told to a Bot
// whose owner could not open that thing. The write keeps its target so queueing,
// sending and the pull list all ask the same question about the same thing.

// WriteTarget is the ticket, project and chat a write touched. All three are
// optional; a write that touches none of them is a workspace-wide one.
type WriteTarget struct {
	IssueID       pgtype.UUID
	ProjectID     pgtype.UUID
	ChatSessionID pgtype.UUID
	// IssueIDs are further tickets the event is about (a PR's linked tickets):
	// the person must be able to open every one of them.
	IssueIDs []pgtype.UUID
	// Repo is "owner/name" of the repository the event is about. A repository
	// registered with the workspace follows its own sharing.
	Repo string
}

// Any reports whether the write is aimed at something that has its own access.
func (t WriteTarget) Any() bool {
	return t.IssueID.Valid || t.ProjectID.Valid || t.ChatSessionID.Valid || len(t.IssueIDs) > 0 || t.Repo != ""
}

// Established says whether a family's event has what its access is judged
// against. Ticket, project and run events need some target; a PR needs a
// ticket, because the tickets it is linked to are where its access lives (the
// PR list is only read through a ticket). Other families are workspace-wide.
func (t WriteTarget) Established(family string) bool {
	switch {
	case family == FamilyPR:
		return t.IssueID.Valid || len(t.IssueIDs) > 0
	case FamilyAccessScoped(family):
		return t.Any()
	}
	return true
}

// TargetVisibility answers whether one person can open the target. The handler
// package implements it once, on top of the same viewer the ticket and project
// lists use; everything that decides who hears about a write calls it.
type TargetVisibility func(ctx context.Context, workspaceID, userID pgtype.UUID, t WriteTarget) bool

// FamilyAccessScoped says whether events of a family belong to something with
// its own access (a ticket, a project, a chat). For these a write whose target
// cannot be found is not pushed: not knowing is not a reason to say it.
func FamilyAccessScoped(family string) bool {
	switch family {
	case FamilyIssue, FamilyProject, FamilyRun, FamilyPR:
		return true
	}
	return false
}

// ResolveWriteTarget turns the ids a request named (path parameters, a few
// well-known body fields, the id of what it created) into the ticket, project
// and chat they point at inside this workspace. Ids that are none of those
// (labels, agents, ...) are ignored. Two different tickets (or projects, or
// chats) in one write cannot be carried on one event, so it reports ambiguous
// and the caller treats the write as unresolved.
func ResolveWriteTarget(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, candidates []pgtype.UUID) (t WriteTarget, ok bool) {
	set := func(slot *pgtype.UUID, id pgtype.UUID) bool {
		if !id.Valid {
			return true
		}
		if slot.Valid && *slot != id {
			return false
		}
		*slot = id
		return true
	}
	seen := map[[16]byte]bool{}
	for _, id := range candidates {
		if !id.Valid || id == workspaceID || seen[id.Bytes] {
			continue
		}
		seen[id.Bytes] = true
		switch {
		case isIssueIn(ctx, q, workspaceID, id):
			if !set(&t.IssueID, id) {
				return WriteTarget{}, false
			}
		case isProjectIn(ctx, q, workspaceID, id):
			if !set(&t.ProjectID, id) {
				return WriteTarget{}, false
			}
		case isChatIn(ctx, q, workspaceID, id):
			if !set(&t.ChatSessionID, id) {
				return WriteTarget{}, false
			}
		default:
			if c, err := q.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{ID: id, WorkspaceID: workspaceID}); err == nil {
				if !set(&t.IssueID, c.IssueID) {
					return WriteTarget{}, false
				}
				continue
			}
			if task, err := q.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{ID: id, WorkspaceID: workspaceID}); err == nil {
				if !set(&t.IssueID, task.IssueID) || !set(&t.ChatSessionID, task.ChatSessionID) {
					return WriteTarget{}, false
				}
			}
		}
	}
	return t, true
}

func isIssueIn(ctx context.Context, q *db.Queries, ws, id pgtype.UUID) bool {
	i, err := q.GetIssue(ctx, id)
	return err == nil && i.WorkspaceID == ws
}

func isProjectIn(ctx context.Context, q *db.Queries, ws, id pgtype.UUID) bool {
	_, err := q.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: id, WorkspaceID: ws})
	return err == nil
}

func isChatIn(ctx context.Context, q *db.Queries, ws, id pgtype.UUID) bool {
	_, err := q.GetChatSessionInWorkspace(ctx, db.GetChatSessionInWorkspaceParams{ID: id, WorkspaceID: ws})
	return err == nil
}
