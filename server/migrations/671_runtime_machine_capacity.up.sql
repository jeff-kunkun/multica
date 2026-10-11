-- Machine capacity is independent of provider and workspace. Registration and
-- heartbeats never overwrite a person's limit. Missing settings mean 12.
CREATE TABLE runtime_machine_capacity (
    machine_key text PRIMARY KEY,
    max_concurrent_runs integer NOT NULL DEFAULT 12
        CHECK (max_concurrent_runs BETWEEN 1 AND 100),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE FUNCTION runtime_machine_key(daemon_id text, owner_id uuid, runtime_id uuid)
RETURNS text LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT CASE WHEN NULLIF(daemon_id, '') IS NULL OR owner_id IS NULL
        THEN 'runtime:' || runtime_id::text
        ELSE 'daemon:' || owner_id::text || ':' || daemon_id END
$$;
