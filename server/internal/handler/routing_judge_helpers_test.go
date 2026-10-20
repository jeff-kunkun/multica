package handler

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/routing"
)

// tierJudge answers every assignment with one rung.
type tierJudge struct {
	tier       string
	confidence float64
}

func (j tierJudge) Assign(context.Context, routing.Target, routing.JudgeState) (routing.Verdict, error) {
	return routing.Verdict{ExecutorTier: j.tier, ExecutorConfidence: j.confidence}, nil
}

func (j tierJudge) Unblock(context.Context, routing.Target, routing.JudgeState) (routing.Advice, error) {
	return routing.Advice{}, nil
}

func (j tierJudge) Stale(context.Context, routing.Target, routing.StaleState) (routing.StaleDecision, error) {
	return routing.StaleDecision{}, nil
}

// enableTestRouting turns routing on for the test workspace and swaps the
// judge, restoring both afterwards. The store stays the real one, so the
// roster routing reads is the workspace's actual tagged agents.
func enableTestRouting(t *testing.T, judge routing.Judge) {
	t.Helper()
	var previous []byte
	dbfx.QueryRow(t, `SELECT settings FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&previous)
	previousRouting := testHandler.Routing
	t.Cleanup(func() {
		dbfx.Exec(t, `UPDATE workspace SET settings = $2 WHERE id = $1`, testWorkspaceID, previous)
		testHandler.Routing = previousRouting
	})
	dbfx.Exec(t, `
		UPDATE workspace
		SET settings = jsonb_set(COALESCE(settings, '{}'::jsonb), '{routing}', '{"enabled": true, "model": "test-model"}'::jsonb)
		WHERE id = $1
	`, testWorkspaceID)
	testHandler.Routing = routing.New(testHandler.RoutingStore(), judge)
}
