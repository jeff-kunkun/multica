package workspacelink

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// BriefHints reads current workspace-level connectivity at claim time. Unlike
// List/View it exposes names and direction only, on both sides, and never loads
// shared projects or changes an agent's permissions.
func (s *Service) BriefHints(ctx context.Context, ws pgtype.UUID) ([]protocol.WorkspaceLinkHint, error) {
	rows, err := s.q.ListWorkspaceLinksForWorkspace(ctx, ws)
	if err != nil {
		return nil, err
	}
	return briefHints(rows, ws), nil
}

func briefHints(rows []db.ListWorkspaceLinksForWorkspaceRow, ws pgtype.UUID) []protocol.WorkspaceLinkHint {
	var hints []protocol.WorkspaceLinkHint
	byPeer := make(map[pgtype.UUID]int)
	for _, row := range rows {
		if row.Status != "active" {
			continue
		}
		var peer pgtype.UUID
		var hint protocol.WorkspaceLinkHint
		switch ws {
		case row.SourceWorkspaceID:
			peer = row.TargetWorkspaceID
			hint = protocol.WorkspaceLinkHint{Name: row.TargetName, Side: string(SideSource)}
		case row.TargetWorkspaceID:
			peer = row.SourceWorkspaceID
			hint = protocol.WorkspaceLinkHint{Name: row.SourceName, Side: string(SideViewer)}
		default:
			continue
		}
		if i, ok := byPeer[peer]; ok {
			if hints[i].Side != hint.Side {
				hints[i].Side = "both"
			}
		} else {
			byPeer[peer] = len(hints)
			hints = append(hints, hint)
		}
	}
	sort.Slice(hints, func(i, j int) bool {
		if hints[i].Name == hints[j].Name {
			return hints[i].Side < hints[j].Side
		}
		return hints[i].Name < hints[j].Name
	})
	return hints
}
