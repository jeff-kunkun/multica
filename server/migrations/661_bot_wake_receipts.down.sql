DROP INDEX bot_wake_pending_unacknowledged;
ALTER TABLE bot_wake_event DROP COLUMN woken_at, DROP COLUMN result_at, DROP COLUMN result_url, DROP COLUMN acknowledged_at;
ALTER TABLE bot_wake DROP COLUMN wake_from;
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
  ELSIF NEW.role='user' THEN SELECT 'member',creator_id INTO atype,aid FROM chat_session WHERE id=cid;
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
  mentioned:=body ~ ('\]\(mention://bot/' || b.id::text || '\)');
  IF mentioned THEN
   INSERT INTO bot_wake_watch(bot_id,issue_id,chat_session_id) VALUES(b.id,iid,cid) ON CONFLICT DO NOTHING;
  END IF;
  interested:=EXISTS(SELECT 1 FROM bot_wake_watch x WHERE x.bot_id=b.id AND (x.issue_id=iid OR x.chat_session_id=cid));
  -- PAT bots inherit the owner's identity. Existing member assignments and
  -- subscriptions therefore carry lifecycle notifications to that owner's bots.
  IF iid IS NOT NULL AND typ <> 'mention' THEN
   interested:=interested OR EXISTS(SELECT 1 FROM issue i WHERE i.id=iid AND i.assignee_type='member' AND i.assignee_id=b.user_id)
    OR EXISTS(SELECT 1 FROM issue_subscriber s WHERE s.issue_id=iid AND s.user_type='member' AND s.user_id=b.user_id);
  END IF;
  IF mentioned OR (typ<>'mention' AND interested) THEN
   INSERT INTO bot_wake_event(event_id,bot_id,workspace_id,event_type,issue_id,chat_session_id,source_id,author_type,author_id)
   VALUES(eid,b.id,ws,CASE WHEN mentioned THEN 'mention' ELSE typ END,iid,cid,sid,atype,aid) ON CONFLICT DO NOTHING;
  END IF;
 END LOOP;
 RETURN NEW;
END $$;

DROP FUNCTION bot_wake_mention_text(text);
