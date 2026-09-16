package middleware

import (
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

// Capability scope for `mat_` task tokens.
//
// A task token is normally the whole workspace credential of the member who
// triggered the task: the agent process holding it can create issues, comment,
// change status and assign, because that is what executing a task means. The
// requirement-alignment carrier (`agent.system_key = 'issue_draft:*'`) is the
// one task whose whole job is to TALK — it produces a structured draft that the
// user confirms, and the server writes the issue from that draft itself. It has
// no legitimate write at all, so its token is scoped down to reads here rather
// than relying on the prompt to behave.
//
// Why the prompt alone is not enough: instructions are advisory to a model, and
// a prompt-injected or simply over-eager carrier can run `multica issue create`
// with a token that authorises it. The prompt says "do not create anything";
// this middleware is what makes that true. Same reasoning as MUL-2600's
// human-only endpoints: the boundary has to be enforced where the credential is
// checked, not where the behaviour is asked for.
//
// What this does NOT cover — deliberately, and it must not be claimed
// otherwise: local side effects of the agent process. The carrier runs a real
// CLI on a real machine, so shell commands, file writes and `git push` inside
// its workdir are outside this server's reach. Providers differ and none of
// them offers a portable sandbox: Claude-family runners can be given
// `--disallowedTools` (see pkg/agent), others have no equivalent knob, so the
// local half is best-effort per provider. Anything that must be impossible has
// to be impossible in the API layer, which is what this file does.
//
// Adding a scope: extend isIssueDraftReadOnlyScope (or add a sibling predicate)
// and stamp the carrier's system_key at creation. Do not special-case a handler
// — a handler-side check is a second place to forget.

// issueDraftCarrierSystemKeyPrefix is the `agent.system_key` prefix of every
// hidden alignment carrier. It matches the SQL guards in
// server/pkg/db/queries/agent.sql and the carrier check in the issue-draft
// handler.
const issueDraftCarrierSystemKeyPrefix = "issue_draft:"

// isIssueDraftReadOnlyScope reports whether a task token belongs to an
// alignment carrier, and therefore may only read.
//
// kind is checked alongside the prefix for the same reason the SQL statements
// check both: system_key is a text column, and the prefix alone would let a
// future user-authored agent whose key happens to start with `issue_draft:`
// inherit a scope nobody intended for it.
func isIssueDraftReadOnlyScope(agentKind string, systemKey pgtype.Text) bool {
	if agentKind != "system" || !systemKey.Valid {
		return false
	}
	return strings.HasPrefix(systemKey.String, issueDraftCarrierSystemKeyPrefix)
}

// isReadOnlyMethod reports whether an HTTP method cannot change server state,
// and is therefore allowed for a read-only token.
//
// HEAD and OPTIONS are included because they are defined as safe by RFC 9110
// and are what clients and probes send before a GET. Everything else — POST,
// PUT, PATCH, DELETE, and any non-standard verb — is refused: this predicate is
// an allowlist on purpose, so a method nobody thought about fails closed
// instead of being treated as a read.
func isReadOnlyMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}
