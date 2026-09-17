package agentconfig

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The canonical shape of DENE-470: which columns a specialisation inherits from
// its base role, and which it owns. Every other test in the tree asserts one
// call site's use of this rule; this one asserts the rule itself, so a field
// added to the list (or accidentally dropped from it) fails here first.

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

// baseRoleRow is a base role with a distinct, non-default value in every
// inherited column, so "the child kept its own value" and "the child inherited
// the base role's" can never look the same.
func baseRoleRow() db.Agent {
	return db.Agent{
		ID:                       pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		Name:                     "base role",
		RuntimeID:                pgtype.UUID{Bytes: [16]byte{2}, Valid: true},
		RuntimeMode:              "base-runtime-mode",
		RuntimeConfig:            []byte(`{"base":"config"}`),
		Visibility:               "workspace",
		PermissionMode:           "public_to",
		MaxConcurrentTasks:       7,
		OwnerID:                  pgtype.UUID{Bytes: [16]byte{3}, Valid: true},
		Instructions:             "base prompt",
		CustomEnv:                []byte(`{"BASE":"env"}`),
		CustomArgs:               []byte(`["--base-arg"]`),
		McpConfig:                []byte(`{"mcpServers":{"base":{}}}`),
		Model:                    text("base-model"),
		ThinkingLevel:            text("base-thinking"),
		ServiceTier:              text("base-tier"),
		ComposioToolkitAllowlist: []string{"base-toolkit"},
		DisabledRuntimeSkills:    []byte(`[{"name":"base-skill"}]`),
		ConversationStarters:     []byte(`[{"label":"base","prompt":"base"}]`),
		SwitchableModels:         []byte(`[{"model":"base-model","role":"default","note":""}]`),
		AutoRetryEnabled:         false,
		Status:                   "idle",
	}
}

// childRow is a specialisation that carries a different value in EVERY column
// the base role above also sets, plus the identity fields it owns.
func childRow() db.Agent {
	return db.Agent{
		ID:                       pgtype.UUID{Bytes: [16]byte{9}, Valid: true},
		WorkspaceID:              pgtype.UUID{Bytes: [16]byte{10}, Valid: true},
		Name:                     "specialisation",
		Description:              "own description",
		AvatarUrl:                pgtype.Text{String: "avatar.png", Valid: true},
		RuntimeID:                pgtype.UUID{Bytes: [16]byte{11}, Valid: true},
		RuntimeMode:              "child-runtime-mode",
		RuntimeConfig:            []byte(`{"child":"config"}`),
		Visibility:               "private",
		PermissionMode:           "private",
		MaxConcurrentTasks:       3,
		OwnerID:                  pgtype.UUID{Bytes: [16]byte{12}, Valid: true},
		Instructions:             "child prompt",
		CustomEnv:                []byte(`{"CHILD":"env"}`),
		CustomArgs:               []byte(`["--child-arg"]`),
		McpConfig:                []byte(`{"mcpServers":{"child":{}}}`),
		Model:                    text("child-model"),
		ThinkingLevel:            text("child-thinking"),
		ServiceTier:              text("child-tier"),
		ComposioToolkitAllowlist: []string{"child-toolkit"},
		DisabledRuntimeSkills:    []byte(`[{"name":"child-skill"}]`),
		ConversationStarters:     []byte(`[{"label":"child","prompt":"child"}]`),
		SwitchableModels:         []byte(`[{"model":"child-model","role":"default","note":""}]`),
		AutoRetryEnabled:         true,
		Status:                   "working",
		SystemKey:                pgtype.Text{String: "child-system-key", Valid: true},
		Kind:                     "user",
		ParentAgentID:            pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
	}
}

func TestInheritedTakesEveryConfigurationFieldFromTheBaseRole(t *testing.T) {
	base := baseRoleRow()
	child := childRow()
	got := Inherited(child, base)

	// Inherited: the base role's value wins in every one of these.
	inherited := map[string]struct{ got, want any }{
		"runtime_id":                 {got.RuntimeID, base.RuntimeID},
		"runtime_mode":               {got.RuntimeMode, base.RuntimeMode},
		"visibility":                 {got.Visibility, base.Visibility},
		"permission_mode":            {got.PermissionMode, base.PermissionMode},
		"max_concurrent_tasks":       {got.MaxConcurrentTasks, base.MaxConcurrentTasks},
		"auto_retry_enabled":         {got.AutoRetryEnabled, base.AutoRetryEnabled},
		"model":                      {got.Model, base.Model},
		"thinking_level":             {got.ThinkingLevel, base.ThinkingLevel},
		"service_tier":               {got.ServiceTier, base.ServiceTier},
		"runtime_config":             {string(got.RuntimeConfig), string(base.RuntimeConfig)},
		"custom_env":                 {string(got.CustomEnv), string(base.CustomEnv)},
		"custom_args":                {string(got.CustomArgs), string(base.CustomArgs)},
		"mcp_config":                 {string(got.McpConfig), string(base.McpConfig)},
		"disabled_runtime_skills":    {string(got.DisabledRuntimeSkills), string(base.DisabledRuntimeSkills)},
		"conversation_starters":      {string(got.ConversationStarters), string(base.ConversationStarters)},
		"switchable_models":          {string(got.SwitchableModels), string(base.SwitchableModels)},
		"composio_toolkit_allowlist": {got.ComposioToolkitAllowlist[0], base.ComposioToolkitAllowlist[0]},
	}
	for name, pair := range inherited {
		if pair.got != pair.want {
			t.Errorf("%s = %v, want the base role's %v", name, pair.got, pair.want)
		}
	}

	// Owned: identity, prompt and status stay with the child. `instructions`
	// is here on purpose — a specialisation's prompt is COMPOSED with the base
	// role's, never replaced by it, so this function must not touch it.
	owned := map[string]struct{ got, want any }{
		"id":              {got.ID, child.ID},
		"workspace_id":    {got.WorkspaceID, child.WorkspaceID},
		"name":            {got.Name, child.Name},
		"description":     {got.Description, child.Description},
		"avatar_url":      {got.AvatarUrl, child.AvatarUrl},
		"instructions":    {got.Instructions, child.Instructions},
		"status":          {got.Status, child.Status},
		"owner_id":        {got.OwnerID, child.OwnerID},
		"parent_agent_id": {got.ParentAgentID, child.ParentAgentID},
		"system_key":      {got.SystemKey, child.SystemKey},
		"kind":            {got.Kind, child.Kind},
	}
	for name, pair := range owned {
		if pair.got != pair.want {
			t.Errorf("%s = %v, want the child's own %v", name, pair.got, pair.want)
		}
	}
}

func TestInheritedLeavesABaseRoleAloneInEffect(t *testing.T) {
	// LoadInherited short-circuits on a base role and never reads a parent; the
	// pure function is the fallback if a caller ever applies it anyway, and
	// applying a row to itself must be a no-op.
	base := baseRoleRow()
	if got := Inherited(base, base); got.Model != base.Model || got.RuntimeID != base.RuntimeID || got.MaxConcurrentTasks != base.MaxConcurrentTasks {
		t.Fatalf("Inherited(row, row) changed the row: %+v", got)
	}
}
