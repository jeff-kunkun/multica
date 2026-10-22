package handler

import (
	"context"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) summoner() service.Summoner {
	return service.Summoner{Queries: h.Queries, TxStarter: h.TxStarter, Bus: h.Bus}
}

// summonPerson is the handler-side call into the entry. Failures are logged
// and reported as a zero result: every caller has already done its own
// durable write, and a missing notification must not undo it.
func (h *Handler) summonPerson(ctx context.Context, in service.SummonInput) (service.SummonResult, error) {
	out, err := h.summoner().Summon(ctx, in)
	if err != nil {
		slog.Warn("summon failed", "error", err, "issue_id", uuidToString(in.Issue.ID),
			"recipient_id", uuidToString(in.Recipient), "source", in.Source)
	}
	return out, err
}

// findWorkspaceMember resolves --to: user UUID, email, or name (exact,
// case-insensitive).
func (h *Handler) findWorkspaceMember(ctx context.Context, workspaceID pgtype.UUID, ref string) (db.ListMembersWithUserRow, bool, error) {
	members, err := h.Queries.ListMembersWithUser(ctx, workspaceID)
	if err != nil {
		return db.ListMembersWithUserRow{}, false, err
	}
	ref = strings.TrimSpace(strings.TrimPrefix(ref, "@"))
	for _, m := range members {
		if uuidToString(m.UserID) == ref || uuidToString(m.ID) == ref ||
			strings.EqualFold(m.UserEmail, ref) || strings.EqualFold(strings.TrimSpace(m.UserName), ref) {
			return m, true, nil
		}
	}
	return db.ListMembersWithUserRow{}, false, nil
}
