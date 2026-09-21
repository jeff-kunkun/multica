package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// TestAgentDisableEnableEndpoints covers the switch the agents list flips
// (DENE-714): one click parks a seat, one click brings it back, and the state
// survives the round trip because it is a column rather than anything the
// client holds.
//
// The 409s matter more than they look. `disabled_at` is a timestamp an operator
// reads as "parked since"; a re-disable that silently rewrote it would erase
// the one fact the column carries beyond a boolean.
func TestAgentDisableEnableEndpoints(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	runtimeID := createClaudeProviderRuntime(t)
	agentID := createAgentOnRuntime(t, "seat-switch-test", runtimeID, "")

	var resp struct {
		DisabledAt *string `json:"disabled_at"`
		ArchivedAt *string `json:"archived_at"`
	}

	// Each call re-zeroes the decode target so a field the handler omitted can
	// never be read as the previous call's value.
	call := func(method, path string, h http.HandlerFunc, want int) {
		t.Helper()
		resp.DisabledAt = nil
		resp.ArchivedAt = nil
		r := testutil.Call(t, h, withURLParam(newRequest(method, path, nil), "id", agentID)).Want(want)
		if want == http.StatusOK {
			r.JSON(&resp)
		}
	}
	get := func(want int) {
		t.Helper()
		call(http.MethodGet, "/api/agents/"+agentID, testHandler.GetAgent, want)
	}
	disable := func(want int) {
		t.Helper()
		call(http.MethodPost, "/api/agents/"+agentID+"/disable", testHandler.DisableAgent, want)
	}
	enable := func(want int) {
		t.Helper()
		call(http.MethodPost, "/api/agents/"+agentID+"/enable", testHandler.EnableAgent, want)
	}

	get(http.StatusOK)
	if resp.DisabledAt != nil {
		t.Fatalf("a new seat starts parked: disabled_at = %v, want null", *resp.DisabledAt)
	}

	disable(http.StatusOK)
	if resp.DisabledAt == nil {
		t.Fatal("disable returned disabled_at = null")
	}
	if resp.ArchivedAt != nil {
		t.Fatalf("disable archived the seat: archived_at = %v, want null", *resp.ArchivedAt)
	}
	parkedAt := *resp.DisabledAt

	// The row is what persists, not the response: this is the "still off after
	// a refresh and a re-login" half of the acceptance criteria.
	var stored *string
	if err := testPool.QueryRow(context.Background(),
		`SELECT disabled_at::text FROM agent WHERE id = $1`, agentID).Scan(&stored); err != nil {
		t.Fatalf("read stored disabled_at: %v", err)
	}
	if stored == nil {
		t.Fatal("disabled_at was not persisted")
	}

	disable(http.StatusConflict)
	get(http.StatusOK)
	if resp.DisabledAt == nil || *resp.DisabledAt != parkedAt {
		t.Fatalf("re-disable moved parked-since: %v, want %s", resp.DisabledAt, parkedAt)
	}

	enable(http.StatusOK)
	if resp.DisabledAt != nil {
		t.Fatalf("enable left disabled_at = %v, want null", *resp.DisabledAt)
	}
	enable(http.StatusConflict)
}

// TestRoutingRosterSkipsDisabledAgents is the gate that decides whether
// automatic dispatch can pick a parked seat. The roster is where routing's
// candidate ladder comes from, so a seat missing here cannot be chosen on any
// rung — which is the point: a seat that was picked and then refused at
// dispatch would strand the ticket on somebody nobody is going to wake.
func TestRoutingRosterSkipsDisabledAgents(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createClaudeProviderRuntime(t)
	name := "roster-switch-test"
	agentID := createAgentOnRuntime(t, name, runtimeID, "")
	if _, err := testPool.Exec(ctx, `UPDATE agent SET routing_tier = 'strong' WHERE id = $1`, agentID); err != nil {
		t.Fatalf("tag routing tier: %v", err)
	}

	store := testHandler.RoutingStore()

	roster, err := store.Roster(ctx, testWorkspaceID)
	if err != nil {
		t.Fatalf("roster while enabled: %v", err)
	}
	if _, ok := roster[name]; !ok {
		t.Fatalf("enabled seat %q missing from roster", name)
	}

	if _, err := testPool.Exec(ctx, `UPDATE agent SET disabled_at = now() WHERE id = $1`, agentID); err != nil {
		t.Fatalf("disable agent: %v", err)
	}
	roster, err = store.Roster(ctx, testWorkspaceID)
	if err != nil {
		t.Fatalf("roster while disabled: %v", err)
	}
	if _, ok := roster[name]; ok {
		t.Fatalf("disabled seat %q is still a routing candidate", name)
	}

	// Parked is not archived: the seat is off the ladder and still in the list,
	// which is what the agents page renders.
	var listed bool
	if err := testPool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM agent WHERE id = $1 AND archived_at IS NULL)`, agentID).Scan(&listed); err != nil {
		t.Fatalf("read list visibility: %v", err)
	}
	if !listed {
		t.Fatal("disabling removed the seat from the active agent list")
	}

	if _, err := testPool.Exec(ctx, `UPDATE agent SET disabled_at = NULL WHERE id = $1`, agentID); err != nil {
		t.Fatalf("enable agent: %v", err)
	}
	roster, err = store.Roster(ctx, testWorkspaceID)
	if err != nil {
		t.Fatalf("roster after enable: %v", err)
	}
	if _, ok := roster[name]; !ok {
		t.Fatalf("re-enabled seat %q did not rejoin the roster", name)
	}
}
