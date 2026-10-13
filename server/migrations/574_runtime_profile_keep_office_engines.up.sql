-- Office runtimes are claude, codex, opencode, and antigravity. NOT VALID
-- leaves historical rows alone and rejects new profiles for the engines
-- this line no longer ships.
ALTER TABLE runtime_profile DROP CONSTRAINT IF EXISTS runtime_profile_protocol_family_check;

ALTER TABLE runtime_profile ADD CONSTRAINT runtime_profile_protocol_family_check
    CHECK (protocol_family IN (
        'claude',
        'codex',
        'opencode',
        'antigravity'
    )) NOT VALID;
