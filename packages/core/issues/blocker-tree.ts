import type { Issue } from "../types";
import { closeProtocolWaitingOn, readCloseProtocol } from "./close-protocol";

export type BlockerState = "ROOT" | "PROPAGATED" | "CLEAR";

export type BlockerIssueRef = Pick<Issue, "id" | "identifier">;

export interface BlockerTreeNode {
  issue: BlockerIssueRef;
  state: BlockerState;
  rootCauses: BlockerIssueRef[];
  frontierStage: number | null;
  sideBlockers: BlockerIssueRef[];
  userActionCount: number;
  /** True when this node is a derived review/wake/cycle blocker. */
  derived: boolean;
  cycle: boolean;
}

export interface BlockerTreeOptions {
  childrenByParent?: ReadonlyMap<string, readonly Issue[]> | Record<string, readonly Issue[]>;
  issueByIdentifier?: ReadonlyMap<string, Issue> | Record<string, Issue | undefined>;
  /** Active task ids, issue ids, or actor keys (`type:id`). */
  activeRuns?: ReadonlySet<string> | readonly string[];
  now?: Date | string;
  maxDepth?: number;
}

export interface BlockerTreeResult extends BlockerTreeNode {
  nodes: ReadonlyMap<string, BlockerTreeNode>;
}

const TERMINAL = new Set(["done", "cancelled"]);
const MEMBER_REVIEW_MS = 24 * 60 * 60 * 1000;
const AGENT_REVIEW_MS = 30 * 60 * 1000;

function lookupChildren(options: BlockerTreeOptions, id: string): readonly Issue[] {
  const source = options.childrenByParent;
  if (!source) return [];
  if (typeof (source as ReadonlyMap<string, readonly Issue[]>).get === "function") {
    return (source as ReadonlyMap<string, readonly Issue[]>).get(id) ?? [];
  }
  return (source as Record<string, readonly Issue[]>)[id] ?? [];
}

function lookupIssue(options: BlockerTreeOptions, identifier: string): Issue | undefined {
  const source = options.issueByIdentifier;
  if (!source) return undefined;
  if (typeof (source as ReadonlyMap<string, Issue>).get === "function") {
    return (source as ReadonlyMap<string, Issue>).get(identifier);
  }
  return (source as Record<string, Issue | undefined>)[identifier];
}

function ref(issue: Issue): BlockerIssueRef {
  return { id: issue.id, identifier: issue.identifier };
}

function hasActiveRun(options: BlockerTreeOptions, issue: Issue): boolean {
  const runs = options.activeRuns;
  if (!runs) return false;
  const set = runs instanceof Set ? runs : new Set(runs);
  return (
    set.has(issue.id) ||
    (issue.assignee_type !== null && issue.assignee_id !== null &&
      (set.has(`${issue.assignee_type}:${issue.assignee_id}`) || set.has(issue.assignee_id)))
  );
}

function reviewOverdue(issue: Issue, options: BlockerTreeOptions, now: number): boolean {
  if (issue.status !== "in_review" || hasActiveRun(options, issue)) return false;
  const close = readCloseProtocol(issue.metadata, issue.status);
  if (!close.at) return false;
  const at = Date.parse(close.at);
  if (!Number.isFinite(at)) return false;
  const threshold = close.nextOwnerType === "member" ? MEMBER_REVIEW_MS : AGENT_REVIEW_MS;
  return now - at >= threshold;
}

function ownRoot(issue: Issue, options: BlockerTreeOptions, now: number): boolean {
  const close = readCloseProtocol(issue.metadata, issue.status);
  const waitingOn = closeProtocolWaitingOn(close.waitingOn);
  const waitingIssue = waitingOn ? lookupIssue(options, waitingOn) : undefined;
  if (waitingIssue && TERMINAL.has(waitingIssue.status)) {
    return true;
  }
  if (reviewOverdue(issue, options, now)) return true;
  return close.conclusion === "blocked" && close.blockKind !== "dependency";
}

function ownNeedsUserAction(issue: Issue, options: BlockerTreeOptions, now: number): boolean {
  const close = readCloseProtocol(issue.metadata, issue.status);
  if (reviewOverdue(issue, options, now)) return close.nextOwnerType === "member";
  if (close.conclusion !== "blocked") return false;
  return close.nextOwnerType === "member" || close.blockKind === "decision" || close.blockKind === "permission";
}

