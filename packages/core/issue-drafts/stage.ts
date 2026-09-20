import type { IssueDraftStatus } from "../types";

/**
 * The four stages an alignment conversation goes through, in order.
 *
 * `aligning` and `ready` are the server's own `draft` / `ready` status — the
 * draft is the agreement in progress, and it is the server that decides when it
 * is worth creating. `creating` and `created` cannot be read off the status:
 * finalize keeps the draft at `ready` until the issue exists, so "a confirm is
 * in flight" and "an issue now exists" are both client-observed facts.
 */
export type IssueDraftStage = "aligning" | "ready" | "creating" | "created";

/** Ordered, for the stepper strip. */
export const ISSUE_DRAFT_STAGES: readonly IssueDraftStage[] = [
  "aligning",
  "ready",
  "creating",
  "created",
] as const;

export function issueDraftStageIndex(stage: IssueDraftStage): number {
  return ISSUE_DRAFT_STAGES.indexOf(stage);
}

/**
 * Which stage the page is showing.
 *
 * `createdIssueId` outranks everything: a successful finalize is final even if
 * the follow-up list refetch has already dropped the completed draft out of the
 * unfinished list. `creating` outranks the stored status for the same reason in
 * the other direction — the confirm has been sent and the answer has not
 * arrived, so the truthful label is the one that says work is happening, not
 * the one the draft had before it was sent.
 */
export function issueDraftStage(input: {
  status: IssueDraftStatus;
  creating: boolean;
  createdIssueId: string | null;
}): IssueDraftStage {
  if (input.createdIssueId) return "created";
  if (input.creating) return "creating";
  switch (input.status) {
    case "ready":
      return "ready";
    case "draft":
      return "aligning";
    // `completed` without a known issue id, and `abandoned`, are terminal
    // states this page cannot act on; they read as "not aligning" rather than
    // as a stage that offers a confirm the server would refuse.
    default:
      return "aligning";
  }
}

/**
 * Whether this page may offer "confirm and create".
 *
 * Combines the server's status with the client's own readiness rather than
 * trusting either alone: the server refuses anything that is not `ready`, and a
 * draft with no title would be refused by validation, so offering the button on
 * either signal by itself would promise a create that cannot happen.
 */
export function issueDraftCanConfirm(input: {
  stage: IssueDraftStage;
  hasTitle: boolean;
  pending: boolean;
}): boolean {
  return input.stage === "ready" && input.hasTitle && !input.pending;
}
