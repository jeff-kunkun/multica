import type { StoredAgentDraft } from "../types";
import { EMPTY_AGENT_DRAFT, type AgentDraft } from "./draft";

export type { StoredAgentDraft };

export function toStoredAgentDraft(
  draft: AgentDraft,
  appliedMessageId: string | null,
): StoredAgentDraft {
  return {
    name: draft.name,
    description: draft.description,
    instructions: draft.instructions,
    conversation_starters: draft.conversationStarters,
    avatar_url: draft.avatarUrl,
    permission_scope: draft.permissionScope,
    member_ids: [...draft.memberIds],
    team_ids: [...draft.teamIds],
    applied_message_id: appliedMessageId,
  };
}

/**
 * Rebuilds the editable draft. `runtimeId` comes from the caller because the
 * server owns it — see the note on {@link StoredAgentDraft}. Fields are picked
 * by name, so keys an older client stored (`model`, `thinking_level`,
 * `service_tier`) are ignored.
 */
export function fromStoredAgentDraft(
  stored: StoredAgentDraft,
  runtimeId: string,
): AgentDraft {
  return {
    ...EMPTY_AGENT_DRAFT,
    name: stored.name,
    description: stored.description,
    instructions: stored.instructions,
    conversationStarters: stored.conversation_starters ?? [],
    avatarUrl: stored.avatar_url,
    runtimeId,
    permissionScope: stored.permission_scope,
    memberIds: new Set(stored.member_ids),
    teamIds: new Set(stored.team_ids),
  };
}

/**
 * The stored form reduced to the fields this client knows. A draft saved by an
 * older client can still carry `model` / `thinking_level` / `service_tier`;
 * without this, those leftover keys would make an untouched draft compare as
 * edited and keep a dead slot alive.
 */
function knownStoredFields(stored: StoredAgentDraft): StoredAgentDraft {
  return {
    name: stored.name,
    description: stored.description,
    instructions: stored.instructions,
    conversation_starters: stored.conversation_starters,
    avatar_url: stored.avatar_url,
    permission_scope: stored.permission_scope,
    member_ids: stored.member_ids,
    team_ids: stored.team_ids,
    applied_message_id: stored.applied_message_id,
  };
}

/**
 * Whether two drafts would serialize identically — the autosave's "has anything
 * actually changed" test. Compared on the stored form so a `Set` rebuilt with
 * the same members does not read as an edit and cause a pointless write.
 */
export function storedAgentDraftsEqual(
  a: StoredAgentDraft,
  b: StoredAgentDraft,
): boolean {
  return (
    JSON.stringify(knownStoredFields(a)) ===
    JSON.stringify(knownStoredFields(b))
  );
}
