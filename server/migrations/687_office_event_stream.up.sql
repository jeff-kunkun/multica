-- Expand-only (DENE-1963): the office event stream. One durable log per
-- workspace of what the office did (chat replies and runs, ticket status,
-- files, settings/members/routing/config), written by triggers inside the
-- transaction of the write itself, so a process that dies after commit cannot
-- lose an event. A dispatcher seals each row (meaning, must_relay, gap-free
-- stream_seq) and fans it out to Bots, which must acknowledge every event by
-- id; delivery retries until they do.
--
-- Who did it comes from the session setting `multica.actor`, stamped by the
-- connection pool from the request (internal/dbactor). Unstamped = system.

-- workspace_id carries no foreign key on purpose: a key check takes FOR KEY
-- SHARE on the workspace row, and the task-write fence holds that row while a
-- workspace is torn down, so every captured write would queue behind the
-- teardown. Teardown deletes these rows itself (DeleteWorkspaceData).
CREATE TABLE office_event (
    id BIGSERIAL PRIMARY KEY,
    event_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    workspace_id UUID NOT NULL,
    -- chat: replies and chat runs; issue: status and ticket runs;
    -- artifact: a file attached; config: settings, members, routing, agents.
    family TEXT NOT NULL CHECK (family IN ('chat', 'issue', 'artifact', 'config')),
    event_type TEXT NOT NULL,
    object_type TEXT NOT NULL,
    object_id TEXT NOT NULL DEFAULT '',
    -- No foreign keys: the log outlives what it describes.
    issue_id UUID,
    chat_session_id UUID,
    chat_message_id UUID,
    comment_id UUID,
    task_id UUID,
    attachment_id UUID,
    -- [{field, old, new}], credential values already masked.
    changes JSONB NOT NULL DEFAULT '[]',
    actor_type TEXT NOT NULL DEFAULT 'system',
    actor_id UUID,
    via_bot_id UUID,
    on_behalf_of_user_id UUID,
    actor_task_id UUID,
    actor_source TEXT NOT NULL DEFAULT '',
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    -- Set once by the dispatcher. stream_seq is the read cursor: assigned
    -- under one lock in commit order, so a reader never skips a row that
    -- committed late.
    stream_seq BIGINT UNIQUE,
    sealed_at TIMESTAMPTZ,
    meaning TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    must_relay BOOLEAN NOT NULL DEFAULT false,
    pending_approval BOOLEAN NOT NULL DEFAULT false,
    -- The same business fact on the older activity lane carries the same key.
    business_key TEXT NOT NULL DEFAULT ''
);
CREATE INDEX office_event_unsealed ON office_event (id) WHERE stream_seq IS NULL;
CREATE INDEX office_event_stream ON office_event (workspace_id, stream_seq) WHERE stream_seq IS NOT NULL;
CREATE INDEX office_event_issue ON office_event (issue_id, stream_seq) WHERE issue_id IS NOT NULL;
CREATE INDEX office_event_chat ON office_event (chat_session_id, stream_seq) WHERE chat_session_id IS NOT NULL;

-- One row per event per Bot.
--   pending -> sending -> sent (HTTP 200, no receipt yet) -> received -> relayed
--   received -> handled (only when must_relay is false)
--   any open state -> blocked (permission or channel gone; reason kept)
CREATE TABLE office_event_delivery (
    id BIGSERIAL PRIMARY KEY,
    event_id UUID NOT NULL REFERENCES office_event(event_id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL,
    bot_id UUID NOT NULL,
    binding_id UUID,
    target_user_id UUID NOT NULL,
    -- owner: the person the result belongs to; stream: the person's binding
    -- streams everything they can see (activity scope owner_visible_all).
    reason TEXT NOT NULL CHECK (reason IN ('owner', 'stream')),
    must_relay BOOLEAN NOT NULL DEFAULT false,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'sending', 'sent', 'received', 'relayed', 'handled', 'blocked')),
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_attempt_at TIMESTAMPTZ,
    sent_at TIMESTAMPTZ,
    received_at TIMESTAMPTZ,
    relayed_at TIMESTAMPTZ,
    relay_channel TEXT NOT NULL DEFAULT '',
    relay_ref TEXT NOT NULL DEFAULT '',
    relay_error TEXT NOT NULL DEFAULT '',
    handled_reason TEXT NOT NULL DEFAULT '',
    blocked_reason TEXT NOT NULL DEFAULT '',
    -- In-app notice when a must_relay event was not relayed in time.
    fallback_at TIMESTAMPTZ,
    -- Per-file receipts: {"<attachment id>": {"status","ref","error","at"}}.
    artifacts JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (event_id, bot_id)
);
CREATE INDEX office_event_delivery_due ON office_event_delivery (next_attempt_at, id)
    WHERE status IN ('pending', 'sent');
