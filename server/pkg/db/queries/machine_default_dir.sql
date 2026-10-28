-- name: SetRuntimeMachineDefaultDir :exec
-- A new path has not been looked at yet; re-saving the same path keeps what
-- the machine already reported for it.
INSERT INTO runtime_machine_default_dir (machine_key, path)
VALUES (@machine_key, @path)
ON CONFLICT (machine_key) DO UPDATE
SET path = EXCLUDED.path,
    path_exists = CASE WHEN runtime_machine_default_dir.path = EXCLUDED.path
        THEN runtime_machine_default_dir.path_exists END,
    updated_at = now();

-- name: ClearRuntimeMachineDefaultDir :exec
DELETE FROM runtime_machine_default_dir WHERE machine_key = @machine_key;

-- name: GetRuntimeMachineDefaultDir :one
SELECT d.path, d.path_exists
FROM agent_runtime r
JOIN runtime_machine_default_dir d ON d.machine_key = runtime_machine_key(r.daemon_id, r.owner_id, r.id)
WHERE r.id = @runtime_id;

-- name: ListRuntimeMachineDefaultDirs :many
SELECT r.id AS runtime_id, d.path, d.path_exists
FROM agent_runtime r
JOIN runtime_machine_default_dir d ON d.machine_key = runtime_machine_key(r.daemon_id, r.owner_id, r.id)
WHERE r.id = ANY(@runtime_ids::uuid[]);

-- name: ReportRuntimeMachineDefaultDir :execrows
-- Applies only while the reported path is still the setting, so a report about
-- a path the owner has since replaced cannot mark the new one.
UPDATE runtime_machine_default_dir d
SET path_exists = @path_exists, updated_at = now()
FROM agent_runtime r
WHERE r.id = @runtime_id
  AND d.machine_key = runtime_machine_key(r.daemon_id, r.owner_id, r.id)
  AND d.path = @path
  AND d.path_exists IS DISTINCT FROM @path_exists;
