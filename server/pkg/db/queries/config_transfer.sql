-- Workspace config export / import (DENE-202 / DENE-204).
-- Export SELECTs deliberately omit secret columns listed in
-- docs/kun/config-export-import.md §2.1.

-- name: CountWorkspaceIssues :one
SELECT count(*)::bigint FROM issue WHERE workspace_id = $1;

-- name: DaemonIDExistsInWorkspace :one
SELECT EXISTS(
    SELECT 1 FROM agent_runtime
    WHERE workspace_id = $1 AND daemon_id = $2
) AS exists;

-- name: TryConfigImportLock :one
-- Transaction-scoped lock taken by a dedicated transaction that stays open
-- for the whole apply, serialising imports into one workspace.
SELECT pg_try_advisory_xact_lock(hashtextextended('config_import:' || $1::text, 0));

-- name: PatchWorkspaceConfigImport :one
UPDATE workspace SET
    settings = COALESCE(sqlc.narg('settings'), settings),
    context = COALESCE(sqlc.narg('context'), context),
    repos = COALESCE(sqlc.narg('repos'), repos),
    issue_prefix = COALESCE(sqlc.narg('issue_prefix'), issue_prefix),
    attribution_fail_closed = COALESCE(sqlc.narg('attribution_fail_closed'), attribution_fail_closed),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ExportLabels :many
SELECT id, resource_type, name, color, description
FROM issue_label
WHERE workspace_id = $1
ORDER BY resource_type, LOWER(name);

-- name: GetLabelByIdentity :one
SELECT * FROM issue_label
WHERE workspace_id = $1
  AND resource_type = $2
  AND LOWER(name) = LOWER($3);

-- name: ExportIssueStatuses :many
SELECT id, key, name, description, category, color, position, is_system,
       (archived_at IS NOT NULL)::bool AS archived
FROM issue_status
WHERE workspace_id = $1
  AND (sqlc.arg('include_archived')::bool OR archived_at IS NULL)
ORDER BY position, key;

-- name: UpdateIssueStatusEntryForImport :one
-- Import may patch built-in name/description/color/position. key and
-- category stay immutable.
UPDATE issue_status SET
    name = COALESCE(sqlc.narg('name'), name),
    description = COALESCE(sqlc.narg('description'), description),
    color = COALESCE(sqlc.narg('color'), color),
    position = COALESCE(sqlc.narg('position'), position),
    updated_at = now()
WHERE id = sqlc.arg('id')::uuid
  AND workspace_id = sqlc.arg('workspace_id')::uuid
RETURNING *;

-- name: ExportIssueProperties :many
SELECT id, name, type, description, icon, config, position,
       (archived_at IS NOT NULL)::bool AS archived
FROM issue_property
WHERE workspace_id = $1
  AND (sqlc.arg('include_archived')::bool OR archived_at IS NULL)
ORDER BY position, LOWER(name);

-- name: GetIssuePropertyByName :one
SELECT * FROM issue_property
WHERE workspace_id = $1 AND LOWER(name) = LOWER($2)
ORDER BY created_at ASC
LIMIT 1;

-- name: UpdateIssuePropertyPosition :exec
UPDATE issue_property SET position = $2, updated_at = now()
WHERE id = $1;

-- name: ExportSkills :many
SELECT id, name, description, content, config
FROM skill
WHERE workspace_id = $1
  AND plugin_installation_id IS NULL
ORDER BY LOWER(name);

-- name: ExportSkillFiles :many
SELECT skill_id, path, content
FROM skill_file
WHERE skill_id = ANY(sqlc.arg('skill_ids')::uuid[])
ORDER BY skill_id, path;

-- name: ExportSkillLabelIDs :many
SELECT skill_id, label_id
FROM skill_to_label
WHERE skill_id = ANY(sqlc.arg('skill_ids')::uuid[])
ORDER BY skill_id, label_id;

-- name: ExportMcpServers :many
-- Transport is derived from non-secret keys only; config itself is not returned.
SELECT id, name,
       LOWER(COALESCE(config->>'type', '')) AS config_type,
       (config ? 'command')::bool AS has_command,
       (config ? 'url')::bool AS has_url
