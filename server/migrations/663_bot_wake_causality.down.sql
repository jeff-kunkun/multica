CREATE OR REPLACE FUNCTION bot_wake_capture() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE ws uuid; iid uuid; cid uuid; sid uuid; typ text; body text := ''; atype text := 'system'; aid uuid;
 eid uuid := gen_random_uuid(); b record; mentioned boolean; interested boolean;
BEGIN
 IF TG_TABLE_NAME='comment' THEN
  iid:=NEW.issue_id; sid:=NEW.id; body:=NEW.content; typ:='mention'; atype:=NEW.author_type; aid:=NEW.author_id;
  SELECT workspace_id INTO ws FROM issue WHERE id=iid;
 ELSIF TG_TABLE_NAME='chat_message' THEN
  cid:=NEW.chat_session_id; sid:=NEW.id; body:=NEW.content; typ:='chat';
  SELECT workspace_id INTO ws FROM chat_session WHERE id=cid;
  IF NEW.sender_user_id IS NOT NULL THEN atype:='member'; aid:=NEW.sender_user_id;
  ELSIF NEW.task_id IS NOT NULL THEN SELECT 'agent',agent_id INTO atype,aid FROM agent_task_queue WHERE id=NEW.task_id;
  END IF;
  atype:=COALESCE(atype,'system');
 ELSIF TG_TABLE_NAME='issue_summon' THEN
  iid:=NEW.issue_id; ws:=NEW.workspace_id; sid:=NEW.id; typ:='needs_kun'; atype:=NEW.caller_type; aid:=NEW.caller_id;
 ELSE
  iid:=NEW.id; ws:=NEW.workspace_id; sid:=NEW.id;
  IF TG_OP='INSERT' THEN typ:='task'; body:=COALESCE(NEW.description,''); atype:=NEW.creator_type; aid:=NEW.creator_id;
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
 FOR b IN SELECT id,user_id FROM personal_access_token WHERE kind='bot' AND workspace_id=ws LOOP
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
   INSERT INTO bot_wake_event(event_id,bot_id,workspace_id,event_type,issue_id,chat_session_id,source_id,author_type,author_id)
   VALUES(eid,b.id,ws,CASE WHEN mentioned THEN 'mention' ELSE typ END,iid,cid,sid,atype,aid) ON CONFLICT DO NOTHING;
  END IF;
 END LOOP;
 RETURN NEW;
END $$;

DROP TRIGGER bot_wake_task_lineage ON agent_task_queue;
DROP TRIGGER bot_wake_source_comment ON comment;
DROP TRIGGER bot_wake_source_chat ON chat_message;
DROP FUNCTION bot_wake_task_lineage();
DROP FUNCTION bot_wake_source_lineage();
DROP TABLE bot_wake_chain;
DROP TABLE bot_wake_cause;
ALTER TABLE bot_wake_event DROP COLUMN via_bot_id,DROP COLUMN via_bot_name,DROP COLUMN source_task_id,DROP COLUMN root_id,DROP COLUMN hop;
ALTER TABLE agent_task_queue DROP COLUMN wake_root_id,DROP COLUMN wake_hop;
ALTER TABLE comment DROP COLUMN wake_root_id,DROP COLUMN wake_hop;
ALTER TABLE chat_message DROP COLUMN wake_root_id,DROP COLUMN wake_hop;
ALTER TABLE issue_summon DROP COLUMN via_bot_id,DROP COLUMN via_bot_name,DROP COLUMN source_task_id;
