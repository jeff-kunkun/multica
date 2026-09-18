/**
 * The client half of the alignment capabilities.
 *
 * The server owns the methods — their text, and which version of it a
 * conversation ran (server/internal/handler/issue_draft_capability.go). The
 * client only ever chooses between the keys it knows, which is why this list is
 * short and explicit: the picker renders from it, so a key this client has
 * never heard of is never sent.
 *
 * A draft reports its own set in `capabilities.keys`, together with the version
 * of the text the carrier was given. That is the audit trail, and it is also
 * what the picker reads back: the boxes are ticked from what the conversation
 * is actually running, not from what this client would pick by default.
 *
 * `grilling` is deliberately absent. It is a capability the server registers
 * and `grill` requires — asking for the entry gets the discipline with it — so
 * offering it here would be a second way to ask for the same thing, and an
 * unchecked box that cannot actually be turned off.
 *
 * Order is the order the picker renders: the map method first, then the
 * interview entry, then the look round.
 */
export const ISSUE_DRAFT_CAPABILITIES = [
  "wayfinder",
  "grill",
  "grill-frontend-look",
] as const;

export type IssueDraftCapabilityKey = (typeof ISSUE_DRAFT_CAPABILITIES)[number];

/** Whether a server-supplied capability key is one this client can offer. */
export function isIssueDraftCapabilityKey(
  value: string,
): value is IssueDraftCapabilityKey {
  return (ISSUE_DRAFT_CAPABILITIES as readonly string[]).includes(value);
}

/**
 * The keys this client offers that a draft is actually running, in this
 * client's render order.
 *
 * Intersected rather than echoed: the recorded list is authoritative — it is
 * what the carrier's prompt was assembled from, and it may name a capability a
 * later build retired — but a key this client cannot render is not a box it can
 * draw, and a key the server recorded that this client does not offer must not
 * silently become an unticked box the user appears to have turned off.
 */
export function issueDraftEnabledCapabilities(
  recorded: readonly string[],
): IssueDraftCapabilityKey[] {
  return ISSUE_DRAFT_CAPABILITIES.filter((key) => recorded.includes(key));
}

/**
 * The list to send when the user opens an alignment with this selection.
 *
 * Sent in this client's order; the server re-orders into its own assembly order
 * before anything is recorded, so two clients that picked the same set produce
 * the same prompt. An empty selection is sent as an empty array and means
 * "none" — omitting the field would mean "the built-in default", which is the
 * opposite of what an emptied picker just asked for.
 */
export function encodeIssueDraftCapabilities(
  selected: readonly IssueDraftCapabilityKey[],
): string[] {
  return ISSUE_DRAFT_CAPABILITIES.filter((key) => selected.includes(key));
}
