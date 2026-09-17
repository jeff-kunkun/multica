package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Two-level specialisation at dispatch time (DENE-302). DENE-301 wrote the
// relationship and the rule — a specialisation runs with its base role's prompt
// and skills — and these tests pin the moment it becomes true for a real run:
// the claim response the daemon boots from.
//
// Everything asserted here is read off ONE claim response, because that payload
// is the whole contract: the daemon never reads the agent rows itself, so a
// value missing here is missing from the run.

// inheritanceFixture is a runtime with a base role, a specialisation of it, and
// a queued task for the specialisation. Either prompt may be empty, which is
// the point of building them per test rather than reusing one seeded pair.
type inheritanceFixture struct {
	RuntimeID string
	ParentID  string
	ChildID   string
	IssueID   string
	TaskID    string
}

func newInheritanceFixture(t *testing.T, name, parentPrompt, childPrompt string) inheritanceFixture {
	t.Helper()

	ctx := context.Background()
	runtimeID := createClaimReclaimRuntime(t, ctx, name+" runtime")
	parentID := dbfx.Agent(t, name+"-base", runtimeID, testutil.Cols{
		"instructions": parentPrompt,
	})
	childID := dbfx.Agent(t, name+"-spec", runtimeID, testutil.Cols{
		"instructions":    childPrompt,
		"parent_agent_id": parentID,
		// Room for a second claim: one test below claims twice on purpose, to
		// prove the base role is re-read rather than snapshotted.
		"max_concurrent_tasks": 4,
	})
	// The concurrency budget a specialisation runs under is its base role's
	// (DENE-470), so the headroom has to sit on the base role. Set here rather
	// than in the fixture's column list to keep the two rows' values distinct:
	// the capacity test below fails if the child's own 4 is what is honoured.
	dbfx.Exec(t, `UPDATE agent SET max_concurrent_tasks = 4 WHERE id = $1`, parentID)
	issueID := dbfx.Issue(t, name+" issue")
	taskID := dbfx.Task(t, childID, testutil.Cols{
		"runtime_id": runtimeID,
		"issue_id":   issueID,
	})

	return inheritanceFixture{
		RuntimeID: runtimeID,
		ParentID:  parentID,
		ChildID:   childID,
		IssueID:   issueID,
		TaskID:    taskID,
	}
}

// inheritanceClaim is the slice of a claim response these tests read: the
// effective prompt, the leader flag the daemon derives its squad role from, and
// both skill transports — inline skills for a daemon without the bundle
// capability, refs for one that has it.
type inheritanceClaim struct {
	TaskID         string
	Instructions   string
	IsLeaderTask   bool
	Skills         []service.AgentSkillData
	SkillRefs      []service.AgentSkillRefData
	CustomEnv      map[string]string
	CustomArgs     []string
	McpConfig      json.RawMessage
	Model          string
	ThinkingLevel  string
	ServiceTier    string
	RuntimeConfig  json.RawMessage
	DisabledSkills []DisabledRuntimeSkill
	Raw            string
}

// inheritanceClaimPayload is the shape claimInheritanceTask decodes. Kept
// beside the claim struct so a field added to one is obviously missing from the
// other.
type inheritanceClaimPayload struct {
	Task *struct {
		ID           string `json:"id"`
		IsLeaderTask bool   `json:"is_leader_task"`
		Agent        *struct {
			Instructions          string                      `json:"instructions"`
			Skills                []service.AgentSkillData    `json:"skills"`
			SkillRefs             []service.AgentSkillRefData `json:"skill_refs"`
			CustomEnv             map[string]string           `json:"custom_env"`
			CustomArgs            []string                    `json:"custom_args"`
			McpConfig             json.RawMessage             `json:"mcp_config"`
			Model                 string                      `json:"model"`
			ThinkingLevel         string                      `json:"thinking_level"`
			ServiceTier           string                      `json:"service_tier"`
			RuntimeConfig         json.RawMessage             `json:"runtime_config"`
			DisabledRuntimeSkills []DisabledRuntimeSkill      `json:"disabled_runtime_skills"`
		} `json:"agent"`
	} `json:"task"`
}

