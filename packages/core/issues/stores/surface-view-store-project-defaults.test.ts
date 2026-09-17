// @vitest-environment node
import { beforeEach, describe, expect, it } from "vitest";
import {
  clearIssueSurfaceViewState,
  getIssueSurfaceViewStore,
} from "./surface-view-store";

/**
 * A project surface opens folded: only the big tasks, finished-through work out
 * of the way, sub-issues reachable in place rather than competing for board
 * space. Every one of these is a DEFAULT — the store must still take a user's
 * own value once they change it. (DENE-444)
 */
describe("project surface display defaults", () => {
  let counter = 0;
  let projectKey: string;
  let workspaceKey: string;

  beforeEach(() => {
    counter += 1;
    projectKey = `project:p-${counter}`;
    workspaceKey = `workspace:w-${counter}`;
    clearIssueSurfaceViewState(projectKey);
    clearIssueSurfaceViewState(workspaceKey);
  });

  it("folds sub-issues, collapses table parents and hides finished work", () => {
    const state = getIssueSurfaceViewStore(projectKey).getState();

    expect(state.showSubIssues).toBe(false);
    expect(state.tableParentsCollapsedByDefault).toBe(true);
    expect(state.hideCompletedParents).toBe(true);
  });

  it("leaves a workspace surface on the flat defaults", () => {
    const state = getIssueSurfaceViewStore(workspaceKey).getState();

    expect(state.showSubIssues).toBe(true);
    expect(state.tableParentsCollapsedByDefault).toBe(false);
    expect(state.hideCompletedParents).toBe(false);
  });

  it("keeps the user's own toggle once they flip it", () => {
    const store = getIssueSurfaceViewStore(projectKey);
    store.getState().toggleHideCompletedParents();
    store.getState().toggleShowSubIssues();

    expect(store.getState().hideCompletedParents).toBe(false);
    expect(store.getState().showSubIssues).toBe(true);
  });
});
