ALTER TABLE chat_session
    ADD COLUMN title_locked BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN progress_text TEXT NOT NULL DEFAULT '',
    ADD COLUMN progress_source TEXT NOT NULL DEFAULT '',
    ADD COLUMN progress_author_type TEXT NOT NULL DEFAULT '',
    ADD COLUMN progress_author_id UUID,
    ADD COLUMN progress_updated_at TIMESTAMPTZ;

ALTER TABLE issue
    ADD COLUMN progress_text TEXT NOT NULL DEFAULT '',
    ADD COLUMN progress_source TEXT NOT NULL DEFAULT '',
    ADD COLUMN progress_author_type TEXT NOT NULL DEFAULT '',
    ADD COLUMN progress_author_id UUID,
    ADD COLUMN progress_updated_at TIMESTAMPTZ;

CREATE TABLE chat_session_progress (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    chat_session_id UUID NOT NULL,
    text TEXT NOT NULL,
    source TEXT NOT NULL,
    author_type TEXT NOT NULL,
    author_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX chat_session_progress_session_created_idx
    ON chat_session_progress (chat_session_id, created_at DESC);

CREATE TABLE issue_progress (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    issue_id UUID NOT NULL,
    text TEXT NOT NULL,
    source TEXT NOT NULL,
    author_type TEXT NOT NULL,
    author_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX issue_progress_issue_created_idx
    ON issue_progress (issue_id, created_at DESC);