func claimInheritanceTask(t *testing.T, runtimeID, capabilities string) inheritanceClaim {
	t.Helper()

	req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil, testWorkspaceID, "dene-302-daemon")
	if capabilities != "" {
		req.Header.Set("X-Client-Capabilities", capabilities)
	}
	req = withURLParam(req, "runtimeId", runtimeID)

	var resp inheritanceClaimPayload
	w := testutil.Call(t, testHandler.ClaimTaskByRuntime, req).Want(http.StatusOK)
	w.JSON(&resp)
	if resp.Task == nil || resp.Task.Agent == nil {
		t.Fatalf("claim returned no task/agent payload: %s", w.Body.String())
	}
	return inheritanceClaim{
		TaskID:         resp.Task.ID,
		Instructions:   resp.Task.Agent.Instructions,
		IsLeaderTask:   resp.Task.IsLeaderTask,
		Skills:         resp.Task.Agent.Skills,
		SkillRefs:      resp.Task.Agent.SkillRefs,
		CustomEnv:      resp.Task.Agent.CustomEnv,
		CustomArgs:     resp.Task.Agent.CustomArgs,
		McpConfig:      resp.Task.Agent.McpConfig,
		Model:          resp.Task.Agent.Model,
		ThinkingLevel:  resp.Task.Agent.ThinkingLevel,
		ServiceTier:    resp.Task.Agent.ServiceTier,
		RuntimeConfig:  resp.Task.Agent.RuntimeConfig,
		DisabledSkills: resp.Task.Agent.DisabledRuntimeSkills,
		Raw:            w.Body.String(),
	}
}

// claimInheritanceTaskOrNone is claimInheritanceTask for the claims that are
// EXPECTED to deliver nothing (capacity), where "no task" is the assertion
// rather than a failure.
func claimInheritanceTaskOrNone(t *testing.T, runtimeID, capabilities string) (inheritanceClaim, bool) {
	t.Helper()

	req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil, testWorkspaceID, "dene-302-daemon")
	if capabilities != "" {
		req.Header.Set("X-Client-Capabilities", capabilities)
	}
	req = withURLParam(req, "runtimeId", runtimeID)

	var resp inheritanceClaimPayload
	w := testutil.Call(t, testHandler.ClaimTaskByRuntime, req).Want(http.StatusOK)
	w.JSON(&resp)
	if resp.Task == nil || resp.Task.Agent == nil {
		return inheritanceClaim{Raw: w.Body.String()}, false
	}
	return inheritanceClaim{
		TaskID:        resp.Task.ID,
		Instructions:  resp.Task.Agent.Instructions,
		Model:         resp.Task.Agent.Model,
		ThinkingLevel: resp.Task.Agent.ThinkingLevel,
		Raw:           w.Body.String(),
	}, true
}

// TestClaim_SpecialisationRunsBaseRolePromptThenItsOwn is the core DENE-302
// assertion: the daemon boots the specialisation with the base role's prompt in
// front of its own, separated by exactly one blank line.
func TestClaim_SpecialisationRunsBaseRolePromptThenItsOwn(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	fx := newInheritanceFixture(t, "inherit-prompt", "base role rules", "specialisation delta")
	claim := claimInheritanceTask(t, fx.RuntimeID, "")
	if claim.TaskID != fx.TaskID {
		t.Fatalf("claimed task id = %q, want %q: %s", claim.TaskID, fx.TaskID, claim.Raw)
	}

	want := "base role rules\n\nspecialisation delta"
	if claim.Instructions != want {
		t.Fatalf("claimed instructions = %q, want %q", claim.Instructions, want)
	}
}

// TestClaim_SpecialisationPromptEdgesDropTheSeparator pins the empty halves: an
// absent parent prompt must not leave a leading blank line, an absent child
// prompt must not leave a trailing one, and a base role must be untouched by
// the whole mechanism.
func TestClaim_SpecialisationPromptEdgesDropTheSeparator(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	tests := []struct {
		name         string
		parentPrompt string
		childPrompt  string
		want         string
	}{
		{
			name:         "only the child has a prompt",
			parentPrompt: "",
			childPrompt:  "specialisation delta",
			want:         "specialisation delta",
		},
		{
			name:         "only the base role has a prompt",
			parentPrompt: "base role rules",
			childPrompt:  "",
			want:         "base role rules",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx := newInheritanceFixture(t, "inherit-edges-"+strings.ReplaceAll(tc.name, " ", "-"), tc.parentPrompt, tc.childPrompt)
			claim := claimInheritanceTask(t, fx.RuntimeID, "")
			if claim.Instructions != tc.want {
				t.Fatalf("claimed instructions = %q, want %q", claim.Instructions, tc.want)
			}
		})
	}
}

