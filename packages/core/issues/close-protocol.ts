import type { IssueMetadata } from "../types";

/**
 * Stage 2 close-protocol keys (DENE-231). Missing any of them means the
 * issue has not been closed under the protocol. Values are primitives on
 * the wire; agents write strings. See docs/kun/scheduling-close-protocol.md §6.1.
 */
export const CLOSE_PROTOCOL_KEYS = [
  "close.conclusion",
  "close.status",
  "close.evidence_comment_id",
  "close.next_owner_type",
  "close.next_owner_id",
  "close.wake_action",
  "close.waiting_on",
  "close.at",
] as const;

export type CloseProtocolKey = (typeof CLOSE_PROTOCOL_KEYS)[number];

export const CLOSE_CONCLUSIONS = [
  "delivered",
  "blocked",
  "awaiting_review",
  "awaiting_human",
] as const;

export const CLOSE_OWNER_TYPES = ["agent", "squad", "member", "none"] as const;

export const CLOSE_WAKE_ACTIONS = ["stage_done", "mention", "none"] as const;

export type CloseProtocolView = {
  complete: boolean;
  missingKeys: CloseProtocolKey[];
  conclusion: string | null;
  closeStatus: string | null;
  /** True when `close.status` is present and differs from `issue.status`. */
  statusDrift: boolean;
  nextOwnerType: string | null;
  nextOwnerId: string | null;
  wakeAction: string | null;
  waitingOn: string | null;
  at: string | null;
  /** Optional blocker attribution written when conclusion=blocked. */
  blockKind: string | null;
  blockAction: string | null;
};

function metaString(
  metadata: IssueMetadata | null | undefined,
  key: string,
): string | null {
  if (!metadata || !Object.prototype.hasOwnProperty.call(metadata, key)) {
    return null;
  }
  const value = metadata[key];
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  return null;
}

/**
 * Read the eight `close.*` keys off an issue metadata bag. Does not validate
 * §6.1 consistency beyond the two Stage 5 exception states: missing keys and
 * `close.status` drift from `issue.status`.
 */
export function readCloseProtocol(
  metadata: IssueMetadata | null | undefined,
  issueStatus: string,
): CloseProtocolView {
  const missingKeys = CLOSE_PROTOCOL_KEYS.filter(
    (key) => !metadata || !Object.prototype.hasOwnProperty.call(metadata, key),
  );
  const closeStatus = metaString(metadata, "close.status");
  const waitingOn = metaString(metadata, "close.waiting_on");
  const nextOwnerId = metaString(metadata, "close.next_owner_id");
  return {
    complete: missingKeys.length === 0,
    missingKeys,
    conclusion: metaString(metadata, "close.conclusion"),
    closeStatus,
    statusDrift: closeStatus !== null && closeStatus !== issueStatus,
    nextOwnerType: metaString(metadata, "close.next_owner_type"),
    nextOwnerId,
    wakeAction: metaString(metadata, "close.wake_action"),
    waitingOn,
    at: metaString(metadata, "close.at"),
    blockKind: metaString(metadata, "close.block_kind"),
    blockAction: metaString(metadata, "close.block_action"),
  };
}

/** Whether a close record is a stage blocker.
 *
 * `in_review` is deliberately not enough: a normal review is an expected
 * hand-off and only becomes a blocker when the caller derives review_overdue.
 */
export function closeProtocolIsStuck(
  issueStatus: string,
  conclusion: string | null,
): boolean {
  if (issueStatus === "blocked") return true;
  if (issueStatus === "in_review") return false;
  return (
    conclusion === "blocked" ||
    conclusion === "awaiting_review" ||
    conclusion === "awaiting_human"
  );
}

/** Non-empty `close.waiting_on` after trimming; empty string is "no wait". */
export function closeProtocolWaitingOn(waitingOn: string | null): string | null {
  if (waitingOn === null) return null;
  const trimmed = waitingOn.trim();
  return trimmed === "" ? null : trimmed;
}

/** `next_owner_id` when type is not `none`; empty id is treated as absent. */
export function closeProtocolNextOwnerId(
  ownerType: string | null,
  ownerId: string | null,
): string | null {
  if (ownerType === null || ownerType === "none") return null;
  if (ownerId === null) return null;
  const trimmed = ownerId.trim();
  return trimmed === "" ? null : trimmed;
}
