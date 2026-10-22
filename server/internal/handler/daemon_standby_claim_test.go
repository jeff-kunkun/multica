package handler

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func TestAgentMayRunOnRuntime(t *testing.T) {
	primary, standby, other := dbid.NewV7(), dbid.NewV7(), dbid.NewV7()
	agent := db.Agent{RuntimeID: primary, FallbackRuntimeID: standby}
	for name, tc := range map[string]struct {
		runtime pgtype.UUID
		want    bool
	}{
		"primary":  {primary, true},
		"standby":  {standby, true},
		"stranger": {other, false},
		"unset":    {pgtype.UUID{}, false},
	} {
		if got := service.AgentMayRunOnRuntime(agent, tc.runtime); got != tc.want {
			t.Errorf("%s: AgentMayRunOnRuntime = %t, want %t", name, got, tc.want)
		}
	}
	agent.FallbackRuntimeID = pgtype.UUID{}
	if service.AgentMayRunOnRuntime(agent, standby) {
		t.Error("cleared standby must not be eligible")
	}
	agent.RuntimeID = pgtype.UUID{}
	if service.AgentMayRunOnRuntime(agent, primary) {
		t.Error("unbound agent must not be eligible on its former runtime")
	}
}