// TestClaim_SpecialisationSquadBriefingStacksLast fixes the layering the
// composition order exists for: inherited prompt innermost, the agent's own
// prompt next, squad context appended last. A briefing injected under the base
// role's rules would read as if the base role had written it.
func TestClaim_SpecialisationSquadBriefingStacksLast(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	fx := newInheritanceFixture(t, "inherit-squad", "base role rules", "specialisation delta")
	squadID := dbfx.Squad(t, "inherit-squad squad", fx.ChildID)
	// The leader task carries the squad id; the claim injects the briefing only
	// when the claiming agent is still that squad's leader.
	dbfx.Exec(t, `UPDATE agent_task_queue SET is_leader_task = TRUE, squad_id = $2 WHERE id = $1`, fx.TaskID, squadID)

	claim := claimInheritanceTask(t, fx.RuntimeID, "")
	if !claim.IsLeaderTask {
		t.Fatalf("claim must still report the leader role: %s", claim.Raw)
	}

	parentAt := strings.Index(claim.Instructions, "base role rules")
	childAt := strings.Index(claim.Instructions, "specialisation delta")
	briefingAt := strings.Index(claim.Instructions, "## Squad Operating Protocol")
	if parentAt < 0 || childAt < 0 || briefingAt < 0 {
		t.Fatalf("claim is missing one of the three layers (parent=%d child=%d briefing=%d):\n%s",
			parentAt, childAt, briefingAt, claim.Instructions)
	}
	if !(parentAt < childAt && childAt < briefingAt) {
		t.Fatalf("layers out of order (parent=%d child=%d briefing=%d), want parent < child < briefing:\n%s",
			parentAt, childAt, briefingAt, claim.Instructions)
	}
}

// seedInheritanceSkill inserts one workspace skill, optionally attaches it to
// agents, and gives it one supporting file so a test can see that an inherited
// skill arrives complete rather than as a bare name.
func seedInheritanceSkill(t *testing.T, name, path string, agentIDs ...string) string {
	t.Helper()

	skillID := dbfx.Insert(t, "skill", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"name":         name,
		"description":  "DENE-302 inheritance fixture",
		"content":      name + " body",
		"config":       testutil.Raw("'{}'::jsonb"),
		"created_by":   testUserID,
	})
	for _, agentID := range agentIDs {
		dbfx.InsertNoID(t, "agent_skill",
			testutil.Cols{"agent_id": agentID, "skill_id": skillID},
			"agent_id = $1 AND skill_id = $2", agentID, skillID)
	}
	if path != "" {
		dbfx.InsertNoID(t, "skill_file",
			testutil.Cols{"skill_id": skillID, "path": path, "content": path + " content"},
			"skill_id = $1", skillID)
	}
	return skillID
}

// countSkillNames counts how many entries carry name, so a union that forgot to
// deduplicate fails instead of passing on a single membership check.
func countSkillNames(names []string, want string) int {
	count := 0
	for _, name := range names {
		if name == want {
			count++
		}
	}
	return count
}

// indexOfSkillName reports where in the payload a skill sits, for the order
// assertions. Absence is len(names), which is past every present entry — the
// membership checks above are what report a missing skill.
func indexOfSkillName(names []string, want string) int {
	for i, name := range names {
		if name == want {
			return i
		}
	}
	return len(names)
}

