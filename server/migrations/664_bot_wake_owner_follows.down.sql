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
   INSERT INTO bot_wake_cause(bot_id,root_id) SELECT id,gen_random_uuid() FROM personal_access_token WHERE id=via AND workspace_id=ws ON CONFLICT DO NOTHING;
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
   INSERT INTO bot_wake_cause(bot_id,root_id) SELECT id,gen_random_uuid() FROM personal_access_token WHERE id=via AND workspace_id=ws ON CONFLICT DO NOTHING;
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
 IF root IS NULL AND (via IS NULL OR EXISTS(SELECT 1 FROM personal_access_token WHERE id=via AND workspace_id=ws)) THEN root:=eid; END IF;
 hops:=COALESCE(hops,0);
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

ALTER TABLE bot_wake_event DROP COLUMN inherited_owner;
ALTER TABLE bot_wake DROP COLUMN inherit_owner_follows;
