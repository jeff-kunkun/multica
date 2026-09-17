-- name: CreateIssueDraft :one
-- Opens the structured half of an alignment conversation. Called in the same
-- transaction as the carrier agent and chat session it belongs to, so a draft
-- can never exist without the conversation that owns it.
--
-- policy_key / policy_version are written next to the carrier agent whose
-- instructions were set from the same policy: the row records the prompt that
-- actually steers this conversation, not the one the registry happens to serve
-- when someone later asks. capability_keys / capability_version are the other
-- half of that record — the methods that prompt was assembled from, and the
-- text of those methods (issue_draft_capability.go).
INSERT INTO issue_draft (chat_session_id, workspace_id, draft, policy_key, policy_version, capability_keys, capability_version)
VALUES (@chat_session_id, @workspace_id, @draft, @policy_key, @policy_version, @capability_keys, @capability_version)
RETURNING *;

-- name: GetIssueDraftInWorkspace :one
-- Every read is workspace-scoped: chat_session_id is a client-supplied path
-- parameter, and without the workspace predicate it would address any draft in
-- the deployment.
SELECT * FROM issue_draft
WHERE chat_session_id = $1 AND workspace_id = $2;

-- name: LockIssueDraftInWorkspace :one
-- The finalize serialiser. FOR UPDATE makes "is this draft ready, at this
-- revision, and has it already become an issue" one decision rather than three
-- reads a concurrent confirm can interleave with — the window in which two
-- confirms of the same draft could each create their own issue.
--
-- Callers MUST re-read every field from the returned row and decide on those
-- values only: a confirm that blocked here resumes holding whatever it read
-- before blocking. The lock is NOT held across the issue create — see
-- FinalizeIssueDraft; the partial unique index on issue (origin_id) is the
-- authority on "at most one issue per draft", and this lock only serialises the
-- decide and record steps around it.
SELECT * FROM issue_draft
WHERE chat_session_id = $1 AND workspace_id = $2
FOR UPDATE;

-- name: ListIssueDraftsByCreator :many
-- The caller's alignment conversations in the given statuses, newest activity
-- first.
--
-- Creator-scoped for the same reason ListAgentBuilderSessionsByCreator is: a
-- draft is a private conversation, and a workspace admin has no business
-- reading someone else's half-formed request. These sessions never appear in
-- ListChatSessionsByCreator either — that list is filtered against
-- `kind = 'user'` agents, and the carrier is `kind = 'system'` — so this
-- statement is the only way back into an alignment conversation.
--
-- `statuses` is a parameter rather than the hardcoded ('draft','ready') this
-- used to be because the same rows answer two different questions: "which
-- alignments can I still act on" (the create dialog's resume banner) and
-- "which alignments have I ever had" (the chat sidebar's alignment records,
-- DENE-371). A terminal draft is a record to read back, not a draft to resume,
-- and the caller's status set is the whole of that distinction.
--
-- `finalize_round` / `finalized_revision` ride along because this list is the
-- only row an open alignment page reads: without them a continuation could not
-- say which round it is on, and the page had to POST /reopen — a write — purely
-- to read the round back (DENE-416).
--
-- `capability_keys` / `capability_version` ride along for the same reason:
-- this row is what the alignment page renders, and the capabilities control is
-- drawn from the set the conversation is actually running rather than from
-- whatever this client would pick by default.
SELECT d.chat_session_id,
       d.workspace_id,
       d.status,
       d.revision,
       d.draft,
       d.issue_id,
       d.policy_key,
       d.policy_version,
       d.capability_keys,
       d.capability_version,
       d.created_at,
       d.updated_at,
       d.finalize_round,
       d.finalized_revision,
       cs.title,
       a.runtime_id,
       COALESCE(lm.content, '') AS last_message_content,
       COALESCE(lm.role, '') AS last_message_role,
       lm.created_at AS last_message_at
FROM issue_draft d
JOIN chat_session cs ON cs.id = d.chat_session_id
JOIN agent a ON a.id = cs.agent_id
LEFT JOIN LATERAL (
  SELECT content, role, created_at
    FROM chat_message m
   WHERE m.chat_session_id = cs.id
   ORDER BY m.created_at DESC
   LIMIT 1
) lm ON true
WHERE d.workspace_id = $1
  AND cs.creator_id = $2
  AND cs.status = 'active'
  AND d.status = ANY(sqlc.arg('statuses')::text[])
ORDER BY COALESCE(lm.created_at, d.updated_at) DESC;

-- name: UpdateIssueDraft :one
-- The save half of the alignment protocol. Optimistic: the caller passes the
-- revision it was looking at, and a save built on a superseded view matches
-- zero rows rather than overwriting what it never saw. Terminal drafts
-- (completed / abandoned) are excluded by the same predicate, so a save racing
-- a confirm loses instead of resurrecting a finished conversation.
UPDATE issue_draft
SET draft = @draft,
    status = @status,
    revision = revision + 1,
    updated_at = now()
