-- Expand-only. Server-owned lineage: no public request can supply these columns.
ALTER TABLE bot_wake_event ADD COLUMN via_bot_id uuid, ADD COLUMN via_bot_name text,
 ADD COLUMN source_task_id uuid, ADD COLUMN root_id uuid, ADD COLUMN hop integer NOT NULL DEFAULT 0;
ALTER TABLE agent_task_queue ADD COLUMN wake_root_id uuid, ADD COLUMN wake_hop integer NOT NULL DEFAULT 0;
ALTER TABLE comment ADD COLUMN wake_root_id uuid, ADD COLUMN wake_hop integer NOT NULL DEFAULT 0;
ALTER TABLE chat_message ADD COLUMN wake_root_id uuid, ADD COLUMN wake_hop integer NOT NULL DEFAULT 0;
ALTER TABLE issue_summon ADD COLUMN via_bot_id uuid, ADD COLUMN via_bot_name text, ADD COLUMN source_task_id uuid;
-- Durable receiver association survives restarts and cannot be reset by omitted headers.
CREATE TABLE bot_wake_cause (
 bot_id uuid PRIMARY KEY REFERENCES personal_access_token(id) ON DELETE CASCADE,
 root_id uuid NOT NULL, hop integer NOT NULL DEFAULT 0
);
-- The root budget is shared across branches, a conservative bound on all four
-- automatic wakes rather than a client-controlled per-message counter.
CREATE TABLE bot_wake_chain (
 root_id uuid PRIMARY KEY, workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
 hops integer NOT NULL DEFAULT 0 CHECK(hops BETWEEN 0 AND 4)
);
CREATE FUNCTION bot_wake_source_lineage() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE task uuid; bot uuid; root uuid; hops integer:=0;
BEGIN
 IF TG_TABLE_NAME='comment' THEN task:=NEW.source_task_id; ELSE task:=NEW.task_id; END IF;
 bot:=NEW.via_bot_id;
 IF task IS NOT NULL THEN SELECT wake_root_id,wake_hop INTO root,hops FROM agent_task_queue WHERE id=task; END IF;
 IF root IS NULL AND bot IS NOT NULL THEN
  INSERT INTO bot_wake_cause(bot_id,root_id) VALUES(bot,gen_random_uuid()) ON CONFLICT DO NOTHING;
  SELECT root_id,hop INTO root,hops FROM bot_wake_cause WHERE bot_id=bot;
 END IF;
 NEW.wake_root_id:=root;NEW.wake_hop:=COALESCE(hops,0);
 RETURN NEW;
END $$;
CREATE TRIGGER bot_wake_source_comment BEFORE INSERT OR UPDATE OF content ON comment FOR EACH ROW EXECUTE FUNCTION bot_wake_source_lineage();
CREATE TRIGGER bot_wake_source_chat BEFORE INSERT OR UPDATE OF content ON chat_message FOR EACH ROW EXECUTE FUNCTION bot_wake_source_lineage();

CREATE FUNCTION bot_wake_task_lineage() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE root uuid; hops integer; parent uuid; ws uuid;
BEGIN
 IF TG_OP='UPDATE' THEN
  -- A cancelled cap record cannot be re-queued by retrying a status mutation.
  IF OLD.error='bot wake chain limit (4)' THEN NEW.status:='cancelled';NEW.error:=OLD.error; END IF;
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
 IF root IS NULL THEN RETURN NEW; END IF;
 INSERT INTO bot_wake_chain(root_id,workspace_id) VALUES(root,ws) ON CONFLICT DO NOTHING;
 UPDATE bot_wake_chain c SET hops=c.hops+1 WHERE c.root_id=root AND c.workspace_id=ws AND c.hops<4 RETURNING c.hops INTO hops;
 NEW.wake_root_id:=root;
 IF hops IS NULL THEN
  NEW.wake_hop:=4;NEW.status:='cancelled';NEW.error:='bot wake chain limit (4)';NEW.completed_at:=clock_timestamp();
 ELSE NEW.wake_hop:=hops;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER bot_wake_task_lineage BEFORE INSERT OR UPDATE OF status ON agent_task_queue FOR EACH ROW EXECUTE FUNCTION bot_wake_task_lineage();
CREATE OR REPLACE FUNCTION bot_wake_capture() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE ws uuid; iid uuid; cid uuid; sid uuid; typ text; body text := ''; atype text := 'system'; aid uuid;
 via uuid; vname text; stask uuid; root uuid; hops integer:=0;
 eid uuid := gen_random_uuid(); b record; mentioned boolean; interested boolean;
