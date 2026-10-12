package service

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// AgentSeatState is an agent row as routing.SeatSelectable reads it: the one
// place a db.Agent becomes "may automatic dispatch pick this seat".
func AgentSeatState(a db.Agent) routing.SeatState {
	return routing.SeatState{
		ID:           util.UUIDToString(a.ID),
		Archived:     a.ArchivedAt.Valid,
		Disabled:     !a.WorkEnabled,
		NoRuntime:    !a.RuntimeID.Valid,
		DispatchMode: a.DispatchMode,
		Projects:     AgentDispatchProjects(a),
	}
}

// AgentDispatchProjects is the agent's project limit as ids; empty when the
// seat serves every project.
func AgentDispatchProjects(a db.Agent) []string {
	if len(a.DispatchProjects) == 0 {
		return nil
	}
	out := make([]string, 0, len(a.DispatchProjects))
	for _, id := range a.DispatchProjects {
		if id.Valid {
			out = append(out, util.UUIDToString(id))
		}
	}
	return out
}

// ProjectExecutorSeats is a project's executor allowlist as agent ids
// (DENE-1959); nil when automatic dispatch may pick any seat for it.
func ProjectExecutorSeats(p db.Project) []string {
	if len(p.ExecutorAgentIds) == 0 {
		return nil
	}
	out := make([]string, 0, len(p.ExecutorAgentIds))
	for _, id := range p.ExecutorAgentIds {
		if id.Valid {
			out = append(out, util.UUIDToString(id))
		}
	}
	return out
}

// LoadProjectExecutorSeats reads the allowlist of the project work belongs
// to. No project is no limit. A project that cannot be read is an error, not
// an empty list: an unread allowlist must not widen dispatch to every seat.
func LoadProjectExecutorSeats(ctx context.Context, q *db.Queries, workspaceID, projectID pgtype.UUID) ([]string, error) {
	if !projectID.Valid {
		return nil, nil
	}
	p, err := q.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: workspaceID})
	if err != nil {
		return nil, err
	}
	return ProjectExecutorSeats(p), nil
}

// LoadProjectsExecutorSeats is the executor list for work that belongs to
// several projects at once (a chat opened in more than one): a seat must be
// on every non-empty list. restricted reports that at least one project has
// a list; restricted with no seats means no agent satisfies them all, and
// the caller must decline rather than read the empty list as "no limit".
func LoadProjectsExecutorSeats(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, projectIDs []pgtype.UUID) ([]string, bool, error) {
	var out []string
	restricted := false
	for _, projectID := range projectIDs {
		seats, err := LoadProjectExecutorSeats(ctx, q, workspaceID, projectID)
		if err != nil {
			return nil, false, err
		}
		if len(seats) == 0 {
			continue
		}
		if !restricted {
			restricted, out = true, seats
			continue
		}
		kept := out[:0:0]
		for _, id := range out {
			if routing.ProjectAdmits(seats, id) {
				kept = append(kept, id)
			}
		}
		out = kept
	}
	return out, restricted, nil
}
