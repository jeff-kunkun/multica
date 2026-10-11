package service

import (
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// AgentMayRunOnRuntime is the one rule for which runtime may claim, build and
// deliver a task addressed to an agent: the agent's bound runtime, or the
// standby it explicitly configured (agent.fallback_runtime_id, ADR-0009).
//
// A task row carries the runtime it was addressed to; this rule says whether
// the agent's current configuration still names that runtime. It is the Go
// mirror of the SQL predicate the claim, candidate, promote and reclaim
// queries share (`a.runtime_id = atq.runtime_id OR a.fallback_runtime_id =
// atq.runtime_id`), so the daemon poll service, the claim response builder
// and the final delivery transaction cannot disagree with the query that
// dispatched the row. Everything the standby relay must still refuse keeps
// its own gate: a real rebind or a cleared standby makes this rule false; the
// same-owner / same-provider pairing is enforced when the standby is
// configured and when an urgent send picks it; a private runtime's owner
// check stays beside the callers.
func AgentMayRunOnRuntime(agent db.Agent, runtimeID pgtype.UUID) bool {
	if !runtimeID.Valid {
		return false
	}
	if agent.RuntimeID.Valid && agent.RuntimeID == runtimeID {
		return true
	}
	return agent.FallbackRuntimeID.Valid && agent.FallbackRuntimeID == runtimeID
}