FROM workspace_mcp_server
WHERE workspace_id = $1
ORDER BY LOWER(name);

-- name: GetWorkspaceMcpServerByName :one
SELECT id, name
FROM workspace_mcp_server
WHERE workspace_id = $1 AND name = $2;

-- name: ExportUserAgents :many
SELECT id, name, description, instructions, avatar_url, runtime_mode, runtime_config,
       custom_args, model, thinking_level, service_tier, visibility, permission_mode,
       max_concurrent_tasks, conversation_starters, disabled_runtime_skills,
       composio_toolkit_allowlist,
       (archived_at IS NOT NULL)::bool AS archived,
       (SELECT COUNT(*)::int FROM jsonb_object_keys(COALESCE(custom_env, '{}'::jsonb))) AS custom_env_key_count,
       (mcp_config IS NOT NULL AND mcp_config <> 'null'::jsonb AND mcp_config <> '{}'::jsonb) AS has_mcp_config,
       (runtime_config #>> '{gateway,token}' IS NOT NULL AND btrim(runtime_config #>> '{gateway,token}') <> '') AS has_gateway_token
FROM agent
WHERE workspace_id = $1 AND kind = 'user'
  AND (sqlc.arg('include_archived')::bool OR archived_at IS NULL)
ORDER BY created_at ASC;

-- name: ExportSystemAgents :many
SELECT system_key, instructions, model, thinking_level, service_tier,
       conversation_starters, disabled_runtime_skills
FROM agent
WHERE workspace_id = $1 AND kind = 'system'
  AND system_key IS NOT NULL AND system_key <> ''
  AND system_key NOT LIKE 'agent_builder:%'
  AND (sqlc.arg('include_archived')::bool OR archived_at IS NULL)
ORDER BY system_key;

-- name: GetUserAgentByName :one
SELECT * FROM agent
WHERE workspace_id = $1 AND kind = 'user' AND name = $2
ORDER BY created_at ASC
LIMIT 1;

-- name: ExportAgentSkillBindings :many
SELECT agent_id, skill_id, enabled
FROM agent_skill
WHERE agent_id = ANY(sqlc.arg('agent_ids')::uuid[])
ORDER BY agent_id, skill_id;

-- name: ExportAgentLabelIDs :many
SELECT agent_id, label_id
FROM agent_to_label
WHERE agent_id = ANY(sqlc.arg('agent_ids')::uuid[])
ORDER BY agent_id, label_id;

-- name: ExportAgentMcpBindings :many
SELECT agent_id, server_id, enabled
FROM agent_mcp_server
WHERE agent_id = ANY(sqlc.arg('agent_ids')::uuid[])
ORDER BY agent_id, server_id;

-- name: DeleteAgentMcpServersByAgent :exec
DELETE FROM agent_mcp_server WHERE agent_id = $1;

-- name: ExportSquads :many
SELECT id, name, description, instructions, avatar_url, leader_id,
       (archived_at IS NOT NULL)::bool AS archived
FROM squad
WHERE workspace_id = $1
  AND (sqlc.arg('include_archived')::bool OR archived_at IS NULL)
ORDER BY created_at ASC;

-- name: ExportSquadMembers :many
SELECT squad_id, member_type, member_id, role
FROM squad_member
WHERE squad_id = ANY(sqlc.arg('squad_ids')::uuid[])
ORDER BY squad_id, created_at ASC;

-- name: GetEarliestSquadByName :one
SELECT * FROM squad
WHERE workspace_id = $1 AND name = $2
ORDER BY created_at ASC
LIMIT 1;

-- name: DeleteSquadMembersBySquad :exec
DELETE FROM squad_member WHERE squad_id = $1;

-- name: ExportProjects :many
SELECT id, title, description, icon, status, priority, lead_type, lead_id, start_date, due_date
FROM project
WHERE workspace_id = $1
  AND (
    sqlc.arg('include_archived')::bool
    OR status NOT IN ('completed', 'cancelled')
  )
ORDER BY created_at ASC;

-- name: ExportProjectResources :many
SELECT project_id, resource_type, resource_ref, label, position
FROM project_resource
WHERE project_id = ANY(sqlc.arg('project_ids')::uuid[])
ORDER BY project_id, position ASC, created_at ASC;

-- name: GetEarliestProjectByTitle :one
SELECT * FROM project
WHERE workspace_id = $1 AND title = $2
ORDER BY created_at ASC
LIMIT 1;

-- name: DeleteProjectResourcesByProject :exec
DELETE FROM project_resource WHERE project_id = $1;

-- name: ExportAutopilots :many
SELECT id, title, description, assignee_type, assignee_id, project_id,
       execution_mode, issue_title_template, status
FROM autopilot
WHERE workspace_id = $1
  AND (sqlc.arg('include_archived')::bool OR status <> 'archived')
ORDER BY created_at ASC;

-- name: ExportAutopilotTriggers :many
SELECT autopilot_id, kind, enabled, cron_expression, timezone, label, provider, event_filters,
       (webhook_token IS NOT NULL AND webhook_token <> '') AS has_webhook_token,
       (signing_secret IS NOT NULL AND signing_secret <> '') AS has_signing_secret
FROM autopilot_trigger
WHERE autopilot_id = ANY(sqlc.arg('autopilot_ids')::uuid[])
ORDER BY autopilot_id, created_at ASC;

-- name: ExportAutopilotSubscribers :many
SELECT autopilot_id, user_type, user_id
FROM autopilot_subscriber
WHERE autopilot_id = ANY(sqlc.arg('autopilot_ids')::uuid[])
ORDER BY autopilot_id, created_at ASC;

-- name: ExportAutopilotCollaborators :many
SELECT autopilot_id, user_type, user_id
FROM autopilot_collaborator
WHERE autopilot_id = ANY(sqlc.arg('autopilot_ids')::uuid[])
ORDER BY autopilot_id, created_at ASC;

-- name: GetEarliestAutopilotByTitle :one
SELECT * FROM autopilot
WHERE workspace_id = $1 AND title = $2
ORDER BY created_at ASC
LIMIT 1;

-- name: DeleteAutopilotTriggersByAutopilot :exec
DELETE FROM autopilot_trigger WHERE autopilot_id = $1;

-- name: ListAutopilotTriggerIDs :many
SELECT id, kind, label, webhook_token, signing_secret
FROM autopilot_trigger
WHERE autopilot_id = $1
ORDER BY created_at ASC;

-- name: ExportQuickActions :many
SELECT id, name, description, assignee_type, assignee_id, prompt, visibility, status, created_by_id
FROM quick_action
WHERE workspace_id = $1
  AND (sqlc.arg('include_archived')::bool OR status = 'active')
ORDER BY LOWER(name);

-- name: GetEarliestQuickActionByName :one
SELECT * FROM quick_action
WHERE workspace_id = $1 AND name = $2
ORDER BY created_at ASC
LIMIT 1;

-- name: ExportIssueViews :many
SELECT id, name, scope_type, scope_id, scope_variant, visibility, definition_version, query, display
FROM issue_view
WHERE workspace_id = $1
  AND visibility IN ('workspace', 'project')
  AND scope_type <> 'my'
ORDER BY created_at ASC;

-- name: GetWorkspaceIssueViewByIdentity :one
SELECT * FROM issue_view
WHERE workspace_id = $1
  AND visibility IN ('workspace', 'project')
  AND scope_type = $2
  AND scope_id IS NOT DISTINCT FROM sqlc.narg('scope_id')::uuid
  AND name = $3
ORDER BY created_at ASC
LIMIT 1;

-- name: ExportGitHubInstallations :many
SELECT account_login, account_type
FROM github_installation
WHERE workspace_id = $1
ORDER BY created_at ASC;

-- name: ExportVCSConnections :many
SELECT provider, instance_url, account_login
FROM vcs_connection
WHERE workspace_id = $1
ORDER BY created_at ASC;

-- name: ExportChannelInstallations :many
SELECT channel_type, agent_id
FROM channel_installation
WHERE workspace_id = $1
ORDER BY created_at ASC;

-- name: ExportPluginInstallations :many
SELECT plugin_key, version, enabled, granted_scopes, config
FROM plugin_installation
WHERE workspace_id = $1
ORDER BY created_at ASC;
