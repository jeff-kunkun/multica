-- name: ListRuntimeAlertMachines :many
-- A physical host can run more than one daemon/profile. Hostname is scoped to
-- its owner; daemon id is the fallback when no host was reported.
WITH named AS (
 SELECT *, (runtime_mode || ':' || COALESCE(NULLIF(split_part(device_info,' · ',1),''), NULLIF(daemon_id,''), id::text))::text AS machine_key
 FROM agent_runtime WHERE last_seen_at IS NOT NULL AND owner_id IS NOT NULL
)
SELECT workspace_id, owner_id, machine_key,
 COALESCE(NULLIF(min(custom_name),''), NULLIF(min(split_part(device_info,' · ',1)),''), min(name))::text AS machine_name,
 COALESCE(array_agg(id ORDER BY id) FILTER (WHERE status='online'),'{}'::uuid[])::uuid[] AS online_ids,
 COALESCE(array_agg(id ORDER BY id) FILTER (WHERE status='offline'),'{}'::uuid[])::uuid[] AS offline_ids
FROM named
GROUP BY workspace_id,owner_id,machine_key;

-- name: GetRuntimeMachineWatch :one
SELECT * FROM runtime_machine_watch WHERE workspace_id=$1 AND owner_id=$2 AND machine_key=$3 FOR UPDATE;

-- name: SetRuntimeMachineWatch :exec
INSERT INTO runtime_machine_watch(workspace_id,owner_id,machine_key,online,observed_online_ids) VALUES($1,$2,$3,$4,$5)
ON CONFLICT(workspace_id,owner_id,machine_key) DO UPDATE SET online=excluded.online,observed_online_ids=excluded.observed_online_ids;

-- name: StartRuntimeOutage :one
INSERT INTO runtime_outage(id,workspace_id,owner_id,machine_key,machine_name,runtime_ids,started_at)
VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING *;

-- name: GetActiveRuntimeOutage :one
SELECT * FROM runtime_outage WHERE workspace_id=$1 AND owner_id=$2 AND machine_key=$3 AND ended_at IS NULL FOR UPDATE;

-- name: GetRuntimeOutage :one
SELECT * FROM runtime_outage WHERE id=$1;

-- name: AlertRuntimeOutage :exec
UPDATE runtime_outage SET alerted_at=$2 WHERE id=$1;

-- name: EndRuntimeOutage :exec
UPDATE runtime_outage SET ended_at=$2 WHERE id=$1;

-- name: ListRuntimeOutages :many
SELECT * FROM runtime_outage WHERE workspace_id=$1 AND owner_id=$2 ORDER BY started_at DESC LIMIT 100;

-- name: QueueRuntimeOutageWake :exec
INSERT INTO bot_wake_event(bot_id,workspace_id,event_type,source_id,author_type,author_id,root_id,runtime_outage_id)
SELECT b.id,sqlc.arg(workspace_id),sqlc.arg(event_type),sqlc.arg(source_id),'system',sqlc.arg(owner_id),sqlc.arg(source_id),sqlc.arg(outage_id)
FROM personal_access_token b JOIN bot_wake w ON w.bot_id=b.id
WHERE b.workspace_id=sqlc.arg(workspace_id) AND b.user_id=sqlc.arg(owner_id) AND b.kind='bot'
 AND b.deleted_at IS NULL AND NOT b.revoked AND (b.expires_at IS NULL OR b.expires_at>now())
 AND sqlc.arg(event_type)::text=ANY(w.events);

-- name: UpdateRuntimeOutageRuntimes :exec
UPDATE runtime_outage SET runtime_ids=ARRAY(SELECT DISTINCT unnest(runtime_ids || sqlc.arg(runtime_ids)::uuid[])) WHERE id=$1;
