-- Expand-only: old servers still observe cancelled cap rows during rollout.
ALTER TABLE bot_wake_chain DROP CONSTRAINT bot_wake_chain_hops_check;
ALTER TABLE bot_wake_chain ADD CONSTRAINT bot_wake_chain_hops_check CHECK(hops>=0);
ALTER TABLE bot_wake_chain ADD COLUMN IF NOT EXISTS generation bigint NOT NULL DEFAULT 0;
ALTER TABLE agent_task_queue ADD COLUMN IF NOT EXISTS wake_generation bigint NOT NULL DEFAULT 0,
 ADD COLUMN IF NOT EXISTS wake_reserved boolean NOT NULL DEFAULT false, ADD COLUMN IF NOT EXISTS wake_human_quote text;
ALTER TABLE comment ADD COLUMN IF NOT EXISTS human_quote text;
ALTER TABLE bot_wake_event ADD COLUMN IF NOT EXISTS wake_generation bigint NOT NULL DEFAULT 0;
CREATE TABLE IF NOT EXISTS bot_wake_block (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL,
 bot_id uuid,
 agent_id uuid,
 root_id uuid NOT NULL, task_id uuid, issue_id uuid,
 chat_session_id uuid,
 limit_value integer NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE FUNCTION workspace_bot_wake_limit(ws uuid) RETURNS integer LANGUAGE sql STABLE AS $$
 SELECT CASE WHEN settings->>'bot_wake_chain_limit' ~ '^[0-9]{1,3}$'
 AND (settings->>'bot_wake_chain_limit')::integer BETWEEN 1 AND 100
 THEN (settings->>'bot_wake_chain_limit')::integer ELSE 4 END FROM workspace WHERE id=ws
$$;
CREATE FUNCTION bot_wake_reset(root uuid) RETURNS void LANGUAGE sql AS $$
 UPDATE bot_wake_chain SET hops=0,generation=generation+1 WHERE root_id=root
$$;
CREATE FUNCTION bot_wake_quote_valid(ws uuid, uid uuid, iid uuid, cid uuid, quote text)
 RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT quote IS NOT NULL AND quote<>'' AND (
 EXISTS(SELECT 1 FROM comment WHERE workspace_id=ws AND issue_id=iid AND author_type='member'
  AND author_id=uid AND via_bot_id IS NULL AND source_task_id IS NULL AND deleted_at IS NULL AND content=quote)
 OR EXISTS(SELECT 1 FROM chat_message m JOIN chat_session s ON s.id=m.chat_session_id
  WHERE s.workspace_id=ws AND m.chat_session_id=cid AND m.role='user' AND m.sender_user_id=uid
  AND m.via_bot_id IS NULL AND m.content=quote))
$$;
CREATE OR REPLACE FUNCTION bot_wake_source_lineage() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE task uuid; bot uuid; root uuid; hops integer:=0;
BEGIN
 IF TG_TABLE_NAME='comment' THEN task:=NEW.source_task_id; ELSE task:=NEW.task_id; END IF;
 bot:=NEW.via_bot_id;
 IF task IS NOT NULL THEN SELECT wake_root_id,wake_hop INTO root,hops FROM agent_task_queue WHERE id=task; END IF;
 -- Only actual agent output resets a chain. A user's message bound to an
 -- input task is not a reply, and content edits cannot reset it repeatedly.
 IF TG_OP='INSERT' AND root IS NOT NULL AND NEW.content<>'' AND (
  (TG_TABLE_NAME='comment' AND to_jsonb(NEW)->>'author_type'='agent' AND to_jsonb(NEW)->>'type'<>'system') OR
  (TG_TABLE_NAME='chat_message' AND to_jsonb(NEW)->>'role'='assistant' AND COALESCE(to_jsonb(NEW)->>'failure_reason','')='')) THEN
  PERFORM bot_wake_reset(root); hops:=0;
 END IF;
 IF root IS NULL AND bot IS NOT NULL THEN
  INSERT INTO bot_wake_cause(bot_id,root_id) SELECT id,gen_random_uuid() FROM personal_access_token WHERE id=bot ON CONFLICT DO NOTHING;
  SELECT root_id,hop INTO root,hops FROM bot_wake_cause WHERE bot_id=bot;
 END IF;
 NEW.wake_root_id:=root;NEW.wake_hop:=COALESCE(hops,0);
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION bot_wake_task_lineage() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE root uuid; hops integer; parent uuid; ws uuid; cap integer; gen bigint; bot uuid; quote text;
BEGIN
 SELECT workspace_id INTO ws FROM agent WHERE id=NEW.agent_id;
 IF TG_OP='UPDATE' THEN
  IF OLD.error LIKE 'bot wake chain limit (%)' OR OLD.error='bot wake source unavailable' THEN
   NEW.status:='cancelled';NEW.error:=OLD.error;RETURN NEW;
  END IF;
  IF OLD.wake_root_id IS NOT NULL THEN
   IF NEW.status='completed' AND OLD.status<>'completed' AND (OLD.started_at IS NOT NULL OR OLD.status='running') THEN
    PERFORM bot_wake_reset(OLD.wake_root_id);NEW.wake_reserved:=false;
   ELSIF OLD.wake_reserved AND NEW.status IN ('cancelled','failed','completed') AND OLD.started_at IS NULL AND OLD.status<>'running' THEN
    UPDATE bot_wake_chain c SET hops=GREATEST(0,c.hops-1)
     WHERE c.root_id=OLD.wake_root_id AND c.generation=OLD.wake_generation;
    NEW.wake_reserved:=false;
   END IF;
  END IF;
  RETURN NEW;
 END IF;
 -- Real human actions and verified quotes are independent of Bot lineage.
 IF NEW.originator_source='direct_human' AND NEW.via_bot_id IS NULL THEN RETURN NEW; END IF;
 quote:=NEW.wake_human_quote;
 IF quote IS NULL AND NEW.trigger_comment_id IS NOT NULL THEN SELECT human_quote INTO quote FROM comment WHERE id=NEW.trigger_comment_id AND workspace_id=ws; END IF;
 IF bot_wake_quote_valid(ws,COALESCE((SELECT user_id FROM personal_access_token WHERE id=NEW.via_bot_id AND workspace_id=ws),NEW.originator_user_id),NEW.issue_id,NEW.chat_session_id,quote) THEN RETURN NEW; END IF;
 parent:=COALESCE(NEW.delegated_from_task_id,NEW.retry_of_task_id,NEW.rerun_of_task_id);
 IF parent IS NOT NULL THEN
  SELECT t.wake_root_id INTO root FROM agent_task_queue t JOIN agent a ON a.id=t.agent_id WHERE t.id=parent AND a.workspace_id=ws;
 END IF;
 IF root IS NULL AND NEW.trigger_comment_id IS NOT NULL THEN
  SELECT wake_root_id INTO root FROM comment WHERE id=NEW.trigger_comment_id AND workspace_id=ws;
 END IF;
 IF root IS NULL AND NEW.trigger_evidence_kind='chat' THEN
  SELECT m.wake_root_id INTO root FROM chat_message m JOIN chat_session c ON c.id=m.chat_session_id
   WHERE m.id=NEW.trigger_evidence_ref_id AND c.workspace_id=ws;
 END IF;
 IF root IS NULL AND NEW.via_bot_id IS NOT NULL THEN
  INSERT INTO bot_wake_cause(bot_id,root_id) SELECT id,gen_random_uuid() FROM personal_access_token WHERE id=NEW.via_bot_id AND workspace_id=ws ON CONFLICT DO NOTHING;
  SELECT root_id INTO root FROM bot_wake_cause WHERE bot_id=NEW.via_bot_id;
 END IF;
 IF root IS NULL THEN
  IF NEW.via_bot_id IS NOT NULL THEN NEW.status:='cancelled';NEW.error:='bot wake source unavailable';NEW.completed_at:=clock_timestamp(); END IF;
  RETURN NEW;
 END IF;
 cap:=workspace_bot_wake_limit(ws);
 INSERT INTO bot_wake_chain(root_id,workspace_id) VALUES(root,ws) ON CONFLICT DO NOTHING;
 UPDATE bot_wake_chain c SET hops=c.hops+1 WHERE c.root_id=root AND c.workspace_id=ws AND c.hops<cap RETURNING c.hops,c.generation INTO hops,gen;
 NEW.wake_root_id:=root;
 IF hops IS NULL THEN
  NEW.wake_hop:=cap;NEW.status:='cancelled';NEW.error:='bot wake chain limit ('||cap||')';NEW.completed_at:=clock_timestamp();
 ELSE NEW.wake_hop:=hops;NEW.wake_generation:=gen;NEW.wake_reserved:=true;
 END IF;
 RETURN NEW;
END $$;
-- AFTER INSERT makes the audit durable even though task creation returns a
-- cancelled row. The notification is an operational alert, not another wake
-- hop, so it does not recurse through the exhausted guard.
CREATE FUNCTION bot_wake_record_block() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE ws uuid; bid uuid; cap integer; eid uuid:=gen_random_uuid();
BEGIN
 IF NEW.error IS NULL OR NEW.error NOT LIKE 'bot wake chain limit (%)' THEN RETURN NEW; END IF;
 SELECT workspace_id INTO ws FROM agent WHERE id=NEW.agent_id;
 cap:=workspace_bot_wake_limit(ws);
 bid:=NEW.via_bot_id;
 IF bid IS NULL THEN SELECT bot_id INTO bid FROM bot_wake_cause WHERE root_id=NEW.wake_root_id ORDER BY bot_id LIMIT 1; END IF;
 INSERT INTO bot_wake_block(workspace_id,bot_id,agent_id,root_id,task_id,issue_id,chat_session_id,limit_value)
 VALUES(ws,bid,NEW.agent_id,NEW.wake_root_id,NEW.id,NEW.issue_id,NEW.chat_session_id,cap);
 IF bid IS NOT NULL AND num_nonnulls(NEW.issue_id,NEW.chat_session_id)=1 THEN
  INSERT INTO bot_wake_event(event_id,bot_id,workspace_id,event_type,issue_id,chat_session_id,source_id,author_type,author_id,via_bot_id,root_id,hop,reason)
  VALUES(eid,bid,ws,'wake_blocked',NEW.issue_id,NEW.chat_session_id,NEW.id,'system',NEW.agent_id,bid,NEW.wake_root_id,cap,NEW.error);
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER bot_wake_record_block AFTER INSERT ON agent_task_queue FOR EACH ROW EXECUTE FUNCTION bot_wake_record_block();