CREATE INDEX office_event_delivery_bot ON office_event_delivery (bot_id, id DESC);
CREATE INDEX office_event_delivery_unrelayed ON office_event_delivery (received_at)
    WHERE must_relay AND status = 'received' AND fallback_at IS NULL;

-- ---- capture ----

CREATE FUNCTION office_actor() RETURNS jsonb LANGUAGE plpgsql STABLE AS $$
DECLARE
    raw text := current_setting('multica.actor', true);
BEGIN
    IF raw IS NULL OR raw = '' THEN
        RETURN '{}'::jsonb;
    END IF;
    RETURN raw::jsonb;
EXCEPTION WHEN others THEN
    RETURN '{}'::jsonb;
END $$;

CREATE FUNCTION office_uuid(v text) RETURNS uuid LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN
    IF v IS NULL OR v = '' THEN RETURN NULL; END IF;
    RETURN v::uuid;
EXCEPTION WHEN others THEN
    RETURN NULL;
END $$;

-- A field whose name says credential is masked whole, at any depth.
CREATE FUNCTION office_is_secret_key(k text) RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
    SELECT k ~* '(secret|token|password|passwd|passphrase|api[_-]?key|apikey|access[_-]?key|private[_-]?key|credential|authorization|ciphertext|_hash$|^env$|custom_env|cookie)'
$$;

CREATE FUNCTION office_mask(v jsonb) RETURNS jsonb LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    out jsonb;
    k text;
    e jsonb;
