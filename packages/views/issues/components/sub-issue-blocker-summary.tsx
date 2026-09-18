"use client";

import { useMemo } from "react";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Circle, CircleDot } from "lucide-react";
import type { Issue } from "@multica/core/types";
import {
  closeProtocolWaitingOn,
  deriveBlockerTree,
  orderBlockerRootCauses,
  readCloseProtocol,
  type BlockerState,
  type BlockerTreeNode,
  type BlockerTreeResult,
} from "@multica/core/issues";
import { childrenByParentsOptions, issueIdentifierOptions } from "@multica/core/issues/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { useActorName } from "@multica/core/workspace/hooks";
import { cn } from "@multica/ui/lib/utils";
import { isIssueIdentifier } from "@multica/ui/markdown";
import { AppLink } from "../../navigation";
import { useT, useTimeAgo } from "../../i18n";

/**
 * Depth `deriveBlockerTree` walks below the parent. The fetch chain below must
 * reach the same depth: the `children` snapshot covers level 1, and each
 * batched level query covers one more. Change both together or the tree stops
 * at a layer nothing fetched.
 */
const MAX_DEPTH = 4;

/** Sorted + deduplicated ids of every issue in a batched children response. */
function nextParentIds(level: ReadonlyMap<string, Issue[]> | undefined): string[] {
  if (!level) return [];
  const ids = new Set<string>();
  for (const list of level.values()) for (const item of list) ids.add(item.id);
  return [...ids].sort();
}

export interface SubIssueBlockerData {
  /** Null until the parent issue itself has loaded. */
  tree: BlockerTreeResult | null;
  /** Every fetched issue keyed by id — the card's link targets. */
  issueById: ReadonlyMap<string, Issue>;
}

/**
 * One derived blocker tree per parent issue, shared by the summary card and
 * every sub-issue row badge.
 *
 * The rows live in `issue-detail.tsx`, outside the card, and each one used to
 * re-derive the tree from a hand-built single-level `childrenByParent` map: a
 * row whose blocker sits on a grandchild came out CLEAR, and the section cost
 * O(n²) per render. Expanding here once and looking nodes up by id fixes both
 * without a second fetch chain (`blocker-attribution-design.md` §3.4 row 3).
 */
export function useSubIssueBlockerData(
  issue: Issue | null,
  children: Issue[],
): SubIssueBlockerData {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  // Expand the already-fetched snapshot one level at a time, one batched
  // request per level rather than one per parent: this card renders on every
  // parent issue with sub-issues, so a per-parent fan-out would cost a request
  // per sub-issue on each visit.
  const level1Ids = useMemo(() => [...new Set(children.map((child) => child.id))].sort(), [children]);
  const level1 = useQuery(childrenByParentsOptions(wsId, level1Ids, qc));
  const level2Ids = useMemo(() => nextParentIds(level1.data), [level1.data]);
  const level2 = useQuery(childrenByParentsOptions(wsId, level2Ids, qc));
  const level3Ids = useMemo(() => nextParentIds(level2.data), [level2.data]);
  const level3 = useQuery(childrenByParentsOptions(wsId, level3Ids, qc));
  const knownByParent = useMemo(() => {
    const result = new Map<string, Issue[]>();
    if (issue) result.set(issue.id, children);
    for (const level of [level1.data, level2.data, level3.data]) {
      if (!level) continue;
      for (const [parentId, kids] of level) result.set(parentId, kids);
    }
    return result;
  }, [issue, children, level1.data, level2.data, level3.data]);
  const allKnown = useMemo(() => {
    const result = new Map<string, Issue>();
    if (issue) result.set(issue.identifier, issue);
    for (const list of knownByParent.values()) for (const item of list) result.set(item.identifier, item);
    return result;
  }, [issue, knownByParent]);
  const waitingIds = useMemo(() => {
    const result = new Set<string>();
    for (const item of allKnown.values()) {
      const waitingOn = closeProtocolWaitingOn(readCloseProtocol(item.metadata, item.status).waitingOn);
      // `close.waiting_on` is free-form metadata; only identifier-shaped values
      // can resolve, and issueIdentifierOptions expects the caller to gate.
      if (waitingOn && isIssueIdentifier(waitingOn) && !allKnown.has(waitingOn)) result.add(waitingOn);
    }
    return [...result];
  }, [allKnown]);
  const waitingQueries = useQueries({
    queries: waitingIds.map((identifier) => issueIdentifierOptions(wsId, identifier)),
  });
  const issueByIdentifier = useMemo(() => {
    const result = new Map(allKnown);
    waitingQueries.forEach((query, index) => {
      const identifier = waitingIds[index];
      const value = query.data;
      if (identifier && value) result.set(identifier, value);
    });
    return result;
  }, [allKnown, waitingIds, waitingQueries]);
  const tree = useMemo(
    () =>
      issue
        ? deriveBlockerTree(issue, {
            childrenByParent: knownByParent,
            issueByIdentifier,
            maxDepth: MAX_DEPTH,
          })
        : null,
    [issue, knownByParent, issueByIdentifier],
  );
  const issueById = useMemo(() => {
    const result = new Map<string, Issue>();
    for (const item of issueByIdentifier.values()) result.set(item.id, item);
    return result;
  }, [issueByIdentifier]);
  return { tree, issueById };
}

