-- Durable, workspace-scoped issue creation drafts. The chat session is the
-- hidden system-carrier conversation; this table owns the structured state.
-- No foreign keys: session/workspace/issue cleanup is explicit in application
-- transactions, matching the repository migration rule.
CREATE TABLE issue_draft (
    chat_session_id UUID PRIMARY KEY,
    workspace_id UUID NOT NULL,
    status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'ready', 'completed', 'abandoned')),
    revision BIGINT NOT NULL DEFAULT 0 CHECK (revision >= 0),
    draft JSONB NOT NULL DEFAULT '{}'::jsonb,
    issue_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