BEGIN
    IF v IS NULL THEN RETURN NULL; END IF;
    CASE jsonb_typeof(v)
    WHEN 'object' THEN
        out := '{}'::jsonb;
        FOR k, e IN SELECT * FROM jsonb_each(v) LOOP
            IF office_is_secret_key(k) AND e <> 'null'::jsonb AND e <> '""'::jsonb THEN
                out := out || jsonb_build_object(k, '[已打码]');
            ELSE
                out := out || jsonb_build_object(k, office_mask(e));
            END IF;
        END LOOP;
        RETURN out;
    WHEN 'array' THEN
        SELECT COALESCE(jsonb_agg(office_mask(x)), '[]'::jsonb) INTO out FROM jsonb_array_elements(v) x;
        RETURN out;
    WHEN 'string' THEN
        IF length(v #>> '{}') > 500 THEN
            RETURN to_jsonb(left(v #>> '{}', 500) || '…（共 ' || length(v #>> '{}') || ' 字）');
        END IF;
        RETURN v;
    ELSE
        RETURN v;
    END CASE;
END $$;

CREATE FUNCTION office_emit(
    p_workspace uuid, p_family text, p_type text, p_object_type text, p_object_id text,
    p_issue uuid, p_chat uuid, p_message uuid, p_comment uuid, p_task uuid, p_attachment uuid,
    p_changes jsonb, p_business_key text
) RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    a jsonb := office_actor();
BEGIN
    -- No workspace, or one being torn down (its members and agents go
    -- through the config trigger): nothing to tell.
    IF p_workspace IS NULL
       OR current_setting('multica.workspace_teardown', true) = 'on'
       OR NOT EXISTS (SELECT 1 FROM workspace WHERE id = p_workspace) THEN
        RETURN;
    END IF;
    INSERT INTO office_event (
        workspace_id, family, event_type, object_type, object_id,
        issue_id, chat_session_id, chat_message_id, comment_id, task_id, attachment_id,
        changes, business_key, actor_type, actor_id, via_bot_id, on_behalf_of_user_id, actor_task_id, actor_source
    ) VALUES (
        p_workspace, p_family, p_type, p_object_type, COALESCE(p_object_id, ''),
        p_issue, p_chat, p_message, p_comment, p_task, p_attachment,
        COALESCE(p_changes, '[]'::jsonb), COALESCE(p_business_key, ''),
        COALESCE(NULLIF(a->>'type', ''), 'system'),
        office_uuid(a->>'id'), office_uuid(a->>'via_bot_id'),
        office_uuid(a->>'on_behalf_of_user_id'), office_uuid(a->>'task_id'),
        COALESCE(a->>'source', '')
    );
END $$;

-- Ticket status.
CREATE FUNCTION office_capture_issue_status() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM office_emit(NEW.workspace_id, 'issue', 'issue.status_changed', 'issue', NEW.id::text,
        NEW.id, NULL, NULL, NULL, NULL, NULL,
        jsonb_build_array(jsonb_build_object('field', 'status', 'old', OLD.status, 'new', NEW.status)),
        -- Same key the activity lane derives from the issue:updated payload.
        'issue-status:' || NEW.id || ':' || OLD.status || '>' || NEW.status || ':' ||
            (extract(epoch FROM NEW.updated_at) * 1000000)::bigint);
    RETURN NULL;
END $$;
CREATE TRIGGER office_issue_status AFTER UPDATE OF status ON issue
    FOR EACH ROW WHEN (OLD.status IS DISTINCT FROM NEW.status)
    EXECUTE FUNCTION office_capture_issue_status();

-- A reply in a chat (the agent's answer, or a ticket's receipt back to it).
CREATE FUNCTION office_capture_chat_reply() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    ws uuid;
BEGIN
    SELECT workspace_id INTO ws FROM chat_session WHERE id = NEW.chat_session_id;
    PERFORM office_emit(ws, 'chat', 'chat.replied', 'chat_message', NEW.id::text,
        NULL, NEW.chat_session_id, NEW.id, NULL, NEW.task_id, NULL, '[]'::jsonb,
        'chat-message:' || NEW.id);
    RETURN NULL;
END $$;
CREATE TRIGGER office_chat_reply AFTER INSERT ON chat_message
    FOR EACH ROW WHEN (NEW.role = 'assistant' AND NEW.message_kind IN ('message', 'issue_receipt'))
    EXECUTE FUNCTION office_capture_chat_reply();

-- A run finished (one round, whatever the ticket's status does).
CREATE FUNCTION office_capture_run() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    ws uuid;
BEGIN
    SELECT workspace_id INTO ws FROM agent WHERE id = NEW.agent_id;
    PERFORM office_emit(ws,
        CASE WHEN NEW.chat_session_id IS NOT NULL THEN 'chat' ELSE 'issue' END,
        'run.' || NEW.status, 'agent_task', NEW.id::text,
        NEW.issue_id, NEW.chat_session_id, NULL, NULL, NEW.id, NULL,
        jsonb_build_array(jsonb_build_object('field', 'status', 'old', OLD.status, 'new', NEW.status)),
        'run:' || NEW.id || ':' || NEW.status);
    RETURN NULL;
END $$;
CREATE TRIGGER office_run_finished AFTER UPDATE OF status ON agent_task_queue
    FOR EACH ROW WHEN (OLD.status IS DISTINCT FROM NEW.status AND NEW.status IN ('completed', 'failed', 'cancelled'))
    EXECUTE FUNCTION office_capture_run();

-- A file joined a comment or a chat message: on upload with its place
-- already known, or bound later (an agent's file is bound when its run ends).
-- A new upload under the same name is a new attachment, so a new event.
CREATE FUNCTION office_capture_attachment() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    sess uuid := NEW.chat_session_id;
BEGIN
    IF NEW.comment_id IS NULL AND NEW.chat_message_id IS NULL THEN
        RETURN NULL;
    END IF;
    IF TG_OP = 'UPDATE'
       AND NEW.comment_id IS NOT DISTINCT FROM OLD.comment_id
       AND NEW.chat_message_id IS NOT DISTINCT FROM OLD.chat_message_id THEN
        RETURN NULL;
    END IF;
    IF sess IS NULL AND NEW.chat_message_id IS NOT NULL THEN
        SELECT chat_session_id INTO sess FROM chat_message WHERE id = NEW.chat_message_id;
    END IF;
    PERFORM office_emit(NEW.workspace_id, 'artifact', 'artifact.attached', 'attachment', NEW.id::text,
        NEW.issue_id, sess, NEW.chat_message_id, NEW.comment_id, NEW.task_id, NEW.id,
        jsonb_build_array(jsonb_build_object('field', 'filename', 'old', NULL, 'new', NEW.filename)),
        'artifact:' || NEW.id || ':' || COALESCE(NEW.comment_id, NEW.chat_message_id));
    RETURN NULL;
END $$;
CREATE TRIGGER office_attachment AFTER INSERT OR UPDATE OF comment_id, chat_message_id ON attachment
    FOR EACH ROW EXECUTE FUNCTION office_capture_attachment();

-- Settings, members, routing and configuration: every column that changed,
-- old -> new, credentials masked. TG_ARGV[0] says how to find the workspace,
-- the rest are columns that churn on their own and are not a change of
-- configuration (heartbeats, counters, timestamps).
CREATE FUNCTION office_capture_config() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    o jsonb := CASE WHEN TG_OP = 'INSERT' THEN '{}'::jsonb ELSE to_jsonb(OLD) END;
    n jsonb := CASE WHEN TG_OP = 'DELETE' THEN '{}'::jsonb ELSE to_jsonb(NEW) END;
    row_data jsonb := CASE WHEN TG_OP = 'DELETE' THEN o ELSE n END;
    ignored text[] := ARRAY['id', 'workspace_id', 'created_at', 'updated_at'];
    changes jsonb := '[]'::jsonb;
    k text;
    sub text;
    ov jsonb;
    nv jsonb;
    ws uuid;
    obj text;
    i int;
BEGIN
    IF TG_TABLE_NAME = 'personal_access_token' AND row_data->>'kind' IS DISTINCT FROM 'bot' THEN
        RETURN NULL; -- a person's own CLI keys are not office configuration
    END IF;
    FOR i IN 1 .. TG_NARGS - 1 LOOP
        ignored := ignored || TG_ARGV[i];
    END LOOP;
    FOR k IN SELECT DISTINCT key FROM (SELECT jsonb_object_keys(o) AS key UNION SELECT jsonb_object_keys(n)) keys LOOP
        CONTINUE WHEN k = ANY(ignored);
        ov := o -> k;
        nv := n -> k;
        CONTINUE WHEN ov IS NOT DISTINCT FROM nv;
        IF office_is_secret_key(k) THEN
            changes := changes || jsonb_build_object('field', k,
                'old', CASE WHEN ov IS NULL OR ov = 'null'::jsonb THEN NULL ELSE to_jsonb('[已打码]'::text) END,
                'new', CASE WHEN nv IS NULL OR nv = 'null'::jsonb THEN NULL ELSE to_jsonb('[已打码]'::text) END,
                'masked', true);
        ELSIF TG_OP = 'UPDATE' AND jsonb_typeof(ov) = 'object' AND jsonb_typeof(nv) = 'object' THEN
            -- A settings document: one entry per top-level key that moved.
            FOR sub IN SELECT DISTINCT key FROM (SELECT jsonb_object_keys(ov) AS key UNION SELECT jsonb_object_keys(nv)) keys LOOP
                CONTINUE WHEN (ov -> sub) IS NOT DISTINCT FROM (nv -> sub);
                IF office_is_secret_key(sub) THEN
                    changes := changes || jsonb_build_object('field', k || '.' || sub, 'old', '[已打码]', 'new', '[已打码]', 'masked', true);
                ELSE
                    changes := changes || jsonb_build_object('field', k || '.' || sub,
                        'old', office_mask(ov -> sub), 'new', office_mask(nv -> sub));
                END IF;
            END LOOP;
        ELSE
            changes := changes || jsonb_build_object('field', k, 'old', office_mask(ov), 'new', office_mask(nv));
        END IF;
    END LOOP;
    IF jsonb_array_length(changes) = 0 THEN
        RETURN NULL;
    END IF;
    CASE TG_ARGV[0]
    WHEN 'workspace' THEN ws := office_uuid(row_data->>'id');
    WHEN 'agent' THEN SELECT workspace_id INTO ws FROM agent WHERE id = office_uuid(row_data->>'agent_id');
    WHEN 'squad' THEN SELECT workspace_id INTO ws FROM squad WHERE id = office_uuid(row_data->>'squad_id');
    WHEN 'bot' THEN SELECT workspace_id INTO ws FROM personal_access_token WHERE id = office_uuid(row_data->>'bot_id');
    WHEN 'link' THEN ws := office_uuid(row_data->>'source_workspace_id');
    ELSE ws := office_uuid(row_data->>'workspace_id');
    END CASE;
    obj := COALESCE(row_data->>'id', row_data->>'bot_id', concat_ws(':', row_data->>'agent_id', row_data->>'skill_id', row_data->>'server_id'));
    PERFORM office_emit(ws, 'config', 'config.' || TG_TABLE_NAME || '.' || lower(TG_OP), TG_TABLE_NAME, obj,
        NULL, NULL, NULL, NULL, NULL, NULL, changes, '');
    RETURN NULL;
END $$;

CREATE TRIGGER office_config AFTER UPDATE ON workspace
    FOR EACH ROW EXECUTE FUNCTION office_capture_config('workspace', 'issue_counter');
CREATE TRIGGER office_config AFTER INSERT OR UPDATE OR DELETE ON member
    FOR EACH ROW EXECUTE FUNCTION office_capture_config('workspace_id');
CREATE TRIGGER office_config AFTER INSERT OR UPDATE OR DELETE ON agent
    FOR EACH ROW EXECUTE FUNCTION office_capture_config('workspace_id', 'status', 'routing_usage', 'plan_limits');
CREATE TRIGGER office_config AFTER INSERT OR UPDATE OR DELETE ON squad
    FOR EACH ROW EXECUTE FUNCTION office_capture_config('workspace_id');
CREATE TRIGGER office_config AFTER INSERT OR UPDATE OR DELETE ON squad_member
    FOR EACH ROW EXECUTE FUNCTION office_capture_config('squad');
CREATE TRIGGER office_config AFTER INSERT OR UPDATE OR DELETE ON project
    FOR EACH ROW EXECUTE FUNCTION office_capture_config('workspace_id');
CREATE TRIGGER office_config AFTER INSERT OR UPDATE OR DELETE ON project_member
    FOR EACH ROW EXECUTE FUNCTION office_capture_config('workspace_id');
CREATE TRIGGER office_config AFTER INSERT OR UPDATE OR DELETE ON project_resource
    FOR EACH ROW EXECUTE FUNCTION office_capture_config('workspace_id', 'position');
CREATE TRIGGER office_config AFTER INSERT OR UPDATE OR DELETE ON member_bot_binding
    FOR EACH ROW EXECUTE FUNCTION office_capture_config('workspace_id', 'broken_notified_at');
CREATE TRIGGER office_config AFTER INSERT OR UPDATE OR DELETE ON bot_wake
    FOR EACH ROW EXECUTE FUNCTION office_capture_config('bot', 'last_processed_at', 'last_error');
CREATE TRIGGER office_config AFTER INSERT OR UPDATE OR DELETE ON personal_access_token
    FOR EACH ROW EXECUTE FUNCTION office_capture_config('workspace_id', 'last_used_at');
CREATE TRIGGER office_config AFTER INSERT OR UPDATE OR DELETE ON workspace_mcp_server
    FOR EACH ROW EXECUTE FUNCTION office_capture_config('workspace_id');
CREATE TRIGGER office_config AFTER INSERT OR UPDATE OR DELETE ON agent_mcp_server
    FOR EACH ROW EXECUTE FUNCTION office_capture_config('agent');
CREATE TRIGGER office_config AFTER INSERT OR UPDATE OR DELETE ON agent_skill
    FOR EACH ROW EXECUTE FUNCTION office_capture_config('agent');
CREATE TRIGGER office_config AFTER INSERT OR UPDATE OR DELETE ON skill
    FOR EACH ROW EXECUTE FUNCTION office_capture_config('workspace_id');
CREATE TRIGGER office_config AFTER INSERT OR UPDATE OR DELETE ON issue_status
    FOR EACH ROW EXECUTE FUNCTION office_capture_config('workspace_id', 'position');
CREATE TRIGGER office_config AFTER INSERT OR UPDATE OR DELETE ON issue_property
    FOR EACH ROW EXECUTE FUNCTION office_capture_config('workspace_id', 'position');
CREATE TRIGGER office_config AFTER INSERT OR UPDATE OR DELETE ON quick_action
    FOR EACH ROW EXECUTE FUNCTION office_capture_config('workspace_id', 'last_used_at', 'use_count');
CREATE TRIGGER office_config AFTER INSERT OR UPDATE OR DELETE ON runtime_profile
    FOR EACH ROW EXECUTE FUNCTION office_capture_config('workspace_id');
CREATE TRIGGER office_config AFTER INSERT OR UPDATE OR DELETE ON resource_share
    FOR EACH ROW EXECUTE FUNCTION office_capture_config('workspace_id');
CREATE TRIGGER office_config AFTER INSERT OR UPDATE OR DELETE ON workspace_link
    FOR EACH ROW EXECUTE FUNCTION office_capture_config('link');
CREATE TRIGGER office_config AFTER INSERT OR UPDATE OR DELETE ON github_installation
    FOR EACH ROW EXECUTE FUNCTION office_capture_config('workspace_id');
