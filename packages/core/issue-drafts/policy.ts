/**
 * The client half of the alignment policy.
 *
 * The server owns the policies — their prompts, and which version of each is
 * running (server/internal/handler/issue_draft_policy.go). The client only ever
 * chooses between the keys it knows, which is why this list is short and
 * explicit: the switch is rendered from it, so a key the client has never heard
 * of is never offered.
 *
 * A draft reports its own policy in `policy`, including the prompt version the
 * carrier was given. That is the audit trail: a conversation keeps reporting the
 * version it ran even after the registry has moved on.
 */
export const ISSUE_DRAFT_POLICIES = ["question", "conversation"] as const;

export type IssueDraftPolicyKey = (typeof ISSUE_DRAFT_POLICIES)[number];

/** Whether a server-supplied policy key is one this client can switch to. */
export function isIssueDraftPolicyKey(
  value: string,
): value is IssueDraftPolicyKey {
  return (ISSUE_DRAFT_POLICIES as readonly string[]).includes(value);
}
