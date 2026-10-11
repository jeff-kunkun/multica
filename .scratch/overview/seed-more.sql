BEGIN;
INSERT INTO autopilot(id,workspace_id,title,project_id,assignee_type,assignee_id,status,execution_mode,created_by_type,created_by_id) SELECT '18030000-0000-4000-8000-000000000006','18030000-0000-4000-8000-000000000001','Daily release report','18030000-0000-4000-8000-000000000004','agent','18030000-0000-4000-8000-000000000003','paused','run_only','member',id FROM "user" WHERE email='dev@localhost';
INSERT INTO autopilot_trigger(autopilot_id,kind,enabled,cron_expression,timezone) VALUES ('18030000-0000-4000-8000-000000000006','schedule',false,'0 9 * * *','America/Chicago');
INSERT INTO squad(workspace_id,name,leader_id,creator_id) SELECT '18030000-0000-4000-8000-000000000001','Release team','18030000-0000-4000-8000-000000000003',id FROM "user" WHERE email='dev@localhost';
COMMIT;
