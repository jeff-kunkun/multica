-- References are viewer-owned snapshots, never source-project foreign keys.
-- Keep stale selections after revocation so users can identify and remove them.
ALTER TABLE project ADD COLUMN linked_projects jsonb NOT NULL DEFAULT '[]'::jsonb
    CHECK (jsonb_typeof(linked_projects) = 'array');
-- NULL inherits the owning project; [] explicitly opts out.
ALTER TABLE autopilot ADD COLUMN linked_projects jsonb
    CHECK (linked_projects IS NULL OR jsonb_typeof(linked_projects) = 'array');