export function SubIssueBlockerSummary({ data }: { data: SubIssueBlockerData }) {
  const { t } = useT("issues");
  const paths = useWorkspacePaths();
  const { tree, issueById } = data;
  // §3.3: needs-you rows first, then longest-blocked, then earliest stage.
  const roots = useMemo(() => (tree ? orderBlockerRootCauses(tree) : []), [tree]);
  if (!tree || (roots.length === 0 && tree.userActionCount === 0)) return null;
  return (
    <section data-testid="sub-issue-blocker-summary" className="mb-3 rounded-lg border border-destructive/20 bg-destructive/5 px-3 py-2">
      <div className="flex items-center gap-2 text-caption font-medium text-foreground">
        <AlertTriangle className="size-3.5 text-destructive" aria-hidden />
        <span>{t(($) => $.blocker_summary.title, { count: roots.length })}</span>
        {tree.userActionCount > 0 && <span className="text-micro text-destructive">{t(($) => $.blocker_summary.needs_you, { count: tree.userActionCount })}</span>}
      </div>
      <ul className="mt-1.5 space-y-1">
        {roots.slice(0, 5).map((root) => {
          const target = issueById.get(root.id);
          // A cross-family `waiting_on` target that no snapshot resolved —
          // identifier query answered nothing, or has not settled yet — has no
          // issue to key `issueById` with: its ref carries the identifier in
          // both fields. Link that identifier so the row still lists the ticket
          // number and stays one click away from the blocker.
          return (
            <BlockerRootCauseRow
              key={root.id}
              node={tree.nodes.get(root.id)}
              label={target ? `${target.identifier} · ${target.title}` : root.identifier}
              href={paths.issueDetail(target?.id ?? root.identifier)}
            />
          );
        })}
      </ul>
    </section>
  );
}

/**
 * One root cause line: the ticket it lives on, plus that ticket's own next
 * action. A parent never restates a child's reason body — it shows the recorded
 * `close.block_kind` + `close.block_action`, or core's copy for a derived
 * review-overdue / wake-missed / cyclic blocker (§3.4 rows 7, 10, 12, 13, 17).
 * Rendered per row so the actor directory is only read when a row needs a name.
 */
function BlockerRootCauseRow({ node, label, href }: { node: BlockerTreeNode | undefined; label: string; href: string }) {
  const hint = useBlockerActionHint(node);
  return (
    <li className="flex min-w-0 items-center gap-1.5 text-caption">
      <CircleDot className="size-3 shrink-0 text-destructive" aria-hidden />
      <AppLink href={href} className="min-w-0 flex-1 truncate text-muted-foreground hover:text-foreground">{label}</AppLink>
      {hint && (
        <span
          data-testid="sub-issue-blocker-action"
          data-blocker-kind={hint.kind}
          // The action is capped at 80 chars but the row is not; the full text
          // stays reachable when CSS truncation kicks in.
          title={hint.text}
          className="max-w-[55%] shrink-0 truncate text-micro text-muted-foreground"
        >
          {hint.text}
        </span>
      )}
    </li>
  );
}

