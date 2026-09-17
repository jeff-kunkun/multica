import type {
  IssueDraftChild,
  IssueDraftCreatedIssue,
  IssueDraftFinalizeResult,
  IssueDraftPayload,
} from "../types";

/**
 * The group an alignment settles on: the parent issue plus its sub-issues, and
 * what confirming it will actually start.
 *
 * Two rules live here, both of them things that must agree between what the
 * panel SHOWS and what the server is asked to CREATE — a preview that promises
 * one thing and a confirm that does another is worse than no preview:
 *
 *   1. A sub-issue's status is derived from its stage, never authored. Stage 1
 *      runs the moment the group exists; stage 2 and later sit in Backlog with
 *      their assignee already bound, waiting for someone to promote the stage.
 *      This is the whole dispatch policy, and it is expressed purely as the
 *      payload's `status` — the server has no stage-to-status rule to keep in
 *      sync with (docs/design/issue-draft-group-finalize.md §6).
 *   2. The stage set is normalized before it is stored: stages are 1..N with no
 *      gaps, and a group where SOME sub-issues are staged gets stage 1 for the
 *      rest. A sub-issue with no stage in a staged group falls out of the stage
 *      barrier silently, which is worse than being late.
 *
 * Everything is pure and framework-free so the panel, the fold and the tests
 * can all ask the same question.
 */

/** The most sub-issues one payload may carry. The server refuses more (§2.4). */
export const ISSUE_DRAFT_MAX_CHILDREN = 20;

/** What the alignment prompt asks for by default: a group a person can read. */
export const ISSUE_DRAFT_RECOMMENDED_CHILDREN = 8;

/**
 * The status a sub-issue is created with, from its stage.
 *
 * "No stage" is not "no work": a group with no stages at all is one implicit
 * stage, and everything in it starts together — that is what a small request
 * that happened to be split looks like. Only an explicit stage ≥ 2 is a parking
 * spot.
 */
export function issueDraftChildStatus(
  stage: number | null | undefined,
): "todo" | "backlog" {
  return (stage ?? 1) <= 1 ? "todo" : "backlog";
}

/** The highest stage in a group, 0 when nothing is staged. */
export function maxIssueDraftChildStage(
  children: readonly IssueDraftChild[],
): number {
  return children.reduce(
    (max, child) => (child.stage != null && child.stage > max ? child.stage : max),
    0,
  );
}

/** Whether a stage is one the server will accept (1..20, or absent). */
function isUsableIssueDraftStage(stage: number | null | undefined): boolean {
  if (stage == null) return true;
  return (
    Number.isInteger(stage) && stage >= 1 && stage <= ISSUE_DRAFT_MAX_CHILDREN
  );
}

/**
 * Stages for the group, as the server will store them: contiguous, in the order
 * the sub-issues appear, and never moved down to 1.
 *
 * An unstaged group stays unstaged — forcing stage numbers onto it would invent
 * an order nobody agreed to, and every value the barrier reads would then come
 * from the client's rendering order. Once ANY sub-issue carries a stage,
 * however, the group is a staged one and every member has to participate:
 * `stageBarrierClosed` skips unstaged siblings, so such a sub-issue would
 * neither hold a stage back nor be woken by one (design §6.4).
 *
 * Gaps are closed (1, 3 → 1, 2) because `stageProgressSummary` prints the real
 * numbers into the promotion comment and "Stage 1 / Stage 3" reads as a bug.
 * The LOWEST stage is deliberately not renumbered: a group whose only stage is
 * 2 is a group that waits, and squashing it to 1 would silently turn a parked
 * sub-issue into one that runs the moment it is created.
 */