// TestClaim_SpecialisationCarriesTheSkillUnion covers both skill transports.
// The inline path is what an older daemon reads; the refs path is what a
// current one reads, and it is the path that then resolves each ref back — so
// the union has to be advertised AND resolvable, or the daemon would be handed
// a ref it can never satisfy and fail the task on a skill the agent is
// legitimately configured with.
func TestClaim_SpecialisationCarriesTheSkillUnion(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	t.Run("inline skills", func(t *testing.T) {
		fx := newInheritanceFixture(t, "inherit-skills-inline", "base role rules", "specialisation delta")
		// Shared by both sides: it must appear once, not twice.
		seedInheritanceSkill(t, "inherit-shared-skill", "", fx.ParentID, fx.ChildID)
		seedInheritanceSkill(t, "inherit-base-only-skill", "docs/base.md", fx.ParentID)
		seedInheritanceSkill(t, "inherit-spec-only-skill", "", fx.ChildID)

		claim := claimInheritanceTask(t, fx.RuntimeID, "")
		names := make([]string, 0, len(claim.Skills))
		for _, skill := range claim.Skills {
			names = append(names, skill.Name)
		}
		for _, want := range []string{"inherit-shared-skill", "inherit-base-only-skill", "inherit-spec-only-skill"} {
			if countSkillNames(names, want) == 0 {
				t.Fatalf("claim is missing %q; skills=%v", want, names)
			}
		}
		if got := countSkillNames(names, "inherit-shared-skill"); got != 1 {
			t.Fatalf("skill bound to both sides appears %d times, want 1; skills=%v", got, names)
		}
		// The base role's half comes first, so the inherited set is a stable
		// prefix of the payload rather than interleaved with the child's own.
		if indexOfSkillName(names, "inherit-base-only-skill") > indexOfSkillName(names, "inherit-spec-only-skill") {
			t.Fatalf("base role skills are not ahead of the specialisation's own; skills=%v", names)
		}
		for _, skill := range claim.Skills {
			if skill.Name != "inherit-base-only-skill" {
				continue
			}
			if len(skill.Files) != 1 || skill.Files[0].Path != "docs/base.md" {
				t.Fatalf("inherited skill arrived without its files: %+v", skill.Files)
			}
		}
	})

	t.Run("skill refs resolve", func(t *testing.T) {
		fx := newInheritanceFixture(t, "inherit-skills-refs", "base role rules", "specialisation delta")
		seedInheritanceSkill(t, "inherit-ref-shared-skill", "", fx.ParentID, fx.ChildID)
		seedInheritanceSkill(t, "inherit-ref-base-only-skill", "", fx.ParentID)

		claim := claimInheritanceTask(t, fx.RuntimeID, protocol.DaemonCapabilitySkillBundlesV1)
		refNames := make([]string, 0, len(claim.SkillRefs))
		for _, ref := range claim.SkillRefs {
			refNames = append(refNames, ref.Name)
		}
		if countSkillNames(refNames, "inherit-ref-base-only-skill") == 0 {
			t.Fatalf("claim refs are missing the base role's skill; refs=%v", refNames)
		}
		if got := countSkillNames(refNames, "inherit-ref-shared-skill"); got != 1 {
			t.Fatalf("shared skill ref appears %d times, want 1; refs=%v", got, refNames)
		}

		// Resolve the base role's ref the way the daemon does — one request per
		// skill, against the claimed task. A 404 here is the failure mode this
		// half of the test exists for: the claim advertised a ref the server
		// would not serve.
		var wanted service.AgentSkillRefData
		for _, ref := range claim.SkillRefs {
			if ref.Name == "inherit-ref-base-only-skill" {
				wanted = ref
			}
		}
		req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+fx.RuntimeID+"/tasks/"+claim.TaskID+"/skill-bundles/resolve",
			resolveSkillBundlesRequest{Skills: []resolveSkillBundleRef{{
				ID:     wanted.ID,
				Source: wanted.Source,
				Hash:   wanted.Hash,
			}}},
			testWorkspaceID, "dene-302-daemon")
		req = withURLParams(req, "runtimeId", fx.RuntimeID, "taskId", claim.TaskID)

		var resolved struct {
			Bundles []service.AgentSkillData `json:"bundles"`
		}
		testutil.Call(t, testHandler.ResolveTaskSkillBundles, req).Want(http.StatusOK).JSON(&resolved)
		if len(resolved.Bundles) != 1 || resolved.Bundles[0].Name != "inherit-ref-base-only-skill" {
			t.Fatalf("resolved bundles = %+v, want only the base role's skill", resolved.Bundles)
		}
	})
}

// errInjectedBaseRoleRead is the transient base-role read failure below. It
// must NOT be pgx.ErrNoRows: a missing base role is a different case (the
// specialisation runs on its own prompt), and a test that conflated the two
// would pass on the degradation it is meant to catch.
var errInjectedBaseRoleRead = errors.New("injected base role read failure")

