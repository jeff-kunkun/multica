-- A machine's default working directory (DENE-2667 PR 8): where a run goes
-- when its project binds no folder. Keyed like runtime_machine_capacity, so it
-- is one setting per device across providers and workspaces. No row means no
-- default. The server cannot see the disk: path_exists is what the machine
-- last reported for this exact path, NULL until it has looked.
CREATE TABLE runtime_machine_default_dir (
    machine_key text PRIMARY KEY,
    path text NOT NULL,
    path_exists boolean,
    updated_at timestamptz NOT NULL DEFAULT now()
);