export function normalizeIssueDraftChildStages(
  children: readonly IssueDraftChild[],
): IssueDraftChild[] {
  const stages = children.map((child) =>
    isUsableIssueDraftStage(child.stage) ? (child.stage ?? null) : null,
  );
  if (stages.every((stage) => stage === null)) {
    return children.map((child) =>
      child.stage == null ? child : { ...child, stage: null },
    );
  }
  const distinct = [...new Set(stages.map((stage) => stage ?? 1))].sort(
    (a, b) => a - b,
  );
  // Renumbered from the lowest stage up, so the carrier's order is the plan and
  // the numbers stay inside the range the server accepts: the values are already
  // 1..20, so a contiguous run starting at the lowest one cannot leave it.
  const start = distinct[0] ?? 1;
  return children.map((child, index) => {
    const stage = stages[index] ?? 1;
    return { ...child, stage: start + distinct.indexOf(stage) };
  });
}

/**
 * Gives every sub-issue a usable key.
 *
 * The carrier is told to keep its keys stable and the parse path mints one when
 * it forgets; this is the same repair applied to a draft that already exists,
 * so a row that arrived without a key cannot reach the confirm — the server
 * refuses a keyless sub-issue with a 400, and a duplicate key derives the same
 * identity twice and collides inside the create transaction.
 */
export function mintIssueDraftChildKeys(
  children: readonly IssueDraftChild[],
): IssueDraftChild[] {
  const used = new Set<string>();
  let next = 1;
  return children.map((child) => {
    const key = child.key.trim();
    if (key.length > 0 && key.length <= 64 && !used.has(key)) {
      used.add(key);
      return child.key === key ? child : { ...child, key };
    }
    while (used.has(`c${next}`)) next += 1;
    const minted = `c${next}`;
    used.add(minted);
    return { ...child, key: minted };
  });
}

/**
 * The sub-issue set as the server should receive it: keys present and unique,
 * stages contiguous, statuses derived from those stages, priority defaulted.
 *
 * Applied at every write boundary (the carrier's block, the panel's save), so
 * the payload stored on the server is already the one the confirm will read —
 * the confirm sends a revision, not a payload, and therefore cannot normalize
 * anything itself.
 */
export function normalizeIssueDraftChildren(
  children: readonly IssueDraftChild[],
): IssueDraftChild[] {
  return mintIssueDraftChildKeys(normalizeIssueDraftChildStages(children)).map(
    (child) => ({
      ...child,
      status: issueDraftChildStatus(child.stage),
      priority: child.priority.trim() || "none",
    }),
  );
}

/** The payload with its group normalized, or the same object when there is
 *  nothing to normalize. */
export function normalizeIssueDraftPayloadGroup(
  payload: IssueDraftPayload,
): IssueDraftPayload {
  const children = payload.children ?? [];
  if (children.length === 0) return payload;
  const normalized = normalizeIssueDraftChildren(children);
  return sameIssueDraftChildren(children, normalized)
    ? payload
    : { ...payload, children: normalized };
}

/** Whether two sub-issue sets are the same, field by field. */
export function sameIssueDraftChildren(
  a: readonly IssueDraftChild[],
  b: readonly IssueDraftChild[],
): boolean {
  if (a.length !== b.length) return false;
  return a.every((child, index) => {
    const other = b[index];
    if (!other) return false;
    return (
      child.key === other.key &&
      child.title === other.title &&
      child.description === other.description &&
      child.status === other.status &&
      child.priority === other.priority &&
      (child.stage ?? null) === (other.stage ?? null) &&
      (child.assignee_type ?? null) === (other.assignee_type ?? null) &&
      (child.assignee_id ?? null) === (other.assignee_id ?? null) &&
      (child.assignee_hint ?? null) === (other.assignee_hint ?? null)
    );
  });
}

/** The fields whose value decides whether an agent starts on its own. */
export interface IssueDraftNodeDispatch {
  status: string;
  assignee_type?: string | null;
  assignee_id?: string | null;
}