// baseRoleReadFailDBTX passes every statement through to the real pool except
// the read of ONE agent id — the base role of the agent under test. Narrowing
// by argument rather than by query name matters here: the same
// GetAgentInWorkspace statement also serves every other agent lookup on the
// claim path, and failing those would test something else.
type baseRoleReadFailDBTX struct {
	inner    db.DBTX
	parentID pgtype.UUID
}

func (f baseRoleReadFailDBTX) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return f.inner.Exec(ctx, sql, args...)
}

func (f baseRoleReadFailDBTX) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if f.matches(sql, args) {
		return nil, errInjectedBaseRoleRead
	}
	return f.inner.Query(ctx, sql, args...)
}

func (f baseRoleReadFailDBTX) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if f.matches(sql, args) {
		return failingRow{err: errInjectedBaseRoleRead}
	}
	return f.inner.QueryRow(ctx, sql, args...)
}

func (f baseRoleReadFailDBTX) matches(sql string, args []any) bool {
	if !strings.Contains(sql, "GetAgentInWorkspace") || len(args) == 0 {
		return false
	}
	id, ok := args[0].(pgtype.UUID)
	return ok && id.Valid && id == f.parentID
}

type failingRow struct{ err error }

func (r failingRow) Scan(...any) error { return r.err }

// TestClaim_SpecialisationRefusesDispatchWhenBaseRoleIsUnreadable pins the
// fail-closed half of the inheritance read. A swallowed error here is
// indistinguishable from a base role with no prompt, so the daemon would boot
// the specialisation missing half its rules with nothing to notice it. The task
// is preserved rather than settled, exactly as a failed skill read is, so the
// stale-dispatched reclaim redelivers it once the read recovers.
func TestClaim_SpecialisationRefusesDispatchWhenBaseRoleIsUnreadable(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	fx := newInheritanceFixture(t, "inherit-read-fail", "base role rules", "specialisation delta")
	failing := New(
		db.New(baseRoleReadFailDBTX{inner: testPool, parentID: parseUUID(fx.ParentID)}),
		testPool,
		testHandler.Hub,
		testHandler.Bus,
		testHandler.EmailService,
		nil,
		nil,
		analytics.NoopClient{},
		Config{},
	)

	req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+fx.RuntimeID+"/tasks/claim", nil, testWorkspaceID, "dene-302-daemon")
	req = withURLParam(req, "runtimeId", fx.RuntimeID)
	w := testutil.Call(t, failing.ClaimTaskByRuntime, req).Want(http.StatusInternalServerError)
	if !strings.Contains(w.Text(), "failed to load agent instructions") {
		t.Fatalf("claim failed for a different reason than the base role read: %s", w.Text())
	}

	var status string
	var started bool
	dbfx.QueryRow(t, `SELECT status, started_at IS NOT NULL FROM agent_task_queue WHERE id = $1`, fx.TaskID).Scan(&status, &started)
	if status != "dispatched" {
		t.Fatalf("task status = %q, want dispatched (a refused claim must not settle the task)", status)
	}
	if started {
		t.Fatal("task was marked started despite the claim being refused")
	}
}

// TestClaim_SpecialisationPicksUpBaseRoleEditsOnTheNextClaim pins the "no cache,
// no snapshot" half of DENE-302: the base role is read on every dispatch, so an
// edit to its prompt — or another skill bound to it — lands on the
// specialisation's next task with nothing to invalidate.
func TestClaim_SpecialisationPicksUpBaseRoleEditsOnTheNextClaim(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	fx := newInheritanceFixture(t, "inherit-reread", "base role rules", "specialisation delta")
	first := claimInheritanceTask(t, fx.RuntimeID, "")
	if first.Instructions != "base role rules\n\nspecialisation delta" {
		t.Fatalf("first claim instructions = %q, want the base role's original prompt first", first.Instructions)
	}

	dbfx.Exec(t, `UPDATE agent SET instructions = 'base role rules v2' WHERE id = $1`, fx.ParentID)
	seedInheritanceSkill(t, "inherit-late-skill", "", fx.ParentID)

	// A new task is a new dispatch; the first claim consumed the only queued one.
	// It hangs off a second issue only because one pending task per (issue,
	// agent, thread) is a unique index — nothing else about the issue matters.
	secondTaskID := dbfx.Task(t, fx.ChildID, testutil.Cols{
		"runtime_id": fx.RuntimeID,
		"issue_id":   dbfx.Issue(t, "inherit-reread second issue"),
	})
	second := claimInheritanceTask(t, fx.RuntimeID, "")
	if second.TaskID != secondTaskID {
		t.Fatalf("second claim delivered task %q, want %q: %s", second.TaskID, secondTaskID, second.Raw)
	}
	if second.Instructions != "base role rules v2\n\nspecialisation delta" {
		t.Fatalf("second claim instructions = %q, want the edited base role prompt", second.Instructions)
	}
	skillNames := make([]string, 0, len(second.Skills))
	for _, skill := range second.Skills {
		skillNames = append(skillNames, skill.Name)
	}
	if countSkillNames(skillNames, "inherit-late-skill") == 0 {
		t.Fatalf("a skill bound to the base role after the first claim never arrived; skills=%v", skillNames)
	}
}

