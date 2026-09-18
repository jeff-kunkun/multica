"use client";

import { createContext, useContext, type ReactNode } from "react";

/**
 * The issue ids the CURRENT row window reported as pinned.
 *
 * A projection of each row's own `is_pinned` flag — the same server field the
 * badge is drawn from — not a second source of truth: the surface builds it
 * from the `/table/rows` pages it already holds, so the badge and the row order
 * cannot disagree. It exists because the row projections flatten
 * `IssueTableRow` down to `Issue`, and re-deriving pins from the sidebar pin
 * list would let a pin that the server did not rank lead a client sort.
 *
 * Deliberately NOT fed by `pinListOptions`: those rows are the sidebar's list,
 * which is unfiltered, while a surface only marks rows its own query returned.
 * (DENE-500)
 */
const EMPTY_PINNED_IDS: ReadonlySet<string> = new Set();

const IssueSurfacePinnedContext =
  createContext<ReadonlySet<string>>(EMPTY_PINNED_IDS);

export function IssueSurfacePinnedProvider({
  pinnedIssueIds,
  children,
}: {
  pinnedIssueIds: ReadonlySet<string>;
  children: ReactNode;
}) {
  return (
    <IssueSurfacePinnedContext.Provider value={pinnedIssueIds}>
      {children}
    </IssueSurfacePinnedContext.Provider>
  );
}

export function useIssuePinnedIds(): ReadonlySet<string> {
  return useContext(IssueSurfacePinnedContext);
}
