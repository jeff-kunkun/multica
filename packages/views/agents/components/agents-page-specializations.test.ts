// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { Agent } from "@multica/core/types";
import {
  flattenSpecializationItems,
  groupRowsByBaseRole,
  SPECIALIZATION_DERIVE_ROW_HEIGHT,
} from "./agents-page-specializations";

interface Row {
  agent: Agent;
}

function row(overrides: Partial<Agent>): Row {
  return {
    agent: {
      id: "agent",
      workspace_id: "ws-1",
      runtime_id: "rt-1",
      name: "Agent",
      description: "",
      instructions: "",
      avatar_url: null,
      runtime_mode: "local",
      runtime_config: {},
      custom_args: [],
      visibility: "private",
      permission_mode: "private",
      invocation_targets: [],
      status: "idle",
      max_concurrent_tasks: 1,
      model: "",
      owner_id: null,
      skills: [],
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
      archived_at: null,
      archived_by: null,
      ...overrides,
    },
  };
}

const base = row({ id: "base-1", name: "Base One", child_count: 2 });
const child = row({
  id: "child-1",
  name: "Child One",
  parent_agent_id: "base-1",
  parent_agent_name: "Base One",
});
const other = row({ id: "base-2", name: "Base Two" });

describe("groupRowsByBaseRole", () => {
  it("nests each visible specialisation under its base role", () => {
    const groups = groupRowsByBaseRole([base, child, other]);
    expect(groups.map((group) => group.base.agent.id)).toEqual([
      "base-1",
      "base-2",
    ]);
    expect(groups[0]?.children.map((r) => r.agent.id)).toEqual(["child-1"]);
    expect(groups[0]?.count).toBe(2);
    // A base role with no specialisations is still a group of one; whether it
    // renders anything group-like (a fold control, a derive entry) is the
    // flattener's call, not the grouper's.
    expect(groups[1]?.children).toEqual([]);
    expect(groups[1]?.count).toBe(0);
  });

  it("falls back to a flat row when the base role is filtered out", () => {
    // Scope "mine", a search, or an active filter can hide the parent while
    // keeping the child. The child must still be listed, as its own row.
    const groups = groupRowsByBaseRole([child, other]);
    expect(groups.map((group) => group.base.agent.id)).toEqual([
      "child-1",
      "base-2",
    ]);
    expect(groups[0]?.children).toEqual([]);
    expect(groups[0]?.count).toBe(0);
  });

  it("keeps row order for both base roles and their children", () => {
    const secondChild = row({
      id: "child-2",
      parent_agent_id: "base-1",
      parent_agent_name: "Base One",
    });
    const groups = groupRowsByBaseRole([base, secondChild, child]);
    expect(groups[0]?.children.map((r) => r.agent.id)).toEqual([
      "child-2",
      "child-1",
    ]);
  });

  it("does not lose rows when the same agent appears twice", () => {
    const groups = groupRowsByBaseRole([base, base, child]);
    expect(groups.map((group) => group.base.agent.id)).toEqual(["base-1"]);
    expect(groups[0]?.children.map((r) => r.agent.id)).toEqual(["child-1"]);
  });
});

describe("flattenSpecializationItems", () => {
  it("emits the base row, its children, then the derive entry", () => {
    const items = flattenSpecializationItems([base, child], new Set());
    expect(items.map((item) => item.kind)).toEqual([
      "base",
      "specialization",
      "derive",
    ]);
    const first = items[0];
    expect(first?.kind === "base" && first.expanded).toBe(true);
    expect(first?.kind === "base" && first.childCount).toBe(2);
    const childItem = items[1];
    expect(
      childItem?.kind === "specialization" && childItem.parentName,
    ).toBe("Base One");
  });

  it("collapses to the base row alone, keeping its count visible", () => {
    const items = flattenSpecializationItems(
      [base, child, other],
      new Set(["base-1"]),
    );
    // `other` has no specialisations, so it contributes one plain row and no
    // derive entry — a folded group is not the only reason a group emits one.
    expect(items.map((item) => item.kind)).toEqual(["base", "base"]);
    const collapsed = items[0];
    expect(collapsed?.kind === "base" && collapsed.expanded).toBe(false);
    expect(collapsed?.kind === "base" && collapsed.childCount).toBe(2);
    expect(collapsed?.kind === "base" && collapsed.hasChildren).toBe(true);
  });

  it("gives a base role with zero specialisations a plain row", () => {
    // DENE-384: an empty derive entry under every base role doubled the list
    // and read as broken; the empty fold control was the same noise in a
    // smaller box. Creating the first specialisation stays reachable from the
    // row's menu and the create flow, so nothing is lost here.
    const items = flattenSpecializationItems([other], new Set());
    expect(items.map((item) => item.kind)).toEqual(["base"]);
    const only = items[0];
    expect(only?.kind === "base" && only.hasChildren).toBe(false);
    expect(only?.kind === "base" && only.expanded).toBe(false);
    expect(only?.kind === "base" && only.childCount).toBe(0);
  });

  it("still folds visible children when the server count reads zero", () => {
    // Scope "archived" can hold a child whose parent's active count is 0. The
    // child is on screen, so the fold control has to be there to fold it.
    const archivedChild = row({
      id: "child-archived",
      parent_agent_id: "base-2",
      archived_at: "2026-02-01T00:00:00Z",
    });
    const items = flattenSpecializationItems(
      [row({ id: "base-2", name: "Base Two", child_count: 0 }), archivedChild],
      new Set(),
    );
    expect(items.map((item) => item.kind)).toEqual([
      "base",
      "specialization",
      "derive",
    ]);
    const head = items[0];
    expect(head?.kind === "base" && head.hasChildren).toBe(true);
    expect(head?.kind === "base" && head.expanded).toBe(true);
  });

  it("does not offer a derive entry on a specialisation row", () => {
    // Only base roles can be parents; an orphaned specialisation (its base
    // role filtered away) is a plain row.
    const items = flattenSpecializationItems([child], new Set());
    expect(items.map((item) => item.kind)).toEqual(["base"]);
    expect(items[0]?.kind === "base" && items[0].row.agent.id).toBe("child-1");
  });

  it("exposes the derive row height the virtualizer needs", () => {
    expect(SPECIALIZATION_DERIVE_ROW_HEIGHT).toBeGreaterThan(0);
  });
});
