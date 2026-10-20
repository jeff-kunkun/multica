package middleware

import (
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

// retiredCarrierSystemKeyPrefix is the `agent.system_key` prefix of the hidden
// requirement-alignment carriers. Alignment is retired, but carrier rows and
// their task tokens can outlive it; such a token is refused outright rather
// than treated as an ordinary task token, which would grant it writes it never
// had. The SQL guards in server/pkg/db/queries/agent.sql match the same prefix.
const retiredCarrierSystemKeyPrefix = "issue_draft:"

// isRetiredAlignmentCarrier reports whether a task token belongs to a leftover
// alignment carrier. kind is checked alongside the prefix because system_key
// is a text column: a user-authored agent whose key happens to start with
// `issue_draft:` is not a carrier.
func isRetiredAlignmentCarrier(agentKind string, systemKey pgtype.Text) bool {
	if agentKind != "system" || !systemKey.Valid {
		return false
	}
	return strings.HasPrefix(systemKey.String, retiredCarrierSystemKeyPrefix)
}