// ---------------------------------------------------------------------------
// Configuration inheritance at dispatch time (DENE-470)
//
// The daemon never reads the agent rows itself: everything it boots the run
// with arrives in this one payload. A configuration field that is not resolved
// from the base role HERE is a field the run does not have, however correct the
// row looks.

// configuredInheritanceFixture is a base role with a distinct value in every
// configuration field the claim payload carries, a specialisation that carries a
// DIFFERENT value in each of the same columns, and a queued task for the
// specialisation. Distinguishing the two rows is the whole point: a payload
// assertion that passes on either row would prove nothing.
type configuredInheritanceFixture struct {
	RuntimeID string
	ParentID  string
	ChildID   string
	IssueID   string
	TaskID    string
}

func newConfiguredInheritanceFixture(t *testing.T, name string) configuredInheritanceFixture {
	t.Helper()

	ctx := context.Background()
	runtimeID := createClaimReclaimRuntime(t, ctx, name+" runtime")
	provider := "handler_test_runtime"
	// Scoped to this runtime and provider: disabledRuntimeSkillsFor drops every
	// entry that does not match both, so an unscoped entry would make the
	// payload assertion pass vacuously.
	baseDisabled := testutil.Raw(fmt.Sprintf(
		`'[{"runtime_id":%q,"provider":%q,"root":"provider","key":"base-skill","name":"Base Skill"}]'::jsonb`,
		runtimeID, provider))
	childDisabled := testutil.Raw(fmt.Sprintf(
		`'[{"runtime_id":%q,"provider":%q,"root":"provider","key":"child-skill","name":"Child Skill"}]'::jsonb`,
		runtimeID, provider))

	parentID := dbfx.Agent(t, name+"-base", runtimeID, testutil.Cols{
		"instructions":            "base role rules",
		"model":                   "base-model",
		"thinking_level":          "base-thinking",
		"service_tier":            "base-tier",
		"custom_env":              testutil.Raw(`'{"BASE_KEY":"base-value"}'::jsonb`),
		"custom_args":             testutil.Raw(`'["--base-flag"]'::jsonb`),
		"mcp_config":              testutil.Raw(`'{"mcpServers":{"base":{}}}'::jsonb`),
		"runtime_config":          testutil.Raw(`'{"base":true}'::jsonb`),
		"disabled_runtime_skills": baseDisabled,
		"max_concurrent_tasks":    4,
	})
	childID := dbfx.Agent(t, name+"-spec", runtimeID, testutil.Cols{
		"instructions":            "specialisation delta",
		"parent_agent_id":         parentID,
		"model":                   "child-model",
		"thinking_level":          "child-thinking",
		"service_tier":            "child-tier",
		"custom_env":              testutil.Raw(`'{"CHILD_KEY":"child-value"}'::jsonb`),
		"custom_args":             testutil.Raw(`'["--child-flag"]'::jsonb`),
		"mcp_config":              testutil.Raw(`'{"mcpServers":{"child":{}}}'::jsonb`),
		"runtime_config":          testutil.Raw(`'{"child":true}'::jsonb`),
		"disabled_runtime_skills": childDisabled,
		"max_concurrent_tasks":    1,
	})
	issueID := dbfx.Issue(t, name+" issue")
	taskID := dbfx.Task(t, childID, testutil.Cols{
		"runtime_id": runtimeID,
		"issue_id":   issueID,
	})

	return configuredInheritanceFixture{
		RuntimeID: runtimeID,
		ParentID:  parentID,
		ChildID:   childID,
		IssueID:   issueID,
		TaskID:    taskID,
	}
}

