-- Expand-only. Receipt timestamps never replace transport status.
ALTER TABLE bot_wake ADD COLUMN wake_from jsonb NOT NULL DEFAULT '{"member":{"mode":"all","ids":[]},"agent":{"mode":"all","ids":[]},"bot":{"mode":"none","ids":[]}}';
ALTER TABLE bot_wake_event ADD COLUMN woken_at timestamptz,
 ADD COLUMN result_at timestamptz, ADD COLUMN result_url text,
 ADD COLUMN acknowledged_at timestamptz;
CREATE INDEX bot_wake_pending_unacknowledged ON bot_wake_event(bot_id,sequence DESC)
 WHERE acknowledged_at IS NULL AND status IN ('pending','failed','uncertain');
CREATE FUNCTION bot_wake_mention_text(body text) RETURNS text LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE line text; trimmed text; fence text := ''; output text := ''; pos integer; n integer; marker text;
BEGIN
 FOREACH line IN ARRAY string_to_array(body,E'\n') LOOP
  trimmed:=ltrim(line,' ');
  IF fence<>'' THEN IF left(trimmed,3)=fence THEN fence:=''; END IF; CONTINUE; END IF;
  IF left(trimmed,3) IN ('```','~~~') THEN fence:=left(trimmed,3); CONTINUE; END IF;
  IF left(line,4)='    ' OR left(line,1)=E'\t' OR left(trimmed,1)='>' THEN CONTINUE; END IF;
  WHILE length(line)>0 LOOP
   pos:=strpos(line,'`'); IF pos=0 THEN output:=output||line; EXIT; END IF;
   output:=output||left(line,pos-1); line:=substr(line,pos); n:=1;
   WHILE substr(line,n,1)='`' LOOP n:=n+1; END LOOP;
   marker:=left(line,n-1);line:=substr(line,n);pos:=strpos(line,marker);
   IF pos=0 THEN EXIT; END IF;line:=substr(line,pos+length(marker));
  END LOOP;
  output:=output||E'\n';
 END LOOP;
 RETURN output;
END $$;
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