function useBlockerActionHint(node: BlockerTreeNode | undefined): { kind: string; text: string } | null {
  const { t } = useT("issues");
  const timeAgo = useTimeAgo();
  const { getActorName } = useActorName();
  const attribution = node?.attribution;
  // Localized label for a recorded `close.block_kind`; unknown kinds have none
  // and fall back to the closer's own action text.
  const kindLabel = (kind: string): string | null => {
    switch (kind) {
      case "decision":
        return t(($) => $.blocker_summary.kind_decision);
      case "permission":
        return t(($) => $.blocker_summary.kind_permission);
      case "external":
        return t(($) => $.blocker_summary.kind_external);
      case "capacity":
        return t(($) => $.blocker_summary.kind_capacity);
      default:
        return null;
    }
  };
  if (!attribution) return null;
  const { kind, action, nextOwnerType, nextOwnerId, waitingOn, at } = attribution;
  const ownerName =
    nextOwnerType && nextOwnerType !== "none" && nextOwnerId
      ? getActorName(nextOwnerType, nextOwnerId)
      : null;
  // Derived kinds generate their own copy; recorded ones lean on the closer's
  // `block_action`.
  switch (kind) {
    case "review_overdue": {
      // Core only derives this kind from a parseable `close.at`, so `at` is set.
      if (!at) return null;
      const base = t(($) => $.blocker_summary.kind_review_overdue, { ago: timeAgo(at) });
      return { kind, text: ownerName ? `${base} · @${ownerName}` : base };
    }
    case "wake_missed":
      return { kind, text: t(($) => $.blocker_summary.kind_wake_missed, { id: waitingOn ?? "" }) };
    case "cycle":
      return { kind, text: t(($) => $.blocker_summary.kind_cycle) };
    default: {
      const label = kindLabel(kind);
      if (label && action) return { kind, text: t(($) => $.blocker_summary.kind_action, { kind: label, action }) };
      if (action) return { kind, text: action };
      // A bare "blocked" says nothing the card's title has not already said.
      if (label && kind !== "blocked") return { kind, text: label };
      return null;
    }
  }
}

export function SubIssueBlockerBadge({ state, rootCause }: { state: BlockerState; rootCause?: string }) {
  const { t } = useT("issues");
  if (state === "CLEAR") return null;
  const root = state === "ROOT";
  return (
    <span data-testid="sub-issue-blocker-badge" data-blocker-state={state} title={root ? t(($) => $.blocker_summary.root) : t(($) => $.blocker_summary.propagated)} className={cn("inline-flex size-4 shrink-0 items-center justify-center rounded-full", root ? "bg-destructive/10 text-destructive" : "border border-muted-foreground/50 text-muted-foreground")}>
      {root ? <CircleDot className="size-2.5" aria-hidden /> : <Circle className="size-2.5" aria-hidden />}
      <span className="sr-only">{root ? t(($) => $.blocker_summary.root) : t(($) => $.blocker_summary.propagated)}{rootCause ? ` ${rootCause}` : ""}</span>
    </span>
  );
}

/**
 * Row badge props for one sub-issue, looked up in the shared tree. A row whose
 * blocker sits on a grandchild is PROPAGATED here, not CLEAR (§3.4 row 3).
 */
export function blockerBadgeState(
  tree: BlockerTreeResult | null,
  childId: string,
): { state: BlockerState; rootCause?: string } | undefined {
  const node = tree?.nodes.get(childId);
  if (!node) return undefined;
  return { state: node.state, rootCause: node.rootCauses[0]?.identifier };
}
