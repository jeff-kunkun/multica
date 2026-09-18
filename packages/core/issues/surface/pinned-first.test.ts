// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { IssueTableQuerySpec, IssueTableRow } from "../../types";
import {
  pinnedIssueIdsFromRows,
  withPinnedFirstSort,
  withoutPinnedFirstSort,
} from "./pinned-first";

// Rollout + invalidation contract for `sort.pinned_first`. Both halves matter:
// sending the flag too early 400s the whole page on an older server, and
// sending it on the count requests makes a pin toggle re-run workspace-wide
// aggregations whose answer cannot change. (DENE-500)

function spec(sort: IssueTableQuerySpec["sort"]): IssueTableQuerySpec {
  return { scope: { kind: "workspace" }, filters: {}, sort };
}

const base = () => spec({ field: "position", direction: "asc" });

describe("withPinnedFirstSort", () => {
  it("stays off the wire until the user actually has an issue pin", () => {
    const off = withPinnedFirstSort(base(), false);
    expect("pinned_first" in off.sort).toBe(false);
    // The whole point: an untouched request must serialize exactly as before,
    // because an older server rejects an unknown field outright.
    expect(JSON.stringify(off.sort)).toBe(
      JSON.stringify({ field: "position", direction: "asc" }),
    );
  });

  it("adds the flag, and only the flag", () => {
    const on = withPinnedFirstSort(base(), true);
    expect(on.sort).toEqual({
      field: "position",
      direction: "asc",
      pinned_first: true,
    });
    expect(on.scope).toEqual(base().scope);
    expect(on.filters).toEqual({});
  });

  it("returns the same object when nothing changes (stable query identity)", () => {
    const already = spec({ field: "title", direction: "desc", pinned_first: true });
    expect(withPinnedFirstSort(already, true)).toBe(already);
    const off = base();
    expect(withPinnedFirstSort(off, false)).toBe(off);
  });
});

describe("withoutPinnedFirstSort", () => {
  it("drops the flag so a pin toggle cannot re-key the count requests", () => {
    const on = withPinnedFirstSort(base(), true);
    const counted = withoutPinnedFirstSort(on);
    expect("pinned_first" in counted.sort).toBe(false);
    // Same identity as the spec with pins never enabled, so React Query keeps
    // the facet/aggregation cache across a pin toggle.
    expect(counted.sort).toEqual(base().sort);
  });

  it("is a no-op (same reference) when the flag was never set", () => {
    const off = base();
    expect(withoutPinnedFirstSort(off)).toBe(off);
  });

  it("keeps every other filter and scope field intact", () => {
    const full: IssueTableQuerySpec = {
      scope: { kind: "project", project_id: "p1" },
      filters: { statuses: ["todo"], include_sub_issues: true },
      search: "abc",
      sort: { field: "priority", direction: "desc", pinned_first: true },
    };
    expect(withoutPinnedFirstSort(full)).toEqual({
      ...full,
      sort: { field: "priority", direction: "desc" },
    });
  });
});

describe("pinnedIssueIdsFromRows", () => {
  const row = (id: string, isPinned: boolean): IssueTableRow =>
    ({
      issue: { id },
      direct_child_count: 0,
      is_pinned: isPinned,
    }) as unknown as IssueTableRow;

  it("reads the id off the row's own flag", () => {
    const ids = pinnedIssueIdsFromRows([row("a", true), row("b", false), row("c", true)]);
    expect([...ids].sort()).toEqual(["a", "c"]);
  });

  it("stays empty when an older server omits the field", () => {
    // parseWithFallback defaults `is_pinned` to false, so the projection is a
    // no-op rather than an error: right order, no badge.
    const legacy = { issue: { id: "a" }, direct_child_count: 0 } as unknown as IssueTableRow;
    expect(pinnedIssueIdsFromRows([legacy]).size).toBe(0);
  });

  it("never marks a row pinned on a truthy-looking non-boolean", () => {
    const odd = {
      issue: { id: "a" },
      direct_child_count: 0,
      is_pinned: "true",
    } as unknown as IssueTableRow;
    expect(pinnedIssueIdsFromRows([odd]).size).toBe(0);
  });
});
