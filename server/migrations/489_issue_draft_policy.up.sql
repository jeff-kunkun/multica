-- Which alignment policy a draft is being conducted under, and which version of
-- that policy's prompt the carrier was given.
--
-- The policy is the questioning behaviour of the alignment conversation: the
-- guided `question` policy asks one high-value question at a time and offers
-- recommended answers, the `conversation` policy just talks. It is configuration
-- rather than code because the prompt is what actually steers the carrier, and
-- a prompt that changes silently is a prompt nobody can audit.
--
-- Both values are recorded on the draft row at creation and rewritten whenever
-- the policy is switched, so a finished conversation can still answer "which
-- prompt produced this issue" after the registry has moved on. The defaults
-- backfill the drafts that already existed: every one of them was created with
-- the guided question prompt, version 1.
ALTER TABLE issue_draft
    ADD COLUMN IF NOT EXISTS policy_key TEXT NOT NULL DEFAULT 'question',
    ADD COLUMN IF NOT EXISTS policy_version TEXT NOT NULL DEFAULT '1';
