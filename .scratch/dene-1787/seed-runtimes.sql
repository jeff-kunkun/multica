INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, owner_id)
SELECT w.id, v.name, 'cloud', v.provider, 'online', 'DENE-1787 local fixture', '{}', now(), u.id
FROM workspace w CROSS JOIN "user" u CROSS JOIN (VALUES ('Claude series fixture','claude'), ('Codex series fixture','codex')) AS v(name,provider)
WHERE w.slug='dev' AND u.email='dev@localhost' AND NOT EXISTS(SELECT 1 FROM agent_runtime r WHERE r.workspace_id=w.id AND r.name=v.name);
SELECT id,name,provider FROM agent_runtime WHERE device_info='DENE-1787 local fixture';
