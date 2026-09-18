// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { Issue } from "@multica/core/types";
import { sortIssues } from "./sort";

// `sortIssues` is the client-side sort Swimlane applies to its own cell
// projection — which is the one place a served pinned-first order would
// otherwise be thrown away. The pinned block therefore has to be re-applied
// here as the leading key, and only the leading key. (DENE-500)

function mk(id: string, overrides: Partial<Issue> = {}): Issue {
  return {
    id,
    title: id,
    status: "todo",
    priority: "none",
    position: 0,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  } as Issue;
}

const pinned = (...ids: string[]) => new Set(ids);

describe("sortIssues pinned-first leading key", () => {
  it("lifts pinned rows to the front without reordering the rest", () => {
    const issues = [mk("a", { position: 1 }), mk("b", { position: 2 }), mk("c", { position: 3 })];
    expect(sortIssues(issues, "position", "asc", pinned("c")).map((i) => i.id)).toEqual([
      "c",
      "a",
      "b",
    ]);
  });

  it("orders the pinned block by the ACTIVE sort field, not the pin order", () => {
    // Both rows are pinned; the block must follow the current sort (title),
    // never the sidebar's pin position, which this helper never sees.
    const issues = [
      mk("zebra", { title: "Zebra" }),
      mk("apple", { title: "Apple" }),
      mk("middle", { title: "Middle" }),
    ];
    expect(
      sortIssues(issues, "title", "asc", pinned("zebra", "apple")).map((i) => i.id),
    ).toEqual(["apple", "zebra", "middle"]);
  });

  it("keeps the served relative order inside each block (stable)", () => {
    const issues = [
      mk("p1", { position: 5 }),
      mk("plain1", { position: 1 }),
      mk("p2", { position: 6 }),
      mk("plain2", { position: 2 }),
    ];
    expect(
      sortIssues(issues, "position", "asc", pinned("p1", "p2")).map((i) => i.id),
    ).toEqual(["p1", "p2", "plain1", "plain2"]);
  });

  it("is a no-op for an empty or absent pinned set", () => {
    const issues = [mk("b", { position: 2 }), mk("a", { position: 1 })];
    const unpinned = ["a", "b"];
    expect(sortIssues(issues, "position", "asc").map((i) => i.id)).toEqual(unpinned);
    expect(sortIssues(issues, "position", "asc", pinned()).map((i) => i.id)).toEqual(unpinned);
  });

  it("does not let a pinned row escape its own descending sort", () => {
    // The leading key is a RANK, not a reversal: a pinned row still sorts
    // against the other pinned rows by the field and its direction.
    const issues = [
      mk("low", { priority: "low" }),
      mk("urgent", { priority: "urgent" }),
      mk("none", { priority: "none" }),
    ];
    expect(
      sortIssues(issues, "priority", "desc", pinned("low", "urgent")).map((i) => i.id),
    ).toEqual(["low", "urgent", "none"]);
  });

  it("keeps property sorts (and their valueless-last rule) inside the pin block", () => {
    const propertyId = "prop-effort";
    const withValue = (id: string, value?: number) =>
      mk(id, { properties: value === undefined ? {} : { [propertyId]: value } });
    const issues = [withValue("big", 8), withValue("none"), withValue("small", 1)];
    expect(
      sortIssues(issues, `property:${propertyId}` as never, "asc", pinned("none", "big")).map(
        (i) => i.id,
      ),
    ).toEqual(["big", "none", "small"]);
  });
});
