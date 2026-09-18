import type { IssueTableQuerySpec, IssueTableRow } from "../../types";

/**
 * `sort.pinned_first` is opt-in on the wire: an older server decodes the table
 * request body with `DisallowUnknownFields()` and 400s the WHOLE page on a
 * field it does not know, so the flag is only ever sent once the caller is
 * known to have at least one issue pin — the same rollout switch
 * `hide_completed_parents` used. (DENE-500, DENE-444)
 */
export function withPinnedFirstSort(
  spec: IssueTableQuerySpec,
  pinnedFirst: boolean,
): IssueTableQuerySpec {
  if (!pinnedFirst || spec.sort.pinned_first === true) return spec;
  return { ...spec, sort: { ...spec.sort, pinned_first: true } };
}

/**
 * The pins-free twin of a query spec, for the requests that only count.
 *
 * `pinned_first` moves rows inside one branch; it cannot change `total`, a
 * status facet or a group's header count — the two arms share one membership
 * predicate server-side. Keeping it OUT of the count requests means toggling a
 * pin re-renders the row windows alone instead of re-running the workspace-wide
 * facet aggregations, which is the expensive half of a Table mount. (DENE-500)
 */
export function withoutPinnedFirstSort(
  spec: IssueTableQuerySpec,
): IssueTableQuerySpec {
  if (spec.sort.pinned_first === undefined) return spec;
  const { pinned_first: _pinnedFirst, ...sort } = spec.sort;
  return { ...spec, sort };
}

/**
 * The pinned ids a table page actually reported, read off each row's own
 * `is_pinned`.
 *
 * This is a PROJECTION of the served rows, never a re-derivation from the
 * sidebar pin list: a pin whose row this query did not return (filtered out, or
 * still on an un-paged arm) must neither lead a client sort nor draw a badge,
 * and an older server that omits the field simply yields an empty set — right
 * order, no marker. (DENE-500)
 */
export function pinnedIssueIdsFromRows(
  rows: readonly IssueTableRow[],
): ReadonlySet<string> {
  const ids = new Set<string>();
  for (const row of rows) {
    if (row.is_pinned === true) ids.add(row.issue.id);
  }
  return ids;
}
