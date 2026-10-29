// @vitest-environment node
import { beforeEach, describe, expect, it } from "vitest";
import { createStore, type StoreApi } from "zustand/vanilla";
import {
  mergeViewStatePersisted,
  viewStorePersistOptions,
  viewStoreSlice,
  type IssueViewState,
} from "./view-store";

describe("board sub-issue accordion state", () => {
  let store: StoreApi<IssueViewState>;
  beforeEach(() => {
    store = createStore<IssueViewState>()((set) => viewStoreSlice(set));
  });

  it("starts with every parent folded", () => {
    expect(store.getState().boardExpandedParents).toEqual([]);
  });

  it("toggles one parent without disturbing the others", () => {
    store.getState().toggleBoardParentExpanded("a");
    store.getState().toggleBoardParentExpanded("b");
    store.getState().toggleBoardParentExpanded("a");

    expect(store.getState().boardExpandedParents).toEqual(["b"]);
  });
});

describe("board parent groups state", () => {
  let store: StoreApi<IssueViewState>;
  beforeEach(() => {
    store = createStore<IssueViewState>()((set) => viewStoreSlice(set));
  });

  it("folds one group without touching the others", () => {
    store.getState().toggleBoardParentGroupCollapsed("status:todo|p1");
    store.getState().toggleBoardParentGroupCollapsed("status:done|p1");
    store.getState().toggleBoardParentGroupCollapsed("status:todo|p1");

    expect(store.getState().boardCollapsedParentGroups).toEqual(["status:done|p1"]);
  });

  it("starts on every parent and remembers the selected tab", () => {
    expect(store.getState().boardParentFilter).toBeNull();

    store.getState().setBoardParentFilter("p1");
    expect(store.getState().boardParentFilter).toBe("p1");
    store.getState().setBoardParentFilter(null);
    expect(store.getState().boardParentFilter).toBeNull();
  });

  it("persists folded groups but never the selected tab", () => {
    const state = { ...store.getState(), boardCollapsedParentGroups: ["k"], boardParentFilter: "p1" };
    const persisted = viewStorePersistOptions("test").partialize(state);

    expect(persisted).toMatchObject({ boardCollapsedParentGroups: ["k"] });
    expect(persisted).not.toHaveProperty("boardParentFilter");
  });
});

describe("persisted merge", () => {
  const defaults = () =>
    viewStoreSlice(createStore<IssueViewState>()((set) => viewStoreSlice(set)).setState);

  it("keeps the surface's collapsed-by-default value out of the snapshot's reach", () => {
    // A project surface must stay default-collapsed even when the snapshot it
    // rehydrates from was written before the field existed — or was seeded
    // from a saved view definition captured on a workspace surface.
    const current = { ...defaults(), tableParentsCollapsedByDefault: true };

    const merged = mergeViewStatePersisted(
      { tableParentsCollapsedByDefault: false },
      current,
    );

    expect(merged.tableParentsCollapsedByDefault).toBe(true);
  });

  it("restores the user's hide-finished choice and ignores a non-boolean", () => {
    const current = { ...defaults(), hideCompletedParents: true };

    expect(
      mergeViewStatePersisted({ hideCompletedParents: false }, current)
        .hideCompletedParents,
    ).toBe(false);
    expect(
      mergeViewStatePersisted({ hideCompletedParents: "yes" }, current)
        .hideCompletedParents,
    ).toBe(true);
  });

  it("falls back to the default when the expanded list is not an array", () => {
    const current = { ...defaults(), boardExpandedParents: ["keep-me"] };

    expect(
      mergeViewStatePersisted({ boardExpandedParents: "nope" }, current)
        .boardExpandedParents,
    ).toEqual(["keep-me"]);
  });

  it("restores folded parent groups, drops a malformed list, and never a tab", () => {
    const current = { ...defaults(), boardCollapsedParentGroups: ["keep-me"] };

    expect(
      mergeViewStatePersisted({ boardCollapsedParentGroups: ["a|p", 3] }, current)
        .boardCollapsedParentGroups,
    ).toEqual(["a|p"]);
    expect(
      mergeViewStatePersisted({ boardCollapsedParentGroups: "nope" }, current)
        .boardCollapsedParentGroups,
    ).toEqual(["keep-me"]);
    expect(
      mergeViewStatePersisted({ boardParentFilter: "p1" }, current).boardParentFilter,
    ).toBeNull();
  });
});
