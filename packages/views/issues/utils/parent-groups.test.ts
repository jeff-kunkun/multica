import { describe, expect, it } from "vitest";
import {
  PARENT_GROUP_COLORS,
  assignParentGroupColors,
  groupByParent,
  groupColumnsByParent,
  parentGroupKey,
  parentGroupRows,
  shortParentTitle,
} from "./parent-groups";

type Item = { id: string; parent: string | null };
const item = (id: string, parent: string | null = null): Item => ({ id, parent });
const parentOf = (i: Item) => i.parent;
const ids = (items: Item[]) => items.map((i) => i.id);

describe("groupByParent", () => {
  it("pulls a parent's sub-issues to its first one and leaves loose cards in place", () => {
    const items = [item("a", "P"), item("x"), item("b", "Q"), item("c", "P"), item("y"), item("d", "Q")];

    expect(ids(groupByParent(items, parentOf))).toEqual(["a", "c", "x", "b", "d", "y"]);
  });

  it("is idempotent, so the column can regroup the board's already grouped order", () => {
    const items = [item("a", "P"), item("x"), item("c", "P"), item("b", "Q")];
    const once = groupByParent(items, parentOf);

    expect(groupByParent(once, parentOf)).toEqual(once);
  });

  it("returns a column without parents unchanged", () => {
    const items = [item("x"), item("y")];

    expect(groupByParent(items, parentOf)).toEqual(items);
  });

  it("groups every column of the board on its own", () => {
    const parents: Record<string, string | null> = { a: "P", b: "P", x: null, c: "P" };

    expect(
      groupColumnsByParent({ todo: ["a", "x", "b"], done: ["x2", "c"] }, (id) => parents[id]),
    ).toEqual({ todo: ["a", "b", "x"], done: ["x2", "c"] });
  });
});

describe("parentGroupRows", () => {
  it("heads each parent's run and counts it", () => {
    const rows = parentGroupRows([item("a", "P"), item("x"), item("c", "P")], parentOf, () => false);

    expect(rows.map((r) => (r.kind === "group" ? `[${r.parentId} ${r.count}]` : r.item.id))).toEqual([
      "[P 2]",
      "a",
      "c",
      "x",
    ]);
  });

  it("keeps a folded group's header and count but drops its cards", () => {
    const rows = parentGroupRows(
      [item("a", "P"), item("c", "P"), item("x")],
      parentOf,
      (parentId) => parentId === "P",
    );

    expect(rows).toEqual([
      { kind: "group", parentId: "P", count: 2, collapsed: true },
      { kind: "card", item: item("x"), parentId: null },
    ]);
  });

  it("leaves a column without parents as plain cards", () => {
    const rows = parentGroupRows([item("x"), item("y")], parentOf, () => false);

    expect(rows.every((r) => r.kind === "card" && r.parentId === null)).toBe(true);
  });
});

describe("assignParentGroupColors", () => {
  it("gives the same parent the same color however often it is asked", () => {
    const first = assignParentGroupColors(["p1", "p2", "p3"]);
    const again = assignParentGroupColors(["p3", "p1", "p2", "p1"]);

    expect(again).toEqual(first);
  });

  it("gives every parent its own color while there are enough colors", () => {
    const parents = Array.from({ length: PARENT_GROUP_COLORS.length }, (_, i) => `parent-${i}`);
    const colors = assignParentGroupColors(parents);

    expect(new Set(colors.values()).size).toBe(PARENT_GROUP_COLORS.length);
  });

  it("does not repaint the parents already shown when a newer one appears", () => {
    // UUIDv7 ids sort by creation time; a newer parent sorts last.
    const shown = ["01a18700-0000-7000-8000-000000000001", "01a18700-0000-7000-8000-000000000002"];
    const before = assignParentGroupColors(shown);
    const after = assignParentGroupColors([...shown, "01a18799-0000-7000-8000-000000000003"]);

    for (const id of shown) expect(after.get(id)).toBe(before.get(id));
  });

  it("still colors every parent when there are more parents than colors", () => {
    const parents = Array.from({ length: PARENT_GROUP_COLORS.length + 3 }, (_, i) => `parent-${i}`);

    expect(assignParentGroupColors(parents).size).toBe(parents.length);
  });
});

describe("shortParentTitle", () => {
  it("drops a leading project tag, in either colon", () => {
    expect(shortParentTitle("Multica 办公室: 看板防卡死")).toBe("看板防卡死");
    expect(shortParentTitle("Multica 办公室：看板防卡死")).toBe("看板防卡死");
  });

  it("keeps a title with no tag, or one whose colon is not a tag", () => {
    expect(shortParentTitle("防卡死")).toBe("防卡死");
    expect(shortParentTitle("Ship the board grouping that keeps every parent's color: v2")).toBe(
      "Ship the board grouping that keeps every parent's color: v2",
    );
  });
});

describe("parentGroupKey", () => {
  it("names one parent in one column", () => {
    expect(parentGroupKey("status:todo", "P")).not.toBe(parentGroupKey("status:done", "P"));
  });
});
