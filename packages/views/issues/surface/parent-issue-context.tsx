"use client";

import { createContext, useContext, useMemo, type ReactNode } from "react";
import type { Issue } from "@multica/core/types";

/** The slice of a parent issue a sub-issue card needs to name its owner. */
export interface ParentIssueRef {
  id: string;
  identifier: string;
  title: string;
}

const ParentIssueLookupContext = createContext<Map<string, ParentIssueRef> | null>(
  null,
);

/**
 * Publishes the surface's loaded issues as a parent lookup, so a sub-issue
 * card can name its parent without a request per card. Fed from the surface's
 * unfiltered set: a parent hidden by the active filters still has to label its
 * children, which are the rows the user is actually looking at. (DENE-480)
 */
export function ParentIssueLookupProvider({
  issues,
  children,
}: {
  issues: Issue[];
  children: ReactNode;
}) {
  const lookup = useMemo(() => {
    const map = new Map<string, ParentIssueRef>();
    for (const issue of issues) {
      map.set(issue.id, {
        id: issue.id,
        identifier: issue.identifier,
        title: issue.title,
      });
    }
    return map;
  }, [issues]);

  return (
    <ParentIssueLookupContext.Provider value={lookup}>
      {children}
    </ParentIssueLookupContext.Provider>
  );
}

/**
 * Resolves a `parent_issue_id` against the surface lookup. Returns null for a
 * top-level issue, outside a provider, and whenever the parent is not in the
 * loaded set — every caller renders nothing rather than a half-named chip.
 */
export function useParentIssueRef(
  parentIssueId: string | null | undefined,
): ParentIssueRef | null {
  const lookup = useContext(ParentIssueLookupContext);
  if (!parentIssueId || !lookup) return null;
  return lookup.get(parentIssueId) ?? null;
}