// TestClaim_SpecialisationRunsOnBaseRoleConfiguration is the configuration
// counterpart of the DENE-302 prompt test: every field the daemon boots with
// comes from the base role, and the specialisation's own column values — which
// this fixture sets to something different on purpose — never reach the run.
func TestClaim_SpecialisationRunsOnBaseRoleConfiguration(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	fx := newConfiguredInheritanceFixture(t, "inherit-config")
	claim := claimInheritanceTask(t, fx.RuntimeID, "")
	if claim.TaskID != fx.TaskID {
		t.Fatalf("claimed task id = %q, want %q: %s", claim.TaskID, fx.TaskID, claim.Raw)
	}

	if claim.Model != "base-model" {
		t.Errorf("model = %q, want the base role's", claim.Model)
	}
	if claim.ThinkingLevel != "base-thinking" {
		t.Errorf("thinking_level = %q, want the base role's", claim.ThinkingLevel)
	}
	if claim.ServiceTier != "base-tier" {
		t.Errorf("service_tier = %q, want the base role's", claim.ServiceTier)
	}
	if claim.CustomEnv["BASE_KEY"] != "base-value" || len(claim.CustomEnv) != 1 {
		t.Errorf("custom_env = %v, want only the base role's variables", claim.CustomEnv)
	}
	if len(claim.CustomArgs) != 1 || claim.CustomArgs[0] != "--base-flag" {
		t.Errorf("custom_args = %v, want only the base role's arguments", claim.CustomArgs)
	}
	if !strings.Contains(string(claim.McpConfig), `"base"`) || strings.Contains(string(claim.McpConfig), `"child"`) {
		t.Errorf("mcp_config = %s, want the base role's servers", claim.McpConfig)
	}
	if !strings.Contains(string(claim.RuntimeConfig), `"base":true`) {
		t.Errorf("runtime_config = %s, want the base role's", claim.RuntimeConfig)
	}
	if len(claim.DisabledSkills) != 1 || claim.DisabledSkills[0].Name != "Base Skill" {
		t.Errorf("disabled_runtime_skills = %+v, want the base role's", claim.DisabledSkills)
	}
}

// TestClaim_SpecialisationCapacityIsTheBaseRolesBudget pins the admission point.
// The child's own row says 4; the base role says 1 and already has one run in
// flight. If the child's column were still what capacity is counted against, the
// claim below would return a task.
func TestClaim_SpecialisationCapacityIsTheBaseRolesBudget(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	fx := newInheritanceFixture(t, "inherit-capacity", "base role rules", "specialisation delta")
	// The base role's budget, not the child's: one slot, already taken.
	dbfx.Exec(t, `UPDATE agent SET max_concurrent_tasks = 1 WHERE id = $1`, fx.ParentID)
	dbfx.Exec(t, `UPDATE agent SET max_concurrent_tasks = 4 WHERE id = $1`, fx.ChildID)
	dbfx.Task(t, fx.ChildID, testutil.Cols{
		"runtime_id": fx.RuntimeID,
		"status":     "running",
		"started_at": testutil.Raw("now()"),
	})
	// A second queued task on its own issue — one pending task per (issue,
	// agent) is a unique index — so the paused claim has something queued to
	// refuse, which is where a wrong capacity answer shows up.
	dbfx.Task(t, fx.ChildID, testutil.Cols{
		"runtime_id": fx.RuntimeID,
		"issue_id":   dbfx.Issue(t, "inherit-capacity second issue"),
	})

	if _, claimed := claimInheritanceTaskOrNone(t, fx.RuntimeID, ""); claimed {
		t.Fatal("a specialisation claimed a task at its own max_concurrent_tasks instead of its base role's")
	}

	// Raising the BASE ROLE's budget is what admits the next run — the child's
	// column has not moved.
	dbfx.Exec(t, `UPDATE agent SET max_concurrent_tasks = 2 WHERE id = $1`, fx.ParentID)
	claim, claimed := claimInheritanceTaskOrNone(t, fx.RuntimeID, "")
	if !claimed {
		t.Fatalf("raising the base role's budget did not admit the queued task: %s", claim.Raw)
	}
	if claim.TaskID == "" {
		t.Fatalf("claim delivered no task id: %s", claim.Raw)
	}
}
