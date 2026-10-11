// @vitest-environment jsdom

import { beforeEach, describe, expect, it } from "vitest";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import { useChatListViewStore } from "./list-view-store";

const KEY = "multica_chat_list_view";

function saved(wsSlug: string) {
  const raw = sessionStorage.getItem(`${KEY}:${wsSlug}`);
  return raw ? (JSON.parse(raw) as { state: Record<string, unknown> }).state : null;
}

describe("useChatListViewStore", () => {
  beforeEach(async () => {
    sessionStorage.clear();
    localStorage.clear();
    setCurrentWorkspace("ws-a", "ws-a-id");
    await useChatListViewStore.persist.rehydrate();
  });

  it("keeps the list position in sessionStorage, scoped to the workspace", () => {
    const store = useChatListViewStore.getState();
    store.setProjectFilter({ type: "project", id: "proj-1" });
    store.setSearch("release");
    store.setView("archived");
    store.setHistoryExpanded(true);

    expect(saved("ws-a")).toEqual({
      projectFilter: { type: "project", id: "proj-1" },
      keepAllProjects: false,
      search: "release",
      view: "archived",
      historyExpanded: true,
    });
    // Session state, not a preference: nothing lands in localStorage.
    expect(Object.keys(localStorage).filter((k) => k.startsWith(KEY))).toEqual([]);
  });

  it("starts another workspace from the full list instead of inheriting a project", async () => {
    useChatListViewStore.getState().setProjectFilter({ type: "project", id: "proj-1" });
    setCurrentWorkspace("ws-b", "ws-b-id");
    await useChatListViewStore.persist.rehydrate();

    expect(useChatListViewStore.getState().projectFilter).toEqual({ type: "all" });

    setCurrentWorkspace("ws-a", "ws-a-id");
    await useChatListViewStore.persist.rehydrate();
    expect(useChatListViewStore.getState().projectFilter).toEqual({
      type: "project",
      id: "proj-1",
    });
  });

  it("falls back per field when the saved snapshot is malformed", async () => {
    sessionStorage.setItem(
      `${KEY}:ws-a`,
      JSON.stringify({
        state: { projectFilter: { type: "project" }, search: 42, view: "trash", historyExpanded: "yes" },
        version: 0,
      }),
    );
    await useChatListViewStore.persist.rehydrate();

    const state = useChatListViewStore.getState();
    expect(state.projectFilter).toEqual({ type: "all" });
    expect(state.search).toBe("");
    expect(state.view).toBe("history");
    expect(state.historyExpanded).toBe(false);
  });

  it("follows linked chats and project changes, including a newly created project", () => {
    const store = useChatListViewStore.getState();
    store.selectProjectFilter({ type: "project", id: "media" });
    store.followProjects(["ipa"]);
    expect(useChatListViewStore.getState().projectFilter).toEqual({ type: "project", id: "ipa" });
    store.followProjects(["new-project"]);
    expect(useChatListViewStore.getState().projectFilter).toEqual({ type: "project", id: "new-project" });
  });

  it("follows from default All, but preserves explicit All across reloads", async () => {
    const store = useChatListViewStore.getState();
    store.followProjects(["ipa"]);
    expect(useChatListViewStore.getState().projectFilter).toEqual({ type: "project", id: "ipa" });
    store.selectProjectFilter({ type: "all" });
    await useChatListViewStore.persist.rehydrate();
    store.followProjects(["media"]);
    expect(useChatListViewStore.getState().projectFilter).toEqual({ type: "all" });
    store.selectProjectFilter({ type: "project", id: "media" });
    store.followProjects(["ipa"]);
    expect(useChatListViewStore.getState().projectFilter).toEqual({ type: "project", id: "ipa" });
  });

  it("keeps a matching multi-project filter and follows detachment", () => {
    const store = useChatListViewStore.getState();
    store.selectProjectFilter({ type: "project", id: "ipa" });
    store.followProjects(["media", "ipa"]);
    expect(useChatListViewStore.getState().projectFilter).toEqual({ type: "project", id: "ipa" });
    store.followProjects([]);
    expect(useChatListViewStore.getState().projectFilter).toEqual({ type: "none" });
  });

  it("does not overwrite explicit All before the workspace snapshot rehydrates", async () => {
    sessionStorage.setItem(`${KEY}:ws-b`, JSON.stringify({ state: {
      projectFilter: { type: "all" }, keepAllProjects: true,
    }, version: 0 }));
    setCurrentWorkspace("ws-b", "ws-b-id");
    useChatListViewStore.getState().followProjects(["ipa"]);
    expect(saved("ws-b")?.keepAllProjects).toBe(true);
    await useChatListViewStore.persist.rehydrate();
    useChatListViewStore.getState().followProjects(["ipa"]);
    expect(useChatListViewStore.getState().projectFilter).toEqual({ type: "all" });
    expect(useChatListViewStore.getState().hydratedWorkspaceSlug).toBe("ws-b");
  });
});
