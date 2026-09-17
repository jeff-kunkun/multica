package agentconfig

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Two-level specialisation (DENE-301) configuration inheritance (DENE-470).
//
// A specialisation (agent.parent_agent_id IS NOT NULL) owns only its identity
// and its own prompt: name, description, avatar, instructions, the parent link
// and its live status. EVERY configuration field below belongs to its base role
// and is not editable on the child. There is deliberately no per-field override
// layer — a base role with two specialisations cannot run them on two models; if
// that is needed, the two are two base roles.
//
// The rule is stated once here so the four places that need it cannot drift:
// this file is the answer to "what does a specialisation inherit", and every
// caller reads it rather than re-listing columns.
//
// How the values reach a run is a separate concern, and the codebase uses both
// halves on purpose:
//
//   - The claim payload, the concurrency admission, the auto-retry switch and
//     the Composio allowlist are resolved at READ time from the base role's live
//     row (see Inherited / LoadInherited). One read, one place per concern.
//   - runtime_id, runtime_mode, permission_mode + the invocation allow-list are
//     MIRRORED into the child row at write time, because ~20 enqueue points and
//     two SQL fences (`a.runtime_id = atq.runtime_id`) read those columns
//     directly from the row. Mirroring keeps every one of them correct without
//     editing a single read path.
//
// InheritanceConfig is the field list in code, and it is what the write-time
// callers copy: same columns, same set, so "读时" and "写时" cannot disagree
// about what is inherited.

// Inherited returns child with every configuration field it inherits from its
// base role replaced by parent's value. Fields the child owns — id, workspace,
// name, description, avatar, instructions (which COMPOSES with the base role's
// rather than replacing it), status, owner, parent link, timestamps and the
// system-agent identity — are left exactly as the child had them.
//
// Pure: it performs no I/O and does not check the parent link, so callers can
// use it for a requested relationship as well as an established one.
func Inherited(child, parent db.Agent) db.Agent {
	child.RuntimeID = parent.RuntimeID
	child.RuntimeMode = parent.RuntimeMode
	child.RuntimeConfig = parent.RuntimeConfig
	child.Model = parent.Model
	child.ThinkingLevel = parent.ThinkingLevel
	child.ServiceTier = parent.ServiceTier
	child.CustomEnv = parent.CustomEnv
	child.CustomArgs = parent.CustomArgs
	child.McpConfig = parent.McpConfig
	child.MaxConcurrentTasks = parent.MaxConcurrentTasks
	child.PermissionMode = parent.PermissionMode
	child.Visibility = parent.Visibility
	child.AutoRetryEnabled = parent.AutoRetryEnabled
	child.SwitchableModels = parent.SwitchableModels
	child.ConversationStarters = parent.ConversationStarters
	child.DisabledRuntimeSkills = parent.DisabledRuntimeSkills
	child.ComposioToolkitAllowlist = parent.ComposioToolkitAllowlist
	return child
}

// LoadInherited resolves agent against its base role and returns the row a read
// path should actually use. A base role, a nil query set (unit-test services
// that never touch the database), and an agent whose base role row returns
// ErrNoRows are returned unchanged: the last case is defensive (the agent API
// refuses to archive a base role that still has active specialisations and has
// no hard delete), and the child keeps its own columns, which the write-time
// mirror keeps equal to the base role's for everything the enqueue paths read.
//
// Any OTHER read error is returned to the caller, which must fail closed: a
// swallowed transient error is indistinguishable from "the base role has no
// configuration", and the run would silently continue on the child's stale
// columns.
func LoadInherited(ctx context.Context, q *db.Queries, agent db.Agent) (db.Agent, error) {
	if q == nil || !agent.ParentAgentID.Valid {
		return agent, nil
	}
	parent, err := q.GetAgent(ctx, agent.ParentAgentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return agent, nil
		}
		return agent, err
	}
	return Inherited(agent, parent), nil
}
