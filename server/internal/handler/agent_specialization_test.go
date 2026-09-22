package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestCancelAgentTasksAlsoCancelsDirectSpecialisations(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	parentID, childID := specializationFixture(t, "cancel-cascade")
	for _, agentID := range []string{parentID, childID} {
		dbfx.Exec(t, `INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority) VALUES ($1, $2, 'queued', 0)`, agentID, testRuntimeID)
	}

	req := testutil.WithURLParams(
		specRequest(http.MethodPost, "/api/agents/"+parentID+"/cancel-tasks", nil),
		"id", parentID,
	)
	resp := testutil.Call(t, testHandler.CancelAgentTasks, req).Want(http.StatusOK)
	var body struct {
		Cancelled int `json:"cancelled"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode cancel response: %v", err)
	}
	if body.Cancelled != 2 {
		t.Fatalf("cancelled count = %d, want 2", body.Cancelled)
	}

	rows, err := testPool.Query(t.Context(), `SELECT status FROM agent_task_queue WHERE agent_id IN ($1, $2) ORDER BY agent_id`, parentID, childID)
	if err != nil {
		t.Fatalf("query cancelled tasks: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			t.Fatalf("scan task status: %v", err)
		}
		if status != "cancelled" {
			t.Errorf("task status = %q, want cancelled", status)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate task statuses: %v", err)
	}
}

// The two-level specialisation contract (DENE-301). One file, because the rules
// only make sense together: depth, self-reference, the child-count direction of
// the same rule, the archive guard, and the solidify escape hatch that is what
// makes the guard livable.
//
// Fixtures come from dbfx (internal/testutil) rather than hand-written
// INSERT/DELETE pairs, and handler calls go through testutil.Call so a failure
// prints the request line, both statuses and the body.

// specializationFixture inserts a base role plus one specialisation of it. The
// parent carries a prompt so the inheritance assertions have something to find.
func specializationFixture(t *testing.T, name string) (parentID, childID string) {
	t.Helper()

	parentID = dbfx.Agent(t, name+"-base", testRuntimeID, testutil.Cols{
		"instructions": "base role rules",
	})
	childID = dbfx.Agent(t, name+"-spec", testRuntimeID, testutil.Cols{
		"instructions":    "specialisation delta",
		"parent_agent_id": parentID,
	})
	return parentID, childID
}

// specRequest is testutil.JSONRequest with the handler suite's authentication
// and workspace headers attached. The shared helper deliberately knows nothing
// about this package's fixture, so the wiring stays here.
func specRequest(method, path string, body any) *http.Request {
	return testutil.WithHeaders(testutil.JSONRequest(method, path, body),
		"X-User-ID", testUserID,
		"X-Workspace-ID", testWorkspaceID,
	)
}

// persistedParentOf reads back just the two columns the tree is made of, so an
// assertion can check what the database holds rather than what a response said.
func persistedParentOf(t *testing.T, agentID string) (parent pgtype.UUID, instructions string, archived bool) {
	t.Helper()

	dbfx.QueryRow(t,
		`SELECT parent_agent_id, instructions, archived_at IS NOT NULL FROM agent WHERE id = $1`,
		agentID,
	).Scan(&parent, &instructions, &archived)
	return parent, instructions, archived
}

// TestAgentSpecializationRejectsThreeLevels covers every way a caller could try
// to build a deeper tree. All four are 400: the request is a legitimate shape,
// the relationship it asks for is not.
func TestAgentSpecializationRejectsThreeLevels(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	_, childID := specializationFixture(t, "depth")

	t.Run("specializing a specialisation", func(t *testing.T) {
		req := specRequest(http.MethodPost, "/api/agents", map[string]any{
			"name":            "depth-grandchild",
			"runtime_id":      testRuntimeID,
			"parent_agent_id": childID,
		})
		testutil.Call(t, testHandler.CreateAgent, req).Want(http.StatusBadRequest)
	})

	t.Run("giving a parent to an agent that already has children", func(t *testing.T) {
		parentID, _ := specializationFixture(t, "depth-two")
		otherBaseID := dbfx.Agent(t, "depth-other-base", testRuntimeID, nil)

		req := testutil.WithURLParams(
			specRequest(http.MethodPut, "/api/agents/"+parentID, map[string]any{
				"parent_agent_id": otherBaseID,
			}), "id", parentID)
		testutil.Call(t, testHandler.UpdateAgent, req).Want(http.StatusBadRequest)

		if parent, _, _ := persistedParentOf(t, parentID); parent.Valid {
			t.Error("base role acquired a parent despite the 400")
		}
	})

	t.Run("self reference", func(t *testing.T) {
		baseID := dbfx.Agent(t, "depth-self", testRuntimeID, nil)

		req := testutil.WithURLParams(
			specRequest(http.MethodPut, "/api/agents/"+baseID, map[string]any{
				"parent_agent_id": baseID,
			}), "id", baseID)
		testutil.Call(t, testHandler.UpdateAgent, req).Want(http.StatusBadRequest)

		if parent, _, _ := persistedParentOf(t, baseID); parent.Valid {
			t.Error("agent became its own parent despite the 400")
		}
	})

	t.Run("archived parent", func(t *testing.T) {
		archivedID := dbfx.Agent(t, "depth-archived-parent", testRuntimeID, testutil.Cols{
			"archived_at": testutil.Raw("now()"),
		})

		req := specRequest(http.MethodPost, "/api/agents", map[string]any{
			"name":            "depth-orphan",
			"runtime_id":      testRuntimeID,
			"parent_agent_id": archivedID,
		})
		testutil.Call(t, testHandler.CreateAgent, req).Want(http.StatusBadRequest)
	})
}

// TestAgentSpecializationResponseShape pins what the front end can rely on:
// a child names its base role and shows the inherited half, a base role reports
// how many active specialisations hang off it, and the agents list carries the
// same grouping data so nested rendering is not an N+1.
func TestAgentSpecializationResponseShape(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	parentID, childID := specializationFixture(t, "shape")
	skillID := dbfx.Insert(t, "skill", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"name":         "shape-inherited-skill",
		"description":  "",
		"content":      "inherited skill body",
		"created_by":   testUserID,
	})
	dbfx.InsertNoID(t, "agent_skill",
		testutil.Cols{"agent_id": parentID, "skill_id": skillID},
		"agent_id = $1 AND skill_id = $2", parentID, skillID)

	t.Run("child detail", func(t *testing.T) {
		req := testutil.WithURLParams(
			specRequest(http.MethodGet, "/api/agents/"+childID, nil), "id", childID)
		resp := testutil.Decode[AgentResponse](t, testHandler.GetAgent, req, http.StatusOK)

		if resp.ParentAgentID != parentID {
			t.Errorf("parent_agent_id = %q, want %q", resp.ParentAgentID, parentID)
		}
		if resp.ParentAgentName != "shape-base" {
			t.Errorf("parent_agent_name = %q, want %q", resp.ParentAgentName, "shape-base")
		}
		if resp.InheritedInstructions != "base role rules" {
			t.Errorf("inherited_instructions = %q, want %q", resp.InheritedInstructions, "base role rules")
		}
		if len(resp.InheritedSkills) != 1 || resp.InheritedSkills[0].ID != skillID {
			t.Fatalf("inherited_skills = %+v, want the parent's skill %s", resp.InheritedSkills, skillID)
		}
	})

	t.Run("base role detail", func(t *testing.T) {
		req := testutil.WithURLParams(
			specRequest(http.MethodGet, "/api/agents/"+parentID, nil), "id", parentID)
		resp := testutil.Decode[AgentResponse](t, testHandler.GetAgent, req, http.StatusOK)

		if resp.ParentAgentID != "" || resp.InheritedInstructions != "" || len(resp.InheritedSkills) != 0 {
			t.Errorf("base role leaked inheritance fields: %+v", resp)
		}
	})

	t.Run("agents list carries grouping data without extra reads", func(t *testing.T) {
		req := specRequest(http.MethodGet, "/api/agents", nil)
		list := testutil.Decode[[]AgentResponse](t, testHandler.ListAgents, req, http.StatusOK)

		byID := map[string]AgentResponse{}
		for _, a := range list {
			byID[a.ID] = a
		}
		parent, ok := byID[parentID]
		if !ok {
			t.Fatalf("base role %s missing from the agents list", parentID)
		}
		child, ok := byID[childID]
		if !ok {
			t.Fatalf("specialisation %s missing from the agents list", childID)
		}
		if parent.ChildCount == nil || *parent.ChildCount != 1 {
			t.Errorf("base role child_count = %v, want 1", parent.ChildCount)
		}
		if child.ChildCount == nil || *child.ChildCount != 0 {
			t.Errorf("specialisation child_count = %v, want 0", child.ChildCount)
		}
		if child.ParentAgentID != parentID || child.ParentAgentName != "shape-base" {
			t.Errorf("list child parent = (%q, %q), want (%q, %q)",
				child.ParentAgentID, child.ParentAgentName, parentID, "shape-base")
		}
	})
}

// The archive guard is what makes solidify necessary, and solidify is what
// makes the guard escapable — so one fixture runs through both, in order.
func TestAgentArchiveGuardAndSolidify(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	parentID, childID := specializationFixture(t, "solidify")
	skillID := dbfx.Insert(t, "skill", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"name":         "solidify-kept-skill",
		"description":  "",
		"content":      "child skill body",
		"created_by":   testUserID,
	})
	dbfx.InsertNoID(t, "agent_skill",
		testutil.Cols{"agent_id": childID, "skill_id": skillID},
		"agent_id = $1 AND skill_id = $2", childID, skillID)

	t.Run("archiving a base role with a specialisation is refused and lists them", func(t *testing.T) {
		req := testutil.WithURLParams(
			specRequest(http.MethodPost, "/api/agents/"+parentID+"/archive", nil), "id", parentID)
		resp := testutil.Call(t, testHandler.ArchiveAgent, req).Want(http.StatusConflict)

		children, _ := resp.Map()["children"].([]any)
		if len(children) != 1 || children[0] != "solidify-spec" {
			t.Fatalf("409 children = %v, want [solidify-spec] (body: %s)", children, resp.Text())
		}
		if _, _, archived := persistedParentOf(t, parentID); archived {
			t.Error("base role was archived despite the 409")
		}
	})

	t.Run("solidify folds the parent prompt in, detaches, and keeps the child's own skill", func(t *testing.T) {
		req := testutil.WithURLParams(
			specRequest(http.MethodPost, "/api/agents/"+childID+"/solidify", nil), "id", childID)
		resp := testutil.Decode[AgentResponse](t, testHandler.SolidifyAgent, req, http.StatusOK)

		if want := "base role rules\n\nspecialisation delta"; resp.Instructions != want {
			t.Errorf("instructions = %q, want %q", resp.Instructions, want)
		}
		if resp.ParentAgentID != "" {
			t.Errorf("parent_agent_id = %q, want empty after solidify", resp.ParentAgentID)
		}
		if resp.InheritedInstructions != "" || len(resp.InheritedSkills) != 0 {
			t.Errorf("solidified child still reports inheritance: %+v", resp)
		}
		if len(resp.Skills) != 1 || resp.Skills[0].ID != skillID {
			t.Errorf("skills = %+v, want the child's own skill %s", resp.Skills, skillID)
		}

		// Persisted state, not just the payload: a solidify that only changed
		// the response would leave the next run inheriting a second copy.
		parent, instructions, _ := persistedParentOf(t, childID)
		if want := "base role rules\n\nspecialisation delta"; instructions != want {
			t.Errorf("persisted instructions = %q, want %q", instructions, want)
		}
		if parent.Valid {
			t.Error("persisted parent_agent_id is still set after solidify")
		}
	})

	t.Run("the base role is archivable once its child is solidified", func(t *testing.T) {
		req := testutil.WithURLParams(
			specRequest(http.MethodPost, "/api/agents/"+parentID+"/archive", nil), "id", parentID)
		testutil.Call(t, testHandler.ArchiveAgent, req).Want(http.StatusOK)

		if _, _, archived := persistedParentOf(t, parentID); !archived {
			t.Error("base role was not archived after its child solidified")
		}
	})
}

// An agent with no specialisations at all is the common case, and it must not
// be dragged into the new guard: archiving a plain agent still succeeds.
func TestAgentArchiveWithoutSpecializationsStillSucceeds(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	agentID := dbfx.Agent(t, "archive-plain-agent", testRuntimeID, nil)

	req := testutil.WithURLParams(
		specRequest(http.MethodPost, "/api/agents/"+agentID+"/archive", nil), "id", agentID)
	testutil.Call(t, testHandler.ArchiveAgent, req).Want(http.StatusOK)
}

// The tri-state parent field: re-parent, preserve, and detach must all be
// distinguishable, because "omitted" preserving the binding is what keeps an
// unrelated metadata edit from silently detaching a specialisation.
func TestAgentSpecializationUpdateTriState(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	_, childID := specializationFixture(t, "tristate")
	otherBaseID := dbfx.Agent(t, "tristate-other-base", testRuntimeID, nil)

	t.Run("re-parent to another base role", func(t *testing.T) {
		req := testutil.WithURLParams(
			specRequest(http.MethodPut, "/api/agents/"+childID, map[string]any{
				"parent_agent_id": otherBaseID,
			}), "id", childID)
		resp := testutil.Decode[AgentResponse](t, testHandler.UpdateAgent, req, http.StatusOK)

		if resp.ParentAgentID != otherBaseID || resp.ParentAgentName != "tristate-other-base" {
			t.Errorf("re-parent gave (%q, %q), want (%q, %q)",
				resp.ParentAgentID, resp.ParentAgentName, otherBaseID, "tristate-other-base")
		}
	})

	t.Run("omitting the field preserves the parent", func(t *testing.T) {
		req := testutil.WithURLParams(
			specRequest(http.MethodPut, "/api/agents/"+childID, map[string]any{
				"description": "metadata only",
			}), "id", childID)
		resp := testutil.Decode[AgentResponse](t, testHandler.UpdateAgent, req, http.StatusOK)

		if resp.ParentAgentID != otherBaseID {
			t.Errorf("omitted parent_agent_id changed the binding: got %q, want %q", resp.ParentAgentID, otherBaseID)
		}
	})

	t.Run("empty string detaches", func(t *testing.T) {
		req := testutil.WithURLParams(
			specRequest(http.MethodPut, "/api/agents/"+childID, map[string]any{
				"parent_agent_id": "",
			}), "id", childID)
		resp := testutil.Decode[AgentResponse](t, testHandler.UpdateAgent, req, http.StatusOK)

		if resp.ParentAgentID != "" {
			t.Errorf("detach left parent_agent_id = %q", resp.ParentAgentID)
		}
	})
}

// Solidify on a base role is a conflict, not a validation error: the agent is
// real and the caller may manage it, there is simply nothing to fold in.
func TestAgentSolidifyRejectsBaseRole(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	baseID := dbfx.Agent(t, "solidify-base-only", testRuntimeID, nil)

	req := testutil.WithURLParams(
		specRequest(http.MethodPost, "/api/agents/"+baseID+"/solidify", nil), "id", baseID)
	testutil.Call(t, testHandler.SolidifyAgent, req).Want(http.StatusConflict)
}

// A specialisation must not become a window into a base role that the viewer
// cannot read on its own. The relationship stays visible (the id is on the
// response); the inherited prompt does not.
func TestAgentSpecializationDoesNotLeakPrivateParentPrompt(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	wsID := dbfx.Workspace(t, "Specialization Privacy", "specialization-privacy")
	ownerID := dbfx.User(t, "Spec Parent Owner", "spec-parent-owner@multica.test")
	memberID := dbfx.User(t, "Spec Plain Member", "spec-plain-member@multica.test")
	dbfx.Member(t, wsID, ownerID, "member")
	dbfx.Member(t, wsID, memberID, "member")

	// The base role is private to ownerID and holds the text that must not escape.
	parentID := dbfx.Agent(t, "privacy-parent", testRuntimeID, testutil.Cols{
		"workspace_id":    wsID,
		"owner_id":        ownerID,
		"instructions":    "owner-only base prompt",
		"visibility":      "private",
		"permission_mode": "private",
	})
	// The specialisation is readable by every member, so memberID can load it.
	childID := dbfx.Agent(t, "privacy-child", testRuntimeID, testutil.Cols{
		"workspace_id":    wsID,
		"owner_id":        ownerID,
		"instructions":    "child delta",
		"visibility":      "workspace",
		"permission_mode": "public_to",
		"parent_agent_id": parentID,
	})
	dbfx.InsertNoID(t, "agent_invocation_target",
		testutil.Cols{"agent_id": childID, "target_type": "workspace", "target_id": wsID},
		"agent_id = $1", childID)

	loadChild := func(userID string) AgentResponse {
		t.Helper()
		req := testutil.WithHeaders(
			testutil.JSONRequest(http.MethodGet, "/api/agents/"+childID, nil),
			"X-User-ID", userID,
			"X-Workspace-ID", wsID,
		)
		req = testutil.WithURLParams(req, "id", childID)
		return testutil.Decode[AgentResponse](t, testHandler.GetAgent, req, http.StatusOK)
	}

	t.Run("owner sees the inherited prompt", func(t *testing.T) {
		if resp := loadChild(ownerID); resp.InheritedInstructions != "owner-only base prompt" {
			t.Errorf("owner inherited_instructions = %q, want the parent prompt", resp.InheritedInstructions)
		}
	})

	t.Run("unrelated member sees the relation but not the prompt", func(t *testing.T) {
		resp := loadChild(memberID)
		if resp.ParentAgentID != parentID {
			t.Errorf("member parent_agent_id = %q, want %q", resp.ParentAgentID, parentID)
		}
		if resp.InheritedInstructions != "" || len(resp.InheritedSkills) != 0 {
			t.Errorf("member saw inherited content from a private base role: %+v", resp)
		}
	})
}

// Attaching is the moment the inheritance relationship is created, so the
// parent's view gate belongs on the WRITE, not only on the reads. Without it a
// plain member could point an agent they own at another member's private base
// role — GetAgent would dutifully hide inherited_instructions, and then
// /solidify would hand the same text back inside their own `instructions`.
func TestAgentSpecializationRefusesUnviewableParent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	wsID := dbfx.Workspace(t, "Specialization Parent Gate", "specialization-parent-gate")
	ownerID := dbfx.User(t, "Gate Parent Owner", "gate-parent-owner@multica.test")
	memberID := dbfx.User(t, "Gate Plain Member", "gate-plain-member@multica.test")
	dbfx.Member(t, wsID, ownerID, "member")
	dbfx.Member(t, wsID, memberID, "member")

	runtimeID := dbfx.Runtime(t, "gate-runtime", testutil.Cols{
		"workspace_id": wsID,
		"owner_id":     memberID,
		"visibility":   "public",
	})
	parentID := dbfx.Agent(t, "gate-private-parent", runtimeID, testutil.Cols{
		"workspace_id":    wsID,
		"owner_id":        ownerID,
		"instructions":    "owner-only base prompt",
		"visibility":      "private",
		"permission_mode": "private",
	})

	memberReq := func(method, path string, body any) *http.Request {
		return testutil.WithHeaders(testutil.JSONRequest(method, path, body),
			"X-User-ID", memberID, "X-Workspace-ID", wsID)
	}

	t.Run("create cannot attach to a base role the actor cannot see", func(t *testing.T) {
		req := memberReq(http.MethodPost, "/api/agents", map[string]any{
			"name":            "gate-child-create",
			"instructions":    "mine",
			"runtime_id":      runtimeID,
			"parent_agent_id": parentID,
		})
		// Same wording as a genuinely missing parent: the endpoint must not
		// double as a probe for which private agents exist.
		testutil.Call(t, testHandler.CreateAgent, req).Want(http.StatusBadRequest)
	})

	t.Run("update cannot attach to a base role the actor cannot see", func(t *testing.T) {
		childID := dbfx.Agent(t, "gate-child-update", runtimeID, testutil.Cols{
			"workspace_id": wsID,
			"owner_id":     memberID,
			"instructions": "mine",
		})
		req := testutil.WithURLParams(
			memberReq(http.MethodPut, "/api/agents/"+childID, map[string]any{"parent_agent_id": parentID}),
			"id", childID)
		testutil.Call(t, testHandler.UpdateAgent, req).Want(http.StatusBadRequest)

		if parent, _, _ := persistedParentOf(t, childID); parent.Valid {
			t.Errorf("parent_agent_id was written despite the refusal: %s", uuidToString(parent))
		}
	})

	t.Run("solidify refuses a parent that went unviewable after attaching", func(t *testing.T) {
		// Written straight to the row: the handler now refuses to create this
		// pair, but rows can still reach it when the base role's owner flips it
		// to private afterwards.
		childID := dbfx.Agent(t, "gate-child-solidify", runtimeID, testutil.Cols{
			"workspace_id":    wsID,
			"owner_id":        memberID,
			"instructions":    "mine",
			"parent_agent_id": parentID,
		})
		req := testutil.WithURLParams(
			memberReq(http.MethodPost, "/api/agents/"+childID+"/solidify", nil), "id", childID)
		testutil.Call(t, testHandler.SolidifyAgent, req).Want(http.StatusForbidden)

		if _, instructions, _ := persistedParentOf(t, childID); instructions != "mine" {
			t.Errorf("instructions = %q, want the child's own text with no inherited prompt folded in", instructions)
		}
	})
}

// The composed prompt is the contract the daemon-side claim path and the
// solidify endpoint share, so its edges are pinned directly rather than only
// through a handler.
func TestComposeAgentInstructions(t *testing.T) {
	tests := []struct {
		name   string
		parent string
		child  string
		want   string
	}{
		{"both halves", "base", "delta", "base\n\ndelta"},
		{"no parent prompt", "", "delta", "delta"},
		{"no child prompt", "base", "", "base"},
		{"neither", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := composeAgentInstructions(tc.parent, tc.child); got != tc.want {
				t.Errorf("composeAgentInstructions(%q, %q) = %q, want %q", tc.parent, tc.child, got, tc.want)
			}
		})
	}
}

// Guards the fixture helper itself: if dbfx ever stops writing parent_agent_id,
// every assertion above would fail for the wrong reason.
func TestSpecializationFixtureWritesTheColumn(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	parentID, childID := specializationFixture(t, "fixture-check")
	parent, _, _ := persistedParentOf(t, childID)
	if !parent.Valid {
		t.Fatal("fixture child has no parent_agent_id")
	}
	if got := uuidToString(parent); got != parentID {
		t.Fatalf("fixture child parent_agent_id = %s, want %s", got, parentID)
	}
}

// A malformed parent_agent_id is a request-boundary problem, so it must be 400
// rather than a 500 from the UUID round-trip.
func TestAgentSpecializationRejectsMalformedParentID(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	_, childID := specializationFixture(t, "malformed")

	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   map[string]any
		call   http.HandlerFunc
	}{
		{
			name:   "create",
			method: http.MethodPost,
			path:   "/api/agents",
			body:   map[string]any{"name": "malformed-child", "runtime_id": testRuntimeID, "parent_agent_id": "not-a-uuid"},
			call:   testHandler.CreateAgent,
		},
		{
			name:   "update",
			method: http.MethodPut,
			path:   "/api/agents/" + childID,
			body:   map[string]any{"parent_agent_id": "not-a-uuid"},
			call:   testHandler.UpdateAgent,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := specRequest(tc.method, tc.path, tc.body)
			if tc.method == http.MethodPut {
				req = testutil.WithURLParams(req, "id", childID)
			}
			resp := testutil.Call(t, tc.call, req).Want(http.StatusBadRequest)

			var body map[string]any
			if err := json.Unmarshal([]byte(resp.Text()), &body); err != nil {
				t.Fatalf("decode 400 body: %v: %s", err, resp.Text())
			}
		})
	}
}
