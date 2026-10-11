-- Expand-only: existing clients may omit attachment digests and wake settings.
ALTER TABLE attachment ADD COLUMN sha256 text CHECK (sha256 ~ '^[0-9a-f]{64}$');
CREATE TABLE bot_wake (
 bot_id uuid PRIMARY KEY REFERENCES personal_access_token(id) ON DELETE CASCADE,
 webhook_secret bytea,
 events text[] NOT NULL DEFAULT ARRAY['mention','started','done','blocked','needs_kun','task','chat'],
 hourly_limit integer NOT NULL DEFAULT 6 CHECK (hourly_limit BETWEEN 0 AND 120),
 quiet_hours jsonb NOT NULL DEFAULT '{"enabled":true,"start":"23:00","end":"08:00","timezone":"Asia/Shanghai"}',
 updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO bot_wake(bot_id) SELECT id FROM personal_access_token WHERE kind='bot';
CREATE FUNCTION bot_wake_initialize() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.kind='bot' THEN INSERT INTO bot_wake(bot_id) VALUES(NEW.id) ON CONFLICT DO NOTHING; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER bot_wake_initialize AFTER INSERT ON personal_access_token FOR EACH ROW EXECUTE FUNCTION bot_wake_initialize();
CREATE TABLE bot_wake_watch (
 bot_id uuid NOT NULL REFERENCES personal_access_token(id) ON DELETE CASCADE,
 issue_id uuid REFERENCES issue(id) ON DELETE CASCADE,
 chat_session_id uuid REFERENCES chat_session(id) ON DELETE CASCADE,
 CHECK (num_nonnulls(issue_id,chat_session_id)=1)
);
CREATE UNIQUE INDEX bot_wake_watch_issue ON bot_wake_watch(bot_id,issue_id) WHERE issue_id IS NOT NULL;
CREATE UNIQUE INDEX bot_wake_watch_chat ON bot_wake_watch(bot_id,chat_session_id) WHERE chat_session_id IS NOT NULL;
CREATE TABLE bot_wake_event (
 sequence bigserial PRIMARY KEY,
 event_id uuid NOT NULL DEFAULT gen_random_uuid(),
 bot_id uuid NOT NULL REFERENCES personal_access_token(id) ON DELETE CASCADE,
 workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
 event_type text NOT NULL,
 issue_id uuid REFERENCES issue(id) ON DELETE CASCADE,
 chat_session_id uuid REFERENCES chat_session(id) ON DELETE CASCADE,
 source_id uuid NOT NULL,
 author_type text NOT NULL DEFAULT 'system',
 author_id uuid,
 status text NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','sending','received','failed','uncertain','pending')),
 reason text NOT NULL DEFAULT '',
 attempts integer NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 sent_at timestamptz,
 received_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(bot_id,event_id),
 CHECK (num_nonnulls(issue_id,chat_session_id)=1)
);
CREATE UNIQUE INDEX bot_wake_mention_once ON bot_wake_event(bot_id,source_id) WHERE event_type='mention';
CREATE INDEX bot_wake_event_ready ON bot_wake_event(next_attempt_at,sequence) WHERE status='queued';
CREATE INDEX bot_wake_event_bot ON bot_wake_event(bot_id,sequence DESC);
CREATE TABLE bot_wake_attempt (bot_id uuid NOT NULL REFERENCES personal_access_token(id) ON DELETE CASCADE, attempted_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX bot_wake_attempt_window ON bot_wake_attempt(bot_id,attempted_at);
-- Transactional outbox: source changes and receipts commit together, including
-- writes from workers. No in-memory event queue can lose a notification.
CREATE FUNCTION bot_wake_capture() RETURNS trigger LANGUAGE plpgsql AS $$
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
CREATE TRIGGER bot_wake_comment AFTER INSERT OR UPDATE OF content ON comment FOR EACH ROW EXECUTE FUNCTION bot_wake_capture();
CREATE TRIGGER bot_wake_chat AFTER INSERT OR UPDATE OF content ON chat_message FOR EACH ROW EXECUTE FUNCTION bot_wake_capture();
CREATE TRIGGER bot_wake_issue AFTER INSERT OR UPDATE OF status,assignee_id ON issue FOR EACH ROW EXECUTE FUNCTION bot_wake_capture();
CREATE TRIGGER bot_wake_summon AFTER INSERT ON issue_summon FOR EACH ROW EXECUTE FUNCTION bot_wake_capture();
