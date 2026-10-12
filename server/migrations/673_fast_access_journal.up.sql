-- Business writes stage compact identities in their own transaction. The
-- deferred flush sees ALL statements, locks feeds in UUID order, assigns the
-- committed prefix, and retains those locks until commit. No sequence value or
-- wall clock is a commit cursor. Journal failure rolls back the business write.
CREATE TABLE fast_access_meta (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    coverage_from timestamptz NOT NULL DEFAULT clock_timestamp(),
    permission_revision bigint NOT NULL DEFAULT 0,
    permission_changed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
INSERT INTO fast_access_meta(singleton) VALUES(true);
CREATE TABLE fast_access_feed (
    workspace_id uuid PRIMARY KEY,
    seq bigint NOT NULL DEFAULT 0,
    floor_seq bigint NOT NULL DEFAULT 0
);
CREATE TABLE fast_access_journal (
    workspace_id uuid NOT NULL,
    seq bigint NOT NULL,
    event_id uuid NOT NULL UNIQUE,
    kind text NOT NULL,
    entity_id uuid NOT NULL,
    status text NOT NULL DEFAULT '',
    changed_at timestamptz NOT NULL,
    PRIMARY KEY(workspace_id,seq)
);
-- This sequence orders statements INSIDE one transaction only, never commits.
CREATE TABLE fast_access_pending (
    ordinal bigserial PRIMARY KEY,
    transaction_id bigint NOT NULL DEFAULT txid_current(),
    workspace_id uuid,
    kind text NOT NULL,
    entity_id uuid,
    status text NOT NULL DEFAULT ''
);
CREATE TABLE fast_access_cursor (
    token text PRIMARY KEY,
    principal text NOT NULL,
    payload jsonb NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE TABLE fast_access_batch (
    id text PRIMARY KEY, principal text NOT NULL, payload jsonb NOT NULL, expires_at timestamptz NOT NULL
);
CREATE TABLE fast_access_stream (
    principal text NOT NULL,
    slot smallint NOT NULL CHECK(slot IN (1,2)),
    lease_id text NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY(principal,slot)
);

CREATE FUNCTION fast_access_flush() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE ws uuid; ev record; n bigint; reset_scope boolean;
BEGIN
    IF NOT EXISTS(SELECT 1 FROM fast_access_pending WHERE transaction_id=txid_current()) THEN RETURN NULL; END IF;
    -- Explicit early SET CONSTRAINTS cannot silently defeat the commit-only
    -- lock protocol. Once flushed, another business write fails with 40001.
    PERFORM set_config('multica.fast_access_flushed','yes',true);
    reset_scope := EXISTS(SELECT 1 FROM fast_access_pending WHERE transaction_id=txid_current() AND kind='scope_reset');
    IF reset_scope THEN PERFORM 1 FROM fast_access_meta WHERE singleton FOR UPDATE; END IF;
    FOR ws IN SELECT DISTINCT workspace_id FROM fast_access_pending
              WHERE transaction_id=txid_current() AND workspace_id IS NOT NULL ORDER BY workspace_id LOOP
        INSERT INTO fast_access_feed(workspace_id) VALUES(ws) ON CONFLICT DO NOTHING;
        PERFORM 1 FROM fast_access_feed WHERE workspace_id=ws FOR UPDATE;
    END LOOP;
    IF reset_scope THEN UPDATE fast_access_meta SET permission_revision=permission_revision+1,permission_changed_at=clock_timestamp() WHERE singleton; END IF;
    FOR ev IN SELECT * FROM fast_access_pending WHERE transaction_id=txid_current() AND kind<>'scope_reset' ORDER BY ordinal LOOP
        UPDATE fast_access_feed SET seq=seq+1 WHERE workspace_id=ev.workspace_id RETURNING seq INTO n;
        INSERT INTO fast_access_journal VALUES(ev.workspace_id,n,gen_random_uuid(),ev.kind,ev.entity_id,ev.status,clock_timestamp());
    END LOOP;
    DELETE FROM fast_access_pending WHERE transaction_id=txid_current();
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER fast_access_commit AFTER INSERT ON fast_access_pending
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fast_access_flush();

CREATE FUNCTION fast_access_capture() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE ws uuid; entity uuid; k text; st text := ''; reset_scope boolean := false;
BEGIN
    -- No prompt, transcript, result, secret, heartbeat or lease in the journal.
    IF TG_TABLE_NAME='agent_task_queue' THEN
        IF TG_OP='UPDATE' AND NEW.status IS NOT DISTINCT FROM OLD.status THEN RETURN NULL; END IF;
        IF TG_OP='DELETE' THEN
            reset_scope := true;
        ELSE
            SELECT workspace_id INTO ws FROM agent WHERE id=NEW.agent_id;
            entity := NEW.id; k := 'run'; st := NEW.status;
        END IF;
    ELSIF TG_TABLE_NAME='issue' THEN
        IF TG_OP='DELETE' THEN reset_scope := true;
        ELSE
            ws := NEW.workspace_id; entity := NEW.id; k := 'issue';
            IF TG_OP='UPDATE' THEN
                reset_scope := (NEW.visibility,NEW.creator_id,NEW.project_id,NEW.triage_state) IS DISTINCT FROM (OLD.visibility,OLD.creator_id,OLD.project_id,OLD.triage_state);
            END IF;
        END IF;
    ELSIF TG_TABLE_NAME='comment' THEN
        IF TG_OP='DELETE' THEN ws:=OLD.workspace_id; entity:=OLD.issue_id;
        ELSE ws:=NEW.workspace_id; entity:=NEW.issue_id; END IF;
        k:='issue';
    ELSIF TG_TABLE_NAME='chat_session' THEN
        IF TG_OP='DELETE' THEN reset_scope:=true;
        ELSE
            ws:=NEW.workspace_id; entity:=NEW.id; k:='chat';
            IF TG_OP='UPDATE' THEN
                reset_scope := (NEW.visibility,NEW.creator_id,NEW.project_id,NEW.explicitly_created_at) IS DISTINCT FROM (OLD.visibility,OLD.creator_id,OLD.project_id,OLD.explicitly_created_at);
            END IF;
        END IF;
    ELSIF TG_TABLE_NAME IN ('chat_message','chat_session_read') THEN
        IF TG_OP='DELETE' THEN entity:=OLD.chat_session_id; ELSE entity:=NEW.chat_session_id; END IF;
        SELECT workspace_id INTO ws FROM chat_session WHERE id=entity;
        k:='chat';
        IF TG_TABLE_NAME='chat_message' THEN
            IF TG_OP='DELETE' THEN reset_scope:=true;
            ELSIF TG_OP='UPDATE' THEN reset_scope:=NEW.message_kind IS DISTINCT FROM OLD.message_kind; END IF;
        END IF;
    ELSIF TG_TABLE_NAME='autopilot_run' THEN
        IF TG_OP='DELETE' THEN reset_scope:=true;
        ELSE
            SELECT workspace_id INTO ws FROM autopilot WHERE id=NEW.autopilot_id;
            entity:=NEW.id; k:='autopilot_run'; st:=NEW.status;
            IF TG_OP='UPDATE' THEN
                reset_scope := (NEW.autopilot_id,NEW.issue_id) IS DISTINCT FROM (OLD.autopilot_id,OLD.issue_id);
            END IF;
        END IF;
    ELSIF TG_TABLE_NAME='workspace' THEN
        IF TG_OP='UPDATE' THEN
            reset_scope := (NEW.name,NEW.issue_prefix,NEW.settings) IS DISTINCT FROM (OLD.name,OLD.issue_prefix,OLD.settings);
        ELSE reset_scope:=true; END IF;
    ELSIF TG_TABLE_NAME='agent' THEN
        IF TG_OP='UPDATE' THEN
            -- Status/last-seen updates must not invalidate every cursor.
            reset_scope := (NEW.visibility,NEW.owner_id,NEW.permission_mode,NEW.doorbell_enabled,NEW.workspace_id) IS DISTINCT FROM (OLD.visibility,OLD.owner_id,OLD.permission_mode,OLD.doorbell_enabled,OLD.workspace_id);
        ELSE reset_scope:=true; END IF;
    ELSE
        -- Conservative permission invalidation, without revealing object IDs.
        reset_scope:=true;
    END IF;
    IF NOT reset_scope AND (ws IS NULL OR entity IS NULL) THEN RETURN NULL; END IF;
    IF current_setting('multica.fast_access_flushed',true)='yes' THEN
        RAISE EXCEPTION 'fast access requires deferred constraints until commit' USING ERRCODE='40001';
    END IF;
    IF reset_scope THEN INSERT INTO fast_access_pending(kind) VALUES('scope_reset'); END IF;
    IF ws IS NOT NULL AND entity IS NOT NULL THEN
        INSERT INTO fast_access_pending(workspace_id,kind,entity_id,status) VALUES(ws,k,entity,st);
    END IF;
    RETURN NULL;
END $$;
DO $$ DECLARE tab text; BEGIN
    FOREACH tab IN ARRAY ARRAY['issue','comment','chat_session','chat_message','chat_session_read','agent_task_queue','autopilot_run','agent','agent_invocation_target','agent_access_pass','member','project','project_member','resource_share','workspace_module_visibility','workspace_link','workspace_link_project','chat_session_project','autopilot','squad','squad_member','workspace'] LOOP
        EXECUTE format('CREATE TRIGGER fast_access_capture AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION fast_access_capture()',tab);
    END LOOP;
END $$;

-- Retention records the removed prefix, so even a recently-issued checkpoint
-- cannot skip an old event whose writer committed late. Pruning follows the
-- same UUID feed-lock order as journal commit and is bounded per call.
CREATE FUNCTION fast_access_prune() RETURNS void LANGUAGE plpgsql AS $$
DECLARE ws uuid; cutoff bigint; targets uuid[];
BEGIN
    SELECT array_agg(DISTINCT workspace_id ORDER BY workspace_id) INTO targets FROM
        (SELECT workspace_id FROM fast_access_journal WHERE changed_at<clock_timestamp()-interval '7 days'
         ORDER BY workspace_id,seq LIMIT 1000) expired;
    FOR ws IN SELECT unnest(targets) ORDER BY 1 LOOP
        PERFORM 1 FROM fast_access_feed WHERE workspace_id=ws FOR UPDATE;
    END LOOP;
    FOR ws,cutoff IN SELECT workspace_id,max(seq) FROM
        (SELECT workspace_id,seq FROM fast_access_journal WHERE workspace_id=ANY(targets) AND changed_at<clock_timestamp()-interval '7 days'
         ORDER BY workspace_id,seq LIMIT 1000) expired GROUP BY workspace_id ORDER BY workspace_id LOOP
        UPDATE fast_access_feed SET floor_seq=GREATEST(floor_seq,cutoff) WHERE workspace_id=ws;
        DELETE FROM fast_access_journal WHERE workspace_id=ws AND seq<=cutoff;
    END LOOP;
END $$;
