package handler

import (
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// A runtime or profile delete is refused while agents are still bound. Telling
// the user to "reassign or archive them" is only true for agents they can
// actually reach: the archive endpoint rejects anything carrying a system_key
// outright. Naming one remedy for every blocker would hand some users an
// instruction that cannot be carried out, so the refusals classify their
// blockers instead. system_key, not kind, is the discriminator.
type blockingAgentClass int

const (
	// blockingAgentUser is an ordinary workspace agent: rebind or archive it.
	blockingAgentUser blockingAgentClass = iota
	// blockingAgentOtherSystem is a product-owned agent. Unarchivable, with no
	// remedy this code can name specifically.
	blockingAgentOtherSystem
)

// Class keys shared with the SQL CASE in ListActiveAgentsByProfile. That query
// has to classify server-side so its per-class counts can cover rows the LIMIT
// excludes, which means the mapping exists in two places; these constants give
// both the same vocabulary, and TestBlockingAgentClassMatchesSQLClassification
// pins them to each other.
const (
	blockingAgentClassKeyUser        = "user"
	blockingAgentClassKeyOtherSystem = "other_system"
)

// Unknown keys fall to other_system, not user. Only the exact "user" key may
// produce the one class whose remedy claims an action ("reassign or archive").
func blockingAgentClassFromKey(key string) blockingAgentClass {
	if key == blockingAgentClassKeyUser {
		return blockingAgentUser
	}
	return blockingAgentOtherSystem
}

func classifyBlockingAgent(systemKey pgtype.Text) blockingAgentClass {
	if !systemKey.Valid || strings.TrimSpace(systemKey.String) == "" {
		return blockingAgentUser
	}
	return blockingAgentOtherSystem
}

// blockingAgentLabel renders one blocker for a refusal sentence. Product-owned
// agents are marked so the reader can tell at a glance why the plain remedy
// does not apply to them.
func blockingAgentLabel(name, runtimeName, runtimeStatus string, class blockingAgentClass) string {
	if class == blockingAgentOtherSystem {
		return fmt.Sprintf("%q on %q (%s, built into Multica)", name, runtimeName, runtimeStatus)
	}
	return fmt.Sprintf("%q on %q (%s)", name, runtimeName, runtimeStatus)
}

// blockingAgentRemedies returns one clause per distinct recovery path present,
// in a fixed order so the sentence is stable. An empty result means every
// blocker was product-owned and the caller should say so rather than suggest
// an action.
func blockingAgentRemedies(classes map[blockingAgentClass]bool) []string {
	// "Reassign or archive them" must not appear to cover the product-owned
	// blockers listed beside them, which is exactly the instruction they cannot
	// follow. When both are present the clause names which ones it applies to.
	var out []string
	if classes[blockingAgentUser] && classes[blockingAgentOtherSystem] {
		out = append(out, "The agents above that are not marked as built into Multica can be reassigned or archived.")
	} else if classes[blockingAgentUser] {
		out = append(out, "Reassign or archive them first.")
	}
	if classes[blockingAgentOtherSystem] {
		out = append(out, "Some blockers are agents built into Multica and cannot be archived.")
	}
	return out
}

// blockingAgentClassesFromAgents collects the classes present on a plain agent
// set, for the callers that already hold db.Agent rows.
func blockingAgentClassesFromAgents(agents []db.Agent) map[blockingAgentClass]bool {
	classes := make(map[blockingAgentClass]bool, 2)
	for _, a := range agents {
		classes[classifyBlockingAgent(a.SystemKey)] = true
	}
	return classes
}
