"use client";

import { useMemo } from "react";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Circle, CircleDot } from "lucide-react";
import type { Issue } from "@multica/core/types";
import { closeProtocolWaitingOn, deriveBlockerTree, readCloseProtocol } from "@multica/core/issues";
import { childrenByParentsOptions, issueIdentifierOptions } from "@multica/core/issues/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { cn } from "@multica/ui/lib/utils";
import { isIssueIdentifier } from "@multica/ui/markdown";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";

/**
 * Depth `deriveBlockerTree` walks below the parent. The fetch chain below must
 * reach the same depth: the `children` prop covers level 1, and each batched
 * level query covers one more. Change both together or the tree stops at a
 * layer the card never fetched.
 */
const MAX_DEPTH = 4;

/** Sorted + deduplicated ids of every issue in a batched children response. */
function nextParentIds(level: ReadonlyMap<string, Issue[]> | undefined): string[] {
  if (!level) return [];
  const ids = new Set<string>();
  for (const list of level.values()) for (const item of list) ids.add(item.id);
  return [...ids].sort();
}

export function SubIssueBlockerSummary({ issue, children }: { issue: Issue; children: Issue[] }) {
  const { t } = useT("issues");
  const paths = useWorkspacePaths();
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
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
    const result = new Map<string, Issue[]>([[issue.id, children]]);
    for (const level of [level1.data, level2.data, level3.data]) {
      if (!level) continue;
      for (const [parentId, kids] of level) result.set(parentId, kids);
    }
    return result;
  }, [issue.id, children, level1.data, level2.data, level3.data]);
  const allKnown = useMemo(() => {
    const result = new Map<string, Issue>();
    result.set(issue.identifier, issue);
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
  const tree = useMemo(() => deriveBlockerTree(issue, {
    childrenByParent: knownByParent,
    issueByIdentifier,
    maxDepth: MAX_DEPTH,
  }), [issue, knownByParent, issueByIdentifier]);
  const roots = tree.rootCauses;
  if (roots.length === 0 && tree.userActionCount === 0) return null;
  const byId = new Map([...allKnown.values(), ...waitingQueries.flatMap((query) => query.data ? [query.data] : [])].map((item) => [item.id, item]));
  return (
    <section data-testid="sub-issue-blocker-summary" className="mb-3 rounded-lg border border-destructive/20 bg-destructive/5 px-3 py-2">
      <div className="flex items-center gap-2 text-caption font-medium text-foreground">
        <AlertTriangle className="size-3.5 text-destructive" aria-hidden />
        <span>{t(($) => $.blocker_summary.title, { count: roots.length })}</span>
        {tree.userActionCount > 0 && <span className="text-micro text-destructive">{t(($) => $.blocker_summary.needs_you, { count: tree.userActionCount })}</span>}
      </div>
      <ul className="mt-1.5 space-y-1">
        {roots.slice(0, 5).map((root) => {
          const target = byId.get(root.id);
          // A cross-family `waiting_on` target that no snapshot resolved —
          // identifier query answered nothing, or has not settled yet — has no
          // issue to key `byId` with: its ref carries the identifier in both
          // fields. Link that identifier so the row still lists the ticket
          // number and stays one click away from the blocker.
          return (
            <li key={root.id} className="flex min-w-0 items-center gap-1.5 text-caption">
              <CircleDot className="size-3 shrink-0 text-destructive" aria-hidden />
              <AppLink href={paths.issueDetail(target?.id ?? root.identifier)} className="truncate text-muted-foreground hover:text-foreground">{target ? `${target.identifier} · ${target.title}` : root.identifier}</AppLink>
            </li>
          );
        })}
      </ul>
    </section>
  );
}

export function SubIssueBlockerBadge({ state, rootCause }: { state: "ROOT" | "PROPAGATED" | "CLEAR"; rootCause?: string }) {
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

export function blockerStateForIssue(parent: Issue, child: Issue, siblings: Issue[]): { state: "ROOT" | "PROPAGATED" | "CLEAR"; rootCause?: string } {
  const tree = deriveBlockerTree(parent, { childrenByParent: new Map([[parent.id, siblings]]) });
  const node = tree.nodes.get(child.id);
  return { state: node?.state ?? "CLEAR", rootCause: node?.rootCauses[0]?.identifier };
}
