package service

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/routing"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// AgentSeries uses the bound CLI provider, never the local/cloud execution
// mode. Manual grouping does not alter the provider used for family rotation.
func AgentSeries(ctx context.Context, q interface {
	GetAgentRuntime(context.Context, pgtype.UUID) (db.AgentRuntime, error)
}, a db.Agent) routing.SeriesInfo {
	provider := ""
	if routing.ModelProvider(a.Model.String) == "" && a.RuntimeID.Valid && q != nil {
		if runtime, err := q.GetAgentRuntime(ctx, a.RuntimeID); err == nil {
			provider = runtime.Provider
		}
	}
	return routing.ResolveSeries(a.Name, a.Model.String, provider, a.SeriesOverride)
}