WHERE chat_session_id = @chat_session_id
  AND workspace_id = @workspace_id
  AND revision = @expected_revision
  AND status IN ('draft', 'ready')
RETURNING *;

-- name: UpdateIssueDraftPolicy :one
-- Re-records which alignment policy a live conversation is running under,
-- together with the version of that policy's prompt the carrier was just given,
-- and the capabilities that prompt was assembled from.
--
-- The capabilities are part of this write rather than a second statement
-- because they and the policy are one decision: the instructions installed on
-- the carrier are contract + capabilities + behaviour, so a row that recorded
-- the new policy with the old capability set would point at a prompt nobody
-- ran. Switching to a policy that requires a capability adds it; switching away
-- does not remove what the user chose.
--
-- Deliberately does NOT bump `revision`: revision is the optimistic-concurrency
-- token for the draft's *content*, and switching how the carrier asks questions
-- changes no field of it. Bumping here would reject the user's next save for a
-- conflict they cannot see. Terminal drafts are excluded, so a policy switch
-- racing a confirm loses rather than resurrecting a finished conversation.
UPDATE issue_draft
SET policy_key = @policy_key,
    policy_version = @policy_version,
    capability_keys = @capability_keys,
    capability_version = @capability_version,
    updated_at = now()
WHERE chat_session_id = @chat_session_id
  AND workspace_id = @workspace_id
  AND status IN ('draft', 'ready')
RETURNING *;

-- name: MarkIssueDraftCompleted :one
-- Pins the issue a confirmed draft became. Runs under
-- LockIssueDraftInWorkspace, after the issue exists.
--
-- The guard is 'not already completed', not 'still ready', and that is
-- deliberate: by the time this runs an issue HAS been created for this draft,
-- and a draft that produced an issue must point at it. Requiring 'ready' would
-- leave an issue orphaned from its conversation whenever the draft was
-- abandoned from another surface while the confirm was in flight — the issue
-- would still exist, just unreachable from the draft that made it. An already
-- completed draft is excluded because it is the idempotent case: its issue is
-- the answer, and the handler returns that instead of calling this.
UPDATE issue_draft
SET status = 'completed',
    issue_id = @issue_id,
    updated_at = now()
WHERE chat_session_id = @chat_session_id
  AND workspace_id = @workspace_id
  AND status <> 'completed'
RETURNING *;

-- name: ReopenIssueDraft :one
-- Starts another round on an alignment whose group already exists: the draft
-- goes back to 'ready' so the same conversation can be continued and confirmed
-- again, and the round counter advances so that the next confirm can tell the
-- nodes it does not find are an increment rather than a group to build.
--
-- finalized_revision records the revision this round confirmed. It is read
-- from the row, not passed in: revision does not move between a confirm and the
-- reopen that follows it — the save paths only accept 'draft'/'ready' — so the
-- value at reopen time IS the revision the round confirmed.
--
-- The guard is 'completed' alone, and that is what makes the call idempotent: a
-- second reopen matches zero rows (the draft is 'ready' by then) and the
-- handler answers with the row as it stands rather than counting the round
-- twice. Abandoned drafts are excluded, for the same reason
-- MarkIssueDraftCompleted excludes them: a discarded alignment does not come
-- back to life.
UPDATE issue_draft
SET status = 'ready',
    finalize_round = finalize_round + 1,
    finalized_revision = revision,
    updated_at = now()
WHERE chat_session_id = @chat_session_id
  AND workspace_id = @workspace_id
  AND status = 'completed'
RETURNING *;

-- name: MarkIssueDraftAbandoned :one
-- Discarding an alignment conversation. Terminal states are excluded so
-- abandoning a draft that has already become an issue cannot orphan that issue
-- from the conversation that produced it.
UPDATE issue_draft
SET status = 'abandoned',
    updated_at = now()
WHERE chat_session_id = @chat_session_id
  AND workspace_id = @workspace_id
  AND status IN ('draft', 'ready')
RETURNING *;

-- name: DeleteIssueDraft :exec
-- issue_draft has no chat_session FK (repo rule), so every path that removes a
-- conversation has to say so explicitly.
DELETE FROM issue_draft WHERE chat_session_id = $1;

-- name: DeleteIssueDraftsBySystemRuntimeAgents :exec
-- chat_session cascades from agent, so a runtime teardown hard-deleting its
-- system carriers (DeleteSystemAgentsByRuntime) silently drops their sessions.
-- Prune the drafts in the same tx and BEFORE the agent rows go — the join below
-- needs them. Mirrors DeleteAgentBuilderDraftsBySystemRuntimeAgents.
DELETE FROM issue_draft
WHERE chat_session_id IN (
    SELECT cs.id FROM chat_session cs
    JOIN agent a ON a.id = cs.agent_id
    WHERE a.runtime_id = $1 AND a.kind = 'system'
);
