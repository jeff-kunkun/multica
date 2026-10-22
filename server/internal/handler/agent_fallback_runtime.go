package handler

import (
	"fmt"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// fallbackRuntimeCompatible is the one rule for which runtime may stand in for
// another: same owner (never run Kun's agent on someone else's machine) and
// same provider (the agent's model, args and session format are per CLI).
func fallbackRuntimeCompatible(primary, fallback db.AgentRuntime) error {
	if primary.ID == fallback.ID {
		return fmt.Errorf("fallback runtime must differ from the primary runtime")
	}
	if !primary.OwnerID.Valid || !fallback.OwnerID.Valid || primary.OwnerID != fallback.OwnerID {
		return fmt.Errorf("fallback runtime must belong to the same owner as the primary runtime")
	}
	if primary.Provider != fallback.Provider {
		return fmt.Errorf("fallback runtime must run the same provider as the primary runtime (%s)", primary.Provider)
	}
	return nil
}
