DROP TRIGGER bot_wake_record_block ON agent_task_queue;
DROP FUNCTION bot_wake_record_block();
CREATE OR REPLACE FUNCTION bot_wake_source_lineage() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE task uuid; bot uuid; root uuid; hops integer:=0;
BEGIN
 IF TG_TABLE_NAME='comment' THEN task:=NEW.source_task_id; ELSE task:=NEW.task_id; END IF;
 bot:=NEW.via_bot_id;
 IF task IS NOT NULL THEN SELECT wake_root_id,wake_hop INTO root,hops FROM agent_task_queue WHERE id=task; END IF;
 IF root IS NULL AND bot IS NOT NULL THEN
  INSERT INTO bot_wake_cause(bot_id,root_id) SELECT id,gen_random_uuid() FROM personal_access_token WHERE id=bot ON CONFLICT DO NOTHING;
  SELECT root_id,hop INTO root,hops FROM bot_wake_cause WHERE bot_id=bot;
 END IF;
 NEW.wake_root_id:=root;NEW.wake_hop:=COALESCE(hops,0);
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION bot_wake_task_lineage() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE root uuid; hops integer; parent uuid; ws uuid;
BEGIN
 IF TG_OP='UPDATE' THEN
  -- A cancelled cap record cannot be re-queued by retrying a status mutation.
  IF OLD.error IN ('bot wake chain limit (4)','bot wake source unavailable') THEN NEW.status:='cancelled';NEW.error:=OLD.error; END IF;
  RETURN NEW;
 END IF;
 IF NEW.originator_source='direct_human' AND NEW.via_bot_id IS NULL THEN RETURN NEW; END IF;
 parent:=COALESCE(NEW.delegated_from_task_id,NEW.retry_of_task_id,NEW.rerun_of_task_id);
 SELECT workspace_id INTO ws FROM agent WHERE id=NEW.agent_id;
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
 INSERT INTO bot_wake_chain(root_id,workspace_id) VALUES(root,ws) ON CONFLICT DO NOTHING;
 UPDATE bot_wake_chain c SET hops=c.hops+1 WHERE c.root_id=root AND c.workspace_id=ws AND c.hops<4 RETURNING c.hops INTO hops;
 NEW.wake_root_id:=root;
 IF hops IS NULL THEN
  NEW.wake_hop:=4;NEW.status:='cancelled';NEW.error:='bot wake chain limit (4)';NEW.completed_at:=clock_timestamp();
 ELSE NEW.wake_hop:=hops;
 END IF;
 RETURN NEW;
END $$;
DROP FUNCTION bot_wake_quote_valid(uuid,uuid,uuid,uuid,text);
DROP FUNCTION bot_wake_reset(uuid);
DROP FUNCTION workspace_bot_wake_limit(uuid);
ALTER TABLE bot_wake_chain DROP CONSTRAINT bot_wake_chain_hops_check;
UPDATE bot_wake_chain SET hops=LEAST(hops,4);
ALTER TABLE bot_wake_chain ADD CONSTRAINT bot_wake_chain_hops_check CHECK(hops BETWEEN 0 AND 4);
-- Retain additive columns/audit table so rolling-back servers can read them.
