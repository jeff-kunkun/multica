DO $$ DECLARE tab text; BEGIN
    FOREACH tab IN ARRAY ARRAY['issue','comment','chat_session','chat_message','chat_session_read','agent_task_queue','autopilot_run','agent','agent_invocation_target','agent_access_pass','member','project','project_member','resource_share','workspace_module_visibility','workspace_link','workspace_link_project','chat_session_project','autopilot','squad','squad_member','workspace'] LOOP
        EXECUTE format('DROP TRIGGER IF EXISTS fast_access_capture ON %I',tab);
    END LOOP;
END $$;
DROP TABLE fast_access_pending;
DROP FUNCTION fast_access_capture();
DROP FUNCTION fast_access_flush();
DROP TABLE fast_access_stream;
DROP TABLE fast_access_cursor;
DROP TABLE fast_access_batch;
DROP FUNCTION fast_access_prune();
DROP TABLE fast_access_journal;
DROP TABLE fast_access_feed;
DROP TABLE fast_access_meta;
