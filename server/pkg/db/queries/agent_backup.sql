-- name: CreateAgentBackup :one
-- Snapshots an agent's whole configuration before it is deleted: the agent row
-- as stored (env values, MCP config, instructions and all) and the rows that
-- configure it. Returns no row when the agent is gone, so a delete never
-- proceeds on an empty backup.
INSERT INTO agent_backup (workspace_id, agent_id, agent_name, deleted_by, snapshot)
SELECT a.workspace_id, a.id, a.name, sqlc.arg('deleted_by')::uuid,
    jsonb_build_object(
        'agent', to_jsonb(a),
        'invocation_targets', COALESCE((SELECT jsonb_agg(to_jsonb(t)) FROM agent_invocation_target t WHERE t.agent_id = a.id), '[]'::jsonb),
        'mcp_servers', COALESCE((SELECT jsonb_agg(to_jsonb(m)) FROM agent_mcp_server m WHERE m.agent_id = a.id), '[]'::jsonb),
        'labels', COALESCE((SELECT jsonb_agg(to_jsonb(l)) FROM agent_to_label l WHERE l.agent_id = a.id), '[]'::jsonb)
    )
FROM agent a
WHERE a.id = sqlc.arg('agent_id')::uuid
RETURNING id, workspace_id, agent_id, agent_name, deleted_by, created_at,
    (SELECT count(*) FROM jsonb_object_keys(snapshot->'agent'->'custom_env'))::int AS env_key_count;

-- name: ListAgentBackups :many
-- Summaries only: the snapshot carries secrets and never leaves the server.
SELECT id, workspace_id, agent_id, agent_name, deleted_by, created_at,
    (SELECT count(*) FROM jsonb_object_keys(snapshot->'agent'->'custom_env'))::int AS env_key_count
FROM agent_backup
WHERE workspace_id = $1
ORDER BY created_at DESC;

-- name: GetAgentBackupInWorkspace :one
SELECT id, workspace_id, agent_id, agent_name, deleted_by, created_at,
    (snapshot->'agent'->>'owner_id')::uuid AS owner_id
FROM agent_backup
WHERE id = $1 AND workspace_id = $2;

-- name: CountSquadsLedByAgent :one
SELECT count(*) FROM squad WHERE leader_id = $1;

-- name: DeleteAgentReferences :exec
-- Removes or releases everything outside the FK cascades that points at an
-- agent about to be hard-deleted. Authorship history (comments, activity,
-- reactions, creators) keeps the id and renders as a deleted agent.
WITH
deleted_targets AS (
    DELETE FROM agent_invocation_target WHERE agent_invocation_target.agent_id = @agent_id::uuid
),
deleted_mcp_servers AS (
    DELETE FROM agent_mcp_server WHERE agent_mcp_server.agent_id = @agent_id::uuid
),
deleted_labels AS (
    DELETE FROM agent_to_label WHERE agent_to_label.agent_id = @agent_id::uuid
),
deleted_pins AS (
    DELETE FROM chat_pinned_agent WHERE chat_pinned_agent.agent_id = @agent_id::uuid
),
deleted_draft_restores AS (
    DELETE FROM chat_draft_restore
    WHERE chat_draft_restore.chat_session_id IN (SELECT chat_session.id FROM chat_session WHERE chat_session.agent_id = @agent_id::uuid)
),
deleted_channel_installations AS (
    DELETE FROM channel_installation WHERE channel_installation.agent_id = @agent_id::uuid
),
deleted_dingtalk_routes AS (
    DELETE FROM dingtalk_group_route WHERE dingtalk_group_route.agent_id = @agent_id::uuid
),
deleted_wakeups AS (
    DELETE FROM issue_wakeup
    WHERE issue_wakeup.agent_id = @agent_id::uuid OR issue_wakeup.filter_agent_id = @agent_id::uuid
       OR (issue_wakeup.filter_actor_type = 'agent' AND issue_wakeup.filter_actor_id = @agent_id::uuid)
),
deleted_squad_memberships AS (
    DELETE FROM squad_member WHERE squad_member.member_type = 'agent' AND squad_member.member_id = @agent_id::uuid
),
deleted_subscriptions AS (
    DELETE FROM issue_subscriber WHERE issue_subscriber.user_type = 'agent' AND issue_subscriber.user_id = @agent_id::uuid
),
deleted_inbox AS (
    DELETE FROM inbox_item WHERE inbox_item.recipient_type = 'agent' AND inbox_item.recipient_id = @agent_id::uuid
),
unassigned_issues AS (
    UPDATE issue SET assignee_type = NULL, assignee_id = NULL, updated_at = now()
    WHERE issue.assignee_type = 'agent' AND issue.assignee_id = @agent_id::uuid
),
released_project_leads AS (
    UPDATE project SET lead_type = NULL, lead_id = NULL, updated_at = now()
    WHERE project.lead_type = 'agent' AND project.lead_id = @agent_id::uuid
),
paused_autopilots AS (
    UPDATE autopilot SET status = 'paused', updated_at = now()
    WHERE autopilot.assignee_type = 'agent' AND autopilot.assignee_id = @agent_id::uuid AND autopilot.status = 'active'
)
UPDATE quick_action SET status = 'archived', updated_at = now()
WHERE quick_action.assignee_type = 'agent' AND quick_action.assignee_id = @agent_id::uuid AND quick_action.status = 'active';

-- name: DeleteUserAgent :execrows
-- Callers delete the agent's tasks in batches and clear DeleteAgentReferences
-- first; chats and daemon connections go with the row through the existing FK
-- cascades.
DELETE FROM agent WHERE id = $1 AND kind = 'user';