BEGIN
 IF TG_TABLE_NAME='comment' THEN
  iid:=NEW.issue_id; sid:=NEW.id; body:=NEW.content; typ:='mention'; atype:=NEW.author_type; aid:=NEW.author_id;
  SELECT workspace_id INTO ws FROM issue WHERE id=iid;
  via:=NEW.via_bot_id;vname:=NEW.via_bot_name;stask:=NEW.source_task_id;root:=NEW.wake_root_id;hops:=NEW.wake_hop;
 ELSIF TG_TABLE_NAME='chat_message' THEN
  cid:=NEW.chat_session_id; sid:=NEW.id; body:=NEW.content; typ:='chat';
  via:=NEW.via_bot_id;vname:=NEW.via_bot_name;stask:=NEW.task_id;root:=NEW.wake_root_id;hops:=NEW.wake_hop;
  SELECT workspace_id INTO ws FROM chat_session WHERE id=cid;
  IF NEW.sender_user_id IS NOT NULL THEN atype:='member'; aid:=NEW.sender_user_id;
  ELSIF NEW.task_id IS NOT NULL THEN SELECT 'agent',agent_id INTO atype,aid FROM agent_task_queue WHERE id=NEW.task_id;
  END IF;
  atype:=COALESCE(atype,'system');
 ELSIF TG_TABLE_NAME='issue_summon' THEN
  iid:=NEW.issue_id; ws:=NEW.workspace_id; sid:=NEW.id; typ:='needs_kun'; atype:=NEW.caller_type; aid:=NEW.caller_id;
  via:=NEW.via_bot_id;vname:=NEW.via_bot_name;stask:=NEW.source_task_id;
  IF stask IS NOT NULL THEN SELECT wake_root_id,wake_hop INTO root,hops FROM agent_task_queue WHERE id=stask; END IF;
  IF root IS NULL AND via IS NOT NULL THEN
   INSERT INTO bot_wake_cause(bot_id,root_id) VALUES(via,gen_random_uuid()) ON CONFLICT DO NOTHING;
   SELECT root_id,hop INTO root,hops FROM bot_wake_cause WHERE bot_id=via;
  END IF;
 ELSE
  iid:=NEW.id; ws:=NEW.workspace_id; sid:=NEW.id;
  atype:=COALESCE(NULLIF(current_setting('multica.actor_type',true),''),'system');
  aid:=NULLIF(current_setting('multica.actor_id',true),'')::uuid;
  via:=NULLIF(current_setting('multica.via_bot_id',true),'')::uuid;
  vname:=NULLIF(current_setting('multica.via_bot_name',true),'');
  stask:=NULLIF(current_setting('multica.source_task_id',true),'')::uuid;
  IF stask IS NOT NULL THEN SELECT wake_root_id,wake_hop INTO root,hops FROM agent_task_queue WHERE id=stask; END IF;
  IF root IS NULL AND via IS NOT NULL THEN
   INSERT INTO bot_wake_cause(bot_id,root_id) VALUES(via,gen_random_uuid()) ON CONFLICT DO NOTHING;
   SELECT root_id,hop INTO root,hops FROM bot_wake_cause WHERE bot_id=via;
  END IF;
  IF TG_OP='INSERT' THEN typ:='task'; body:=COALESCE(NEW.description,'');
  ELSIF NEW.status IS DISTINCT FROM OLD.status THEN
   typ:=CASE NEW.status WHEN 'in_progress' THEN 'started' WHEN 'done' THEN 'done' WHEN 'blocked' THEN 'blocked' ELSE NULL END;
  ELSIF NEW.assignee_id IS DISTINCT FROM OLD.assignee_id THEN typ:='task';
  ELSE RETURN NEW;
  END IF;
 END IF;
 IF TG_OP='UPDATE' AND TG_TABLE_NAME IN ('comment','chat_message') THEN
  IF NEW.content IS NOT DISTINCT FROM OLD.content THEN RETURN NEW; END IF;
  typ:='mention';
 END IF;
 IF typ IS NULL THEN RETURN NEW; END IF;
 IF via IS NOT NULL AND (atype='member' OR stask IS NULL) THEN atype:='bot';aid:=via; END IF;
 IF TG_TABLE_NAME='comment' AND atype='agent' AND stask IS NULL AND via IS NULL THEN atype:='system';aid:=NULL; END IF;
 root:=COALESCE(root,eid);hops:=COALESCE(hops,0);
 FOR b IN SELECT id,user_id FROM personal_access_token WHERE kind='bot' AND workspace_id=ws AND NOT revoked AND deleted_at IS NULL LOOP
  mentioned:=bot_wake_mention_text(body) ~ ('(?n)\[@?(.+?)\]\(mention://bot/' || b.id::text || '\)');
  IF mentioned THEN
   INSERT INTO bot_wake_watch(bot_id,issue_id,chat_session_id) VALUES(b.id,iid,cid) ON CONFLICT DO NOTHING;
  END IF;
  interested:=EXISTS(SELECT 1 FROM bot_wake_watch x WHERE x.bot_id=b.id AND (x.issue_id=iid OR x.chat_session_id=cid));
  -- A PAT owner's subscriptions/assignments are not Bot subscriptions.
  IF iid IS NOT NULL AND typ <> 'mention' THEN
   interested:=interested OR EXISTS(SELECT 1 FROM issue i WHERE i.id=iid AND i.assignee_type='bot' AND i.assignee_id=b.id);
  END IF;
  IF mentioned OR (typ<>'mention' AND interested) THEN
   INSERT INTO bot_wake_event(event_id,bot_id,workspace_id,event_type,issue_id,chat_session_id,source_id,author_type,author_id,via_bot_id,via_bot_name,source_task_id,root_id,hop)
   VALUES(eid,b.id,ws,CASE WHEN mentioned THEN 'mention' ELSE typ END,iid,cid,sid,atype,aid,via,vname,stask,root,hops) ON CONFLICT DO NOTHING;
  END IF;
 END LOOP;
 RETURN NEW;
END $$;