/**
 * Derive the current blocker frontier from issue metadata and staged children.
 * This is intentionally pure: callers provide their already-fetched snapshots.
 */
export function deriveBlockerTree(root: Issue, options?: BlockerTreeOptions): BlockerTreeResult;
export function deriveBlockerTree(input: BlockerTreeOptions & { root: Issue }): BlockerTreeResult;
export function deriveBlockerTree(
  rootOrInput: Issue | (BlockerTreeOptions & { root: Issue }),
  maybeOptions?: BlockerTreeOptions,
): BlockerTreeResult {
  const root = "root" in rootOrInput ? rootOrInput.root : rootOrInput;
  const options = "root" in rootOrInput ? rootOrInput : (maybeOptions ?? {});
  const now = Date.parse(options.now instanceof Date ? options.now.toISOString() : options.now ?? new Date().toISOString());
  const nodes = new Map<string, BlockerTreeNode>();
  const maxDepth = options.maxDepth ?? 4;

  function walk(issue: Issue, ancestors: Set<string>, depth: number): BlockerTreeNode {
    const loop = ancestors.has(issue.id);
    if (loop || depth > maxDepth) {
      // A cycle placeholder is authoritative for this path.  Do not allow the
      // outer frame to memoize a propagated result over the cycle attribution.
      const result: BlockerTreeNode = { issue: ref(issue), state: loop ? "ROOT" : "CLEAR", rootCauses: loop ? [ref(issue)] : [], frontierStage: null, sideBlockers: [], userActionCount: 0, derived: loop, cycle: loop };
      nodes.set(issue.id, result);
      return result;
    }
    const existing = nodes.get(issue.id);
    if (existing) return existing;
    const close = readCloseProtocol(issue.metadata, issue.status);
    const own = ownRoot(issue, options, now);
    const children = lookupChildren(options, issue.id).filter((child) => !TERMINAL.has(child.status));
    const staged = children.filter((child) => child.stage !== null);
    const frontierStage = staged.length ? Math.min(...staged.map((child) => child.stage!)) : null;
    const frontier = frontierStage === null ? children : children.filter((child) => child.stage === frontierStage);
    const nextAncestors = new Set(ancestors).add(issue.id);
    const childResults = frontier.map((child) => walk(child, nextAncestors, depth + 1));
    const waiting = closeProtocolWaitingOn(close.waitingOn);
    const waitingIssue = waiting ? lookupIssue(options, waiting) : undefined;
    const waitingResult = waitingIssue && !TERMINAL.has(waitingIssue.status) ? walk(waitingIssue, nextAncestors, depth + 1) : undefined;
    // waiting_on is itself an active dependency edge while the target is
    // non-terminal. Keep a reference even when its snapshot is missing or it
    // currently has no own blocker, so cross-ticket waits are never silent.
    const waitingRef = waiting
      ? (waitingIssue ? ref(waitingIssue) : { id: waiting, identifier: waiting })
      : undefined;
    const causes = [...(own ? [ref(issue)] : []), ...childResults.flatMap((child) => child.rootCauses), ...(waitingRef ? [waitingRef] : []), ...(waitingResult?.rootCauses ?? [])];
    const unique = [...new Map(causes.map((cause) => [cause.id, cause])).values()];
    const state: BlockerState = own ? "ROOT" : unique.length ? "PROPAGATED" : "CLEAR";
    const userActionCount = (own && ownNeedsUserAction(issue, options, now) ? 1 : 0) +
      childResults.reduce((count, child) => count + child.userActionCount, 0) +
      (waitingResult?.userActionCount ?? 0);
    const prior = nodes.get(issue.id);
    if (prior?.cycle) return prior;
    const result: BlockerTreeNode = { issue: ref(issue), state, rootCauses: unique, frontierStage, sideBlockers: children.filter((child) => !frontier.includes(child)).map(ref), userActionCount, derived: own && close.conclusion !== "blocked", cycle: false };
    nodes.set(issue.id, result);
    return result;
  }

  return { ...walk(root, new Set(), 0), nodes };
}