/**
 * Whether creating this node starts an agent straight away.
 *
 * The server's rule, mirrored from `shouldEnqueueAgentTaskWithQueries`: work is
 * enqueued unless the effective status is Backlog, and there is nothing to
 * enqueue without an agent assignee. `status` is normalized the way the server
 * normalizes it — an empty string is `todo`.
 *
 * A member assignee is deliberately not "running": assigning a person enqueues
 * nothing, and saying otherwise would tell the user a human had been paged.
 */
export function issueDraftNodeRunsOnCreate(
  node: IssueDraftNodeDispatch,
): boolean {
  const status = node.status.trim() || "todo";
  if (status === "backlog") return false;
  return node.assignee_type === "agent" && !!node.assignee_id;
}

/** One line of the confirm preview: a node and what confirming does with it. */
export interface IssueDraftGroupRow {
  /** The node's key. Empty for the group's root. */
  key: string;
  title: string;
  stage: number | null;
  /** The status the node will be created with. */
  status: string;
  assigneeType: string | null;
  assigneeId: string | null;
  assigneeHint: string | null;
  isRoot: boolean;
  /** Confirming creates this issue AND starts its assignee's work. */
  startsOnCreate: boolean;
}

export interface IssueDraftGroupPlan {
  /** The root first, then the sub-issues in payload order. */
  rows: IssueDraftGroupRow[];
  /** How many issues the confirm creates. */
  total: number;
  /** How many of them start an agent the moment they exist. */
  starting: number;
  /** How many are created in Backlog, waiting for their stage. */
  parked: number;
}

/**
 * What pressing "confirm and create" will do, as one list.
 *
 * The root's status is taken as the payload carries it (the panel owns that
 * field); a sub-issue's is derived from its stage, which is what makes the
 * preview agree with the stored payload even while the user is mid-edit.
 */
export function planIssueDraftGroup(
  payload: IssueDraftPayload | null,
): IssueDraftGroupPlan {
  if (!payload) return { rows: [], total: 0, starting: 0, parked: 0 };

  const root: IssueDraftGroupRow = {
    key: "",
    title: payload.title,
    stage: null,
    status: payload.status.trim() || "todo",
    assigneeType: payload.assignee_type ?? null,
    assigneeId: payload.assignee_id ?? null,
    assigneeHint: null,
    isRoot: true,
    startsOnCreate: issueDraftNodeRunsOnCreate(payload),
  };
  const children = (payload.children ?? []).map<IssueDraftGroupRow>((child) => {
    const stage = child.stage ?? null;
    const status = issueDraftChildStatus(stage);
    return {
      key: child.key,
      title: child.title,
      stage,
      status,
      assigneeType: child.assignee_type ?? null,
      assigneeId: child.assignee_id ?? null,
      assigneeHint: child.assignee_hint ?? null,
      isRoot: false,
      startsOnCreate: issueDraftNodeRunsOnCreate({
        status,
        assignee_type: child.assignee_type,
        assignee_id: child.assignee_id,
      }),
    };
  });
  const rows = [root, ...children];
  return {
    rows,
    total: rows.length,
    starting: rows.filter((row) => row.startsOnCreate).length,
    parked: rows.filter((row) => row.status === "backlog").length,
  };
}

/**
 * The group a confirm produced, from the confirm's own response.
 *
 * A backend that predates groups sends no `issues`, and a group with no
 * sub-issues is reported as exactly that: one row for the root. Degrading here
 * rather than at the call site is what lets the panel render the same list
 * whichever server it is talking to, without a second request.
 */
export function issueDraftCreatedGroup(
  result: Pick<IssueDraftFinalizeResult, "issue_id" | "issues">,
): IssueDraftCreatedIssue[] {
  if (result.issues && result.issues.length > 0) return result.issues;
  return [
    {
      id: result.issue_id,
      identifier: "",
      title: "",
      status: "",
      stage: null,
      assignee_type: null,
      assignee_id: null,
      parent_issue_id: null,
    },
  ];
}
