-- Expand-only: processing attempts are separate from network send attempts.
ALTER TABLE bot_wake_event ADD COLUMN processing_attempts integer NOT NULL DEFAULT 0;
ALTER TABLE bot_wake ADD COLUMN last_processed_at timestamptz,
 ADD COLUMN last_error text NOT NULL DEFAULT '';
-- One ordinary chat notification per reply, including replies completed by UPDATE.
CREATE OR REPLACE FUNCTION bot_wake_capture() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE ws uuid; iid uuid; cid uuid; sid uuid; typ text; body text := ''; atype text := 'system'; aid uuid;
 msg chat_message%ROWTYPE; via uuid; vname text; stask uuid; root uuid; hops integer:=0;
 eid uuid := gen_random_uuid(); b record; mentioned boolean; interested boolean; inherited boolean;
BEGIN
 IF TG_TABLE_NAME='comment' THEN
  iid:=NEW.issue_id; sid:=NEW.id; body:=NEW.content; typ:='mention'; atype:=NEW.author_type; aid:=NEW.author_id;
  SELECT workspace_id INTO ws FROM issue WHERE id=iid;
  via:=NEW.via_bot_id;vname:=NEW.via_bot_name;stask:=NEW.source_task_id;root:=NEW.wake_root_id;hops:=NEW.wake_hop;
 ELSIF TG_TABLE_NAME IN ('chat_message','attachment') THEN
  IF TG_TABLE_NAME='attachment' THEN
   IF NEW.chat_message_id IS NULL THEN RETURN NEW; END IF;
   SELECT * INTO msg FROM chat_message WHERE id=NEW.chat_message_id;
   IF NOT FOUND THEN RETURN NEW; END IF;
  ELSE msg:=NEW; END IF;
  IF msg.role='assistant' AND ((msg.content='' AND NOT EXISTS(SELECT 1 FROM attachment WHERE chat_message_id=msg.id)) OR COALESCE(msg.failure_reason,'')<>'' OR msg.message_kind='no_response') THEN RETURN NEW; END IF;
  cid:=msg.chat_session_id; sid:=msg.id; body:=msg.content; typ:='chat';
  via:=msg.via_bot_id;vname:=msg.via_bot_name;stask:=msg.task_id;root:=msg.wake_root_id;hops:=msg.wake_hop;
  SELECT workspace_id INTO ws FROM chat_session WHERE id=cid;
  IF msg.sender_user_id IS NOT NULL THEN atype:='member'; aid:=msg.sender_user_id;
  ELSIF msg.task_id IS NOT NULL THEN SELECT 'agent',agent_id INTO atype,aid FROM agent_task_queue WHERE id=msg.task_id;
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
  IF TG_TABLE_NAME='chat_message' AND to_jsonb(NEW)->>'role'='assistant' THEN typ:='chat';
  ELSE typ:='mention'; END IF;
 END IF;
 IF typ IS NULL THEN RETURN NEW; END IF;
 -- Stable chat identity deduplicates completion retries without imposing a
 -- new uniqueness constraint on historical event rows.
 IF typ='chat' THEN eid:=sid; END IF;
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
  inherited:=false;
  IF iid IS NOT NULL AND typ<>'mention' AND NOT mentioned AND NOT interested THEN
   inherited:=EXISTS(SELECT 1 FROM bot_wake w WHERE w.bot_id=b.id AND w.inherit_owner_follows)
    AND (EXISTS(SELECT 1 FROM issue_subscriber s WHERE s.issue_id=iid AND s.user_type='member' AND s.user_id=b.user_id)
     OR EXISTS(SELECT 1 FROM issue i WHERE i.id=iid AND i.assignee_type='member' AND i.assignee_id=b.user_id));
  END IF;
  IF typ='chat' AND NOT mentioned AND EXISTS(SELECT 1 FROM bot_wake_event e WHERE e.bot_id=b.id AND e.source_id=sid AND e.event_type='chat') THEN CONTINUE; END IF;
  IF mentioned OR (typ<>'mention' AND (interested OR inherited)) THEN
   INSERT INTO bot_wake_event(event_id,bot_id,workspace_id,event_type,issue_id,chat_session_id,source_id,author_type,author_id,via_bot_id,via_bot_name,source_task_id,root_id,hop,inherited_owner)
   VALUES(CASE WHEN mentioned THEN gen_random_uuid() ELSE eid END,b.id,ws,CASE WHEN mentioned THEN 'mention' ELSE typ END,iid,cid,sid,atype,aid,via,vname,stask,root,hops,inherited) ON CONFLICT DO NOTHING;
  END IF;
 END LOOP;
 RETURN NEW;
END $$;

-- Completion creates the message before binding its files. Capture only after
-- binding, in the same transaction, so a file-only reply is not an empty placeholder.
CREATE TRIGGER bot_wake_chat_attachment AFTER INSERT OR UPDATE OF chat_message_id ON attachment
 FOR EACH ROW EXECUTE FUNCTION bot_wake_capture();
